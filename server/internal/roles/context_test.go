package roles

import (
	"strconv"
	"strings"
	"testing"

	"github.com/touwaeriol/agent-teams/server/internal/store"
)

func policyText(r store.Role) string {
	n := func(p *int) string {
		if p == nil {
			return "-"
		}
		return strconv.Itoa(*p)
	}
	return n(r.ContextLimit) + "/" + n(r.CompactBetweenPct) + "/" + n(r.CompactInTaskPct) + "/" + n(r.ClearAfterTasks)
}

// TestContextDefaults: a new role without settings gets its kind's defaults (dev 60/80/8 with a
// focus, gates —/80/1, lead and coordinator —/70/—); a saved role keeps what it has, and the lead
// and coordinator never keep between-task or clear settings.
func TestContextDefaults(t *testing.T) {
	list := []store.Role{
		{Tier: Coordinator, Role: "coordinator"}, {Tier: Lead, Role: "lead"},
		{Tier: Member, Role: "dev"}, {Tier: Member, Role: "review", Gate: "review"}, {Tier: Member, Role: "pick", Gate: "decision"},
		{ID: "ro_1", Tier: Member, Role: "old"},
		{Tier: Lead, Role: "lead2", CompactBetweenPct: ptr(50), ClearAfterTasks: ptr(3), CompactInTaskPct: ptr(90)},
	}
	Normalize(list, looks)
	want := []string{"-/-/70/-", "-/-/70/-", "-/60/80/8", "-/-/80/1", "-/-/80/1", "-/-/-/-", "-/-/90/-"}
	for i, r := range list {
		if got := policyText(r); got != want[i] {
			t.Errorf("%s: %s, want %s", r.Role, got, want[i])
		}
	}
	if list[2].CompactFocus != DevFocus || list[3].CompactFocus != "" {
		t.Fatalf("focus: %q / %q", list[2].CompactFocus, list[3].CompactFocus)
	}
	for _, r := range Defaults(looks) {
		if r.CompactInTaskPct == nil {
			t.Fatalf("default team role %s has no in-task pct", r.Role)
		}
	}
}

// TestContextValidation: limit ≥ 10000, percentages 1–100, clear count ≥ 0, focus ≤ 2000 characters.
func TestContextValidation(t *testing.T) {
	base := Defaults(looks)
	bad := store.Role{ID: "x", Tier: Member, Role: "qa", Runtime: Claude, Count: 1, ContextLimit: ptr(5000),
		CompactBetweenPct: ptr(0), CompactInTaskPct: ptr(101), ClearAfterTasks: ptr(-1), CompactFocus: strings.Repeat("字", MaxFocus+1)}
	list := append(base, bad)
	Normalize(list, looks)
	problems := Validate(list, Context{Nodes: nodes(), Looks: looks})
	got := map[string]bool{}
	for _, p := range problems {
		got[p.Path] = true
	}
	for _, f := range []string{"context_limit", "compact_between_pct", "compact_in_task_pct", "clear_after_tasks", "compact_focus"} {
		if !got["roles[3]."+f] {
			t.Errorf("no problem for %s: %v", f, problems)
		}
	}
	good := store.Role{ID: "y", Tier: Member, Role: "qa", Runtime: Claude, Count: 1, ContextLimit: ptr(600000),
		CompactBetweenPct: ptr(60), CompactInTaskPct: ptr(80), ClearAfterTasks: ptr(0)}
	list = append(Defaults(looks), good)
	Normalize(list, looks)
	if p := Validate(list, Context{Nodes: nodes(), Looks: looks}); len(p) != 0 {
		t.Fatalf("valid settings refused: %v", p)
	}
}

// TestContextYAML: the context: block round-trips; a role without one gets its kind's defaults.
func TestContextYAML(t *testing.T) {
	list := Defaults(looks)
	list[2].ContextLimit = ptr(600000)
	text, err := ToYAML(list, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"context:", "limit: 600000", "compact_between_pct: 60", "compact_in_task_pct: 80", "clear_after_tasks: 8", "focus: " + DevFocus} {
		if !strings.Contains(text, want) {
			t.Fatalf("yaml lacks %q:\n%s", want, text)
		}
	}
	p, err := FromYAML(text, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := policyText(p.Roles[2]); got != "600000/60/80/8" {
		t.Fatalf("round trip %s", got)
	}
	p, err = FromYAML("lead: {}\nmembers:\n  - role: review\n    gate: review\n  - role: dev\n    context:\n      compact_in_task_pct: 90\n", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := policyText(p.Roles[0]) + " " + policyText(p.Roles[1]) + " " + policyText(p.Roles[2]); got != "-/-/70/- -/-/80/1 -/-/90/-" {
		t.Fatalf("from yaml %s", got)
	}
}
