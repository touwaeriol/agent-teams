package orchestrator

import (
	"context"
	"strings"
	"testing"

	protocol "github.com/touwaeriol/agent-teams/protocol"

	"github.com/touwaeriol/agent-teams/server/internal/store"
)

var designBundle = []protocol.SkillBundle{{Name: "design", Files: []protocol.SkillFile{{Path: "SKILL.md", Content: "# Design rules"}}}}

func TestApplySkillsClaude(t *testing.T) {
	o, err := buildOpen(open(t, emp("lead", "lead", store.Role{}), store.Node{Isolation: "sandbox"}))
	if err != nil {
		t.Fatal(err)
	}
	applySkills(&o, designBundle)
	if len(o.Skills) != 1 || !contains(o.Claude.Tools, "Skill") || !contains(o.Claude.AllowedTools, "Skill") {
		t.Fatalf("claude skills %+v tools %v allowed %v", o.Skills, o.Claude.Tools, o.Claude.AllowedTools)
	}
	if ok, bad := ToolsAllowed(emp("lead", "lead", store.Role{}), o.Claude.Tools); !ok {
		t.Fatalf("Skill must pass the read-only init check, refused %s", bad)
	}
}

func TestApplySkillsCodex(t *testing.T) {
	e := emp("member", "dev", store.Role{})
	e.Runtime = "codex"
	o, err := buildOpen(open(t, e, store.Node{Isolation: "agent_user"}))
	if err != nil {
		t.Fatal(err)
	}
	applySkills(&o, designBundle)
	if len(o.Skills) != 0 || !strings.Contains(*o.Codex.DeveloperInstructions, "# Design rules") ||
		!strings.HasPrefix(*o.Codex.DeveloperInstructions, "sys") {
		t.Fatalf("codex %+v", o.Codex)
	}
}

func TestAddSkillsSkipsCoordinatorAndEmpty(t *testing.T) {
	o := &Orchestrator{}
	coord := emp("coordinator", "coordinator", store.Role{Skills: []string{"design"}})
	open := protocol.SessionOpen{}
	// No database is touched for the coordinator or a role without skills.
	if err := o.addSkills(context.Background(), nil, "u1", coord, &open); err != nil || open.Skills != nil {
		t.Fatalf("coordinator got skills: %v %+v", err, open.Skills)
	}
	if err := o.addSkills(context.Background(), nil, "u1", emp("member", "dev", store.Role{}), &open); err != nil || open.Skills != nil {
		t.Fatalf("empty role got skills: %v", err)
	}
}

func TestApplySkillsAntigravity(t *testing.T) {
	e := emp("member", "dev", store.Role{})
	e.Runtime = "antigravity"
	o, err := buildOpen(open(t, e, store.Node{Isolation: "agent_user"}))
	if err != nil {
		t.Fatal(err)
	}
	applySkills(&o, designBundle)
	if len(o.Skills) != 0 || !strings.Contains(*o.Antigravity.SystemAppend, "# Design rules") ||
		!strings.HasPrefix(*o.Antigravity.SystemAppend, "sys") {
		t.Fatalf("antigravity %+v", o.Antigravity)
	}
}
