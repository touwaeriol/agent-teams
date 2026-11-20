package nodegw

import (
	"context"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/platform"
)

// Limits for node.usage (the console keeps only these fields; the decoder already dropped the rest).
const (
	maxUsageWindows = 8
	maxUsageKey     = 64
	maxUsagePlan    = 64
	maxUsageError   = 500
	maxUsageLabel   = 40 // characters
)

// node.usage_refresh (POST /api/nodes/{id}/usage/refresh).
const (
	// UsageCacheTTL: a report received this recently is returned as is, without asking the node.
	UsageCacheTTL = 60 * time.Second
	// UsageRefreshWait is how long a refresh waits for the node's answer before returning stale data.
	UsageRefreshWait = 15 * time.Second
)

// usageWait is one in-flight node.usage_refresh; concurrent refreshes of a node share it.
type usageWait struct {
	done chan struct{} // closed when a node.usage arrives or the wait times out
	got  bool          // a node.usage arrived; guarded by Gateway.mu
}

// recordUsage keeps the node's latest node.usage report (memory only; it survives the link).
func (g *Gateway) recordUsage(nodeID string, b *protocol.NodeUsage, now time.Time) {
	u := sanitizeUsage(b, now)
	g.mu.Lock()
	g.usage[nodeID] = u
	w := g.usageWaits[nodeID]
	if w != nil {
		delete(g.usageWaits, nodeID)
		w.got = true
		close(w.done)
	}
	g.mu.Unlock()
}

func (g *Gateway) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

// usageFresh reports whether the newest runtime report is younger than UsageCacheTTL.
func usageFresh(u *platform.NodeUsage, now time.Time) bool {
	if u == nil {
		return false
	}
	var newest time.Time
	for _, r := range []*platform.RuntimeUsage{u.Claude, u.Codex} {
		if r != nil && r.ReceivedAt.After(newest) {
			newest = r.ReceivedAt
		}
	}
	return !newest.IsZero() && now.Sub(newest) < UsageCacheTTL
}

// RefreshUsage implements platform.NodeRegistry: the cached report when it is fresh, else
// node.usage_refresh and the node's answer (stale = no answer in time). No transaction is held.
func (g *Gateway) RefreshUsage(ctx context.Context, nodeID string) (*platform.NodeUsage, bool, error) {
	l := g.link(nodeID)
	if l == nil {
		return nil, false, platform.ErrNodeOffline
	}
	if !hasFeature(l.hello.Features, "usage") {
		return nil, false, platform.ErrUsageUnsupported
	}
	if cur := g.Usage(nodeID); usageFresh(cur, g.now()) {
		return cur, false, nil
	}
	wait := g.RefreshWait
	if wait <= 0 {
		wait = UsageRefreshWait
	}
	g.mu.Lock()
	w := g.usageWaits[nodeID]
	first := w == nil
	if first {
		// The wait belongs to the node, not to the request that started it: it ends itself after
		// `wait`, so a caller that leaves early (ctx cancelled) never leaves a stale entry behind.
		w = &usageWait{done: make(chan struct{})}
		g.usageWaits[nodeID] = w
		time.AfterFunc(wait, func() { g.endWait(nodeID, w) })
	}
	g.mu.Unlock()
	if first {
		sendCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), wait)
		err := l.sendUnreliable(sendCtx, protocol.TypeNodeUsageRefresh, protocol.NodeUsageRefresh{})
		cancel()
		if err != nil {
			g.log.Warn("node.usage_refresh", "node_id", nodeID, "err", err)
			g.endWait(nodeID, w)
		}
	}
	select {
	case <-w.done:
	case <-ctx.Done():
		return g.Usage(nodeID), true, nil
	}
	g.mu.Lock()
	got := w.got
	g.mu.Unlock()
	return g.Usage(nodeID), !got, nil
}

// endWait gives up on w (timeout or send failure) so the next refresh asks the node again;
// a no-op when w already ended.
func (g *Gateway) endWait(nodeID string, w *usageWait) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.usageWaits[nodeID] == w {
		delete(g.usageWaits, nodeID)
		close(w.done)
	}
}

func hasFeature(features []string, f string) bool {
	for _, x := range features {
		if x == f {
			return true
		}
	}
	return false
}

// Usage returns the node's latest usage report, nil when it never sent one.
func (g *Gateway) Usage(nodeID string) *platform.NodeUsage {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.usage[nodeID]
}

func sanitizeUsage(b *protocol.NodeUsage, now time.Time) *platform.NodeUsage {
	now = now.UTC()
	return &platform.NodeUsage{Claude: sanitizeRuntimeUsage(b.Runtimes.Claude, now),
		Codex: sanitizeRuntimeUsage(b.Runtimes.Codex, now)}
}

func sanitizeRuntimeUsage(r *protocol.RuntimeUsage, now time.Time) *platform.RuntimeUsage {
	if r == nil {
		return nil
	}
	out := &platform.RuntimeUsage{Auth: string(r.Auth), Status: string(r.Status), Windows: []platform.UsageWindow{},
		FetchedAt: utcTime(r.FetchedAt), ReceivedAt: now}
	if r.Plan != nil {
		out.Plan = cleanString(*r.Plan, maxUsagePlan)
	}
	if r.Error != nil {
		out.Error = cleanString(*r.Error, maxUsageError)
	}
	for _, w := range r.Windows {
		if len(out.Windows) == maxUsageWindows {
			break
		}
		key := cleanString(w.Key, maxUsageKey)
		if key == "" {
			continue
		}
		win := platform.UsageWindow{Key: key, Label: cutRunes(cleanString(ptrValue(w.Label), 4*maxUsageLabel), maxUsageLabel), UsedPercent: clampPercent(w.UsedPercent), ResetsAt: utcTime(w.ResetsAt)}
		if w.WindowMinutes != nil && *w.WindowMinutes > 0 {
			win.WindowMinutes = ptr(*w.WindowMinutes)
		}
		out.Windows = append(out.Windows, win)
	}
	return out
}

func clampPercent(v float64) float64 {
	switch {
	case math.IsNaN(v) || v < 0:
		return 0
	case v > 100:
		return 100
	}
	return v
}

func utcTime(t *time.Time) *time.Time {
	if t == nil || t.IsZero() {
		return nil
	}
	return ptr(t.UTC())
}

// cleanString trims, drops control characters and cuts to n bytes on a rune boundary.
func cleanString(s string, n int) string {
	s = strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, strings.ToValidUTF8(s, "")))
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

func ptrValue(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// cutRunes keeps at most n characters.
func cutRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
