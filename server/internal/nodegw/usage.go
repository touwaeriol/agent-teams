package nodegw

import (
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
)

// recordUsage keeps the node's latest node.usage report (memory only; it survives the link).
func (g *Gateway) recordUsage(nodeID string, b *protocol.NodeUsage, now time.Time) {
	u := sanitizeUsage(b, now)
	g.mu.Lock()
	g.usage[nodeID] = u
	g.mu.Unlock()
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
		win := platform.UsageWindow{Key: key, UsedPercent: clampPercent(w.UsedPercent), ResetsAt: utcTime(w.ResetsAt)}
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
