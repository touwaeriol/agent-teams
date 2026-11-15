package roles

import (
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/touwaeriol/agent-teams/server/internal/office"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

var looks = office.Looks{
	Outfits:     []string{"coat", "hoodie", "suit", "tee"},
	Accessories: []string{"cap", "glasses", "headset", "none"},
	Colors:      []string{"#4C7BD0", "#3F8F6B"},
	Defaults: map[string]office.LookDefault{
		"coordinator": {Color: "#7A4FD1", Outfit: "hoodie", Accessory: "headset"},
		"*":           {Color: "#4C7BD0", Outfit: "tee", Accessory: "none"},
	},
}

func nodes() map[string]store.Node {
	return map[string]store.Node{
		"n1": {ID: "n1", Name: "rc-erio", Isolation: "none"},
		"n2": {ID: "n2", Name: "cvp", Isolation: "agent_user"},
		"n3": {ID: "n3", Name: "box", Isolation: "sandbox", Isolated: true},
	}
}

func TestDefaultsValidate(t *testing.T) {
	roles := Defaults(looks)
	if p := Validate(roles, Context{Nodes: nodes(), Looks: looks}); len(p) != 0 {
		t.Fatalf("defaults invalid: %v", p)
	}
	if roles[0].Look.Outfit != "hoodie" || roles[2].Look.Outfit != "tee" {
		t.Fatalf("looks not filled: %+v", roles[0].Look)
	}
}

func problemPaths(ps []Problem) string {
	var s []string
	for _, p := range ps {
		s = append(s, p.Path)
	}
	return strings.Join(s, " ")
}

func TestValidateRules(t *testing.T) {
	roles := Defaults(looks)
	ro := true
	roles = append(roles,
		store.Role{Tier: Member, Role: "Bad Key", Count: 9},
		store.Role{Tier: Member, Role: "qa", Runtime: Codex, NodeIDs: []string{"n1"}},
		store.Role{Tier: Member, Role: "ops", SkipPermissions: true, NodeIDs: []string{"n1", "n2"}},
		store.Role{Tier: Member, Role: "rev", Gate: "review", SkipPermissions: true, NodeIDs: []string{"n2"}},
		store.Role{Tier: Member, Role: "x", ReadOnly: &ro, NodeIDs: []string{"ghost"}, Look: store.Look{Outfit: "kimono"}},
	)
	Normalize(roles, looks)
	got := problemPaths(Validate(roles, Context{Nodes: nodes(), Looks: looks}))
	for _, want := range []string{"roles[3].role", "roles[3].count", "roles[4].node_ids", "roles[5].node_ids",
		"roles[6].read_only", "roles[7].node_ids", "roles[7].look.outfit"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in %s", want, got)
		}
	}
	if roles[6].ReadOnly == nil || !*roles[6].ReadOnly {
		t.Fatal("gate review should default read_only")
	}
}

func TestTierCountsAndCoordinatorRuntime(t *testing.T) {
	roles := []store.Role{{Tier: Coordinator, Role: "coordinator", Runtime: Codex}}
	Normalize(roles, looks)
	got := Validate(roles, Context{Nodes: nodes(), Looks: looks})
	if !strings.Contains(problemPaths(got), "roles[0].runtime") || len(got) < 2 {
		t.Fatalf("problems %v", got)
	}
}

func TestSkipPermissionsOnIsolatedNodesPasses(t *testing.T) {
	roles := append(Defaults(looks), store.Role{Tier: Member, Role: "ops", SkipPermissions: true, NodeIDs: []string{"n2", "n3"}},
		store.Role{Tier: Member, Role: "cx", Runtime: Codex})
	Normalize(roles, looks)
	if p := Validate(roles, Context{Nodes: nodes(), Looks: looks}); len(p) != 0 {
		t.Fatalf("unexpected %v", p)
	}
}

func TestWarnings(t *testing.T) {
	roles := []store.Role{{Role: "ops", SkipPermissions: true, NodeIDs: []string{"gone", "n1"}}}
	w := Warnings(roles, nodes())
	if len(w) != 2 {
		t.Fatalf("warnings %v", w)
	}
}

const v1TeamYAML = `
leader: lead
employees:
  - name: lead
    type: claude_code
    node: rc-erio
    instructions: 你是技术负责人。
  - name: Engineer_1
    type: claude_code
    node: rc-erio
    permission_mode: bypassPermissions
    allowed_tools: ["Bash(git *)"]
    workdir: /tmp/x
  - name: qa
    type: codex
    node: missing
    sandbox: danger-full-access
`

func TestConvertV1Team(t *testing.T) {
	p, err := FromYAML(v1TeamYAML, map[string]string{"rc-erio": "n1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Roles) != 4 {
		t.Fatalf("roles %+v", p.Roles)
	}
	coord, lead, eng, qa := p.Roles[0], p.Roles[1], p.Roles[2], p.Roles[3]
	if coord.Tier != Coordinator || coord.Instructions != "你是技术负责人。" || coord.NodeIDs[0] != "n1" {
		t.Fatalf("coordinator %+v", coord)
	}
	if lead.Tier != Lead || lead.Role != "lead" {
		t.Fatalf("lead %+v", lead)
	}
	if eng.Role != "engineer-1" || !eng.SkipPermissions || eng.AllowedTools[0] != "Bash(git *)" {
		t.Fatalf("engineer %+v", eng)
	}
	if qa.Runtime != Codex || !qa.SkipPermissions || len(qa.NodeIDs) != 0 {
		t.Fatalf("qa %+v", qa)
	}
	report := strings.Join(p.Report, "\n")
	if !strings.Contains(report, "workdir") || !strings.Contains(report, "missing") {
		t.Fatalf("report %q", report)
	}
}

func TestYAMLRoundTrip(t *testing.T) {
	roles := append(Defaults(looks), store.Role{Tier: Member, Role: "qa", Title: "测试", Runtime: Codex,
		Sandbox: "workspace-write", NodeIDs: []string{"n2"}, Count: 1})
	Normalize(roles, looks)
	text, err := ToYAML(roles, map[string]string{"n2": "cvp"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "coordinator:") || !strings.Contains(text, "type: codex") || !strings.Contains(text, "- cvp") {
		t.Fatalf("yaml:\n%s", text)
	}
	back, err := FromYAML(text, map[string]string{"cvp": "n2"})
	if err != nil {
		t.Fatal(err)
	}
	Normalize(back.Roles, looks)
	if len(back.Roles) != 4 || back.Roles[3].Runtime != Codex || back.Roles[3].NodeIDs[0] != "n2" || back.Roles[2].Count != 2 {
		t.Fatalf("round trip %+v", back.Roles)
	}
	if _, err := FromYAML("members:\n  - {role: dev, colour: red}\n", nil); err == nil {
		t.Fatal("unknown field accepted")
	}
}

func TestUniqueKey(t *testing.T) {
	taken := map[string]bool{"lead": true}
	if k := uniqueKey("Lead", taken); k != "lead-2" {
		t.Fatalf("key %q", k)
	}
	if k := uniqueKey("工程师", taken); !keyPattern.MatchString(k) {
		t.Fatalf("key %q", k)
	}
}

func TestRandomLook(t *testing.T) {
	l := RandomLook(looks, rand.New(rand.NewPCG(1, 2)))
	if l.Color == "" || l.Outfit == "" || l.Accessory == "" {
		t.Fatalf("look %+v", l)
	}
}

func TestSkillsValidation(t *testing.T) {
	roles := Defaults(looks)
	roles[0].Skills = []string{"design"}
	roles[2].Skills = []string{"design", "design", "ghost"}
	Normalize(roles, looks)
	if len(roles[2].Skills) != 2 {
		t.Fatalf("skills not deduplicated: %v", roles[2].Skills)
	}
	known := map[string]bool{"design": true, "docs": true}
	got := problemPaths(Validate(roles, Context{Nodes: nodes(), Looks: looks, Skills: known}))
	if got != "roles[0].skills roles[2].skills" {
		t.Fatalf("problems %q", got)
	}
	roles[0].Skills, roles[2].Skills = nil, []string{"docs"}
	if p := Validate(roles, Context{Nodes: nodes(), Looks: looks, Skills: known}); len(p) != 0 {
		t.Fatalf("valid skills refused: %v", p)
	}
}

func TestDesignerPreset(t *testing.T) {
	p := Presets(looks)
	if len(p) == 0 || p[0].Role != "designer" || p[0].Title != "设计师" || strings.Join(p[0].Skills, ",") != "design,docs" {
		t.Fatalf("presets %+v", p)
	}
	roles := append(Defaults(looks), p[0])
	known := map[string]bool{"design": true, "docs": true}
	if probs := Validate(roles, Context{Nodes: nodes(), Looks: looks, Skills: known}); len(probs) != 0 {
		t.Fatalf("designer preset invalid: %v", probs)
	}
	text, err := ToYAML(roles, nil)
	if err != nil || !strings.Contains(text, "skills:") {
		t.Fatalf("yaml %v %s", err, text)
	}
	parsed, err := FromYAML(text, nil)
	if err != nil || strings.Join(parsed.Roles[3].Skills, ",") != "design,docs" {
		t.Fatalf("yaml round trip %+v %v", parsed.Roles, err)
	}
}
