package sshgw

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/ssh"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/credvault"
	"github.com/touwaeriol/agent-teams/server/internal/sshdial"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// conn is one session SSH connection: the front half (node ssh client → gateway, session
// certificate) and, from the first channel on, the back half (gateway → target, the server's
// credential). One front connection has at most one back connection.
type conn struct {
	g       *Gateway
	id      string
	nc      net.Conn
	node    store.Node
	sess    store.SSHSession
	server  store.RemoteServer // without credential
	gen     int
	started time.Time

	authFailed atomic.Bool
	serial     int64
	sc         *ssh.ServerConn

	bmu     sync.Mutex                 // serializes the back half's dial (kill never takes it)
	backend atomic.Pointer[ssh.Client] // the back half once logged in
	killed  atomic.Bool
	secrets []string // the server's password / passphrase, for redaction by value (under bmu)

	in, out atomic.Int64 // bytes node → target, target → node
	last    atomic.Int64 // unix nanos of the last data either way

	cmu      sync.Mutex
	chans    map[*channel]struct{}
	execs    int
	lastCmd  string
	lastExit *int
	reason   string

	wg       sync.WaitGroup
	killOnce sync.Once
	done     chan struct{}
}

func newConn(g *Gateway, nc net.Conn, node store.Node, x store.SSHSession, srv store.RemoteServer, gen int) *conn {
	c := &conn{g: g, id: core.NewID("sshc"), nc: nc, node: node, sess: x, server: srv, gen: gen, started: g.now(),
		chans: map[*channel]struct{}{}, done: make(chan struct{})}
	c.last.Store(c.started.UnixNano())
	return c
}

func (c *conn) base() store.SSHAudit {
	var serial *int64
	if c.serial != 0 {
		s := c.serial
		serial = &s
	}
	return auditBase(c.sess, c.node.ID, serial, c.server.ID, c.server.Name, c.id)
}

// serve runs the front half until it closes.
func (c *conn) serve(ctx context.Context) {
	defer close(c.done)
	_, host, _, _, err := c.g.keys(ctx)
	if err != nil {
		c.g.log.Warn("ssh: gateway keys", "err", err)
		return
	}
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(m ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
			return c.checkCert(ctx, m, k)
		},
		ServerVersion: "SSH-2.0-agent-teams-gateway",
		MaxAuthTries:  6,
	}
	cfg.AddHostKey(host)
	_ = c.nc.SetDeadline(c.g.now().Add(c.g.cfg.FrontHandshake))
	sc, chans, reqs, err := ssh.NewServerConn(c.nc, cfg)
	if err != nil {
		c.g.log.Info("ssh: front handshake failed", "session_id", c.sess.SessionID, "server", c.server.Name, "err", err)
		_ = c.nc.Close()
		return
	}
	_ = c.nc.SetDeadline(time.Time{})
	c.sc = sc
	c.serial, _ = strconv.ParseInt(sc.Permissions.Extensions["serial"], 10, 64)
	c.g.track(c)
	defer c.g.untrack(c)
	c.g.audit(ctx, c.base(), store.SSHAuditConnect, "", nil)
	go c.globalRequests(ctx, reqs)
	go c.watch(ctx)
	for nch := range chans {
		c.wg.Add(1)
		go func() {
			defer c.wg.Done()
			c.channel(ctx, nch)
		}()
	}
	c.kill("") // the front half closed: close the back half too
	c.wg.Wait()
	c.finish(ctx)
}

// checkCert is the front half's only authentication: a session certificate signed by the session
// CA, valid now, without critical options, for the server named as user, whose serial is recorded,
// not revoked and bound to this session, generation, node and key (remote-servers.md §4.7).
func (c *conn) checkCert(ctx context.Context, meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
	fail := func(reason string) (*ssh.Permissions, error) {
		if c.authFailed.CompareAndSwap(false, true) {
			c.g.audit(ctx, c.base(), store.SSHAuditAuthFail, reason, nil)
		}
		return nil, errors.New(reason)
	}
	cert, ok := key.(*ssh.Certificate)
	if !ok {
		return fail("not_a_certificate")
	}
	if meta.User() != c.server.Name {
		return fail("user_must_be_server_name")
	}
	ca, _, _, _, err := c.g.keys(ctx)
	if err != nil {
		return nil, err
	}
	caPub := ca.PublicKey().Marshal()
	checker := ssh.CertChecker{
		IsUserAuthority: func(auth ssh.PublicKey) bool { return bytes.Equal(auth.Marshal(), caPub) },
		Clock:           c.g.now,
	}
	if _, err := checker.Authenticate(meta, cert); err != nil {
		return fail("bad_certificate: " + err.Error())
	}
	ext := cert.Permissions.Extensions
	if ext[extSession] != c.sess.SessionID || ext[extGen] != strconv.Itoa(c.gen) || ext[extNode] != c.node.ID {
		return fail("certificate_not_for_this_session")
	}
	row, err := c.g.store.SSHCert(ctx, int64(cert.Serial))
	switch {
	case err != nil:
		return fail("unknown_serial")
	case row.RevokedAt != nil:
		return fail("revoked")
	case row.SessionID != c.sess.SessionID || row.OpenGen != c.gen || row.NodeID != c.node.ID ||
		row.PubkeyFP != ssh.FingerprintSHA256(cert.Key) || !contains(row.Principals, c.server.Name):
		return fail("certificate_mismatch")
	}
	x, err := c.g.store.SSHSession(ctx, c.sess.SessionID)
	if err != nil || x.State == "closed" || x.OpenGen != c.gen || x.NodeID != c.node.ID {
		return fail("session_gone")
	}
	return &ssh.Permissions{Extensions: map[string]string{"serial": strconv.FormatUint(cert.Serial, 10)}}, nil
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// stillAllowed re-checks the connection against the database (every channel, every Recheck, every
// ssh:revoke hint): "" when it may go on, else the reason shown to the session. A database error
// keeps the connection (the next check decides).
func (c *conn) stillAllowed(ctx context.Context) string {
	row, err := c.g.store.SSHCert(ctx, c.serial)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return "会话证书已作废"
	case err != nil:
		c.g.log.Warn("ssh: recheck certificate", "serial", c.serial, "err", err)
		return ""
	case row.RevokedAt != nil:
		return "会话证书已吊销（" + row.RevokeReason + "）"
	}
	x, srv, err := c.g.store.SessionServerGrant(ctx, c.sess.SessionID, c.server.Name)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return "会话已不存在"
	case err != nil && !errors.Is(err, store.ErrNotGranted):
		c.g.log.Warn("ssh: recheck grant", "session_id", c.sess.SessionID, "err", err)
		return ""
	case x.State == "closed" || x.OpenGen != c.gen || x.NodeID != c.node.ID:
		return "会话已结束或已重新打开"
	case err != nil:
		return "这个会话已无权使用 " + c.server.Name
	case srv.ID != c.server.ID:
		return "服务器 " + c.server.Name + " 已被替换"
	}
	return ""
}

// noticeTimeout bounds how long kill waits for the reason to reach the node's channels: a client
// that does not read (a full window) must not hold the connection open.
const noticeTimeout = 2 * time.Second

// kill cuts the connection: every open channel gets the reason on stderr and exit-status 255 (best
// effort, at most noticeTimeout), then both halves are closed whatever the client does. It never
// blocks on the peer for longer than noticeTimeout, nor on a back half still dialling.
func (c *conn) kill(reason string) {
	c.killOnce.Do(func() {
		c.killed.Store(true)
		c.cmu.Lock()
		if c.reason == "" {
			c.reason = reason
		}
		chans := make([]*channel, 0, len(c.chans))
		for ch := range c.chans {
			chans = append(chans, ch)
		}
		c.cmu.Unlock()
		if reason != "" && len(chans) > 0 {
			noticed := make(chan struct{})
			var wg sync.WaitGroup
			for _, ch := range chans {
				wg.Add(1)
				go func() { // a write blocks while the peer's window is 0; closing the transport ends it
					defer wg.Done()
					_, _ = fmt.Fprintf(ch.f.Stderr(), "agent-teams: %s，连接已断开\r\n", reason)
					_, _ = ch.f.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{255}))
					_ = ch.f.Close()
				}()
			}
			go func() {
				wg.Wait()
				close(noticed)
			}()
			select {
			case <-noticed:
			case <-time.After(noticeTimeout):
			}
		}
		if b := c.backend.Load(); b != nil {
			_ = b.Close()
		}
		if c.sc != nil {
			_ = c.sc.Close()
		}
		_ = c.nc.Close()
	})
}

// watch sends keepalives on both halves and enforces the idle and maximum durations.
func (c *conn) watch(ctx context.Context) {
	t := time.NewTicker(c.g.cfg.Keepalive)
	defer t.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-ctx.Done():
			c.kill("平台重启，连接中断")
			return
		case <-t.C:
		}
		now := c.g.now()
		if now.Sub(time.Unix(0, c.last.Load())) > c.g.cfg.Idle {
			c.kill(fmt.Sprintf("空闲超过 %s", c.g.cfg.Idle))
			return
		}
		if now.Sub(c.started) > c.g.cfg.MaxConn {
			c.kill(fmt.Sprintf("连接超过 %s 上限", c.g.cfg.MaxConn))
			return
		}
		go c.keepalive(c.sc, "节点 ssh 客户端没有响应")
		if b := c.backend.Load(); b != nil {
			go c.keepalive(b, "目标服务器没有响应")
		}
	}
}

type requester interface {
	SendRequest(name string, wantReply bool, payload []byte) (bool, []byte, error)
}

func (c *conn) keepalive(r requester, reason string) {
	done := make(chan error, 1)
	go func() {
		_, _, err := r.SendRequest("keepalive@openssh.com", true, nil)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			c.kill(reason)
		}
	case <-time.After(3 * c.g.cfg.Keepalive):
		c.kill(reason)
	case <-c.done:
	}
}

// globalRequests answers the front half's global requests: keepalives yes, forwarding no.
func (c *conn) globalRequests(ctx context.Context, reqs <-chan *ssh.Request) {
	for r := range reqs {
		switch r.Type {
		case "keepalive@openssh.com":
			_ = r.Reply(true, nil)
		case "tcpip-forward", "cancel-tcpip-forward", "streamlocal-forward@openssh.com":
			c.g.audit(ctx, c.base(), store.SSHAuditDenied, "global_request:"+r.Type, nil)
			_ = r.Reply(false, nil)
		default:
			if r.WantReply {
				_ = r.Reply(false, nil)
			}
		}
	}
}

// channel handles one channel the session opens: only "session", only while the grant holds.
func (c *conn) channel(ctx context.Context, nch ssh.NewChannel) {
	if nch.ChannelType() != "session" {
		c.g.audit(ctx, c.base(), store.SSHAuditDenied, "channel:"+nch.ChannelType(), nil)
		_ = nch.Reject(ssh.Prohibited, "agent-teams: only exec, shell and sftp are allowed (no port or agent forwarding)")
		return
	}
	if reason := c.stillAllowed(ctx); reason != "" {
		c.g.audit(ctx, c.base(), store.SSHAuditDenied, reason, nil)
		_ = nch.Reject(ssh.Prohibited, "agent-teams: "+reason)
		go c.kill(reason)
		return
	}
	b, err := c.backendClient(ctx)
	if err != nil {
		_ = nch.Reject(ssh.ConnectionFailed, "agent-teams: "+err.Error())
		return
	}
	bch, breqs, err := b.OpenChannel("session", nil)
	if err != nil {
		_ = nch.Reject(ssh.ConnectionFailed, "agent-teams: the target refused the channel")
		return
	}
	fch, freqs, err := nch.Accept()
	if err != nil {
		_ = bch.Close()
		return
	}
	c.bridge(ctx, &channel{f: fch, started: c.g.now()}, freqs, bch, breqs)
}

// backendClient returns the back half, dialing and logging in on first use. The host key must
// equal the one the user confirmed (with a valid MAC); there is no way around it.
func (c *conn) backendClient(ctx context.Context) (*ssh.Client, error) {
	c.bmu.Lock()
	defer c.bmu.Unlock()
	if b := c.backend.Load(); b != nil {
		return b, nil
	}
	if c.killed.Load() {
		return nil, errors.New("connection closed")
	}
	deny := func(kind, reason, msg string, detail map[string]any) (*ssh.Client, error) {
		c.g.audit(ctx, c.base(), kind, reason, detail)
		return nil, errors.New(msg)
	}
	_, srv, err := c.g.store.SessionServerGrant(ctx, c.sess.SessionID, c.server.Name)
	if err != nil || srv.ID != c.server.ID {
		return deny(store.SSHAuditDenied, "not_granted", "this session may not use "+c.server.Name, nil)
	}
	unconfirmed := "目标服务器 " + srv.Name + " 的主机密钥还没有确认：请用户先在设置里对它点“测试连接”并确认主机密钥"
	if srv.HostKey == "" {
		return deny(store.SSHAuditDenied, "host_key_unconfirmed", unconfirmed, nil)
	}
	if ok, _ := c.g.vault.VerifyMAC(credvault.HostKeyMACData(srv.ID, srv.Host, srv.Port, srv.HostKey), srv.HostKeyMAC); !ok {
		return deny(store.SSHAuditDenied, "host_key_mac_invalid", unconfirmed, nil)
	}
	if _, err := sshdial.ParseHostKey(srv.HostKey); err != nil {
		return deny(store.SSHAuditDenied, "host_key_unparsable", unconfirmed, nil)
	}
	cred, err := c.g.vault.OpenCredential(credvault.ServerAAD(srv.UserID, srv.ID, srv.AuthKind, srv.Host, srv.Port, srv.Username), srv.Sealed)
	if err != nil {
		return deny(store.SSHAuditDenied, "credential_unreadable", "服务器 "+srv.Name+" 的凭证无法解开，请用户在设置里重新填写", nil)
	}
	secrets := []string{}
	for _, v := range []string{cred.Password, cred.Passphrase} {
		if v != "" {
			secrets = append(secrets, v)
		}
	}
	c.secrets = secrets
	// sshdial resolves the host again and checks every address (DNS rebinding, internal networks),
	// dials the checked address and verifies the pinned host key before any credential is sent.
	res, err := c.g.policy(ctx).Dial(ctx, sshdial.Target{Host: srv.Host, Port: srv.Port, Username: srv.Username,
		AuthKind: srv.AuthKind, Cred: cred, HostKey: srv.HostKey}, c.g.cfg.BackHandshake)
	switch {
	case err == nil:
	case errors.Is(err, sshdial.ErrHostKeyMismatch):
		detail := map[string]any{"expected": srv.HostKeyFP}
		if res.Seen != nil {
			detail["got"], detail["got_type"] = res.Seen.Fingerprint, res.Seen.Type
		}
		return deny(store.SSHAuditHostKeyMismatch, "host_key_mismatch",
			"目标服务器 "+srv.Name+" 的主机密钥变了，已拒绝连接（没有发送凭证），请用户在设置里核实", detail)
	case errors.Is(err, sshdial.ErrBlockedAddress), errors.Is(err, sshdial.ErrInvalidHost), errors.Is(err, sshdial.ErrResolve):
		return deny(store.SSHAuditDenied, "address_refused", "服务器 "+srv.Name+" 的地址不允许连接："+err.Error(), nil)
	case errors.Is(err, sshdial.ErrAuth):
		return deny(store.SSHAuditDenied, "target_login_failed", "登录服务器 "+srv.Name+" 失败",
			map[string]any{"error": Redact(err.Error(), secrets...)})
	case errors.Is(err, credvault.ErrInvalidCredential):
		return deny(store.SSHAuditDenied, "credential_invalid", "服务器 "+srv.Name+" 的凭证无效，请用户在设置里重新填写", nil)
	default:
		return deny(store.SSHAuditDenied, "dial_failed", "连不上服务器 "+srv.Name,
			map[string]any{"error": Redact(err.Error(), secrets...)})
	}
	c.backend.Store(res.Client) // its channel opens from the target (forwarding, agent) are rejected
	if c.killed.Load() {        // cut while dialling: kill may have missed the client
		_ = res.Client.Close()
		return nil, errors.New("connection closed")
	}
	return res.Client, nil
}

// policy is the dialing policy with the administrator's allowed private networks (sshdial.SettingKey).
func (g *Gateway) policy(ctx context.Context) sshdial.Policy {
	var set sshdial.AllowedSetting
	if _, err := g.store.Setting(ctx, sshdial.SettingKey, &set); err != nil {
		g.log.Warn("ssh: allowed networks", "err", err)
	}
	allowed, err := sshdial.ParseAllowed(set)
	if err != nil {
		g.log.Warn("ssh: allowed networks", "err", err)
	}
	return sshdial.Policy{Allowed: allowed, Resolver: g.cfg.Resolver}
}

// ---- channels -------------------------------------------------------------------------------

// channel is one bridged session channel.
type channel struct {
	f       ssh.Channel
	started time.Time

	mu      sync.Mutex
	kind    string // exec, shell, subsystem ("" until one of them is asked for)
	command string
	pty     string
	exit    *int
	signal  string
	in, out atomic.Int64
}

func (ch *channel) start(kind, command string) bool {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	if ch.kind != "" {
		return false
	}
	ch.kind, ch.command = kind, command
	return true
}

func (c *conn) addChan(ch *channel) bool {
	c.cmu.Lock()
	defer c.cmu.Unlock()
	select {
	case <-c.done:
		return false
	default:
	}
	c.chans[ch] = struct{}{}
	return true
}

func (c *conn) removeChan(ch *channel) {
	c.cmu.Lock()
	delete(c.chans, ch)
	c.cmu.Unlock()
}

// counter counts the bytes read and marks activity.
type counter struct {
	r      io.Reader
	ch     *atomic.Int64
	total  *atomic.Int64
	active *atomic.Int64
}

func (r counter) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if n > 0 {
		r.ch.Add(int64(n))
		r.total.Add(int64(n))
		r.active.Store(time.Now().UnixNano())
	}
	return n, err
}

// bridge copies data and requests between the node's channel f and the target's channel b until
// either side closes, then writes the audit row of what ran in it.
func (c *conn) bridge(ctx context.Context, ch *channel, freqs <-chan *ssh.Request, b ssh.Channel, breqs <-chan *ssh.Request) {
	f := ch.f
	c.addChan(ch)
	defer c.removeChan(ch)
	var out sync.WaitGroup
	out.Add(3)
	go func() {
		defer out.Done()
		_, _ = io.Copy(f, counter{b, &ch.out, &c.out, &c.last})
	}()
	go func() {
		defer out.Done()
		_, _ = io.Copy(f.Stderr(), counter{b.Stderr(), &ch.out, &c.out, &c.last})
	}()
	go func() {
		defer out.Done()
		for r := range breqs {
			c.fromTarget(ch, r)
		}
	}()
	go func() {
		_, _ = io.Copy(b, counter{f, &ch.in, &c.in, &c.last})
		_ = b.CloseWrite()
	}()
	frontDone := make(chan struct{})
	go func() {
		defer close(frontDone)
		for r := range freqs {
			c.fromNode(ctx, ch, b, r)
		}
	}()
	allOut := make(chan struct{})
	go func() {
		out.Wait()
		close(allOut)
	}()
	select {
	case <-allOut: // the target closed the channel; exit-status was passed on
	case <-frontDone: // the node closed it
	}
	_ = f.Close()
	_ = b.Close()
	select {
	case <-allOut:
	case <-time.After(5 * time.Second):
	}
	c.channelDone(ctx, ch)
}

// fromTarget passes the target's channel requests (exit-status, exit-signal, eow) to the node.
func (c *conn) fromTarget(ch *channel, r *ssh.Request) {
	switch r.Type {
	case "exit-status":
		var st struct{ Status uint32 }
		if ssh.Unmarshal(r.Payload, &st) == nil {
			code := int(st.Status)
			ch.mu.Lock()
			ch.exit = &code
			ch.mu.Unlock()
		}
	case "exit-signal":
		var sig struct {
			Signal string
			Core   bool
			Msg    string
			Lang   string
		}
		if ssh.Unmarshal(r.Payload, &sig) == nil {
			ch.mu.Lock()
			ch.signal = sig.Signal
			ch.mu.Unlock()
		}
	}
	ok, err := ch.f.SendRequest(r.Type, r.WantReply, r.Payload)
	if r.WantReply {
		_ = r.Reply(ok && err == nil, nil)
	}
}

// fromNode filters the node's channel requests: exec, shell, pty, window changes, signals, LANG/LC_*
// and the sftp subsystem go to the target; agent and X11 forwarding and other subsystems do not.
func (c *conn) fromNode(ctx context.Context, ch *channel, b ssh.Channel, r *ssh.Request) {
	reply := func(ok bool) {
		if r.WantReply {
			_ = r.Reply(ok, nil)
		}
	}
	forward := func() {
		ok, err := b.SendRequest(r.Type, r.WantReply, r.Payload)
		reply(ok && err == nil)
	}
	switch r.Type {
	case "window-change", "signal", "keepalive@openssh.com":
		forward()
	case "pty-req":
		var p struct {
			Term          string
			Columns, Rows uint32
			Width, Height uint32
			Modes         string
		}
		if ssh.Unmarshal(r.Payload, &p) == nil {
			ch.mu.Lock()
			ch.pty = fmt.Sprintf("%s %dx%d", p.Term, p.Columns, p.Rows)
			ch.mu.Unlock()
		}
		forward()
	case "env":
		var e struct{ Name, Value string }
		if ssh.Unmarshal(r.Payload, &e) == nil && (e.Name == "LANG" || strings.HasPrefix(e.Name, "LC_")) {
			forward()
			return
		}
		reply(false)
	case "exec":
		var e struct{ Command string }
		if ssh.Unmarshal(r.Payload, &e) != nil || !ch.start("exec", e.Command) {
			reply(false)
			return
		}
		forward()
	case "shell":
		if !ch.start("shell", "") {
			reply(false)
			return
		}
		forward()
	case "subsystem":
		var s struct{ Name string }
		if ssh.Unmarshal(r.Payload, &s) != nil || s.Name != "sftp" {
			c.g.audit(ctx, c.base(), store.SSHAuditDenied, "subsystem:"+s.Name, nil)
			reply(false)
			return
		}
		if !ch.start("subsystem", "sftp") {
			reply(false)
			return
		}
		forward()
	default:
		if r.Type == "x11-req" || strings.HasPrefix(r.Type, "auth-agent-req") {
			c.g.audit(ctx, c.base(), store.SSHAuditDenied, "request:"+r.Type, nil)
		}
		reply(false)
	}
}

// channelDone writes the audit row of what ran in the channel.
func (c *conn) channelDone(ctx context.Context, ch *channel) {
	ch.mu.Lock()
	kind, command, pty, exit, signal := ch.kind, ch.command, ch.pty, ch.exit, ch.signal
	ch.mu.Unlock()
	if kind == "" {
		return
	}
	dur := c.g.now().Sub(ch.started).Milliseconds()
	a := c.base()
	a.Command = Redact(command, c.secretsCopy()...)
	a.ExitCode, a.DurationMS, a.BytesIn, a.BytesOut = exit, &dur, ch.in.Load(), ch.out.Load()
	detail := map[string]any{}
	if pty != "" {
		detail["pty"] = pty
	}
	if signal != "" {
		detail["signal"] = signal
	}
	auditKind := map[string]string{"exec": store.SSHAuditExec, "shell": store.SSHAuditShell, "subsystem": store.SSHAuditSubsystem}[kind]
	c.g.audit(ctx, a, auditKind, "", detail)
	c.cmu.Lock()
	c.execs++
	switch kind {
	case "exec":
		c.lastCmd = a.Command
	default:
		c.lastCmd = command
		if kind == "shell" {
			c.lastCmd = "shell"
		}
	}
	c.lastExit = exit
	c.cmu.Unlock()
}

func (c *conn) secretsCopy() []string {
	c.bmu.Lock()
	defer c.bmu.Unlock()
	return append([]string(nil), c.secrets...)
}

// finish writes the disconnect audit row and the thread-visible summary.
func (c *conn) finish(ctx context.Context) {
	dur := c.g.now().Sub(c.started).Milliseconds()
	c.cmu.Lock()
	reason, execs, last, exit := c.reason, c.execs, c.lastCmd, c.lastExit
	c.cmu.Unlock()
	if reason == "" {
		reason = "closed"
	}
	a := c.base()
	a.DurationMS, a.BytesIn, a.BytesOut = &dur, c.in.Load(), c.out.Load()
	c.g.audit(ctx, a, store.SSHAuditDisconnect, reason, nil)
	payload := map[string]any{"server": c.server.Name, "server_id": c.server.ID, "employee": c.sess.Employee,
		"session_id": c.sess.SessionID, "conn_id": c.id, "duration_ms": dur, "commands": execs,
		"bytes_in": a.BytesIn, "bytes_out": a.BytesOut, "reason": reason}
	summary := fmt.Sprintf("%s 连接了 %s（%s）", c.sess.Employee, c.server.Name, (time.Duration(dur) * time.Millisecond).Round(time.Second))
	if last != "" {
		short := shorten(last, 200)
		payload["last_command"] = short
		summary = fmt.Sprintf("%s 在 %s 上执行了 %s", c.sess.Employee, c.server.Name, short)
		if exit != nil {
			payload["exit_code"] = *exit
			summary += fmt.Sprintf("（退出码 %d，%s）", *exit, (time.Duration(dur) * time.Millisecond).Round(time.Second))
		}
		if execs > 1 {
			summary += fmt.Sprintf("，共 %d 个命令", execs)
		}
	}
	if reason != "closed" {
		summary += "；" + reason
	}
	payload["summary"] = summary
	c.g.emitServerEvent(ctx, c.sess, payload)
}

func shorten(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}
