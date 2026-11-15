package orchestrator

import (
	"context"
	"strings"
	"testing"

	protocol "github.com/touwaeriol/agent-teams/protocol"

	"github.com/touwaeriol/agent-teams/server/internal/store"
)

func pluginBundle(agents bool, mcp bool) protocol.PluginBundle {
	b := protocol.PluginBundle{Name: "tinyplug", Version: 2, Skills: []string{"tinyplug:greet"},
		Files: []protocol.PluginFile{{Path: ".claude-plugin/plugin.json", Content: "{}"}, {Path: "skills/greet/SKILL.md", Content: "PINEAPPLE-42"}}}
	if agents {
		b.Agents = []string{"tinyplug:echoer"}
	}
	if mcp {
		cmd := "node"
		b.McpServers = protocol.PluginBundleMcpServers{"calc": {Type: "stdio", Command: &cmd}}
	}
	return b
}

func TestApplyPluginsClaude(t *testing.T) {
	o, err := buildOpen(open(t, emp("member", "dev", store.Role{}), store.Node{Isolation: "sandbox"}))
	if err != nil {
		t.Fatal(err)
	}
	applyPlugins(&o, []protocol.PluginBundle{pluginBundle(true, true)})
	if len(o.Plugins) != 1 || !contains(o.Claude.Tools, "Skill") || !contains(o.Claude.AllowedTools, "Skill") {
		t.Fatalf("plugins %+v tools %v allowed %v", o.Plugins, o.Claude.Tools, o.Claude.AllowedTools)
	}
	// Subagents need the Agent tool; it is not added to allowed_tools.
	if !contains(o.Claude.Tools, "Agent") || contains(o.Claude.AllowedTools, "Agent") {
		t.Fatalf("agent tool: tools %v allowed %v", o.Claude.Tools, o.Claude.AllowedTools)
	}
	// MCP tools of a plugin are never pre-approved either.
	for _, a := range o.Claude.AllowedTools {
		if strings.HasPrefix(a, "mcp__plugin") {
			t.Fatalf("plugin MCP tool pre-approved: %v", o.Claude.AllowedTools)
		}
	}
}

func TestApplyPluginsWithoutAgentsOrSkills(t *testing.T) {
	o, _ := buildOpen(open(t, emp("member", "dev", store.Role{}), store.Node{Isolation: "sandbox"}))
	b := pluginBundle(false, true)
	b.Skills = nil
	applyPlugins(&o, []protocol.PluginBundle{b})
	if contains(o.Claude.Tools, "Agent") || contains(o.Claude.Tools, "Skill") || len(o.Plugins) != 1 {
		t.Fatalf("tools %v", o.Claude.Tools)
	}
}

func TestApplyPluginsCodex(t *testing.T) {
	e := emp("member", "dev", store.Role{})
	e.Runtime = "codex"
	o, err := buildOpen(open(t, e, store.Node{Isolation: "agent_user"}))
	if err != nil {
		t.Fatal(err)
	}
	applyPlugins(&o, []protocol.PluginBundle{pluginBundle(true, false), func() protocol.PluginBundle {
		b := pluginBundle(false, true)
		b.Name = "withmcp"
		return b
	}()})
	if !strings.Contains(*o.Codex.DeveloperInstructions, "PINEAPPLE-42") || !strings.HasPrefix(*o.Codex.DeveloperInstructions, "sys") {
		t.Fatalf("codex instructions %q", *o.Codex.DeveloperInstructions)
	}
	if len(o.Plugins) != 1 || o.Plugins[0].Name != "withmcp" {
		t.Fatalf("codex gets only plugins with MCP servers: %+v", o.Plugins)
	}
}

func TestAddPluginsSkipsCoordinatorAndEmpty(t *testing.T) {
	o := &Orchestrator{}
	coord := emp("coordinator", "coordinator", store.Role{Plugins: []string{"tinyplug"}})
	open := protocol.SessionOpen{}
	// No database is touched for the coordinator or a role without plugins.
	if err := o.addPlugins(context.Background(), nil, "u1", coord, &open); err != nil || open.Plugins != nil {
		t.Fatalf("coordinator got plugins: %v %+v", err, open.Plugins)
	}
	if err := o.addPlugins(context.Background(), nil, "u1", emp("member", "dev", store.Role{}), &open); err != nil || open.Plugins != nil {
		t.Fatalf("empty role got plugins: %v", err)
	}
}
