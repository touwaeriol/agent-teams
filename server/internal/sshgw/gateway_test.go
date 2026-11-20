package sshgw

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/sshdial"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// TestGatewayEndToEnd: an authorized member runs exec and sftp through WS → gateway → target; the
// target's password never shows on the node side; audit and the thread event are written.
func TestGatewayEndToEnd(t *testing.T) {
	f := newFixture(t)
	dev := f.session("dev-1", "member", "dev", `{}`)
	id := f.identity(dev)
	if names := id.resp.Servers; len(names) != 1 || names[0].Name != "prod" {
		t.Fatalf("servers %+v", names)
	}
	cert := id.signer.PublicKey().(*ssh.Certificate)
	if cert.ValidBefore-cert.ValidAfter != uint64((10*time.Minute+time.Minute).Seconds()) ||
		cert.Permissions.Extensions[extSession] != dev || cert.Permissions.Extensions[extNode] != f.node.ID ||
		len(cert.Permissions.CriticalOptions) != 0 || cert.ValidPrincipals[0] != "prod" {
		t.Fatalf("certificate %+v", cert)
	}

	client, rec, err := f.connect(dev, "prod", "prod", id)
	if err != nil {
		t.Fatal(err)
	}
	sess, _ := client.NewSession()
	_ = sess.Setenv("LANG", "C.UTF-8")
	_ = sess.Setenv("SECRET_FROM_NODE", "x") // not passed on
	out, err := sess.Output("docker compose up -d")
	if err != nil || string(out) != "ran: docker compose up -d\n" {
		t.Fatalf("exec %q %v", out, err)
	}
	sess, _ = client.NewSession()
	var stderr bytes.Buffer
	sess.Stderr = &stderr
	err = sess.Run("exit 7")
	var exit *ssh.ExitError
	if !errors.As(err, &exit) || exit.ExitStatus() != 7 || stderr.String() != "failing with 7\n" {
		t.Fatalf("exit status: %v %q", err, stderr.String())
	}
	// stdin reaches the target (scp -O / rsync style).
	sess, _ = client.NewSession()
	sess.Stdin = strings.NewReader("payload over stdin")
	if out, err := sess.Output("cat"); err != nil || string(out) != "payload over stdin" {
		t.Fatalf("cat %q %v", out, err)
	}
	// The sftp subsystem is passed through.
	sess, _ = client.NewSession()
	w, _ := sess.StdinPipe()
	r, _ := sess.StdoutPipe()
	if err := sess.RequestSubsystem("sftp"); err != nil {
		t.Fatalf("sftp: %v", err)
	}
	_, _ = w.Write([]byte("sftp-packet"))
	buf := make([]byte, 11)
	if _, err := ioReadFull(r, buf); err != nil || string(buf) != "sftp-packet" {
		t.Fatalf("sftp echo %q %v", buf, err)
	}
	_ = w.Close()
	_ = sess.Wait()
	// Other subsystems, agent and X11 forwarding are refused.
	sess, _ = client.NewSession()
	if err := sess.RequestSubsystem("netconf"); err == nil {
		t.Fatal("netconf subsystem accepted")
	}
	sess, _ = client.NewSession()
	if ok, _ := sess.SendRequest("auth-agent-req@openssh.com", true, nil); ok {
		t.Fatal("agent forwarding accepted")
	}
	_ = sess.Close()
	if _, err := client.Dial("tcp", "127.0.0.1:5432"); err == nil {
		t.Fatal("port forwarding accepted")
	}
	// An exec whose command carries the server password is masked in the audit.
	sess, _ = client.NewSession()
	_, _ = sess.Output("echo " + targetPassword + " | sudo -S true")
	_ = client.Close()

	if got := f.tg.ran(); len(got) != 4 || got[0] != "docker compose up -d" {
		t.Fatalf("target ran %v", got)
	}
	if f.tg.sawAgent.Load() || f.tg.subsystem.Load() != "sftp" {
		t.Fatal("agent request reached the target or sftp did not")
	}
	f.tg.mu.Lock()
	envs := strings.Join(f.tg.envs, ",")
	f.tg.mu.Unlock()
	if envs != "LANG" {
		t.Fatalf("env passed on: %s", envs)
	}

	// Audit: connect, exec rows with exit codes, sftp, denied requests, disconnect with bytes.
	f.waitAudit(store.SSHAuditDisconnect, "closed")
	if a := f.waitAudit(store.SSHAuditExec, "docker compose up -d"); a.ExitCode == nil || *a.ExitCode != 0 ||
		a.Employee != "dev-1" || a.ServerName != "prod" || a.CertSerial == nil || a.SessionID != dev || a.Role != "dev" {
		t.Fatalf("exec audit %+v", a)
	}
	if a := f.waitAudit(store.SSHAuditExec, "exit 7"); a.ExitCode == nil || *a.ExitCode != 7 {
		t.Fatalf("exit audit %+v", a)
	}
	f.waitAudit(store.SSHAuditExec, "echo *** | sudo -S true")
	f.waitAudit(store.SSHAuditSubsystem, "sftp")
	f.waitAudit(store.SSHAuditDenied, "subsystem:netconf")
	f.waitAudit(store.SSHAuditDenied, "request:auth-agent-req@openssh.com")
	f.waitAudit(store.SSHAuditDenied, "channel:direct-tcpip")
	if len(f.audit(store.SSHAuditConnect)) != 1 {
		t.Fatal("connect audit missing")
	}
	var events int64
	var payload string
	for i := 0; i < 100 && events == 0; i++ {
		_ = f.s.Pool.Gorm(f.ctx).Table("at_events").Where("project_id = ? AND type = ?", f.project, core.EvServer).Count(&events).Error
		time.Sleep(20 * time.Millisecond)
	}
	_ = f.s.Pool.Gorm(f.ctx).Table("at_events").Select("payload::text").Where("project_id = ? AND type = ?", f.project, core.EvServer).
		Scan(&payload).Error
	if events != 1 || !strings.Contains(payload, "dev-1 在 prod 上执行了") {
		t.Fatalf("server event %d %s", events, payload)
	}

	// The target's password is in nothing the node side saw or the console stored in clear.
	certJSON, _ := json.Marshal(id.resp)
	rec.mu.Lock()
	front := rec.buf.String()
	rec.mu.Unlock()
	audits, _ := json.Marshal(f.audit(""))
	for what, text := range map[string]string{"cert_response": string(certJSON), "websocket bytes": front, "audit": string(audits),
		"event": payload, "stdout/stderr": string(out) + stderr.String()} {
		if strings.Contains(text, targetPassword) {
			t.Errorf("the target password shows in %s", what)
		}
	}
}

func ioReadFull(r interface{ Read([]byte) (int, error) }, buf []byte) (int, error) {
	n := 0
	for n < len(buf) {
		m, err := r.Read(buf[n:])
		n += m
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

// TestGatewayRefusesUngrantedSessions: other roles, the lead, read-only roles, other nodes and
// stale generations get no certificate and no WebSocket.
func TestGatewayRefusesUngrantedSessions(t *testing.T) {
	f := newFixture(t)
	ops := f.session("ops-1", "member", "ops", `{}`)
	lead := f.session("lead-1", "lead", "dev", `{}`)
	review := f.session("rev-1", "member", "dev", `{"gate": "review", "read_only": false}`)
	ro := f.session("ro-1", "member", "dev", `{"read_only": true}`)
	dev := f.session("dev-1", "member", "dev", `{}`)
	_, pub := sessionKey(t)
	for sid, want := range map[string]string{ops: "no_servers", lead: "not_granted", review: "not_granted", ro: "not_granted"} {
		if resp := f.certify(sid, 1, pub); resp.Ok || *resp.Error != want {
			t.Errorf("%s certificate: %+v, want %s", sid, resp, want)
		}
		if _, status, _ := f.dialWS(sid, 1, "prod", f.token); status != http.StatusForbidden {
			t.Errorf("%s websocket status %d", sid, status)
		}
	}
	// crashed is terminal: no certificate, no WebSocket.
	crashed := f.session("dev-crashed", "member", "dev", `{}`)
	f.exec(`UPDATE at_agent_sessions SET state = 'crashed' WHERE id = $1`, crashed)
	if resp := f.certify(crashed, 1, pub); resp.Ok || *resp.Error != "session_not_found" {
		t.Errorf("crashed session certificate: %+v", resp)
	}
	if _, status, _ := f.dialWS(crashed, 1, "prod", f.token); status != http.StatusNotFound {
		t.Errorf("crashed session websocket: %d", status)
	}
	if resp := f.certify(dev, 2, pub); resp.Ok || *resp.Error != "stale_open_gen" {
		t.Errorf("stale gen: %+v", resp)
	}
	if resp := f.certify(dev, 1, "ssh-rsa AAAA"); resp.Ok || *resp.Error != "bad_public_key" {
		t.Errorf("bad key: %+v", resp)
	}
	if resp := f.certify("s_missing", 1, pub); resp.Ok || *resp.Error != "session_not_found" {
		t.Errorf("unknown session: %+v", resp)
	}
	if _, status, _ := f.dialWS(dev, 1, "prod", "wrong-token"); status != http.StatusUnauthorized {
		t.Errorf("bad token: %d", status)
	}
	if _, status, _ := f.dialWS(dev, 2, "prod", f.token); status != http.StatusNotFound {
		t.Errorf("stale gen websocket: %d", status)
	}
	if _, status, _ := f.dialWS(dev, 1, "staging", f.token); status != http.StatusForbidden {
		t.Errorf("unknown server: %d", status)
	}
	// Another node of the same user cannot use the session.
	other, err := f.s.CreateNode(f.ctx, f.user, "n2", "hash-n2")
	if err != nil {
		t.Fatal(err)
	}
	if resp := f.g.Sign(f.ctx, other, &protocol.SshCertRequest{SessionID: protocol.SessionID(dev), OpenGen: 1, PublicKey: pub}); resp.Ok {
		t.Error("certificate for a session of another node")
	}
}

// craft makes a certificate for the fixture's session with the given CA and tweaks.
func (f *fixture) craft(sid string, ca ssh.Signer, key ssh.Signer, tweak func(*ssh.Certificate)) identity {
	f.t.Helper()
	// A real certificate first (records a serial), then rewrite and re-sign it.
	pub := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key.PublicKey())))
	resp := f.certify(sid, 1, pub)
	if !resp.Ok {
		f.t.Fatalf("certificate refused: %v", *resp.Error)
	}
	pk, _, _, _, _ := ssh.ParseAuthorizedKey([]byte(*resp.Certificate))
	cert := pk.(*ssh.Certificate)
	if tweak != nil {
		tweak(cert)
	}
	if err := cert.SignCert(rand.Reader, ca); err != nil {
		f.t.Fatal(err)
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(cert)))
	resp.Certificate = &line
	return f.identityFrom(key, resp)
}

// TestGatewayCertificateChecks: every way a front certificate can be wrong is refused and audited.
func TestGatewayCertificateChecks(t *testing.T) {
	f := newFixture(t)
	dev := f.session("dev-1", "member", "dev", `{}`)
	other := f.session("dev-2", "member", "dev", `{}`)
	ca, _, _, _, err := f.g.keys(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := sessionKey(t)
	foreignCA, _ := sessionKey(t)
	now := time.Now()
	cases := []struct {
		name, user, reason string
		id                 func() identity
	}{
		{"plain key", "prod", "not_a_certificate", func() identity {
			id := f.identity(dev)
			id.signer = key // the bare key, no certificate
			return id
		}},
		{"user@ form", "root", "user_must_be_server_name", func() identity { return f.identity(dev) }},
		{"foreign CA", "prod", "bad_certificate", func() identity { return f.craft(dev, foreignCA, key, nil) }},
		{"expired", "prod", "bad_certificate", func() identity {
			return f.craft(dev, ca, key, func(c *ssh.Certificate) {
				c.ValidAfter, c.ValidBefore = uint64(now.Add(-time.Hour).Unix()), uint64(now.Add(-time.Minute).Unix())
			})
		}},
		{"critical option", "prod", "bad_certificate", func() identity {
			return f.craft(dev, ca, key, func(c *ssh.Certificate) { c.CriticalOptions = map[string]string{"force-command": "true"} })
		}},
		{"principal missing", "prod", "bad_certificate", func() identity {
			return f.craft(dev, ca, key, func(c *ssh.Certificate) { c.ValidPrincipals = []string{"staging"} })
		}},
		{"other session's certificate", "prod", "certificate_not_for_this_session", func() identity { return f.identity(other) }},
		{"unknown serial", "prod", "unknown_serial", func() identity {
			return f.craft(dev, ca, key, func(c *ssh.Certificate) { c.Serial = 1 << 40 })
		}},
		{"revoked", "prod", "revoked", func() identity {
			id := f.identity(dev)
			if _, err := f.s.RevokeSessionCerts(f.ctx, dev, "test"); err != nil {
				t.Fatal(err)
			}
			return id
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			id := c.id()
			client, _, err := f.connect(dev, "prod", c.user, id)
			if err == nil {
				_ = client.Close()
				t.Fatal("authenticated")
			}
			f.waitAudit(store.SSHAuditAuthFail, c.reason)
		})
	}
}

// TestGatewayRevocation: a closed session's certificate is revoked and its connection cut; a role
// that loses its grant cannot open new channels and is cut at the next check.
func TestGatewayRevocation(t *testing.T) {
	f := newFixture(t)
	dev := f.session("dev-1", "member", "dev", `{}`)
	id := f.identity(dev)
	client, _, err := f.connect(dev, "prod", "prod", id)
	if err != nil {
		t.Fatal(err)
	}
	sess, _ := client.NewSession()
	var stderr bytes.Buffer
	sess.Stderr = &stderr
	stdin, _ := sess.StdinPipe() // kept open: "sleep" runs until the channel closes
	defer stdin.Close()
	if err := sess.Start("sleep"); err != nil {
		t.Fatal(err)
	}
	// Withdraw the grant: the store revokes the certificate; the gateway is told and cuts.
	serials, err := f.s.SetProjectServerGrants(f.ctx, f.user, f.project, f.server.ID, []string{})
	if err != nil || len(serials) != 1 {
		t.Fatalf("revoked %v %v", serials, err)
	}
	f.g.Revoked(f.ctx, serials)
	done := make(chan error, 1)
	go func() { done <- sess.Wait() }()
	select {
	case err := <-done:
		var exit *ssh.ExitError
		if !errors.As(err, &exit) || exit.ExitStatus() != 255 || !strings.Contains(stderr.String(), "吊销") {
			t.Fatalf("cut connection: %v %q", err, stderr.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("connection not cut after the grant was withdrawn")
	}
	f.waitAudit(store.SSHAuditDisconnect, "吊销")

	// New channels are checked against the grant, not the certificate: re-grant, connect, then
	// withdraw without revoking anything (as if the role became read-only).
	if _, err := f.s.SetProjectServerGrants(f.ctx, f.user, f.project, f.server.ID, []string{"dev"}); err != nil {
		t.Fatal(err)
	}
	id = f.identity(dev)
	client, _, err = f.connect(dev, "prod", "prod", id)
	if err != nil {
		t.Fatal(err)
	}
	f.exec(`UPDATE at_employees SET spec = '{"read_only": true}' WHERE id = (SELECT employee_id FROM at_agent_sessions WHERE id = $1)`, dev)
	if _, err := client.NewSession(); err == nil {
		t.Fatal("new channel opened after the role became read-only")
	}
	f.waitAudit(store.SSHAuditDenied, "无权使用 prod")
	f.exec(`UPDATE at_employees SET spec = '{}' WHERE id = (SELECT employee_id FROM at_agent_sessions WHERE id = $1)`, dev)

	// Session closed: its certificates are revoked in the same transaction, the connection is cut
	// and the old certificate no longer opens anything.
	id = f.identity(dev)
	client, _, err = f.connect(dev, "prod", "prod", id)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.cs.InTx(f.ctx, func(tx *core.Tx) error { return core.CloseSession(f.ctx, tx, dev, "test") }); err != nil {
		t.Fatal(err)
	}
	waitClosed(t, client)
	cert := id.signer.PublicKey().(*ssh.Certificate)
	if row, err := f.s.SSHCert(f.ctx, int64(cert.Serial)); err != nil || row.RevokedAt == nil || row.RevokeReason != "session_closed" {
		t.Fatalf("certificate after close %+v %v", row, err)
	}
	if _, status, _ := f.dialWS(dev, 1, "prod", f.token); status != http.StatusNotFound {
		t.Fatalf("websocket after close: %d", status)
	}
	f.exec(`UPDATE at_agent_sessions SET state = 'active' WHERE id = $1`, dev) // even if it were live again
	if _, _, err := f.connect(dev, "prod", "prod", id); err == nil {
		t.Fatal("revoked certificate accepted")
	}
	f.waitAudit(store.SSHAuditAuthFail, "revoked")

	// open_gen advancing revokes the older generation's certificates.
	id = f.identity(dev)
	if err := f.cs.InTx(f.ctx, func(tx *core.Tx) error { _, err := core.BumpOpenGen(f.ctx, tx, dev); return err }); err != nil {
		t.Fatal(err)
	}
	cert = id.signer.PublicKey().(*ssh.Certificate)
	if row, _ := f.s.SSHCert(f.ctx, int64(cert.Serial)); row.RevokedAt == nil || row.RevokeReason != "open_gen_advanced" {
		t.Fatalf("certificate after gen bump %+v", row)
	}
}

// TestGatewayKillDoesNotWaitForAStuckClient: a channel flooding output to a client that never
// reads (full window) is still cut promptly when its grant goes, and does not hold up the
// re-check of the other connections.
func TestGatewayKillDoesNotWaitForAStuckClient(t *testing.T) {
	f := newFixture(t)
	dev := f.session("dev-1", "member", "dev", `{}`)
	stuck, _, err := f.connect(dev, "prod", "prod", f.identity(dev))
	if err != nil {
		t.Fatal(err)
	}
	sess, _ := stuck.NewSession()
	_, _ = sess.StdinPipe()
	_, _ = sess.StdoutPipe() // never read: the window fills up
	if err := sess.Start("flood"); err != nil {
		t.Fatal(err)
	}
	other := f.session("dev-2", "member", "dev", `{}`)
	second, _, err := f.connect(other, "prod", "prod", f.identity(other))
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond) // let the flood fill every window
	f.exec(`UPDATE at_agent_sessions SET state = 'closed' WHERE id IN ($1, $2)`, dev, other)
	start := time.Now()
	f.g.RecheckAll(context.Background())
	if d := time.Since(start); d > time.Second {
		t.Fatalf("RecheckAll took %s", d)
	}
	waitClosed(t, second)
	waitClosed(t, stuck)
	if d := time.Since(start); d > noticeTimeout+3*time.Second {
		t.Fatalf("cutting a stuck client took %s", d)
	}
}

func waitClosed(t *testing.T, client *ssh.Client) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		_ = client.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("connection not cut")
	}
}

// TestGatewayHostKeyPinning: a changed target host key is refused before any credential is sent;
// an unconfirmed (or forged, MAC-less) host key is refused without dialing.
func TestGatewayHostKeyPinning(t *testing.T) {
	f := newFixture(t)
	dev := f.session("dev-1", "member", "dev", `{}`)
	impostor := newTarget(t, "other")
	f.pinHostKey(f.server, impostor.hostKeyLine())
	id := f.identity(dev)
	client, _, err := f.connect(dev, "prod", "prod", id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.NewSession(); err == nil || !strings.Contains(err.Error(), "主机密钥变了") {
		t.Fatalf("mismatched host key: %v", err)
	}
	if n := f.tg.logins.Load(); n != 0 {
		t.Fatalf("the credential was sent to a host with the wrong key (%d attempts)", n)
	}
	a := f.waitAudit(store.SSHAuditHostKeyMismatch, "host_key_mismatch")
	if a.Detail["got"] != ssh.FingerprintSHA256(f.tg.hostKey.PublicKey()) {
		t.Fatalf("mismatch detail %+v", a.Detail)
	}
	_ = client.Close()

	// A host key written without the console's MAC is not trusted.
	f.exec(`UPDATE at_remote_servers SET host_key = $2, host_key_mac = '\x00' WHERE id = $1`, f.server.ID, f.tg.hostKeyLine())
	client, _, err = f.connect(dev, "prod", "prod", f.identity(dev))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.NewSession(); err == nil || !strings.Contains(err.Error(), "测试连接") {
		t.Fatalf("unconfirmed host key: %v", err)
	}
	f.waitAudit(store.SSHAuditDenied, "host_key_mac_invalid")
	_ = client.Close()
	if n := f.tg.logins.Load(); n != 0 {
		t.Fatalf("dialed the target with an untrusted host key (%d attempts)", n)
	}

	// The target's address is checked again at every dial: without the administrator's allowance
	// loopback is refused before any connection.
	f.pinHostKey(f.server, f.tg.hostKeyLine())
	if err := f.s.DeleteSetting(f.ctx, sshdial.SettingKey); err != nil {
		t.Fatal(err)
	}
	client, _, err = f.connect(dev, "prod", "prod", f.identity(dev))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.NewSession(); err == nil || !strings.Contains(err.Error(), "地址不允许连接") {
		t.Fatalf("loopback target: %v", err)
	}
	f.waitAudit(store.SSHAuditDenied, "address_refused")
	_ = client.Close()
	if n := f.tg.logins.Load(); n != 0 {
		t.Fatalf("dialed a refused address (%d attempts)", n)
	}
	if err := f.s.SaveSetting(f.ctx, sshdial.SettingKey, sshdial.AllowedSetting{CIDRs: []string{"127.0.0.1/32"}}); err != nil {
		t.Fatal(err)
	}

	// Confirmed again: works, also through keyboard-interactive.
	f.pinHostKey(f.server, f.tg.hostKeyLine())
	f.tg.keyboard = true
	client, _, err = f.connect(dev, "prod", "prod", f.identity(dev))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	sess, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	if out, err := sess.Output("uptime"); err != nil || string(out) != "ran: uptime\n" {
		t.Fatalf("keyboard-interactive login %q %v", out, err)
	}
}

// TestGatewayLimitsAndPeriodicRecheck: the per-session limit refuses the fifth connection; the
// periodic check cuts a connection whose session was closed without a hint.
func TestGatewayLimitsAndPeriodicRecheck(t *testing.T) {
	f := newFixture(t)
	dev := f.session("dev-1", "member", "dev", `{}`)
	id := f.identity(dev)
	clients := []*ssh.Client{}
	for i := 0; i < 4; i++ {
		c, _, err := f.connect(dev, "prod", "prod", id)
		if err != nil {
			t.Fatalf("connection %d: %v", i, err)
		}
		clients = append(clients, c)
	}
	if _, status, _ := f.dialWS(dev, 1, "prod", f.token); status != http.StatusTooManyRequests {
		t.Fatalf("fifth connection: %d", status)
	}
	for _, c := range clients[1:] {
		_ = c.Close()
	}
	f.exec(`UPDATE at_agent_sessions SET state = 'closed' WHERE id = $1`, dev) // no revocation, no hint
	f.g.RecheckAll(context.Background())
	waitClosed(t, clients[0])
	f.waitAudit(store.SSHAuditDisconnect, "会话已结束")
}

// TestCertRequestFullSlotsDoNotBlock: with every slot taken the request is dropped at once.
func TestCertRequestFullSlotsDoNotBlock(t *testing.T) {
	for i := 0; i < certInflight; i++ {
		certSlots <- struct{}{}
	}
	defer func() {
		for i := 0; i < certInflight; i++ {
			<-certSlots
		}
	}()
	g := New(Deps{Log: discard()})
	done := make(chan error, 1)
	go func() {
		done <- g.CertRequest(context.Background(), store.Node{ID: "n"}, &protocol.SshCertRequest{ReqID: "r"})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("blocked")
	}
}

func TestNextVersionGrows(t *testing.T) {
	g := New(Deps{Log: discard()})
	last := 0
	for i := 0; i < 100; i++ {
		v := g.nextVersion()
		if v <= last {
			t.Fatalf("version %d after %d", v, last)
		}
		last = v
	}
}

// TestGatewayCertRateLimit: a session gets at most CertsPerMinute certificates a minute.
func TestGatewayCertRateLimit(t *testing.T) {
	f := newFixture(t)
	dev := f.session("dev-1", "member", "dev", `{}`)
	other := f.session("dev-2", "member", "dev", `{}`)
	_, pub := sessionKey(t)
	for i := 0; i < 20; i++ {
		if resp := f.certify(dev, 1, pub); !resp.Ok {
			t.Fatalf("certificate %d refused: %v", i, *resp.Error)
		}
	}
	if resp := f.certify(dev, 1, pub); resp.Ok || *resp.Error != "rate_limited" {
		t.Fatalf("21st certificate: %+v", resp)
	}
	if resp := f.certify(other, 1, pub); !resp.Ok {
		t.Fatal("another session was limited too")
	}
	f.g.now = func() time.Time { return time.Now().Add(61 * time.Second) }
	if resp := f.certify(dev, 1, pub); !resp.Ok {
		t.Fatalf("after a minute: %v", *resp.Error)
	}
}

// TestGatewayChannelCapAndAuditAtStart: a connection holds at most PerConnChans channels; an exec
// is in the audit while it runs (no exit code yet) and completed when it ends.
func TestGatewayChannelCapAndAuditAtStart(t *testing.T) {
	f := newFixture(t)
	dev := f.session("dev-1", "member", "dev", `{}`)
	client, _, err := f.connect(dev, "prod", "prod", f.identity(dev))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	type running struct {
		s  *ssh.Session
		in interface{ Close() error }
	}
	var open []running
	for i := 0; i < 16; i++ {
		sess, err := client.NewSession()
		if err != nil {
			t.Fatalf("channel %d: %v", i, err)
		}
		in, _ := sess.StdinPipe()
		if err := sess.Start("sleep"); err != nil {
			t.Fatal(err)
		}
		open = append(open, running{sess, in})
	}
	if _, err := client.NewSession(); err == nil {
		t.Fatal("17th channel opened")
	}
	f.waitAudit(store.SSHAuditDenied, "too_many_channels")
	// The running execs are already audited, without an exit code.
	rows := f.audit(store.SSHAuditExec)
	if len(rows) != 16 || rows[0].ExitCode != nil || rows[0].Detail["state"] != "running" || rows[0].Command != "sleep" {
		t.Fatalf("audit while running: %d rows, first %+v", len(rows), rows[0])
	}
	for _, r := range open {
		_ = r.in.Close()
		_ = r.s.Wait()
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		rows = f.audit(store.SSHAuditExec)
		done := 0
		for _, r := range rows {
			if r.ExitCode != nil && *r.ExitCode == 0 && r.Detail["state"] == nil && r.DurationMS != nil {
				done++
			}
		}
		if len(rows) == 16 && done == 16 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("audit after the end: %d rows, %d completed", len(rows), done)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// A slot is free again.
	if sess, err := client.NewSession(); err != nil {
		t.Fatalf("channel after others closed: %v", err)
	} else {
		_ = sess.Close()
	}
}
