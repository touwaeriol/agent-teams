package roles

import (
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// MinContextLimit is the smallest context limit a role may set (tokens).
const MinContextLimit = 10000

// MaxFocus is how long compact_focus may be (characters).
const MaxFocus = 2000

// DevFocus is what a writing member's compaction keeps by default (shared-members.md §5.2).
const DevFocus = "保留当前任务 id、分支、改过的文件、做过的决定、还没做完的事和已知的坑"

// ContextPolicy is a role's session-management settings (shared-members.md §5.1).
type ContextPolicy struct {
	Limit      *int
	BetweenPct *int
	InTaskPct  *int
	ClearAfter *int
	Focus      string
}

// ContextDefaults are the settings a role of this kind starts with: writing members 60 / 80 / 8,
// review and decision gates — / 80 / 1 (every task in a fresh session), lead and coordinator — / 70 / —.
func ContextDefaults(tier, gate string) ContextPolicy {
	switch {
	case tier != Member:
		return ContextPolicy{InTaskPct: ptr(70)}
	case gate == "review" || gate == "decision":
		return ContextPolicy{InTaskPct: ptr(80), ClearAfter: ptr(1)}
	}
	return ContextPolicy{BetweenPct: ptr(60), InTaskPct: ptr(80), ClearAfter: ptr(8), Focus: DevFocus}
}

// PolicyOf reads a role's settings.
func PolicyOf(r store.Role) ContextPolicy {
	return ContextPolicy{Limit: r.ContextLimit, BetweenPct: r.CompactBetweenPct, InTaskPct: r.CompactInTaskPct,
		ClearAfter: r.ClearAfterTasks, Focus: r.CompactFocus}
}

func (p ContextPolicy) apply(r *store.Role) {
	r.ContextLimit, r.CompactBetweenPct, r.CompactInTaskPct, r.ClearAfterTasks, r.CompactFocus =
		p.Limit, p.BetweenPct, p.InTaskPct, p.ClearAfter, p.Focus
}

func (p ContextPolicy) unset() bool {
	return p.Limit == nil && p.BetweenPct == nil && p.InTaskPct == nil && p.ClearAfter == nil && p.Focus == ""
}

// normalizeContext gives a new role without any setting its kind's defaults; the lead and the
// coordinator never claim queued tasks, so they have no between-task compaction or clear count.
func normalizeContext(r *store.Role) {
	if r.ID == "" && PolicyOf(*r).unset() {
		ContextDefaults(r.Tier, r.Gate).apply(r)
	}
	if r.Tier != Member {
		r.CompactBetweenPct, r.ClearAfterTasks = nil, nil
	}
}

func contextChecks(r *store.Role) []check {
	return []check{
		{"context_limit", r.ContextLimit != nil && *r.ContextLimit < MinContextLimit, "上下文上限至少 10000 token"},
		{"compact_between_pct", badPct(r.CompactBetweenPct), "领任务时压缩的比例在 1 到 100 之间"},
		{"compact_in_task_pct", badPct(r.CompactInTaskPct), "任务中自动压缩的比例在 1 到 100 之间"},
		{"clear_after_tasks", r.ClearAfterTasks != nil && *r.ClearAfterTasks < 0, "连续任务数不能是负数（0 = 不按次数清空）"},
		{"compact_focus", len([]rune(r.CompactFocus)) > MaxFocus, "压缩重点最多 2000 字"},
	}
}

func badPct(p *int) bool { return p != nil && (*p < 1 || *p > 100) }

func ptr(n int) *int { return &n }

// yamlContext is a role's context: block in the YAML view.
type yamlContext struct {
	Limit             *int   `yaml:"limit,omitempty"`
	CompactBetweenPct *int   `yaml:"compact_between_pct,omitempty"`
	CompactInTaskPct  *int   `yaml:"compact_in_task_pct,omitempty"`
	ClearAfterTasks   *int   `yaml:"clear_after_tasks,omitempty"`
	Focus             string `yaml:"focus,omitempty"`
}

// contextFromYAML: a role without a context: block gets its kind's defaults.
func contextFromYAML(y *yamlContext, tier, gate string) ContextPolicy {
	if y == nil {
		return ContextDefaults(tier, gate)
	}
	return ContextPolicy{Limit: y.Limit, BetweenPct: y.CompactBetweenPct, InTaskPct: y.CompactInTaskPct,
		ClearAfter: y.ClearAfterTasks, Focus: y.Focus}
}

func contextToYAML(r store.Role) *yamlContext {
	p := PolicyOf(r)
	if p.unset() {
		return nil
	}
	return &yamlContext{Limit: p.Limit, CompactBetweenPct: p.BetweenPct, CompactInTaskPct: p.InTaskPct,
		ClearAfterTasks: p.ClearAfter, Focus: p.Focus}
}
