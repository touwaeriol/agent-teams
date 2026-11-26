// Package nodegw is the node WebSocket gateway (protocol.md): it authenticates
// daemons by token, negotiates protocol 2 (hello/welcome), keeps one link per
// node, persists console commands in at_node_commands (the outbox) with seq,
// acks and replay, acknowledges the daemon's reliable messages after they are
// handled, checks that every message only touches the node's own sessions and
// jobs (§2.3), and feeds the node registry the REST API reads.
package nodegw

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"gorm.io/gorm"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/auth"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/fanout"
	"github.com/touwaeriol/agent-teams/server/internal/platform"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// Close codes (protocol.md §10).
const (
	CloseUnauthorized websocket.StatusCode = 4401
	CloseReplaced     websocket.StatusCode = 4409
	CloseUpgrade      websocket.StatusCode = 4426
	CloseTooFast      websocket.StatusCode = 4429
)

// MaxFrame is the frame size limit (4 MiB).
const MaxFrame = 4 << 20

// Features the console supports (welcome.features is the intersection with hello.features).
var Features = []string{"sessions", "codex_app_server", "git_worktree", "git_sync", "cred_helper", "upgrade", "file_download", "usage", "ssh_gateway", "self_update", FeatureRuntimeStatus, FeatureContextDetail, "fs_mount", "fs_external"}

// Handler receives the messages of authenticated links, one at a time per link.
type Handler interface {
	// Handle processes one daemon message that passed the schema and ownership checks.
	// An error means "not durably processed": the link is closed and the daemon replays it
	// (a deadlock or serialization failure is first run again in place, see link.handle).
	Handle(ctx context.Context, node store.Node, typ string, body any) error
	// NodeOnline runs after the welcome and the command replay.
	NodeOnline(ctx context.Context, node store.Node)
	// NodeOffline runs when the node's link is gone.
	NodeOffline(ctx context.Context, node store.Node)
}

// Config tunes the gateway.
type Config struct {
	ParkAfterS   int
	MinDaemon    string
	PingInterval time.Duration
	HelloTimeout time.Duration
	// Validate checks every inbound frame against the protocol schemas.
	Validate bool
	// AuthFailLimit rejected node tokens per client address within AuthFailWindow before that
	// address gets 429 on every daemon route (0 = DefaultAuthFailLimit, negative = no limit).
	AuthFailLimit  int
	AuthFailWindow time.Duration
}

// Gateway owns the node links.
type Gateway struct {
	store     *store.Store
	core      *core.Store
	hub       *fanout.Hub
	validator *protocol.Validator
	handler   Handler
	log       *slog.Logger
	cfg       Config

	mu            sync.Mutex
	links         map[string]*link
	usage         map[string]*platform.NodeUsage        // latest node.usage per node (memory only)
	usageWaits    map[string]*usageWait                 // in-flight node.usage_refresh per node
	runtimesWaits map[string]*runtimesWait              // in-flight node.runtimes_refresh by command id
	upgrades      map[string]*platform.NodeUpgradeState // last node.upgrade per node (memory only)
	takeovers     map[string]*takeoverState             // displacements by another daemon instance (takeover.go)
	authFails     *authLimiter                          // rejected tokens per client address

	// Now is the clock shared by the usage cache and the takeover alert window (tests); nil = time.Now.
	Now func() time.Time
	// RefreshWait overrides UsageRefreshWait and RuntimesRefreshWait (tests); 0 = default.
	RefreshWait time.Duration
	// AfterRegister runs in attach right after a new link is registered, before its second token
	// check (tests: a revocation landing in that window).
	AfterRegister func(nodeID string)
	// StillValid replaces the token re-check (tests: a database error); nil = stillValid.
	StillValid func(ctx context.Context, q core.DB, nodeID, tokenHash string) (bool, error)
}

// New builds the gateway. handler may be set later with SetHandler (before serving).
func New(st *store.Store, cs *core.Store, hub *fanout.Hub, log *slog.Logger, cfg Config) (*Gateway, error) {
	v, err := protocol.NewValidator()
	if err != nil {
		return nil, err
	}
	if cfg.ParkAfterS == 0 {
		cfg.ParkAfterS = 900
	}
	if cfg.MinDaemon == "" {
		cfg.MinDaemon = "2.0.0"
	}
	if cfg.PingInterval == 0 {
		cfg.PingInterval = 20 * time.Second
	}
	if cfg.HelloTimeout == 0 {
		cfg.HelloTimeout = 15 * time.Second
	}
	if cfg.AuthFailLimit == 0 {
		cfg.AuthFailLimit = DefaultAuthFailLimit
	}
	if cfg.AuthFailWindow == 0 {
		cfg.AuthFailWindow = DefaultAuthFailWindow
	}
	return &Gateway{authFails: newAuthLimiter(cfg.AuthFailLimit, cfg.AuthFailWindow, nil), store: st, core: cs, hub: hub, validator: v, log: log, cfg: cfg, links: map[string]*link{},
		usage: map[string]*platform.NodeUsage{}, usageWaits: map[string]*usageWait{},
		runtimesWaits: map[string]*runtimesWait{},
		upgrades:      map[string]*platform.NodeUpgradeState{}, takeovers: map[string]*takeoverState{}}, nil
}

// SetHandler installs the message handler.
func (g *Gateway) SetHandler(h Handler) { g.handler = h }

// Authentication outcomes besides success.
var (
	// ErrTokenRejected: no token, an unknown one, or one of an inactive owner (counted per address).
	ErrTokenRejected = errors.New("node token rejected")
	// ErrAuthThrottled: the client address used up its rejections; the token was not looked at.
	ErrAuthThrottled = errors.New("too many rejected node tokens")
)

// ServeWS is GET /nodes/connect (and /nodes/v2/connect). The token is checked before the
// upgrade: a rejected one gets a plain 401 (no reason), an address with too many rejections a
// 429, a database failure a 503; only an authenticated daemon gets the 101.
func (g *Gateway) ServeWS(w http.ResponseWriter, r *http.Request) {
	ctx := context.WithoutCancel(r.Context())
	node, tokenHash, err := g.authenticateHash(ctx, r)
	if err != nil {
		WriteAuthError(w, err)
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionContextTakeover})
	if err != nil {
		return
	}
	conn.SetReadLimit(MaxFrame)
	g.run(ctx, conn, node, tokenHash, remoteAddr(r))
}

// AuthStatus maps a failed Authenticate to its HTTP answer: 429 (setting Retry-After on w) when
// throttled, 401 for a rejected token, 503 otherwise. code is a short reason that never says why
// a token was rejected; every daemon HTTP route answers through it, whatever its body format.
func AuthStatus(w http.ResponseWriter, err error) (status int, code string) {
	var t *throttled
	if errors.As(err, &t) {
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(t.wait.Seconds()))))
		return http.StatusTooManyRequests, "too_many_requests"
	}
	if errors.Is(err, ErrTokenRejected) {
		return http.StatusUnauthorized, "unauthorized"
	}
	return http.StatusServiceUnavailable, "unavailable"
}

// WriteAuthError answers a failed Authenticate on a daemon HTTP route with a plain-text body
// (AuthStatus).
func WriteAuthError(w http.ResponseWriter, err error) {
	status, code := AuthStatus(w, err)
	http.Error(w, strings.ReplaceAll(code, "_", " "), status)
}

type throttled struct{ wait time.Duration }

func (t *throttled) Error() string { return ErrAuthThrottled.Error() }
func (t *throttled) Unwrap() error { return ErrAuthThrottled }

// Authenticate identifies a daemon by its Bearer node token (plain HTTP routes for daemons).
// Errors: ErrAuthThrottled, ErrTokenRejected, or a store error (see WriteAuthError).
func (g *Gateway) Authenticate(r *http.Request) (store.Node, error) {
	return g.authenticate(r.Context(), r)
}

// AuthenticateHash is Authenticate that also returns the token hash that matched, for a
// long-lived connection to check again later with TokenStillValid (the SSH gateway, #55 L13).
func (g *Gateway) AuthenticateHash(r *http.Request) (store.Node, string, error) {
	return g.authenticateHash(r.Context(), r)
}

// TokenStillValid reports whether the node still exists, still has the token hash it
// authenticated with (not rotated) and its owner is still active.
func (g *Gateway) TokenStillValid(ctx context.Context, nodeID, tokenHash string) (bool, error) {
	return stillValid(ctx, g.core.Pool, nodeID, tokenHash)
}

func (g *Gateway) authenticate(ctx context.Context, r *http.Request) (store.Node, error) {
	node, _, err := g.authenticateHash(ctx, r)
	return node, err
}

// authenticateHash also returns the token hash that matched (the WebSocket checks it again at hello).
func (g *Gateway) authenticateHash(ctx context.Context, r *http.Request) (store.Node, string, error) {
	addr := remoteAddr(r)
	if blocked, wait := g.authFails.blocked(addr); blocked {
		return store.Node{}, "", &throttled{wait: wait}
	}
	node, hash, err := g.checkToken(ctx, r)
	if errors.Is(err, ErrTokenRejected) {
		g.authFails.fail(addr)
		g.log.Info("node token rejected", "ip", addr, "path", r.URL.Path)
	}
	return node, hash, err
}

func (g *Gateway) checkToken(ctx context.Context, r *http.Request) (store.Node, string, error) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || strings.TrimSpace(token) == "" {
		return store.Node{}, "", ErrTokenRejected
	}
	hash := auth.HashToken(strings.TrimSpace(token))
	node, err := g.store.NodeByToken(ctx, hash)
	if errors.Is(err, store.ErrNotFound) {
		return store.Node{}, "", ErrTokenRejected
	}
	if err != nil {
		return store.Node{}, "", err
	}
	owner, err := g.store.UserByID(ctx, node.UserID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && !owner.Active) {
		return store.Node{}, "", ErrTokenRejected
	}
	if err != nil {
		return store.Node{}, "", err
	}
	return node, hash, nil
}

// errRevoked: between the upgrade and the hello the token was rotated, the node deleted or its
// owner disabled (#55 N2). The link is closed with 4401 like a revoked live link.
var errRevoked = errors.New("node token revoked before hello")

// stillValid re-reads the node row (q may be a transaction holding it FOR UPDATE): the node
// exists, still has tokenHash and its owner is active.
func (g *Gateway) stillValid(ctx context.Context, q core.DB, nodeID, tokenHash string) (bool, error) {
	if g.StillValid != nil {
		return g.StillValid(ctx, q, nodeID, tokenHash)
	}
	return stillValid(ctx, q, nodeID, tokenHash)
}

func stillValid(ctx context.Context, q core.DB, nodeID, tokenHash string) (bool, error) {
	var row struct {
		TokenHash string
		Active    bool
	}
	err := core.MapErr(q.Gorm(ctx).Table("at_nodes n").Select("n.token_hash, u.active").
		Joins("JOIN at_users u ON u.id = n.user_id").Where("n.id = ?", nodeID).Take(&row).Error)
	if err == core.ErrNotFound {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return row.Active && row.TokenHash == tokenHash, nil
}

func (g *Gateway) run(ctx context.Context, conn *websocket.Conn, node store.Node, tokenHash, addr string) {
	hello, err := g.readHello(ctx, conn)
	if err != nil {
		g.log.Info("node handshake failed", "node_id", node.ID, "err", err)
		return
	}
	if !supports(hello.Protocols, 2) {
		_ = conn.Close(CloseUpgrade, "protocol 2 required")
		return
	}
	l, err := g.attach(ctx, conn, node, tokenHash, addr, hello)
	if errors.Is(err, errRevoked) {
		g.log.Info("node token revoked before hello", "node_id", node.ID)
		_ = conn.Close(CloseUnauthorized, "token rejected")
		return
	}
	if err != nil {
		g.log.Warn("node attach failed", "node_id", node.ID, "err", err)
		_ = conn.Close(websocket.StatusInternalError, "attach failed")
		return
	}
	defer g.detach(context.Background(), l)
	l.serve(ctx)
}

func (g *Gateway) readHello(ctx context.Context, conn *websocket.Conn) (*protocol.Hello, error) {
	rctx, cancel := context.WithTimeout(ctx, g.cfg.HelloTimeout)
	defer cancel()
	_, frame, err := conn.Read(rctx)
	if err != nil {
		return nil, err
	}
	var env protocol.Envelope
	if err := json.Unmarshal(frame, &env); err != nil || env.Type != protocol.TypeHello {
		_ = conn.Close(websocket.StatusPolicyViolation, "hello expected")
		return nil, errors.New("first frame is not hello")
	}
	var hello protocol.Hello
	if err := json.Unmarshal(env.Body, &hello); err != nil {
		_ = conn.Close(websocket.StatusPolicyViolation, "bad hello")
		return nil, err
	}
	return &hello, nil
}

func supports(list []int, v int) bool {
	for _, p := range list {
		if p == v {
			return true
		}
	}
	return false
}

// attach records the hello, replaces an older link, sends welcome and replays commands.
// The token is checked again twice (#55 N2): in recordHello's transaction (the node row is locked)
// and once the link is registered, so a rotation, node deletion or owner cut-off that commits
// after the first check either finds this link through Disconnect or is seen by the second one.
func (g *Gateway) attach(ctx context.Context, conn *websocket.Conn, node store.Node, tokenHash, addr string, hello *protocol.Hello) (*link, error) {
	ackSeq, err := g.recordHello(ctx, node.ID, tokenHash, hello)
	if err != nil {
		return nil, err
	}
	l := newLink(g, conn, node, hello, ackSeq)
	l.tokenHash, l.addr = tokenHash, addr
	g.mu.Lock()
	old := g.links[node.ID]
	g.links[node.ID] = l
	g.mu.Unlock()
	if g.AfterRegister != nil {
		g.AfterRegister(node.ID)
	}
	if ok, err := g.stillValid(ctx, g.core.Pool, node.ID, tokenHash); err != nil || !ok {
		g.unregister(ctx, l, old)
		if err != nil {
			return nil, err
		}
		return nil, errRevoked
	}
	if old != nil {
		old.close(CloseReplaced, "replaced by a newer link")
		g.noteReplacement(ctx, old, l)
	}
	if err := l.welcome(ctx); err != nil {
		g.detach(context.WithoutCancel(ctx), l) // registered: take it out (and report the node offline)
		return nil, err
	}
	if err := g.markAckedUpTo(ctx, node.ID, int64(hello.AckedCmdSeq)); err != nil {
		g.detach(context.WithoutCancel(ctx), l)
		return nil, err
	}
	l.sentSeq = int64(hello.AckedCmdSeq)
	return l, nil
}

// unregister takes back a link attach registered but refused: the link it displaced (not yet
// told it was replaced) goes back unless it ended meanwhile; then the node is offline. A link put
// back has its own token checked again once it is registered: the revocation that refused l may
// have run Disconnect while l, not old, was the registered link (reviewer-2, #55 N2).
func (g *Gateway) unregister(ctx context.Context, l, old *link) {
	l.stop()
	g.mu.Lock()
	restored, offline := false, false
	if g.links[l.node.ID] == l {
		if old != nil && old.ctx.Err() == nil {
			g.links[l.node.ID], restored = old, true
		} else {
			delete(g.links, l.node.ID)
			offline = old != nil // old's own detach saw another link and skipped NodeOffline
		}
	}
	g.mu.Unlock()
	if restored {
		// Registered again, so its detach takes it out and reports the node offline. A revoked
		// token gets 4401 (the daemon waits 10 minutes); a failed check only an internal error,
		// which the daemon treats as a dropped link and reconnects with backoff.
		ok, err := g.stillValid(ctx, g.core.Pool, old.node.ID, old.tokenHash)
		switch {
		case err != nil:
			g.log.Warn("re-check of a restored node link failed", "node_id", old.node.ID, "err", err)
			old.close(websocket.StatusInternalError, "try again")
		case !ok:
			old.close(CloseUnauthorized, "token rejected")
		}
		return
	}
	if offline && g.handler != nil {
		g.handler.NodeOffline(ctx, l.node)
	}
}

func (g *Gateway) detach(ctx context.Context, l *link) {
	l.stop()
	g.mu.Lock()
	current := g.links[l.node.ID] == l
	if current {
		delete(g.links, l.node.ID)
	}
	g.mu.Unlock()
	seen := map[string]any{"last_seen_at": gorm.Expr("now()")}
	if current {
		seen["fs_status"] = gorm.Expr("'{}'::jsonb") // an offline node mounts nothing we can count on
	}
	_ = nodeRow(ctx, g.core.Pool, l.node.ID).Updates(seen).Error
	if current && g.handler != nil {
		g.handler.NodeOffline(ctx, l.node)
	}
}

func (g *Gateway) link(nodeID string) *link {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.links[nodeID]
}

// recordHello stores the hello and handles an epoch change; returns the ack seq for welcome.
func (g *Gateway) recordHello(ctx context.Context, nodeID, tokenHash string, hello *protocol.Hello) (int64, error) {
	stored, _ := json.Marshal(helloSummary(hello))
	isolation := "none"
	if hello.Isolation != nil {
		isolation = string(*hello.Isolation)
	}
	var ackSeq int64
	err := g.core.InTx(ctx, func(tx *core.Tx) error {
		var cur struct {
			Epoch  string
			AckSeq int64
		}
		if err := nodeRow(ctx, tx, nodeID).Select("epoch, ack_seq").Clauses(core.ForUpdate).Take(&cur).Error; err != nil {
			if core.MapErr(err) == core.ErrNotFound {
				return errRevoked // deleted since the upgrade
			}
			return err
		}
		if ok, err := g.stillValid(ctx, tx, nodeID, tokenHash); err != nil || !ok {
			if err != nil {
				return err
			}
			return errRevoked
		}
		ackSeq = cur.AckSeq
		if cur.Epoch != hello.Epoch {
			ackSeq = 0
			if err := commands(ctx, tx, nodeID).Where(unacked).Update("status", "expired").Error; err != nil {
				return err
			}
		}
		// fs_status starts over: the daemon reports its mount again after the welcome.
		return nodeRow(ctx, tx, nodeID).Updates(map[string]any{"epoch": hello.Epoch, "ack_seq": ackSeq,
			"last_hello": stored, "isolation": isolation, "last_seen_at": gorm.Expr("now()"), "fs_status": gorm.Expr("'{}'::jsonb")}).Error
	})
	return ackSeq, err
}

// nodeRow selects one at_nodes row for a read or write.
func nodeRow(ctx context.Context, q core.DB, nodeID string) *gorm.DB {
	return q.Gorm(ctx).Table("at_nodes").Where("id = ?", nodeID)
}

// helloSummary is the part of hello kept in at_nodes.last_hello (NodeView while offline).
func helloSummary(h *protocol.Hello) map[string]any {
	return map[string]any{"version": releaseOf(h.Daemon), "protocol": 2, "runtimes": h.Runtimes, "capacity": h.Capacity,
		"features": h.Features, "host": h.Host, "root": h.Root, "isolation": h.Isolation}
}

func (g *Gateway) welcomeFeatures(hello *protocol.Hello) []string {
	out := []string{}
	for _, f := range Features {
		for _, h := range hello.Features {
			if f == h {
				out = append(out, f)
			}
		}
	}
	return out
}
