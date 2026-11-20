// Package sshgw is the console's SSH gateway (remote-servers.md §4, §5, §6.4, §7.7, §9.4): it signs
// 10-minute session certificates over the node link (ssh.cert_request), terminates the session's
// SSH on /nodes/ssh (a WebSocket per connection, authenticated by the node token, the SSH inside by
// the session certificate), checks the grant again on every channel and every 30 seconds, and logs
// in to the target with the credential that only the console can open. Nothing a node can see ever
// carries the target's password or private key.
package sshgw

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/credvault"
	"github.com/touwaeriol/agent-teams/server/internal/fanout"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// Feature is the hello / welcome feature of daemons that speak ssh.* (protocol.md §8.1).
const Feature = "ssh_gateway"

// Subprotocol is the WebSocket subprotocol of /nodes/ssh.
const Subprotocol = "agent-teams-ssh.v1"

// KnownHostsAlias is the host name the session's ssh_config pins the gateway host key under.
const KnownHostsAlias = "agent-teams-gateway"

// Certificate extensions naming the session (remote-servers.md §4.3).
const (
	extSession = "session@agent-teams.dev"
	extGen     = "gen@agent-teams.dev"
	extNode    = "node@agent-teams.dev"
)

// Config holds the limits and timeouts (remote-servers.md §5.7, simplified: the connection counts
// are per replica, not shared through Valkey).
type Config struct {
	CertTTL        time.Duration // certificate lifetime (10 min)
	ClockSkew      time.Duration // ValidAfter = now - skew (60 s)
	PerSession     int           // simultaneous connections per session (4)
	PerServer      int           // per server (8)
	PerProject     int           // per project (16)
	FrontHandshake time.Duration // node ssh client → gateway: SSH handshake and authentication (15 s)
	BackHandshake  time.Duration // gateway → target: dial, handshake and login (20 s)
	Idle           time.Duration // no data either way (30 min)
	MaxConn        time.Duration // longest connection (6 h)
	Keepalive      time.Duration // keepalive@openssh.com on both sides and WebSocket ping (30 s)
	Recheck        time.Duration // periodic re-check of every connection's grant (30 s)
	// Dial reaches a target (tests; address checks). Default: net.Dialer.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
}

func (c Config) withDefaults() Config {
	def := func(d *time.Duration, v time.Duration) {
		if *d <= 0 {
			*d = v
		}
	}
	defInt := func(n *int, v int) {
		if *n <= 0 {
			*n = v
		}
	}
	def(&c.CertTTL, 10*time.Minute)
	def(&c.ClockSkew, time.Minute)
	defInt(&c.PerSession, 4)
	defInt(&c.PerServer, 8)
	defInt(&c.PerProject, 16)
	def(&c.FrontHandshake, 15*time.Second)
	def(&c.BackHandshake, 20*time.Second)
	def(&c.Idle, 30*time.Minute)
	def(&c.MaxConn, 6*time.Hour)
	def(&c.Keepalive, 30*time.Second)
	def(&c.Recheck, 30*time.Second)
	if c.Dial == nil {
		d := &net.Dialer{}
		c.Dial = d.DialContext
	}
	return c
}

// Deps are what the gateway uses.
type Deps struct {
	Store *store.Store
	Core  *core.Store
	Nodes *nodegw.Gateway
	Fan   *fanout.Hub
	Vault *credvault.Vault
	Log   *slog.Logger
	Cfg   Config
}

// Gateway is the SSH gateway of one console replica.
type Gateway struct {
	store *store.Store
	core  *core.Store
	nodes *nodegw.Gateway
	fan   *fanout.Hub
	vault *credvault.Vault
	log   *slog.Logger
	cfg   Config
	now   func() time.Time

	keyMu   sync.Mutex
	ca      ssh.Signer
	caKey   credvault.SealedKey
	host    ssh.Signer
	hostKey credvault.SealedKey

	mu     sync.Mutex
	conns  map[*conn]struct{}
	counts map[string]int // "s:<session>", "v:<server>", "p:<project>"

	version atomic.Int64 // last ssh.servers_update version handed out
}

// New builds the gateway; keys are loaded (and generated the first time) on first use.
func New(d Deps) *Gateway {
	return &Gateway{store: d.Store, core: d.Core, nodes: d.Nodes, fan: d.Fan, vault: d.Vault, log: d.Log,
		cfg: d.Cfg.withDefaults(), now: time.Now, conns: map[*conn]struct{}{}, counts: map[string]int{}}
}

// ErrUnavailable: the console has no secret key (AGENT_TEAMS_SECRET_KEYS), so it cannot open the
// session CA, the host key or any credential.
var ErrUnavailable = errors.New("ssh gateway unavailable: no secret key configured")

// keys returns the session CA and the gateway host key, generating them on first use (all
// replicas end up with the same pair: credvault.EnsureKey).
func (g *Gateway) keys(ctx context.Context) (ca, host ssh.Signer, caKey, hostKey credvault.SealedKey, err error) {
	g.keyMu.Lock()
	defer g.keyMu.Unlock()
	if g.ca != nil && g.host != nil {
		return g.ca, g.host, g.caKey, g.hostKey, nil
	}
	if !g.vault.Ready() {
		return nil, nil, caKey, hostKey, ErrUnavailable
	}
	if g.ca, g.caKey, err = credvault.EnsureKey(ctx, g.store, g.vault, credvault.SessionCAKey); err != nil {
		g.ca = nil
		return nil, nil, caKey, hostKey, err
	}
	if g.host, g.hostKey, err = credvault.EnsureKey(ctx, g.store, g.vault, credvault.GatewayHostKeyKey); err != nil {
		g.host = nil
		return nil, nil, caKey, hostKey, err
	}
	return g.ca, g.host, g.caKey, g.hostKey, nil
}

// KnownHosts is the known_hosts line of the gateway host key (ssh.cert_response.known_hosts).
func KnownHosts(hostKey credvault.SealedKey) string {
	return KnownHostsAlias + " " + strings.TrimSpace(hostKey.PublicKey)
}

// ---- connection registry and limits ---------------------------------------------------------

// reserve counts a new connection against the limits; ok=false means one of them is reached.
func (g *Gateway) reserve(x store.SSHSession, serverID string) (release func(), ok bool) {
	keys := []string{"s:" + x.SessionID, "v:" + serverID, "p:" + x.ProjectID}
	limits := []int{g.cfg.PerSession, g.cfg.PerServer, g.cfg.PerProject}
	g.mu.Lock()
	defer g.mu.Unlock()
	for i, k := range keys {
		if g.counts[k] >= limits[i] {
			return nil, false
		}
	}
	for _, k := range keys {
		g.counts[k]++
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			defer g.mu.Unlock()
			for _, k := range keys {
				if g.counts[k]--; g.counts[k] <= 0 {
					delete(g.counts, k)
				}
			}
		})
	}, true
}

func (g *Gateway) track(c *conn) {
	g.mu.Lock()
	g.conns[c] = struct{}{}
	g.mu.Unlock()
}

func (g *Gateway) untrack(c *conn) {
	g.mu.Lock()
	delete(g.conns, c)
	g.mu.Unlock()
}

func (g *Gateway) live() []*conn {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]*conn, 0, len(g.conns))
	for c := range g.conns {
		out = append(out, c)
	}
	return out
}

// ---- revocation ---------------------------------------------------------------------------

// revokeMsg is the (informative) body of an ssh:revoke hint.
type revokeMsg struct {
	SessionID string  `json:"session_id,omitempty"`
	Serials   []int64 `json:"serials,omitempty"`
}

// Revoked tells every replica's gateway that certificates were revoked (store functions return the
// serials: server deleted or renamed, removed from a project, a role's grant withdrawn), so their
// connections are cut now instead of at the next periodic check.
func (g *Gateway) Revoked(ctx context.Context, serials []int64) {
	if len(serials) == 0 || g.fan == nil {
		return
	}
	raw, _ := json.Marshal(revokeMsg{Serials: serials})
	g.fan.Publish(ctx, core.SSHRevokeChannel, string(raw))
}

// Run re-checks every connection on each ssh:revoke hint and every Config.Recheck until ctx ends.
func (g *Gateway) Run(ctx context.Context) {
	var hints <-chan string
	if g.fan != nil {
		ch, cancel := g.fan.SubscribePrefix(core.SSHRevokeChannel)
		defer cancel()
		hints = ch
	}
	t := time.NewTicker(g.cfg.Recheck)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			g.closeAll("平台重启，连接中断")
			return
		case <-hints:
		case <-t.C:
		}
		g.RecheckAll(ctx)
	}
}

// RecheckAll re-checks every connection of this replica against the database and cuts the ones
// that may no longer run.
func (g *Gateway) RecheckAll(ctx context.Context) {
	for _, c := range g.live() {
		if reason := c.stillAllowed(ctx); reason != "" {
			c.kill(reason)
		}
	}
}

func (g *Gateway) closeAll(reason string) {
	for _, c := range g.live() {
		c.kill(reason)
	}
}
