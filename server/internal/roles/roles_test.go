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
	if problemPaths(got) != "roles[1].runtime" || got[0].Message != "组长只能用 Claude Code" {
		t.Fatalf("problems %v", got)
	}
}

// TestModelValidation: role.model is typed by hand for every tier: empty follows the project and
// the built-in default; otherwise only the format is checked (no list).
func TestModelValidation(t *testing.T) {
	member := func(model, runtime string) store.Role {
		return store.Role{Tier: Member, Role: "m", Runtime: runtime, Model: model, NodeIDs: []string{"n1"}}
	}
	cases := []struct {
		name string
		role store.Role
		want string // the problem message, "" = valid
	}{
		{"empty follows the defaults", member("", Claude), ""},
		{"any claude id", member("claude-sonnet-x", Claude), ""},
		{"any codex id", member("gpt-x", Codex), ""},
		{"bad format", member("a b", Claude), composer.ModelFormatProblem},
		{"lead model kept and checked", store.Role{Tier: Lead, Role: "lead", Model: "claude-lead-x"}, ""},
		{"lead bad format", store.Role{Tier: Lead, Role: "lead", Model: "bad model"}, composer.ModelFormatProblem},
		{"coordinator model kept", store.Role{Tier: Coordinator, Role: "coordinator", Model: "claude-coord-x"}, ""},
	}
	for _, c := range cases {
		roles := Defaults(looks)
		var at int
		switch c.role.Tier {
		case Coordinator:
			roles[0].Model, at = c.role.Model, 0
		case Lead:
			roles[1].Model, at = c.role.Model, 1
		default:
			roles, at = append(roles, c.role), len(roles)
		}
		Normalize(roles, looks)
		if roles[at].Model != strings.TrimSpace(c.role.Model) {
			t.Errorf("%s: model not kept: %q", c.name, roles[at].Model)
		}
		var msgs []string
		for _, p := range Validate(roles, Context{Nodes: nodes(), Looks: looks}) {
			if strings.HasSuffix(p.Path, ".model") {
				msgs = append(msgs, p.Message)
			}
		}
		if got := strings.Join(msgs, "; "); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// TestSeveralRuntimes: runtimes is the list (runtime its first entry), models keeps the further
// runtimes' models; a role may name Codex next to Claude even where no node isolates Codex (it
// then only runs Claude: a warning, not a problem); passthrough commands need every runtime to
// have slash commands; leads stay on runtimes that can lead.
func TestSeveralRuntimes(t *testing.T) {
	roles := Defaults(looks)
	roles[2].Runtimes = []string{Claude, Codex, Claude}
	roles[2].Models = map[string]string{Claude: "ignored", Codex: " gpt-x ", "gemini": "nope"}
	roles[2].NodeIDs = []string{"n1"}
	roles[1].Runtimes = []string{Claude, Codex}
	Normalize(roles, looks)
	dev := roles[2]
	if strings.Join(dev.Runtimes, ",") != "claude,codex" || dev.Runtime != Claude || len(dev.Models) != 1 || dev.Models[Codex] != "gpt-x" {
		t.Fatalf("normalized %v %s %v", dev.Runtimes, dev.Runtime, dev.Models)
	}
	if dev.ModelFor(Codex) != "gpt-x" || dev.ModelFor(Claude) != dev.Model {
		t.Fatalf("ModelFor %q %q", dev.ModelFor(Codex), dev.ModelFor(Claude))
	}
	got := Validate(roles, Context{Nodes: nodes(), Looks: looks})
	if problemPaths(got) != "roles[1].runtime" {
		t.Fatalf("problems %v", got)
	}
	if w := Warnings([]store.Role{dev}, nodes()); len(w) != 1 || !strings.Contains(w[0].Message, "只会用其他 agent") {
		t.Fatalf("warnings %v", w)
	}
	dev.Commands = []store.CustomCommand{{Name: "ship", Mode: store.CommandPassthrough}}
	if p := commandProblems("r", &dev); len(p) != 1 || p[0].Path != "r.commands[0].mode" {
		t.Fatalf("passthrough on a role with Codex: %v", p)
	}
}

// TestRuntimeOnlyClients: a client that only sets runtime (or changes it next to the old
// single-entry list) gets that runtime.
func TestRuntimeOnlyClients(t *testing.T) {
	roles := []store.Role{{Tier: Member, Role: "a", Runtime: Codex}, {Tier: Member, Role: "b", Runtime: Codex, Runtimes: []string{Claude}},
		{Tier: Member, Role: "c", Runtime: Claude, Runtimes: []string{Codex, Claude}}}
	Normalize(roles, looks)
	if strings.Join(roles[0].Runtimes, ",") != "codex" || strings.Join(roles[1].Runtimes, ",") != "codex" ||
		strings.Join(roles[2].Runtimes, ",") != "codex,claude" || roles[2].Runtime != Codex {
		t.Fatalf("runtimes %v %v %v", roles[0].Runtimes, roles[1].Runtimes, roles[2].Runtimes)
	}
}

func TestYAMLSeveralRuntimes(t *testing.T) {
	text := "members:\n  - role: dev\n    types: [claude_code, codex]\n    model: claude-fable-5-2\n    models: {codex: gpt-x}\n"
	p, err := FromYAML(text, nil)
	if err != nil {
		t.Fatal(err)
	}
	Normalize(p.Roles, looks)
	r := p.Roles[0]
	if strings.Join(r.Runtimes, ",") != "claude,codex" || r.Model != "claude-fable-5-2" || r.Models[Codex] != "gpt-x" {
		t.Fatalf("from yaml %+v", r)
	}
	out, err := ToYAML(p.Roles, nil)
	if err != nil || !strings.Contains(out, "types:") || !strings.Contains(out, "- codex") || !strings.Contains(out, "codex: gpt-x") || strings.Contains(out, "type: claude_code") {
		t.Fatalf("to yaml %v:\n%s", err, out)
	}
}
