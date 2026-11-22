package runtimes

import "testing"

func TestRegistry(t *testing.T) {
	if Default().ID != Claude || Of("").ID != Claude || Of("nope").ID != Claude {
		t.Fatal("unknown and empty ids fall back to Claude")
	}
	if !Valid(Codex) || Valid("") || Valid("gemini") {
		t.Fatal("Valid")
	}
	if FromYAMLType("codex") != Codex || FromYAMLType("claude_code") != Claude || FromYAMLType("") != Claude || FromYAMLType("claude") != Claude {
		t.Fatal("FromYAMLType")
	}
	if c := Of(Codex); c.SlashCommands || c.CanLead || !c.NeedsIsolation || c.DefaultModel != "" {
		t.Fatalf("codex capabilities: %+v", c)
	}
	if c := Of(Claude); !c.SlashCommands || !c.CanLead || c.NeedsIsolation || !c.LiveModeSwitch {
		t.Fatalf("claude capabilities: %+v", c)
	}
	if RunsOn(Codex, false, "sandbox") || !RunsOn(Codex, false, "agent_user") || !RunsOn(Codex, true, "") || !RunsOn(Claude, false, "none") {
		t.Fatal("RunsOn")
	}
	if len(IDs()) != len(All()) || IDs()[0] != Claude {
		t.Fatal("IDs")
	}
}

func TestAntigravity(t *testing.T) {
	a, ok := Get(Antigravity)
	if !ok || !Valid("antigravity") || a.Title != "Antigravity" || a.YAMLType != "antigravity" || a.TeamType != "antigravity" {
		t.Fatalf("antigravity entry: %+v", a)
	}
	if a.DefaultModel != "" || a.SlashCommands || a.LiveModeSwitch || !a.NeedsIsolation || a.CanLead || !a.NeedsHelloReady {
		t.Fatalf("antigravity capabilities: %+v", a)
	}
	if FromYAMLType("antigravity") != Antigravity {
		t.Fatal("FromYAMLType(antigravity)")
	}
	if RunsOn(Antigravity, false, "sandbox") || !RunsOn(Antigravity, false, "agent_user") || !RunsOn(Antigravity, true, "") {
		t.Fatal("RunsOn(antigravity)")
	}
	if LeaderTitles() != "Claude Code" {
		t.Fatalf("LeaderTitles = %q", LeaderTitles())
	}
	if Of(Claude).NeedsHelloReady || Of(Codex).NeedsHelloReady {
		t.Fatal("only antigravity needs a ready hello")
	}
}
