package engine_test

import (
	"encoding/json"
	"strings"
	"testing"

	protocol "github.com/touwaeriol/agent-teams/protocol"

	"github.com/touwaeriol/agent-teams/server/internal/composer"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/httpapi"
	"github.com/touwaeriol/agent-teams/server/internal/orchapi"
	"github.com/touwaeriol/agent-teams/server/internal/orchestrator"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// expectSetMode waits for a session.set_mode to this session and returns its model.
func (a *fakeAgent) expectSetMode() string {
	a.t.Helper()
	ctx, cancel := a.w.waitCtx()
	defer cancel()
	f, err := a.node.Wait(ctx, protocol.TypeSessionSetMode, a.mine)
	if err != nil {
		a.t.Fatal(err)
	}
	var m protocol.SessionSetMode
	_ = f.Decode(&m)
	if m.Model == nil {
		return ""
	}
	return *m.Model
}

// TestModelLists: everyone reads the platform list (Opus 5.5, the Claude default) followed by
// their own entries; users replace only their own list; the platform list is admin-only, keeps
// order and one default per runtime, and the default is what sessions open with.
func TestModelLists(t *testing.T) {
	tm := startTeam(t) // the first user is the administrator
	user := tm.w.signUp("plain-models@example.com")
	var list httpapi.ModelsBody
	user.must(200, "GET", "/api/models", nil, &list)
	if len(list.Models) != 1 || list.Models[0].Model != "claude-opus-5-5" || !list.Models[0].Default || list.Models[0].Mine {
		t.Fatalf("platform list: %+v", list.Models)
	}
	mine := []map[string]any{{"runtime": "claude", "model": "claude-sonnet-x", "label": "Sonnet"}, {"runtime": "codex", "model": "gpt-x", "default": true}}
	user.must(200, "PUT", "/api/models/mine", map[string]any{"models": mine}, &list)
	if len(list.Models) != 3 || !list.Models[1].Mine || list.Models[1].Label != "Sonnet" || list.Models[2].Default {
		t.Fatalf("own list (a user entry is never a default): %+v", list.Models)
	}
	tm.b.must(200, "GET", "/api/models", nil, &list)
	if len(list.Models) != 1 {
		t.Fatalf("another user's entries leaked: %+v", list.Models)
	}
	for _, bad := range []map[string]any{{"runtime": "claude", "model": "has space"}, {"runtime": "gemini", "model": "x"}} {
		user.must(422, "PUT", "/api/models/mine", map[string]any{"models": []map[string]any{bad}}, nil)
	}
	user.must(422, "PUT", "/api/models/mine", map[string]any{"models": []map[string]any{mine[0], mine[0]}}, nil)
	platform := []map[string]any{{"runtime": "claude", "model": "claude-haiku-y", "label": "Haiku", "default": true}, {"runtime": "claude", "model": "claude-opus-5-5", "label": "Opus 5.5"}}
	if code, _ := user.do("PUT", "/api/admin/models", map[string]any{"models": platform}); code != 403 {
		t.Fatalf("non-admin saved the platform list: %d", code)
	}
	tm.b.must(422, "PUT", "/api/admin/models", map[string]any{"models": []map[string]any{platform[0], {"runtime": "claude", "model": "z", "default": true}}}, nil)
	tm.b.must(200, "PUT", "/api/admin/models", map[string]any{"models": platform}, &list)
	if len(list.Models) != 2 || list.Models[0].Model != "claude-haiku-y" || !list.Models[0].Default {
		t.Fatalf("platform list saved: %+v", list.Models)
	}
	if got := composer.DefaultModel(tm.w.ctx, tm.w.e.Core.Pool, "claude"); got != "claude-haiku-y" {
		t.Fatalf("default = %q", got)
	}
	var cm orchapi.ConversationModel
	tm.b.must(200, "GET", "/api/conversations/"+tm.thread.ID+"/model", nil, &cm)
	if cm.Model != "claude-haiku-y" || cm.Inherited != "claude-haiku-y" || cm.Override != "" {
		t.Fatalf("lead follows the platform default: %+v", cm)
	}
}

// TestModelCommand: /model in a thread changes the lead (or every employee of the thread with
// the lead's runtime) and sends the running sessions session.set_mode; in the coordinator chat it
// sets the project's coordinator model. A turn_ended model shows as the session's model.
func TestModelCommand(t *testing.T) {
	tm := startTeam(t)
	b := tm.b
	conv := "/api/conversations/" + tm.thread.ID + "/model"
	var res orchestrator.ModelResult
	b.must(200, "PUT", conv, map[string]any{"model": "claude-sonnet-x"}, &res)
	if len(res.Applied) != 1 || res.Applied[0].Employee != "lead-1" || !res.Applied[0].Live || res.Applied[0].Model != "claude-sonnet-x" {
		t.Fatalf("lead only: %+v", res)
	}
	if m := tm.lead.expectSetMode(); m != "claude-sonnet-x" {
		t.Fatalf("lead set_mode model %q", m)
	}
	var cm orchapi.ConversationModel
	b.must(200, "GET", conv, nil, &cm)
	if cm.Scope != "thread" || cm.Model != "claude-sonnet-x" || cm.Override != "claude-sonnet-x" || cm.Inherited != "claude-opus-5-5" || !cm.Live || len(cm.Members) != 3 {
		t.Fatalf("thread model: %+v", cm)
	}
	for _, m := range cm.Members {
		if m.Name != "lead-1" && m.Model != "claude-opus-5-5" {
			t.Fatalf("members untouched: %+v", cm.Members)
		}
	}

	b.must(200, "PUT", conv, map[string]any{"model": "claude-haiku-y", "scope": "thread"}, &res)
	if len(res.Applied) != 3 {
		t.Fatalf("whole thread: %+v", res)
	}
	if tm.lead.expectSetMode() != "claude-haiku-y" || tm.dev.expectSetMode() != "claude-haiku-y" {
		t.Fatal("whole thread set_mode")
	}
	b.must(422, "PUT", conv, map[string]any{"model": "bad model"}, nil)
	b.must(200, "PUT", conv, map[string]any{"model": ""}, &res)
	if res.Applied[0].Model != "claude-opus-5-5" || tm.lead.expectSetMode() != "claude-opus-5-5" {
		t.Fatalf("cleared: %+v", res)
	}

	tm.lead.event("turn_ended", func(ev *protocol.SessionEvent) {
		m, sub, isErr, turns, ms := "claude-haiku-y", "success", false, 1, 10
		usage := json.RawMessage(`{"input_tokens":10,"output_tokens":5}`)
		ev.Model, ev.ResultSubtype, ev.IsError, ev.NumTurns, ev.DurationMS, ev.Usage = &m, &sub, &isErr, &turns, &ms, &usage
	})
	var cc orchapi.ComposerContext
	eventually(t, "turn model", func() bool {
		b.must(200, "GET", "/api/conversations/"+tm.thread.ID+"/context", nil, &cc)
		return cc.Session != nil && cc.Session.Model == "claude-haiku-y"
	})

	var coord struct {
		ConversationID string `json:"conversation_id"`
	}
	b.must(200, "GET", "/api/projects/"+tm.projectID+"/coordinator", nil, &coord)
	b.must(200, "PUT", "/api/conversations/"+coord.ConversationID+"/model", map[string]any{"model": "claude-sonnet-x", "scope": "thread"}, &res)
	if len(res.Applied) != 1 || res.Applied[0].Live {
		t.Fatalf("coordinator without a session: %+v", res)
	}
	var p struct {
		CoordinatorModel string `json:"coordinator_model"`
	}
	b.must(200, "GET", "/api/projects/"+tm.projectID, nil, &p)
	b.must(200, "GET", "/api/conversations/"+coord.ConversationID+"/model", nil, &cm)
	if p.CoordinatorModel != "claude-sonnet-x" || cm.Scope != "coordinator" || cm.Override != "claude-sonnet-x" || len(cm.Members) != 0 {
		t.Fatalf("coordinator model: %q %+v", p.CoordinatorModel, cm)
	}
	other := tm.w.signUp("other-model@example.com")
	if code, _ := other.do("PUT", conv, map[string]any{"model": "x"}); code != 404 {
		t.Fatalf("another user's conversation = %d", code)
	}
}

// TestCustomCommands: a role's prompt command is expanded on the server (arguments inserted
// literally) while the stored message keeps the typed text; passthrough commands go as typed;
// platform built-ins reach every role; names of platform commands, duplicates and passthrough
// for Codex roles are refused; the "/" list carries the hidden platform commands and the customs.
func TestCustomCommands(t *testing.T) {
	tm := startTeam(t)
	b := tm.b
	var roles httpapi.RolesBody
	b.must(200, "GET", "/api/roles", nil, &roles)
	lead := -1
	for i, r := range roles.Roles {
		if r.Tier == "lead" {
			lead = i
		}
	}
	roles.Roles[lead].HiddenCommands = []string{"usage", "memory"}
	roles.Roles[lead].Commands = []store.CustomCommand{
		{Name: "plan", Description: "先出计划", Mode: "prompt", Prompt: "先列出计划再动手。目标：{{args}}"},
		{Name: "compact", Mode: "passthrough"},
	}
	for i := range roles.Roles {
		if roles.Roles[i].Role == "dev" {
			roles.Roles[i].Commands = []store.CustomCommand{{Name: "check", Mode: "prompt", Prompt: "检查 {{args}}，只读不改"}}
		}
	}
	b.must(200, "PUT", "/api/roles", map[string]any{"roles": roles.Roles}, &roles)

	for _, bad := range [][]store.CustomCommand{
		{{Name: "model", Mode: "prompt", Prompt: "x"}},
		{{Name: "a", Mode: "prompt", Prompt: "x"}, {Name: "a", Mode: "prompt", Prompt: "y"}},
		{{Name: "Bad Name", Mode: "prompt", Prompt: "x"}},
		{{Name: "go", Mode: "prompt", Prompt: ""}},
		{{Name: "go", Mode: "prompt", Prompt: "/compact"}},
	} {
		next := append([]store.Role{}, roles.Roles...)
		next[lead].Commands = bad
		b.must(422, "PUT", "/api/roles", map[string]any{"roles": next}, nil)
	}
	for _, runtime := range []string{"codex", "claude"} {
		member := append([]store.Role{}, roles.Roles...)
		for i := range member {
			if member[i].Tier == "member" {
				member[i].Runtime = runtime
				member[i].Commands = []store.CustomCommand{{Name: "review", Mode: "passthrough"}}
			}
		}
		b.must(422, "PUT", "/api/roles", map[string]any{"roles": member}, nil)
	}
	b.must(422, "PUT", "/api/admin/commands", map[string]any{"commands": []map[string]any{{"name": "status", "prompt": "x"}}}, nil)
	b.must(200, "PUT", "/api/admin/commands", map[string]any{"commands": []map[string]any{{"name": "tests", "description": "跑测试", "prompt": "跑一遍测试并汇报"}, {"name": "plan", "prompt": "被岗位覆盖"}}}, nil)

	var cmds orchapi.ComposerCommands
	b.must(200, "GET", "/api/conversations/"+tm.thread.ID+"/commands", nil, &cmds)
	if strings.Join(cmds.Hidden, ",") != "usage,memory" || len(cmds.Custom) != 3 || cmds.Custom[0].Name != "plan" || cmds.Custom[0].Source != "role" ||
		cmds.Custom[2].Name != "tests" || cmds.Custom[2].Source != "platform" {
		t.Fatalf("commands: %+v", cmds)
	}

	var msg core.MessageView
	b.must(202, "POST", "/api/conversations/"+tm.thread.ID+"/messages", map[string]any{"content": "/plan 登录页 {{args}} $1"}, &msg)
	send := tm.lead.expectSend("先列出计划")
	if send.Text != "先列出计划再动手。目标：登录页 {{args}} $1" {
		t.Fatalf("expanded: %q", send.Text)
	}
	cmd, _ := msg.Meta["command"].(map[string]any)
	if msg.Content != "/plan 登录页 {{args}} $1" || cmd["name"] != "plan" || cmd["mode"] != "prompt" {
		t.Fatalf("stored: %+v", msg)
	}
	b.must(202, "POST", "/api/conversations/"+tm.thread.ID+"/messages", map[string]any{"content": "/tests"}, nil)
	if s := tm.lead.expectSend("跑一遍测试"); s.Text != "跑一遍测试并汇报" {
		t.Fatalf("built-in: %q", s.Text)
	}
	b.must(202, "POST", "/api/conversations/"+tm.thread.ID+"/messages", map[string]any{"content": "/compact 留下结论"}, nil)
	if s := tm.lead.expectSend("/compact"); s.Text != "/compact 留下结论" {
		t.Fatalf("passthrough: %q", s.Text)
	}
	team := text(tm.dev.mustCall("list_team", map[string]any{}))
	if !strings.Contains(team, "dev-1") || !strings.Contains(team, "/check /tests") {
		t.Fatalf("list_team: %s", team)
	}
	tm.lead.mustCall("send_message", map[string]any{"to": "dev-1", "text": "/check 登录页"})
	if s := tm.dev.expectSend("检查 登录页"); s.Text != "[Message from lead-1]\n检查 登录页，只读不改" {
		t.Fatalf("lead → member: %q", s.Text)
	}
	b.must(202, "POST", "/api/threads/"+tm.thread.ID+"/members/dev-1/message", map[string]any{"content": "/check 表单"}, nil)
	if s := tm.dev.expectSend("检查 表单"); s.Text != "[用户直接对你说] 检查 表单，只读不改" {
		t.Fatalf("user → member: %q", s.Text)
	}
	var builtin httpapi.CommandsBody
	other := tm.w.signUp("cmds-user@example.com")
	other.must(200, "GET", "/api/commands", nil, &builtin)
	if len(builtin.Commands) != 2 || len(builtin.Platform) != len(composer.PlatformCommands) {
		t.Fatalf("built-ins: %+v", builtin)
	}
	if code, _ := other.do("PUT", "/api/admin/commands", map[string]any{"commands": []any{}}); code != 403 {
		t.Fatalf("non-admin saved built-ins: %d", code)
	}
}
