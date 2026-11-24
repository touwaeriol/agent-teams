package orchestrator

import (
	"encoding/json"
	"strings"
	"testing"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

func soloEmp(runtime, perm string) core.Employee {
	e := emp("lead", "dev", store.Role{Solo: true, PermissionMode: perm})
	e.Runtime = runtime
	return e
}

// TestSoloInitialModes: a session's Claude employee opens for auto (as default with prefer_auto,
// the daemon switches when the model supports it) and may skip prompts only when its role
// allows; Codex and Antigravity keep the role's mode. A mode picked in the session wins, except
// bypass on an ask role.
func TestSoloInitialModes(t *testing.T) {
	o, _ := buildOpen(open(t, soloEmp("claude", store.PermBypass), store.Node{Isolation: "none"}))
	c := o.Claude
	if c.PermissionMode != "default" || c.PreferAuto == nil || !*c.PreferAuto || c.AllowDangerouslySkipPermissions == nil || !*c.AllowDangerouslySkipPermissions {
		t.Fatalf("claude session opens for auto: %+v", c)
	}
	if openMode(o) != "default" {
		t.Fatalf("recorded as default until session.opened says auto: %q", openMode(o))
	}
	ask := soloEmp("claude", store.PermAsk)
	if o, _ = buildOpen(open(t, ask, store.Node{Isolation: "none"})); o.Claude.PreferAuto == nil || o.Claude.AllowDangerouslySkipPermissions != nil {
		t.Fatalf("ask role: auto, no bypass: %+v", o.Claude)
	}
	ask.SessionMode = "bypassPermissions"
	if o, _ = buildOpen(open(t, ask, store.Node{Isolation: "none"})); o.Claude.PermissionMode != "default" || o.Claude.PreferAuto == nil {
		t.Fatalf("a bypass pick on an ask role is ignored: %+v", o.Claude)
	}
	for _, m := range []string{"plan", "acceptEdits", "default", "bypassPermissions"} {
		e := soloEmp("claude", store.PermBypass)
		e.SessionMode = m
		o, _ = buildOpen(open(t, e, store.Node{Isolation: "none"}))
		if string(o.Claude.PermissionMode) != m || o.Claude.PreferAuto != nil {
			t.Fatalf("picked %s: %+v", m, o.Claude)
		}
	}
	member, _ := buildOpen(open(t, emp("member", "dev", store.Role{PermissionMode: store.PermBypass}), store.Node{Isolation: "none"}))
	if member.Claude.PermissionMode != "bypassPermissions" || member.Claude.PreferAuto != nil {
		t.Fatalf("members are unchanged: %+v", member.Claude)
	}

	cx := soloEmp("codex", store.PermAsk)
	o, _ = buildOpen(open(t, cx, store.Node{Isolation: "agent_user"}))
	if o.Codex.Sandbox != "workspace-write" || o.Codex.ApprovalPolicy != "untrusted" || o.Codex.ApprovalsReviewer != nil || openMode(o) != "default" {
		t.Fatalf("codex session keeps the ask role: %+v", o.Codex)
	}
	cx.SessionMode, cx.SessionEffort = "auto", "high"
	o, _ = buildOpen(open(t, cx, store.Node{Isolation: "agent_user"}))
	if o.Codex.Sandbox != "workspace-write" || o.Codex.ApprovalPolicy != "on-request" || o.Codex.ApprovalsReviewer == nil ||
		*o.Codex.ApprovalsReviewer != "auto_review" || o.Codex.Effort == nil || *o.Codex.Effort != "high" ||
		!strings.Contains(string(*o.Codex.Config), "o__web/.git") || openMode(o) != "auto" || openControls(o).Effort != "high" {
		t.Fatalf("codex auto: %+v", o.Codex)
	}
	byp := soloEmp("codex", store.PermBypass)
	if o, _ = buildOpen(open(t, byp, store.Node{Isolation: "agent_user"})); o.Codex.Sandbox != "danger-full-access" || openMode(o) != "bypassPermissions" {
		t.Fatalf("codex session keeps the bypass role: %+v", o.Codex)
	}

	ag := soloEmp("antigravity", store.PermBypass)
	if o, _ = buildOpen(open(t, ag, store.Node{Isolation: "agent_user"})); o.Antigravity.Approval != "auto" {
		t.Fatalf("antigravity keeps the role: %+v", o.Antigravity)
	}
	ag.SessionMode = "default"
	if o, _ = buildOpen(open(t, ag, store.Node{Isolation: "agent_user"})); o.Antigravity.Approval != "ask" || openMode(o) != "default" {
		t.Fatalf("antigravity picked default: %+v", o.Antigravity)
	}
}

// TestSoloModelAndEffort: a session's employee runs the model and effort its user picked in the
// session (unlike a thread lead, whose model is fixed).
func TestSoloModelAndEffort(t *testing.T) {
	p := store.Project{ThreadModel: "claude-project", ThreadEffort: "high"}
	e := soloEmp("claude", store.PermBypass)
	e.Spec.Model = "claude-role"
	if Model(e, p) != "claude-project" || effort(e, p) != "high" {
		t.Fatalf("before a pick: %q %q", Model(e, p), effort(e, p))
	}
	e.Model, e.SessionEffort = "claude-picked", "low"
	if Model(e, p) != "claude-picked" || effort(e, p) != "low" {
		t.Fatalf("picked: %q %q", Model(e, p), effort(e, p))
	}
	lead := emp("lead", "lead", store.Role{Model: "claude-role"})
	lead.Model = "claude-picked"
	if Model(lead, p) != "claude-project" {
		t.Fatalf("a thread lead's model stays fixed: %q", Model(lead, p))
	}
}

// TestModesFor: the menu follows the runtime and the model: auto hidden for a model without
// supportsAutoMode, bypass only when allowed, Codex and Antigravity from the next turn.
func TestModesFor(t *testing.T) {
	models := []ModelChoice{{ID: "claude-a", SupportsAuto: true, Efforts: []string{"low", "high"}}, {ID: "claude-old", Efforts: []string{}}}
	ids := func(list []ModeOption) string {
		out := []string{}
		for _, m := range list {
			out = append(out, m.ID+"/"+m.Applies)
		}
		return strings.Join(out, ",")
	}
	if got := ids(modesFor("claude", models, "claude-a", true)); got != "auto/now,default/now,acceptEdits/now,plan/now,bypassPermissions/now" {
		t.Fatalf("claude %s", got)
	}
	if got := ids(modesFor("claude", models, "claude-old", false)); got != "default/now,acceptEdits/now,plan/now" {
		t.Fatalf("claude without auto or bypass %s", got)
	}
	if got := ids(modesFor("claude", nil, "claude-unknown", false)); !strings.HasPrefix(got, "auto/now") {
		t.Fatalf("an unknown model keeps auto (the daemon refuses it if not): %s", got)
	}
	if got := ids(modesFor("codex", nil, "", true)); got != "auto/next_turn,default/next_turn,bypassPermissions/next_turn" {
		t.Fatalf("codex %s", got)
	}
	if got := ids(modesFor("antigravity", nil, "", true)); got != "default/next_turn,bypassPermissions/next_turn" {
		t.Fatalf("antigravity %s", got)
	}
	cur := SessionControls{Runtime: "claude", PermissionMode: "auto", Model: "claude-a", Effort: "high", Models: models,
		Modes: modesFor("claude", models, "claude-a", false)}
	str := func(s string) *string { return &s }
	if _, err := checkChange(cur, ControlsChange{PermissionMode: str("bypassPermissions")}); err == nil {
		t.Fatal("bypass is not on the menu")
	}
	if _, err := checkChange(cur, ControlsChange{Model: str("claude-old"), PermissionMode: str("auto")}); err == nil {
		t.Fatal("auto with a model without auto")
	}
	if _, err := checkChange(cur, ControlsChange{Effort: str("max")}); err == nil {
		t.Fatal("an effort the model lacks")
	}
	if _, err := checkChange(cur, ControlsChange{Model: str("gpt-x")}); err == nil {
		t.Fatal("a model not listed")
	}
	ch, err := checkChange(cur, ControlsChange{PermissionMode: str("auto"), Effort: str("low"), Model: str("claude-a")})
	if err != nil || ch != (ControlsPending{Effort: "low"}) {
		t.Fatalf("unchanged values are dropped: %+v %v", ch, err)
	}
	if _, err := checkChange(SessionControls{Runtime: "antigravity", Models: []ModelChoice{}}, ControlsChange{Effort: str("high")}); err == nil {
		t.Fatal("antigravity has no effort apart from the model")
	}
	cur.Pending = &ControlsPending{Effort: "low"}
	if _, err := checkChange(cur, ControlsChange{Effort: str("low")}); err != ErrControlsBusy {
		t.Fatalf("one switch at a time: %v", err)
	}
}

func TestInitModels(t *testing.T) {
	yes := true
	name := "Opus"
	raw, _ := json.Marshal(protocol.SessionInit{Tools: []string{}, Models: []protocol.ModelOption{{ID: "claude-a", Name: &name, Efforts: []string{"low"}, SupportsAuto: &yes, Default: &yes}, {ID: "x"}}})
	got := initModels(raw)
	if len(got) != 2 || got[0].ID != "claude-a" || got[0].Name != "Opus" || strings.Join(got[0].Efforts, ",") != "low" || !got[0].SupportsAuto ||
		!got[0].Default || got[1].Efforts == nil || got[1].SupportsAuto {
		t.Fatalf("models %+v", got)
	}
	if len(initModels(nil)) != 0 {
		t.Fatal("no init")
	}
}
