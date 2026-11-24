// Package runtimes describes the agent runtimes an employee can run on (Claude Code, Codex…).
// Everything the control plane decides by runtime reads a capability here instead of comparing
// ids, so adding a runtime is one more entry in the registry (plus its daemon adapter and its
// session.open options block in the protocol).
package runtimes

import "strings"

// ClaudeDefaultModel is the built-in model of Claude sessions (CLAUDE.md).
const ClaudeDefaultModel = "claude-opus-5-5"

// Runtime ids (protocol Runtime, at_roles.runtime, at_employees.runtime).
const (
	Claude      = "claude"
	Codex       = "codex"
	Antigravity = "antigravity"
)

// Runtime is what the control plane knows about one runtime.
type Runtime struct {
	ID    string
	Title string // shown in messages
	// YAMLType is the role's `type:` in the team YAML; TeamType is list_team's member type.
	YAMLType string
	TeamType string
	// DefaultModel is the last resort when no level names a model; "" leaves the choice to the
	// runtime itself (the daemon then leaves the model out).
	DefaultModel string
	// SlashCommands: the runtime runs "/name" text as a command (passthrough commands, session
	// init command list). Without it only prompt templates work.
	SlashCommands bool
	// LiveModeSwitch: a live session can change its permission mode, model and effort
	// (session.set_mode; a session's controls, #53).
	LiveModeSwitch bool
	// ModeSwitchNow: set_mode is in force at once (Claude: setPermissionMode); without it the
	// change applies from the next turn (Codex turn/start, Antigravity's permission answers).
	// The thread auto-approve switch only goes to these runtimes.
	ModeSwitchNow bool
	// Modes are the session.set_mode permission modes a session's user may pick, in menu order
	// ("auto" is shown only when the model supports it; "bypassPermissions" only when the role
	// lets the employee skip permission prompts).
	Modes []string
	// Efforts: the runtime has a thinking effort apart from the model id.
	Efforts bool
	// NeedsIsolation: may only run on nodes that isolate it (agent_user or isolated,
	// architecture.md §10.3), because the runtime cannot be held to the platform's tool policy.
	NeedsIsolation bool
	// CanLead: may be a coordinator or a thread lead.
	CanLead bool
	// NeedsHelloReady: may only run on nodes whose hello reports the runtime installed and logged
	// in (it has no node.usage report to tell whether it is logged in).
	NeedsHelloReady bool
}

// order is the registry's display order; the first entry is the default runtime.
var order = []Runtime{
	{ID: Claude, Title: "Claude Code", YAMLType: "claude_code", TeamType: "claude_code", DefaultModel: ClaudeDefaultModel,
		SlashCommands: true, LiveModeSwitch: true, ModeSwitchNow: true, CanLead: true, Efforts: true,
		Modes: []string{"auto", "default", "acceptEdits", "plan", "bypassPermissions"}},
	// Codex: auto = workspace-write + on-request with approvals reviewed by its auto_review subagent.
	{ID: Codex, Title: "Codex", YAMLType: "codex", TeamType: "codex", NeedsIsolation: true, LiveModeSwitch: true, Efforts: true,
		Modes: []string{"auto", "default", "bypassPermissions"}},
	// Google Antigravity CLI (agy) over ACP; agy picks the model when none is set, and it has no
	// usage API. v1 cannot lead.
	// Its effort is part of the model id (switching effort = switching model).
	{ID: Antigravity, Title: "Antigravity", YAMLType: "antigravity", TeamType: "antigravity", NeedsIsolation: true,
		NeedsHelloReady: true, LiveModeSwitch: true, Modes: []string{"default", "bypassPermissions"}},
}

var byID = func() map[string]Runtime {
	m := make(map[string]Runtime, len(order))
	for _, r := range order {
		m[r.ID] = r
	}
	return m
}()

// Default is the runtime of a role or employee that names none.
func Default() Runtime { return order[0] }

// Get looks a runtime up by id.
func Get(id string) (Runtime, bool) {
	r, ok := byID[id]
	return r, ok
}

// Of is the runtime with this id; "" or an unknown id is the default runtime.
func Of(id string) Runtime {
	if r, ok := byID[id]; ok {
		return r
	}
	return Default()
}

// Valid reports whether id names a registered runtime.
func Valid(id string) bool {
	_, ok := byID[id]
	return ok
}

// All lists the registered runtimes in display order.
func All() []Runtime { return append([]Runtime(nil), order...) }

// IDs lists the registered runtime ids in display order.
func IDs() []string {
	out := make([]string, len(order))
	for i, r := range order {
		out[i] = r.ID
	}
	return out
}

// LeaderTitles names the runtimes that may lead (coordinator, lead), for messages.
func LeaderTitles() string {
	var out []string
	for _, r := range order {
		if r.CanLead {
			out = append(out, r.Title)
		}
	}
	return strings.Join(out, "、")
}

// FromYAMLType maps a team-YAML `type:` to a runtime id: "" and unknown types are the default
// runtime, and a bare runtime id is accepted too.
func FromYAMLType(t string) string {
	for _, r := range order {
		if t == r.YAMLType || t == r.ID {
			return r.ID
		}
	}
	return Default().ID
}

// Isolated reports whether a node with these settings may run runtimes that need isolation.
func Isolated(isolatedFlag bool, isolation string) bool {
	return isolatedFlag || isolation == "agent_user"
}

// RunsOn reports whether runtime id may run on a node with these isolation settings.
func RunsOn(id string, isolatedFlag bool, isolation string) bool {
	return !Of(id).NeedsIsolation || Isolated(isolatedFlag, isolation)
}
