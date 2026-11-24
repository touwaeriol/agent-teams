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
	"net/http"
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
var Features = []string{"sessions", "codex_app_server", "git_worktree", "git_sync", "cred_helper", "upgrade", "file_download", "usage", "ssh_gateway", "self_update", FeatureRuntimeStatus, FeatureContextDetail}

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

	// Now is the clock for the usage cache (tests); nil = time.Now.
	Now func() time.Time
	// RefreshWait overrides UsageRefreshWait and RuntimesRefreshWait (tests); 0 = default.
	RefreshWait time.Duration
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
	return &Gateway{store: st, core: cs, hub: hub, validator: v, log: log, cfg: cfg, links: map[string]*link{},
		usage: map[string]*platform.NodeUsage{}, usageWaits: map[string]*usageWait{},
		runtimesWaits: map[string]*runtimesWait{},
		upgrades:      map[string]*platform.NodeUpgradeState{}}, nil
}

// SetHandler installs the message handler.
func (g *Gateway) SetHandler(h Handler) { g.handler = h }

// ServeWS is GET /nodes/connect (and /nodes/v2/connect).
func (g *Gateway) ServeWS(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionContextTakeover})
	if err != nil {
		return
	}
	conn.SetReadLimit(MaxFrame)
	ctx := context.WithoutCancel(r.Context())
	node, err := g.authenticate(ctx, r)
	if err != nil {
		_ = conn.Close(CloseUnauthorized, "token rejected")
		return
	}
	g.run(ctx, conn, node)
}

// Authenticate identifies a daemon by its Bearer node token (plain HTTP routes for daemons).
func (g *Gateway) Authenticate(r *http.Request) (store.Node, error) {
	return g.authenticate(r.Context(), r)
}

func (g *Gateway) authenticate(ctx context.Context, r *http.Request) (store.Node, error) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || strings.TrimSpace(token) == "" {
		return store.Node{}, errors.New("no token")
	}
	node, err := g.store.NodeByToken(ctx, auth.HashToken(strings.TrimSpace(token)))
	if err != nil {
		return store.Node{}, err
	}
	owner, err := g.store.UserByID(ctx, node.UserID)
	if err != nil || !owner.Active {
		return store.Node{}, errors.New("owner inactive")
	}
	return node, nil
}

func (g *Gateway) run(ctx context.Context, conn *websocket.Conn, node store.Node) {
	hello, err := g.readHello(ctx, conn)
	if err != nil {
		g.log.Info("node handshake failed", "node_id", node.ID, "err", err)
		return
	}
	if !supports(hello.Protocols, 2) {
		_ = conn.Close(CloseUpgrade, "protocol 2 required")
		return
	}
	l, err := g.attach(ctx, conn, node, hello)
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
func (g *Gateway) attach(ctx context.Context, conn *websocket.Conn, node store.Node, hello *protocol.Hello) (*link, error) {
	ackSeq, err := g.recordHello(ctx, node.ID, hello)
	if err != nil {
		return nil, err
	}
	l := newLink(g, conn, node, hello, ackSeq)
	g.mu.Lock()
	old := g.links[node.ID]
	g.links[node.ID] = l
	g.mu.Unlock()
	if old != nil {
		old.close(CloseReplaced, "replaced by a newer link")
	}
	if err := l.welcome(ctx); err != nil {
		return nil, err
	}
	if err := g.markAckedUpTo(ctx, node.ID, int64(hello.AckedCmdSeq)); err != nil {
		return nil, err
	}
	l.sentSeq = int64(hello.AckedCmdSeq)
	return l, nil
}

func (g *Gateway) detach(ctx context.Context, l *link) {
	l.stop()
	g.mu.Lock()
	current := g.links[l.node.ID] == l
	if current {
		delete(g.links, l.node.ID)
	}
	g.mu.Unlock()
	_ = nodeRow(ctx, g.core.Pool, l.node.ID).Update("last_seen_at", gorm.Expr("now()")).Error
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
func (g *Gateway) recordHello(ctx context.Context, nodeID string, hello *protocol.Hello) (int64, error) {
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
			return err
		}
		ackSeq = cur.AckSeq
		if cur.Epoch != hello.Epoch {
			ackSeq = 0
			if err := commands(ctx, tx, nodeID).Where(unacked).Update("status", "expired").Error; err != nil {
				return err
			}
		}
		return nodeRow(ctx, tx, nodeID).Updates(map[string]any{"epoch": hello.Epoch, "ack_seq": ackSeq,
			"last_hello": stored, "isolation": isolation, "last_seen_at": gorm.Expr("now()")}).Error
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
