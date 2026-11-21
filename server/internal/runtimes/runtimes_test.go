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
