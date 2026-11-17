package orchapi

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/composer"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/plugins"
	"github.com/touwaeriol/agent-teams/server/internal/skills"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// skillsPlugin is the local plugin the daemon loads a role's skills as (node sessions/skills.ts):
// its commands are shown and typed without the prefix (the daemon qualifies "/name" again).
const skillsPlugin = "agent-teams-skills:"

// ComposerCommand is a skill or plugin command the employee's session runs when a message starts with "/name".
type ComposerCommand struct {
	Name         string `json:"name" doc:"Typed after the slash"`
	Description  string `json:"description"`
	ArgumentHint string `json:"argument_hint"`
	Source       string `json:"source" enum:"skill,plugin"`
}

// ComposerCommands is GET /api/conversations/{cid}/commands.
type ComposerCommands struct {
	Employee string              `json:"employee"`
	Tier     string              `json:"tier" enum:"coordinator,lead"`
	Runtime  string              `json:"runtime"`
	From     string              `json:"from" enum:"session,role" doc:"session: what the latest session reported; role: the role's configured skills and plugins"`
	Commands []ComposerCommand   `json:"commands" doc:"Empty for Codex employees"`
	Hidden   []string            `json:"hidden" doc:"Platform commands the employee's role leaves out of its chats"`
	Custom   []composer.Resolved `json:"custom" doc:"The role's custom commands, then the platform built-ins (prompt: expanded by the server; passthrough: sent to Claude Code as typed)"`
}

type commandsOutput struct{ Body ComposerCommands }

// TokenUsage sums tokens of one or more turns.
type TokenUsage struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cache_read"`
	CacheWrite int64 `json:"cache_write"`
}

// SessionUsage is the token use of the employee's latest session, from the SDK result usage.
type SessionUsage struct {
	State    string     `json:"state"`
	Model    string     `json:"model"`
	Turns    int        `json:"turns"`
	Total    TokenUsage `json:"total"`
	LastTurn TokenUsage `json:"last_turn"`
	CostUSD  float64    `json:"cost_usd"`
}

// ComposerContext is GET /api/conversations/{cid}/context: what the employee works from.
type ComposerContext struct {
	Employee          string        `json:"employee"`
	Runtime           string        `json:"runtime"`
	InstructionsChars int           `json:"instructions_chars" doc:"项目指令 length"`
	MemoryIndexChars  int           `json:"memory_index_chars" doc:"MEMORY.md length (read by every session)"`
	MemoryFiles       int           `json:"memory_files" doc:"Topic files besides MEMORY.md"`
	Messages          int           `json:"messages" doc:"Messages in this conversation"`
	MessageChars      int           `json:"message_chars"`
	Session           *SessionUsage `json:"session" doc:"null before the first session"`
}

type contextOutput struct{ Body ComposerContext }

func (a *API) registerComposer(api huma.API) {
	t := "conversations"
	huma.Register(api, op("conversation-commands", "GET", "/api/conversations/{cid}/commands", t,
		summary("Skills and plugin commands the conversation's employee can run as /name")), a.commands)
	huma.Register(api, op("conversation-context", "GET", "/api/conversations/{cid}/context", t,
		summary("What the conversation's employee works from: instructions, memory, conversation and session token use")), a.context)
	huma.Register(api, op("project-references", "GET", "/api/projects/{pid}/references", "projects",
		summary("Repository files, threads, deliverables and memory files to reference with @")), a.references)
}

// employeeOf is the employee a conversation talks to: the project coordinator or the thread lead.
func (a *API) employeeOf(ctx context.Context, u store.User, c core.Conversation) (core.Employee, error) {
	if c.Kind == "thread" {
		return core.Lead(ctx, a.e.Core.Pool, c.ID)
	}
	if c.Kind != "coordinator" || c.ProjectID == nil {
		return core.Employee{}, core.ErrNotFound
	}
	e, _, err := a.e.Orch.EnsureCoordinator(ctx, u.ID, *c.ProjectID)
	return e, err
}

func (a *API) conversationEmployee(ctx context.Context, cid string) (store.User, core.Conversation, core.Employee, error) {
	u, c, err := a.conversation(ctx, cid)
	if err != nil {
		return u, c, core.Employee{}, err
	}
	e, err := a.employeeOf(ctx, u, c)
	return u, c, e, fail(err, "没有这个对话")
}

func (a *API) commands(ctx context.Context, in *cidInput) (*commandsOutput, error) {
	u, _, e, err := a.conversationEmployee(ctx, in.CID)
	if err != nil {
		return nil, err
	}
	out := ComposerCommands{Employee: e.Name, Tier: e.Tier, Runtime: e.Runtime, From: "role", Commands: []ComposerCommand{}}
	role := composer.RoleOf(ctx, a.e.Core.Pool, e)
	out.Hidden = append([]string{}, role.HiddenCommands...)
	if out.Custom, err = composer.EmployeeCommands(ctx, a.e.Core.Pool, e, role); err != nil {
		return nil, err
	}
	if e.Runtime == "codex" {
		return &commandsOutput{Body: out}, nil
	}
	if init, ok := latestInit(ctx, a.e.Core.Pool, e.ID); ok && init.Commands != nil {
		out.From, out.Commands = "session", sessionCommands(init.Commands)
		return &commandsOutput{Body: out}, nil
	}
	out.Commands, err = a.roleCommands(ctx, u.ID, e)
	return &commandsOutput{Body: out}, err
}

// expandCommand turns a message that names a prompt custom command ("/name args") into the text
// the employee gets; passthrough commands and everything else are delivered as typed. meta
// records the command on the stored message, which keeps the user's own words.
func (a *API) expandCommand(ctx context.Context, u store.User, c core.Conversation, content string) (string, map[string]any, error) {
	if !strings.HasPrefix(content, "/") {
		return "", nil, nil
	}
	e, err := a.employeeOf(ctx, u, c)
	if err != nil {
		return "", nil, fail(err, "没有这个对话")
	}
	deliver, cmd, err := composer.ExpandFor(ctx, a.e.Core.Pool, e, content)
	if err != nil || cmd == nil {
		return "", nil, err
	}
	return deliver, map[string]any{"command": cmd}, nil
}

// latestInit is session.opened's init of the employee's latest session.
func latestInit(ctx context.Context, q core.DB, employeeID string) (protocol.SessionInit, bool) {
	var init protocol.SessionInit
	s, err := core.LatestSession(ctx, q, employeeID)
	if err != nil || len(s.Init) == 0 || json.Unmarshal(s.Init, &init) != nil {
		return init, false
	}
	return init, true
}

func sessionCommands(list []protocol.SlashCommand) []ComposerCommand {
	out := make([]ComposerCommand, 0, len(list))
	for _, c := range list {
		cmd := ComposerCommand{Name: c.Name, Source: "plugin", Description: deref(c.Description), ArgumentHint: deref(c.ArgumentHint)}
		if name, ok := strings.CutPrefix(c.Name, skillsPlugin); ok {
			cmd.Name, cmd.Source = name, "skill"
			cmd.Description = strings.TrimSpace(strings.TrimPrefix(cmd.Description, "(agent-teams-skills)"))
		}
		out = append(out, cmd)
	}
	return out
}

// roleCommands lists the role's skills and its plugins' skills and commands (before any session
// reported its own list). The coordinator never gets skills or plugins.
func (a *API) roleCommands(ctx context.Context, userID string, e core.Employee) ([]ComposerCommand, error) {
	out := []ComposerCommand{}
	if e.Tier == "coordinator" {
		return out, nil
	}
	library, err := skills.List(ctx, a.e.Store.Pool, userID)
	if err != nil {
		return nil, err
	}
	desc := map[string]string{}
	for _, s := range library {
		desc[s.Name] = s.Description
	}
	for _, name := range e.Spec.Skills {
		out = append(out, ComposerCommand{Name: name, Description: desc[name], Source: "skill"})
	}
	more, err := a.pluginCommands(ctx, userID, e.Spec.Plugins)
	return append(out, more...), err
}

func (a *API) pluginCommands(ctx context.Context, userID string, refs []string) ([]ComposerCommand, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	list, err := plugins.List(ctx, a.e.Store.Pool, userID)
	if err != nil {
		return nil, err
	}
	byName := map[string]plugins.Plugin{}
	for _, p := range list {
		byName[p.Name] = p
	}
	var out []ComposerCommand
	for _, s := range refs {
		r, ok := plugins.ParseRef(s)
		p, found := byName[r.Name]
		if !ok || !found || !p.Enabled {
			continue
		}
		v, ok := pluginVersion(p, r.Version)
		if !ok {
			continue
		}
		for _, name := range append(append([]string{}, v.Contents.Skills...), v.Contents.Commands...) {
			out = append(out, ComposerCommand{Name: p.Name + ":" + name, Description: v.Description, Source: "plugin"})
		}
	}
	return out, nil
}

func pluginVersion(p plugins.Plugin, want int) (plugins.Version, bool) {
	if want == 0 {
		want = p.Latest
	}
	for _, v := range p.Versions {
		if v.Version == want {
			return v, true
		}
	}
	return plugins.Version{}, false
}

func (a *API) context(ctx context.Context, in *cidInput) (*contextOutput, error) {
	u, c, e, err := a.conversationEmployee(ctx, in.CID)
	if err != nil {
		return nil, err
	}
	p, err := a.e.Store.ProjectOf(ctx, u.ID, e.ProjectID)
	if err != nil {
		return nil, err
	}
	out := ComposerContext{Employee: e.Name, Runtime: e.Runtime, InstructionsChars: len([]rune(p.Context))}
	if err := a.memorySize(ctx, p.ID, &out); err != nil {
		return nil, err
	}
	q := a.e.Core.Pool
	if err := store.QueryRow(ctx, q, `SELECT count(*), COALESCE(sum(char_length(content)), 0) FROM at_messages
		WHERE conversation_id = $1`, c.ID).Scan(&out.Messages, &out.MessageChars); err != nil {
		return nil, err
	}
	out.Session, err = sessionUsage(ctx, q, e.ID)
	return &contextOutput{Body: out}, err
}

func (a *API) memorySize(ctx context.Context, pid string, out *ComposerContext) error {
	files, err := store.ListMemoryFiles(ctx, a.e.Core.Pool, pid)
	if err != nil {
		return err
	}
	for _, f := range files {
		if f.Name == "MEMORY.md" {
			out.MemoryIndexChars = f.Size
			continue
		}
		out.MemoryFiles++
	}
	return nil
}

// sessionUsage sums at_usage (one row per turn, from the SDK result usage) of the latest session.
func sessionUsage(ctx context.Context, q core.DB, employeeID string) (*SessionUsage, error) {
	s, err := core.LatestSession(ctx, q, employeeID)
	if errors.Is(err, core.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	u := &SessionUsage{State: s.State}
	if init, ok := latestInit(ctx, q, employeeID); ok && init.Model != nil {
		u.Model = *init.Model
	}
	t := &u.Total
	if err := store.QueryRow(ctx, q, `SELECT count(*), COALESCE(sum(input_tokens), 0), COALESCE(sum(output_tokens), 0),
		COALESCE(sum(cache_read_tokens), 0), COALESCE(sum(cache_write_tokens), 0), COALESCE(sum(cost_usd), 0)::float8
		FROM at_usage WHERE session_id = $1`, s.ID).Scan(&u.Turns, &t.Input, &t.Output, &t.CacheRead, &t.CacheWrite, &u.CostUSD); err != nil {
		return nil, err
	}
	if u.Turns == 0 {
		return u, nil
	}
	l := &u.LastTurn
	var model string
	err = store.QueryRow(ctx, q, `SELECT COALESCE(input_tokens, 0), COALESCE(output_tokens, 0), COALESCE(cache_read_tokens, 0),
		COALESCE(cache_write_tokens, 0), model FROM at_usage WHERE session_id = $1 ORDER BY id DESC LIMIT 1`, s.ID).
		Scan(&l.Input, &l.Output, &l.CacheRead, &l.CacheWrite, &model)
	if model != "" {
		u.Model = model // the model that answered the last turn (after a /model switch)
	}
	return u, err
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
