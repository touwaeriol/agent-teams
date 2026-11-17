package composer

import (
	"strings"
	"testing"

	"github.com/touwaeriol/agent-teams/server/internal/store"
)

func TestSplitAndExpand(t *testing.T) {
	cases := []struct{ in, name, args string }{
		{"/plan 登录页", "plan", "登录页"},
		{"/plan", "plan", ""},
		{"/review:check  a b ", "review:check", "a b"},
	}
	for _, c := range cases {
		name, args, ok := Split(c.in)
		if !ok || name != c.name || args != c.args {
			t.Fatalf("Split(%q) = %q %q %v", c.in, name, args, ok)
		}
	}
	for _, in := range []string{"plan", " /plan", "看 /plan", "/"} {
		if _, _, ok := Split(in); ok {
			t.Fatalf("Split(%q) matched", in)
		}
	}
	if got := Expand("目标：{{args}}。再说一遍：{{args}}", "A {{args}} $1 \\n"); got != "目标：A {{args}} $1 \\n。再说一遍：A {{args}} $1 \\n" {
		t.Fatalf("literal args: %q", got)
	}
	if got := Expand("跑测试", "只跑 web"); got != "跑测试\n\n只跑 web" {
		t.Fatalf("appended: %q", got)
	}
	if got := Expand("跑测试", ""); got != "跑测试" {
		t.Fatalf("no args: %q", got)
	}
}

func TestValidateCommands(t *testing.T) {
	ok := NormalizeCommands([]store.CustomCommand{{Name: " /plan ", Prompt: " 做计划 {{args}} "}, {Name: "rv:check", Mode: "passthrough", Prompt: "dropped"}})
	if ok[0].Name != "plan" || ok[0].Mode != "prompt" || ok[0].Prompt != "做计划 {{args}}" || ok[1].Prompt != "" {
		t.Fatalf("normalize: %+v", ok)
	}
	if p := ValidateCommands("commands", ok, "claude"); len(p) != 0 {
		t.Fatalf("valid list: %+v", p)
	}
	for _, kind := range []string{For("codex", "member"), For("claude", "member")} {
		if p := ValidateCommands("commands", ok, kind); len(p) != 1 || p[0].Path != "commands[1].mode" {
			t.Fatalf("%s passthrough: %+v", kind, p)
		}
	}
	if For("claude", "lead") != ForClaude || !PromptOnly("claude", "member") || PromptOnly("claude", "coordinator") {
		t.Fatal("For / PromptOnly")
	}
	bad := map[string]store.CustomCommand{
		"commands[0].name":        {Name: "status", Mode: "prompt", Prompt: "x"},
		"commands[0].name ":       {Name: "9x", Mode: "prompt", Prompt: "x"},
		"commands[0].mode":        {Name: "x", Mode: "shell"},
		"commands[0].prompt":      {Name: "x", Mode: "prompt", Prompt: "/run"},
		"commands[0].description": {Name: "x", Mode: "prompt", Prompt: "x", Description: strings.Repeat("长", 201)},
	}
	for path, c := range bad {
		p := ValidateCommands("commands", []store.CustomCommand{c}, "")
		if len(p) == 0 || p[0].Path != strings.TrimSpace(path) {
			t.Fatalf("%+v: %+v", c, p)
		}
	}
	dup := []store.CustomCommand{{Name: "a", Mode: "prompt", Prompt: "x"}, {Name: "a", Mode: "prompt", Prompt: "y"}}
	if p := ValidateCommands("commands", dup, ""); len(p) != 1 || p[0].Path != "commands[1].name" {
		t.Fatalf("duplicate: %+v", p)
	}
	if p := HiddenProblems("hidden", []string{"model", "nope"}); len(p) != 1 {
		t.Fatalf("hidden: %+v", p)
	}
}

func TestMergeAndModels(t *testing.T) {
	role := []store.CustomCommand{{Name: "plan", Mode: "prompt", Prompt: "role"}}
	builtin := []store.CustomCommand{{Name: "plan", Mode: "prompt", Prompt: "platform"}, {Name: "compact", Mode: "passthrough"}, {Name: "tests", Mode: "prompt", Prompt: "t"}}
	claude := Merge(role, builtin, false)
	if len(claude) != 3 || claude[0].Prompt != "role" || claude[1].Source != "platform" {
		t.Fatalf("claude: %+v", claude)
	}
	codex := Merge(nil, builtin, true)
	if len(codex) != 2 || codex[1].Name != "tests" {
		t.Fatalf("codex skips passthrough: %+v", codex)
	}
	if c, ok := Find(claude, "tests"); !ok || c.Prompt != "t" {
		t.Fatal("find")
	}
	list := NormalizeModels([]Model{{Model: " claude-x[1m] ", Default: true}, {Runtime: "codex", Model: "gpt-5.5", Default: true}})
	if list[0].Runtime != "claude" || list[0].Model != "claude-x[1m]" || !list[0].Default || !list[1].Default {
		t.Fatalf("user models (one default per runtime kept): %+v", list)
	}
	if p := ValidateModels(list); len(p) != 0 {
		t.Fatalf("valid: %+v", p)
	}
	two := NormalizeModels([]Model{{Model: "a", Default: true}, {Model: "b", Default: true}})
	if p := ValidateModels(two); len(p) != 1 || p[0].Path != "models[1].default" {
		t.Fatalf("two defaults: %+v", p)
	}
	if p := ValidateModels(nil); len(p) != 1 || p[0].Path != "models" {
		t.Fatalf("empty list: %+v", p)
	}
	if ValidModel("a b") || ValidModel("") || !ValidModel("us.anthropic.claude-x:0") {
		t.Fatal("ValidModel")
	}
}
