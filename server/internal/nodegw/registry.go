package nodegw

import (
	"context"
	"encoding/json"
	"time"

	"github.com/coder/websocket"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/platform"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

var _ platform.NodeRegistry = (*Gateway)(nil)

// Online reports whether the node has a live link.
func (g *Gateway) Online(nodeID string) bool { return g.link(nodeID) != nil }

// Status implements platform.NodeRegistry.
func (g *Gateway) Status(nodeID string) platform.NodeLive {
	l := g.link(nodeID)
	if l == nil {
		return platform.NodeLive{}
	}
	l.mu.Lock()
	stats := l.stats
	l.mu.Unlock()
	since := l.since
	live := platform.NodeLive{Online: true, Since: &since, Version: l.hello.Daemon.Version, Protocol: 2,
		Runtimes: toMap(l.hello.Runtimes), Capacity: toMap(l.hello.Capacity), ActiveSessions: stats.ActiveSessions,
		ParkedSessions: stats.ParkedSessions, Queued: stats.Queued}
	if l.hello.Isolation != nil {
		live.Isolation = string(*l.hello.Isolation)
	}
	return live
}

// Capacity is the node's max_active_sessions (setting, else hello, else 4).
func (g *Gateway) Capacity(node store.Node) int {
	if node.MaxActiveSessions != nil {
		return *node.MaxActiveSessions
	}
	if l := g.link(node.ID); l != nil && l.hello.Capacity != nil && l.hello.Capacity.MaxActiveSessions != nil {
		return *l.hello.Capacity.MaxActiveSessions
	}
	return 4
}

func toMap(v any) map[string]any {
	raw, _ := json.Marshal(v)
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return out
}

// Disconnect implements platform.NodeRegistry (token rotated, node or owner removed).
func (g *Gateway) Disconnect(_ context.Context, nodeID, reason string) {
	if l := g.link(nodeID); l != nil {
		l.close(CloseUnauthorized, truncate(reason, 100))
	}
}

// Configure implements platform.NodeRegistry: node.config with the new settings.
func (g *Gateway) Configure(ctx context.Context, node store.Node) {
	body := protocol.NodeConfig{MaxActiveSessions: node.MaxActiveSessions, ParkAfterS: node.ParkAfterS, Isolated: &node.Isolated}
	if err := g.send(ctx, node.ID, Command{Type: protocol.TypeNodeConfig, Body: body}); err != nil {
		g.log.Warn("node.config", "node_id", node.ID, "err", err)
	}
}

// Upgrade implements platform.NodeRegistry.
func (g *Gateway) Upgrade(ctx context.Context, nodeID string, r platform.NodeRelease) error {
	if !g.Online(nodeID) {
		return platform.ErrNodeOffline
	}
	return g.send(ctx, nodeID, Command{Type: protocol.TypeNodeUpgrade,
		Body: map[string]string{"version": r.Version, "sha256": r.SHA256, "signature": r.Signature}})
}

// Drain implements platform.NodeRegistry.
func (g *Gateway) Drain(ctx context.Context, nodeID string, deadline time.Duration) error {
	if !g.Online(nodeID) {
		return platform.ErrNodeOffline
	}
	return g.send(ctx, nodeID, Command{Type: protocol.TypeNodeDrain, Body: protocol.NodeDrain{DeadlineS: int(deadline.Seconds())}})
}

// send enqueues one command in its own transaction.
func (g *Gateway) send(ctx context.Context, nodeID string, cmd Command) error {
	return g.core.InTx(ctx, func(tx *core.Tx) error {
		_, err := g.Enqueue(ctx, tx, nodeID, cmd)
		return err
	})
}

// CloseAll closes every link (shutdown).
func (g *Gateway) CloseAll() {
	g.mu.Lock()
	links := make([]*link, 0, len(g.links))
	for _, l := range g.links {
		links = append(links, l)
	}
	g.mu.Unlock()
	for _, l := range links {
		l.close(websocket.StatusNormalClosure, "console stopping")
	}
}
