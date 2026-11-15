package orchestrator

import (
	"context"
	"encoding/json"

	protocol "github.com/touwaeriol/agent-teams/protocol"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/plugins"
)

// agentTool is the Agent SDK tool that runs subagents (plugin agents/).
const agentTool = "Agent"

// payloadBudget keeps session.open (skills and plugins included) well under the 4 MiB frame.
const payloadBudget = 3 << 20

// addPlugins puts the role's plugins into session.open at their exact versions (architecture.md
// §6.5). The coordinator never gets plugins. Claude loads them as local plugins; Codex gets the
// skill and command text in its developer instructions plus the plugins' MCP servers.
func (o *Orchestrator) addPlugins(ctx context.Context, q core.DB, userID string, e core.Employee, open *protocol.SessionOpen) error {
	if e.Tier == "coordinator" || len(e.Spec.Plugins) == 0 {
		return nil
	}
	used, err := json.Marshal(open)
	if err != nil {
		return err
	}
	bundles, err := plugins.Bundles(ctx, q, userID, e.Spec.Plugins, payloadBudget-len(used))
	if err != nil || len(bundles) == 0 {
		return err
	}
	applyPlugins(open, bundles)
	return nil
}

func applyPlugins(open *protocol.SessionOpen, bundles []protocol.PluginBundle) {
	if open.Codex != nil {
		applyCodexPlugins(open, bundles)
		return
	}
	if open.Claude == nil {
		return
	}
	open.Plugins = bundles
	if plugins.HasSkills(bundles) {
		open.Claude.Tools = mergeLists(open.Claude.Tools, []string{skillTool})
		open.Claude.AllowedTools = mergeLists(open.Claude.AllowedTools, []string{skillTool})
	}
	// Subagents need the Agent tool. It is left out of allowed_tools; the CLI starts a subagent
	// without asking (verified on 0.3.325), and every tool the subagent calls goes through canUseTool.
	if plugins.HasAgents(bundles) {
		open.Claude.Tools = mergeLists(open.Claude.Tools, []string{agentTool})
	}
}

// applyCodexPlugins: skills and commands as text; only bundles with MCP servers are sent, since
// the daemon needs their files for the server commands. Subagents and hooks are Claude-only.
func applyCodexPlugins(open *protocol.SessionOpen, bundles []protocol.PluginBundle) {
	dev := ""
	if open.Codex.DeveloperInstructions != nil {
		dev = *open.Codex.DeveloperInstructions
	}
	dev += plugins.Instructions(bundles)
	open.Codex.DeveloperInstructions = &dev
	for _, b := range bundles {
		if len(b.McpServers) > 0 {
			open.Plugins = append(open.Plugins, b)
		}
	}
}
