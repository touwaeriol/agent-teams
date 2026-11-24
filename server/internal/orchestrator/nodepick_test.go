package orchestrator

import (
	"slices"
	"testing"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/platform"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// roomy is a usable pair with free slots; at 95% or more its (weekly) quota is tight.
func roomy(id string, used float64) nodeState {
	s := nodeState{id: id, usable: true, free: 1 << 20, used: used}
	if used >= store.DefaultQuotaWeeklyPct {
		s.tight = "weekly"
	}
	return s
}

// TestChooseNode: a new session goes to the usable pair with the lowest load score (used quota
// plus SessionWeight per live session), passing over nodes whose quota is tight while another is
// not, wherever the employee was before; it stays only when it must and its node is still usable
// with a free slot and quota under QuotaHardFull.
func TestChooseNode(t *testing.T) {
	cases := []struct {
		name    string
		current string
		cands   []nodeState
		stay    bool
		want    string
	}{
		{"a newer, idle node takes a member off its old home", "a", []nodeState{roomy("a", 40), roomy("b", 0)}, false, "b"},
		{"ties keep the current node", "b", []nodeState{roomy("a", 10), roomy("b", 10)}, false, "b"},
		{"ties without a current node: most free slots", "", []nodeState{{id: "a", usable: true, free: 2, used: 10}, {id: "b", usable: true, free: 3, used: 10}}, false, "b"},
		{"stay keeps a busier node", "a", []nodeState{roomy("a", 60), roomy("b", 0)}, true, "a"},
		{"stay leaves a node out of quota", "a", []nodeState{roomy("a", 97), roomy("b", 80)}, true, "b"},
		{"stay leaves an offline node", "a", []nodeState{{id: "a"}, roomy("b", 80)}, true, "b"},
		{"stay leaves a node that is no candidate any more", "x", []nodeState{roomy("b", 80)}, true, "b"},
		{"a full node loses to any node with a free slot", "", []nodeState{{id: "a", usable: true, free: 0, used: 0}, roomy("b", 90)}, false, "b"},
		{"all out of quota: the least used", "", []nodeState{roomy("a", 99), roomy("b", 96)}, false, "b"},
		{"runtime not logged in: never picked", "", []nodeState{{id: "a", free: 1 << 20}, roomy("b", 70)}, false, "b"},
		{"nothing usable: wait for the current node", "a", []nodeState{{id: "a"}, {id: "b"}}, false, "a"},
		{"nothing usable, no current node: the first candidate", "", []nodeState{{id: "a"}, {id: "b"}}, false, "a"},
	}
	for _, c := range cases {
		if got := chooseNode(nodeState{id: c.current}, c.cands, c.stay).id; got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}

// TestChooseNodeSpreads: the load score (used + SessionWeight × active) spreads new sessions over
// the usable nodes instead of sending all of them to the least used one.
func TestChooseNodeSpreads(t *testing.T) {
	busy := func(id string, used float64, active int) nodeState {
		s := roomy(id, used)
		s.active, s.free = active, s.free-active
		return s
	}
	// Two nodes with the same quota: the first session takes a, the second (a now has one live
	// session) goes to b, the third back to a.
	cands := []nodeState{busy("a", 20, 0), busy("b", 20, 0)}
	var got []string
	for range 3 {
		pick := chooseNode(nodeState{}, cands, false)
		got = append(got, pick.id)
		for i := range cands {
			if cands[i].id == pick.id {
				cands[i].active++
				cands[i].free--
			}
		}
	}
	if want := []string{"a", "b", "a"}; !slices.Equal(got, want) {
		t.Errorf("same quota: got %v, want %v", got, want)
	}
	cases := []struct {
		name    string
		current string
		cands   []nodeState
		stay    bool
		want    string
	}{
		{"0% with 3 live sessions (30) loses to 20% with none (20)", "", []nodeState{busy("a", 0, 3), busy("b", 20, 0)}, false, "b"},
		{"the less used node still gets more: 0% with 1 session beats 20%", "", []nodeState{busy("a", 0, 1), busy("b", 20, 0)}, false, "a"},
		{"a full node stays last however idle", "", []nodeState{busy("a", 96, 0), busy("b", 60, 5)}, false, "b"},
		{"a node without free slots stays last", "", []nodeState{{id: "a", usable: true, free: 0, used: 0, active: 2}, busy("b", 80, 4)}, false, "b"},
		{"stay keeps the employee on a busier node", "a", []nodeState{busy("a", 30, 4), busy("b", 0, 0)}, true, "a"},
		{"equal scores keep the current node", "b", []nodeState{busy("a", 10, 1), busy("b", 20, 0)}, false, "b"},
		{"equal scores, no current node: more free slots", "", []nodeState{busy("a", 10, 1), busy("b", 20, 0)}, false, "b"},
		{"all equal: the first candidate (list order)", "", []nodeState{busy("a", 20, 1), busy("b", 20, 1)}, false, "a"},
	}
	for _, c := range cases {
		if got := chooseNode(nodeState{id: c.current}, c.cands, c.stay).id; got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}

// TestQuotaUsed: the fullest window that applies to the model and has not reset yet.
func TestQuotaUsed(t *testing.T) {
	now := time.Date(2026, 11, 21, 8, 0, 0, 0, time.UTC)
	later, past := now.Add(time.Hour), now.Add(-time.Minute)
	claude := &platform.RuntimeUsage{Auth: "subscription", Status: "ok", Windows: []platform.UsageWindow{
		{Key: "five_hour", UsedPercent: 30, ResetsAt: &later},
		{Key: "weekly", UsedPercent: 20, ResetsAt: &later},
		{Key: "weekly_model", Label: "Fable", UsedPercent: 90, ResetsAt: &later},
		{Key: "weekly_opus", UsedPercent: 70, ResetsAt: &past},
	}}
	cases := []struct {
		name  string
		ru    *platform.RuntimeUsage
		model string
		want  float64
	}{
		{"opus: 5 h and weekly; the opus window has reset", claude, "claude-opus-5-5", 30},
		{"fable: its weekly window counts", claude, "claude-fable-5-2", 90},
		{"no report", nil, "claude-opus-5-5", unknownUsage},
		{"failed read", &platform.RuntimeUsage{Auth: "subscription", Status: "error"}, "claude-opus-5-5", unknownUsage},
		{"codex: every window", &platform.RuntimeUsage{Auth: "chatgpt", Status: "ok", Windows: []platform.UsageWindow{
			{Key: "primary", UsedPercent: 12}, {Key: "secondary", UsedPercent: 64}}}, "", 64},
	}
	for _, c := range cases {
		if got := quotaUsed(c.ru, c.model, now); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
	if loggedIn(&platform.RuntimeUsage{Auth: "none"}) || !loggedIn(nil) || !loggedIn(claude) {
		t.Error("loggedIn")
	}
}

// TestChooseRuntime: a role with several runtimes balances them by load score, over every
// runtime's candidate nodes; stay keeps the current runtime too.
func TestChooseRuntime(t *testing.T) {
	pair := func(rt, id string, used float64) nodeState {
		s := roomy(id, used)
		s.rt = rt
		return s
	}
	cases := []struct {
		name    string
		current nodeState
		cands   []nodeState
		stay    bool
		want    nodeState
	}{
		{"Claude busier than Codex: Codex", nodeState{id: "a", rt: "claude"},
			[]nodeState{pair("claude", "a", 70), pair("codex", "b", 20)}, false, nodeState{id: "b", rt: "codex"}},
		{"same node, both runtimes: the one with quota left", nodeState{},
			[]nodeState{pair("claude", "a", 96), pair("codex", "a", 90)}, false, nodeState{id: "a", rt: "codex"}},
		{"tie keeps the current runtime on its node", nodeState{id: "a", rt: "codex"},
			[]nodeState{pair("claude", "a", 30), pair("codex", "a", 30)}, false, nodeState{id: "a", rt: "codex"}},
		{"stay keeps the runtime even when the other has more left", nodeState{id: "a", rt: "claude"},
			[]nodeState{pair("claude", "a", 60), pair("codex", "a", 0)}, true, nodeState{id: "a", rt: "claude"}},
		{"stay on an exhausted runtime moves", nodeState{id: "a", rt: "claude"},
			[]nodeState{pair("claude", "a", 99), pair("codex", "a", 40)}, true, nodeState{id: "a", rt: "codex"}},
		{"a runtime not logged in is skipped", nodeState{},
			[]nodeState{{id: "a", rt: "codex", free: 1 << 20}, pair("claude", "a", 80)}, false, nodeState{id: "a", rt: "claude"}},
	}
	for _, c := range cases {
		if got := chooseNode(c.current, c.cands, c.stay); !got.same(c.want) {
			t.Errorf("%s: got %s/%s, want %s/%s", c.name, got.rt, got.id, c.want.rt, c.want.id)
		}
	}
}

// TestQuotaTight: a 5-hour window against FiveHourPct, a weekly one against WeeklyPct (Codex by
// window length), only windows that apply to the model and have not reset; thresholds follow
// the user's settings.
func TestQuotaTight(t *testing.T) {
	now := time.Date(2026, 11, 24, 8, 0, 0, 0, time.UTC)
	later, past := now.Add(time.Hour), now.Add(-time.Minute)
	claude := func(five, weekly, fable float64) *platform.RuntimeUsage {
		return &platform.RuntimeUsage{Auth: "subscription", Status: "ok", Windows: []platform.UsageWindow{
			{Key: "five_hour", UsedPercent: five, ResetsAt: &later},
			{Key: "weekly", UsedPercent: weekly, ResetsAt: &later},
			{Key: "weekly_model", Label: "Fable", UsedPercent: fable, ResetsAt: &later},
		}}
	}
	mins := func(m int) *int { return &m }
	codex := func(primary, secondary float64) *platform.RuntimeUsage {
		return &platform.RuntimeUsage{Auth: "chatgpt", Status: "ok", Windows: []platform.UsageWindow{
			{Key: "primary", UsedPercent: primary, WindowMinutes: mins(300)},
			{Key: "secondary", UsedPercent: secondary, WindowMinutes: mins(10080)},
		}}
	}
	def := store.DefaultQuotaLimits()
	opus, fable := "claude-opus-5-5", "claude-fable-5-2"
	cases := []struct {
		name   string
		ru     *platform.RuntimeUsage
		model  string
		limits store.QuotaLimits
		want   string
	}{
		{"5 h 86% is tight", claude(86, 10, 0), opus, def, "five_hour 86% ≥ 85%"},
		{"5 h 84%, weekly 94%: not tight", claude(84, 94, 0), opus, def, ""},
		{"weekly 95% is tight", claude(10, 95, 0), opus, def, "weekly 95% ≥ 95%"},
		{"the model's weekly window counts", claude(10, 10, 96), fable, def, "weekly_model 96% ≥ 95%"},
		{"another family's weekly window does not", claude(10, 10, 96), opus, def, ""},
		{"a lower 5 h threshold applies", claude(60, 10, 0), opus, store.QuotaLimits{FiveHourPct: 50, WeeklyPct: 95}, "five_hour 60% ≥ 50%"},
		{"a higher 5 h threshold lets 90% through", claude(90, 10, 0), opus, store.QuotaLimits{FiveHourPct: 100, WeeklyPct: 95}, ""},
		{"codex primary (5 h) against the 5 h threshold", codex(86, 0), "", def, "primary 86% ≥ 85%"},
		{"codex secondary (weekly) against the weekly threshold", codex(10, 90), "", def, ""},
		{"no numbers: not tight", &platform.RuntimeUsage{Auth: "subscription", Status: "error"}, opus, def, ""},
		{"a window that has reset does not count", &platform.RuntimeUsage{Auth: "subscription", Status: "ok",
			Windows: []platform.UsageWindow{{Key: "five_hour", UsedPercent: 100, ResetsAt: &past}}}, opus, def, ""},
	}
	for _, c := range cases {
		if got := quotaTight(c.ru, c.model, now, c.limits); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// TestChooseNodeTight: a node whose quota is tight is passed over while another usable node is
// not; when every node is tight one is still picked (by load score), never none.
func TestChooseNodeTight(t *testing.T) {
	tight := func(id string, used float64, why string) nodeState {
		s := roomy(id, used)
		s.tight = why
		return s
	}
	cases := []struct {
		name  string
		cands []nodeState
		want  string
	}{
		{"5 h 86% loses to 5 h 10%", []nodeState{tight("a", 86, "five_hour 86% ≥ 85%"), roomy("b", 10)}, "b"},
		{"tight loses even to a busier node", []nodeState{tight("a", 86, "five_hour"), func() nodeState { s := roomy("b", 60); s.active = 5; return s }()}, "b"},
		{"all tight: the lowest load score", []nodeState{tight("a", 97, "weekly"), tight("b", 88, "five_hour")}, "b"},
	}
	for _, c := range cases {
		if got := chooseNode(nodeState{}, c.cands, false).id; got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}

// TestStayIgnoresThresholds: the user's thresholds only keep new sessions off a node; a session
// that must stay keeps its node until a window reaches QuotaHardFull or no slot is free.
func TestStayIgnoresThresholds(t *testing.T) {
	at := func(id string, used float64, tight string) nodeState {
		s := roomy(id, used)
		s.tight = tight
		return s
	}
	cases := []struct {
		name  string
		cands []nodeState
		stay  bool
		want  string
	}{
		{"5 h 88% (over the default 85) with an idle node: stay keeps it", []nodeState{at("a", 88, "five_hour 88% ≥ 85%"), roomy("b", 0)}, true, "a"},
		{"the same without stay: the idle node", []nodeState{at("a", 88, "five_hour 88% ≥ 85%"), roomy("b", 0)}, false, "b"},
		{"96% (hard full): stay moves", []nodeState{at("a", 96, "five_hour 96% ≥ 85%"), roomy("b", 0)}, true, "b"},
		{"no free slot: stay moves", []nodeState{{id: "a", usable: true, free: 0, used: 10}, roomy("b", 50)}, true, "b"},
	}
	for _, c := range cases {
		if got := chooseNode(nodeState{id: "a"}, c.cands, c.stay).id; got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}
