package orchestrator

import (
	"testing"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/platform"
)

func roomy(id string, used float64) nodeState {
	return nodeState{id: id, usable: true, free: 1 << 20, used: used}
}

// TestChooseNode: a new session goes to the usable node with the least used quota, wherever the
// employee was before; it stays only when it must and its node is still usable with quota.
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

// TestChooseRuntime: a role with several runtimes balances them by quota left, over every
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
