package nodegw

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// Takeover alerts (#55 C2): one node token used by two daemon instances that keep displacing
// each other is the sign of a stolen token (the real daemon reconnects, the impostor comes back).
// The links are not cut: a false alarm must not take a node offline.
//
// A displacement counts when a new link replaces a registered one and the two hellos carry
// different epochs. The epoch lives in the daemon's state directory (node/src/link/outbox.ts),
// so a restart, a reconnect after a network blip or a half-open old link (same daemon) never
// counts; a reinstall counts once. TakeoverThreshold counting displacements within TakeoverWindow
// raise one alert (audit node_token_takeover + a "notice" user event to the owner), then the node
// stays quiet for TakeoverCooldown.
const (
	TakeoverWindow    = 10 * time.Minute
	TakeoverThreshold = 2
	TakeoverCooldown  = time.Hour
)

type takeoverState struct {
	hits    []time.Time // epoch-changing displacements within the window
	alerted time.Time   // last alert (zero = never)
}

// noteReplacement records that l displaced old and alerts the owner when the rule above trips.
func (g *Gateway) noteReplacement(ctx context.Context, old, l *link) {
	if old == nil || old.hello == nil || l.hello == nil || old.hello.Epoch == l.hello.Epoch {
		return
	}
	now := g.now()
	g.mu.Lock()
	st := g.takeovers[l.node.ID]
	if st == nil {
		st = &takeoverState{}
		g.takeovers[l.node.ID] = st
	}
	kept := st.hits[:0]
	for _, t := range st.hits {
		if now.Sub(t) < TakeoverWindow {
			kept = append(kept, t)
		}
	}
	st.hits = append(kept, now)
	hits := len(st.hits)
	alert := hits >= TakeoverThreshold && (st.alerted.IsZero() || now.Sub(st.alerted) >= TakeoverCooldown)
	if alert {
		st.alerted = now
	}
	g.mu.Unlock()
	g.log.Info("node link displaced by another daemon instance", "node_id", l.node.ID,
		"old_epoch", old.hello.Epoch, "new_epoch", l.hello.Epoch, "old_ip", old.addr, "new_ip", l.addr, "hits", hits)
	if alert {
		g.alertTakeover(context.WithoutCancel(ctx), old, l, hits)
	}
}

func (g *Gateway) alertTakeover(ctx context.Context, old, l *link, hits int) {
	node := l.node
	detail, _ := json.Marshal(map[string]any{"node_name": node.Name, "displacements": hits,
		"window_minutes": int(TakeoverWindow / time.Minute), "old_epoch": old.hello.Epoch, "new_epoch": l.hello.Epoch,
		"old_ip": old.addr, "new_ip": l.addr, "old_host": old.hello.Host, "new_host": l.hello.Host})
	uid := node.UserID
	var ip *string
	if l.addr != "" {
		ip = &l.addr
	}
	if err := g.store.Audit(ctx, store.AuditEntry{UserID: &uid, Actor: "node:" + node.ID, Action: "node_token_takeover",
		TargetType: "node", TargetID: node.ID, Detail: detail, IP: ip}); err != nil {
		g.log.Warn("audit node takeover", "node_id", node.ID, "err", err)
	}
	text := fmt.Sprintf("节点「%s」的令牌正被两个不同的守护进程实例交替使用（%d 分钟内互相顶替 %d 次，来源 %s / %s）。"+
		"如果不是你在操作（例如同时开着两份安装），请立即在节点页重置令牌。", node.Name, int(TakeoverWindow/time.Minute), hits,
		orUnknown(old.addr), orUnknown(l.addr))
	err := g.core.InTx(ctx, func(tx *core.Tx) error {
		_, err := tx.EmitUser(ctx, node.UserID, "notice", map[string]any{"kind": "node_takeover", "node_id": node.ID,
			"node_name": node.Name, "text": text})
		return err
	})
	if err != nil {
		g.log.Warn("notify node takeover", "node_id", node.ID, "err", err)
	}
	g.log.Warn("node token used by two daemon instances", "node_id", node.ID, "hits", hits)
}

func orUnknown(s string) string {
	if s == "" {
		return "未知"
	}
	return s
}
