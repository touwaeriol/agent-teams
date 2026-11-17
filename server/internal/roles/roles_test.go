package roles

import (
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/touwaeriol/agent-teams/server/internal/composer"
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
		store.Role{Tier: Member, Role: "ops", PermissionMode: "always", NodeIDs: []string{"n1", "n2"}},
		store.Role{Tier: Member, Role: "rev", Gate: "review", PermissionMode: store.PermBypass, NodeIDs: []string{"n2"}},
		store.Role{Tier: Member, Role: "x", ReadOnly: &ro, NodeIDs: []string{"ghost"}, Look: store.Look{Outfit: "kimono"}},
	)
	Normalize(roles, looks)
	got := problemPaths(Validate(roles, Context{Nodes: nodes(), Looks: looks}))
	for _, want := range []string{"roles[3].role", "roles[3].count", "roles[4].node_ids", "roles[5].permission_mode",
		"roles[7].node_ids", "roles[7].look.outfit"} {
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

// TestBypassIsDefault: members default to bypass and keep it on any node without a warning,
// legacy auto reads as bypass, ask stays, read-only roles may carry bypass, non-members are bypass.
func TestBypassIsDefault(t *testing.T) {
	ro := true
	roles := append(Defaults(looks), store.Role{Tier: Member, Role: "ops", PermissionMode: store.PermAsk, NodeIDs: []string{"n1"}},
		store.Role{Tier: Member, Role: "old", PermissionMode: store.PermLegacyAuto, NodeIDs: []string{"n1"}},
		store.Role{Tier: Member, Role: "rev", ReadOnly: &ro, PermissionMode: store.PermBypass},
		store.Role{Tier: Lead, Role: "lead2", PermissionMode: store.PermAsk})
	Normalize(roles, looks)
	if p := Validate(roles[:6], Context{Nodes: nodes(), Looks: looks}); len(p) != 0 {
		t.Fatalf("unexpected %v", p)
	}
	var got []string
	for _, r := range roles {
		got = append(got, r.PermissionMode)
	}
	if strings.Join(got, ",") != "bypass,bypass,bypass,ask,bypass,bypass,bypass" {
		t.Fatalf("modes %v", got)
	}
	if w := Warnings(roles, nodes()); len(w) != 0 {
		t.Fatalf("warnings %v", w)
	}
	if p := Validate([]store.Role{{Tier: Member, Role: "x", PermissionMode: store.PermLegacyAuto}}, Context{Nodes: nodes(), Looks: looks}); !strings.Contains(problemPaths(p), "permission_mode") {
		t.Fatalf("auto accepted without normalize: %v", p)
	}
}

func TestWarnings(t *testing.T) {
	roles := []store.Role{{Role: "ops", PermissionMode: store.PermBypass, NodeIDs: []string{"gone", "n1"}}}
	w := Warnings(roles, nodes())
	if len(w) != 1 || w[0].Message != "候选节点已删除" {
		t.Fatalf("warnings %v", w)
	}
}

// TestOldFormatRejected: the retired platform's leader/employees YAML is no longer read.
func TestOldFormatRejected(t *testing.T) {
	if _, err := FromYAML("leader: lead\nemployees:\n  - name: lead\n", nil); err == nil || !strings.Contains(err.Error(), "不再支持") {
		t.Fatalf("old format: %v", err)
	}
}

func TestYAMLRoundTrip(t *testing.T) {
	roles := append(Defaults(looks), store.Role{Tier: Member, Role: "qa", Title: "测试", Runtime: Codex,
		Sandbox: "workspace-write", NodeIDs: []string{"n2"}, Count: 1, PermissionMode: store.PermAsk})
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
	if len(back.Roles) != 4 || back.Roles[3].Runtime != Codex || back.Roles[3].NodeIDs[0] != "n2" || back.Roles[2].Count != 2 ||
		back.Roles[3].PermissionMode != store.PermAsk || back.Roles[2].PermissionMode != store.PermBypass || strings.Contains(text, "bypass") {
		t.Fatalf("round trip %+v", back.Roles)
	}
	if _, err := FromYAML("members:\n  - {role: dev, colour: red}\n", nil); err == nil {
		t.Fatal("unknown field accepted")
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

func TestPluginsValidation(t *testing.T) {
	roles := Defaults(looks)
	roles[0].Plugins = []string{"tinyplug"}
	roles[2].Plugins = []string{"tinyplug@3", "tinyplug@3"}
	Normalize(roles, looks)
	if len(roles[2].Plugins) != 1 {
		t.Fatalf("plugins not deduplicated: %v", roles[2].Plugins)
	}
	choices := map[string]map[int]bool{"tinyplug": {1: true, 2: true}}
	got := problemPaths(Validate(roles, Context{Nodes: nodes(), Looks: looks, Plugins: choices}))
	if got != "roles[0].plugins roles[2].plugins" {
		t.Fatalf("problems %q", got)
	}
	roles[0].Plugins, roles[2].Plugins = nil, []string{"tinyplug@2"}
	if p := Validate(roles, Context{Nodes: nodes(), Looks: looks, Plugins: choices}); len(p) != 0 {
		t.Fatalf("valid plugins refused: %v", p)
	}
	text, err := ToYAML(roles, nil)
	if err != nil || !strings.Contains(text, "tinyplug@2") {
		t.Fatalf("yaml %v %s", err, text)
	}
	parsed, err := FromYAML(text, nil)
	if err != nil || strings.Join(parsed.Roles[2].Plugins, ",") != "tinyplug@2" {
		t.Fatalf("yaml round trip %+v %v", parsed.Roles, err)
	}
}

func TestLeadRuntimeIsClaude(t *testing.T) {
	roles := Defaults(looks)
	roles[1].Runtime = Codex
	Normalize(roles, looks)
	got := Validate(roles, Context{Nodes: nodes(), Looks: looks})
	if problemPaths(got) != "roles[1].runtime" || got[0].Message != "组长只能用 Claude" {
		t.Fatalf("problems %v", got)
	}
}

// TestModelValidation: role.model empty follows the defaults; otherwise it must be in the user's
// list with the role's runtime, unless it is the value the saved role already had.
func TestModelValidation(t *testing.T) {
	models := []composer.Model{{Runtime: Claude, Model: "claude-opus-5-5"}, {Runtime: Codex, Model: "gpt-x"}}
	member := func(id, runtime, model string) store.Role {
		return store.Role{ID: id, Tier: Member, Role: "m" + id, Runtime: runtime, Model: model, NodeIDs: []string{"n1"}}
	}
	saved := map[string]store.Role{
		"r1": member("r1", Claude, "claude-gone"),
		"r2": member("r2", Claude, "claude-gone"),
		"r3": member("r3", Codex, "claude-opus-5-5"),
	}
	cases := []struct {
		name string
		role store.Role
		want string // the problem message, "" = valid
	}{
		{"empty follows the defaults", member("", Claude, ""), ""},
		{"claude in list", member("", Claude, "claude-opus-5-5"), ""},
		{"codex in list", member("", Codex, "gpt-x"), ""},
		{"not in list", member("", Claude, "claude-nope"), "模型不在你的模型列表里：claude-nope"},
		{"codex role with a claude model", member("", Codex, "claude-opus-5-5"), "Codex 岗位不能用 Claude 模型：claude-opus-5-5"},
		{"claude role with a codex model", member("", Claude, "gpt-x"), "Claude 岗位不能用 Codex 模型：gpt-x"},
		{"unchanged old value passes", member("r1", Claude, "claude-gone"), ""},
		{"old value on another role is checked", member("r9", Claude, "claude-gone"), "模型不在你的模型列表里：claude-gone"},
		{"old value with the runtime changed is checked", member("r2", Codex, "claude-gone"), "模型不在你的模型列表里：claude-gone"},
		{"runtime changed to match the model is checked", member("r3", Claude, "claude-opus-5-5"), ""},
		{"lead model checked too", store.Role{Tier: Lead, Role: "lead", Model: "claude-nope"}, "模型不在你的模型列表里：claude-nope"},
	}
	for _, c := range cases {
		roles := Defaults(looks)
		if c.role.Tier == Lead {
			roles[1].Model = c.role.Model
		} else {
			roles = append(roles, c.role)
		}
		Normalize(roles, looks)
		var msgs []string
		for _, p := range Validate(roles, Context{Nodes: nodes(), Looks: looks, Models: models, Saved: saved}) {
			if strings.HasSuffix(p.Path, ".model") {
				msgs = append(msgs, p.Message)
			}
		}
		if got := strings.Join(msgs, "; "); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	// No model list (nil) skips the check.
	roles := append(Defaults(looks), member("", Claude, "anything"))
	Normalize(roles, looks)
	if p := Validate(roles, Context{Nodes: nodes(), Looks: looks}); len(p) != 0 {
		t.Fatalf("nil models: %v", p)
	}
}
