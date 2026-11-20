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

// TestModelListGone: there is no model list any more (each role and the project settings name
// their models by hand): GET /api/models, PUT /api/models/mine and PUT /api/admin/models are 404.
func TestModelListGone(t *testing.T) {
	tm := startTeam(t) // the first user is the administrator
	user := tm.w.signUp("plain-models@example.com")
	for _, c := range []*browser{user, tm.b} {
		for _, r := range []struct{ method, path string }{{"GET", "/api/models"}, {"PUT", "/api/models/mine"}, {"PUT", "/api/admin/models"}} {
			if code, _ := c.do(r.method, r.path, map[string]any{"models": []any{}}); code != 404 {
				t.Fatalf("%s %s = %d, want 404", r.method, r.path, code)
			}
		}
	}
}

// TestModelFallback: a member's model falls back from the employee (/model) to the project, the
// role and finally Opus 5.5; the lead from the project's thread_model to its role and Opus 5.5;
// the coordinator from the project's coordinator_model to its role and Opus 5.5.
func TestModelFallback(t *testing.T) {
	tm := startTeam(t)
	b := tm.b
	conv := "/api/conversations/" + tm.thread.ID + "/model"
	var coord struct {
		ConversationID string `json:"conversation_id"`
	}
	b.must(200, "GET", "/api/projects/"+tm.projectID+"/coordinator", nil, &coord)
	coordConv := "/api/conversations/" + coord.ConversationID + "/model"
	var cm, cc orchapi.ConversationModel
	b.must(200, "GET", conv, nil, &cm)
	if cm.Model != "claude-opus-5-5" || cm.Inherited != "claude-opus-5-5" || cm.Override != "" {
		t.Fatalf("nothing configured: %+v", cm)
	}
	for _, m := range cm.Members {
		if m.Model != "claude-opus-5-5" {
			t.Fatalf("members built in: %+v", cm.Members)
		}
	}
	b.must(200, "GET", coordConv, nil, &cc)
	if cc.Model != "claude-opus-5-5" {
		t.Fatalf("coordinator built in: %+v", cc)
	}

	memberModel := func(cm orchapi.ConversationModel) orchapi.MemberModel {
		t.Helper()
		for _, m := range cm.Members {
			if m.Tier == "member" {
				return m
			}
		}
		t.Fatalf("no member in %+v", cm.Members)
		return orchapi.MemberModel{}
	}

	// The role's model (the employees' copy of their role).
	if _, err := store.Exec(tm.w.ctx, tm.w.e.Core.Pool, `UPDATE at_employees SET spec = jsonb_set(spec, '{model}', to_jsonb(tier || '-role-m'))
		WHERE project_id = $1`, tm.projectID); err != nil {
		t.Fatal(err)
	}
	b.must(200, "GET", conv, nil, &cm)
	b.must(200, "GET", coordConv, nil, &cc)
	if m := memberModel(cm); cm.Model != "lead-role-m" || m.Model != "member-role-m" || cc.Model != "coordinator-role-m" {
		t.Fatalf("roles ahead of the built-in default: %+v %+v", cm, cc)
	}

	b.must(200, "PATCH", "/api/projects/"+tm.projectID, map[string]any{"thread_model": "claude-project-z", "coordinator_model": "claude-coord-z"}, nil)
	b.must(200, "GET", conv, nil, &cm)
	b.must(200, "GET", coordConv, nil, &cc)
	if m := memberModel(cm); cm.Model != "claude-project-z" || m.Model != "claude-project-z" || cc.Model != "claude-coord-z" || cc.Inherited != "claude-coord-z" {
		t.Fatalf("project ahead of the roles: %+v %+v", cm, cc)
	}
	b.must(200, "PUT", conv, map[string]any{"model": "claude-picked", "scope": "thread"}, nil)
	b.must(200, "GET", conv, nil, &cm)
	if m := memberModel(cm); cm.Model != "claude-project-z" || cm.Override != "" || m.Model != "claude-picked" || m.Override != "claude-picked" {
		t.Fatalf("/model ahead of the project for members only: %+v", cm)
	}

	b.must(200, "PUT", conv, map[string]any{"model": "", "scope": "thread"}, nil)
	b.must(200, "PATCH", "/api/projects/"+tm.projectID, map[string]any{"thread_model": "", "coordinator_model": ""}, nil)
	if _, err := store.Exec(tm.w.ctx, tm.w.e.Core.Pool, `UPDATE at_employees SET spec = jsonb_set(spec, '{model}', '""') WHERE project_id = $1`, tm.projectID); err != nil {
		t.Fatal(err)
	}
	b.must(200, "GET", conv, nil, &cm)
	b.must(200, "GET", coordConv, nil, &cc)
	if m := memberModel(cm); cm.Model != "claude-opus-5-5" || m.Model != "claude-opus-5-5" || cc.Model != "claude-opus-5-5" {
		t.Fatalf("nothing set falls back to Opus 5.5: %+v %+v", cm, cc)
	}
}

// TestRoleModelReachesFixed: saving the coordinator and lead templates' model reaches the
// existing coordinator and the lead of the open thread (their role copy), the lead's live session
// gets session.set_mode, and GET /model shows it; clearing it goes back to the built-in default.
func TestRoleModelReachesFixed(t *testing.T) {
	tm := startTeam(t)
	b := tm.b
	conv := "/api/conversations/" + tm.thread.ID + "/model"
	var coord struct {
		ConversationID string `json:"conversation_id"`
	}
	b.must(200, "GET", "/api/projects/"+tm.projectID+"/coordinator", nil, &coord)
	coordConv := "/api/conversations/" + coord.ConversationID + "/model"
	setModels := func(coordModel, leadModel string) {
		t.Helper()
		var roles httpapi.RolesBody
		b.must(200, "GET", "/api/roles", nil, &roles)
		for i := range roles.Roles {
			switch roles.Roles[i].Tier {
			case "coordinator":
				roles.Roles[i].Model = coordModel
			case "lead":
				roles.Roles[i].Model = leadModel
			}
		}
		b.must(200, "PUT", "/api/roles", map[string]any{"roles": roles.Roles}, nil)
	}
	setModels("claude-coord-r", "claude-lead-r")
	if got := tm.lead.expectSetMode(); got != "claude-lead-r" {
		t.Fatalf("lead set_mode %q", got)
	}
	var cm orchapi.ConversationModel
	b.must(200, "GET", conv, nil, &cm)
	if cm.Model != "claude-lead-r" {
		t.Fatalf("lead model: %+v", cm)
	}
	b.must(200, "GET", coordConv, nil, &cm)
	if cm.Model != "claude-coord-r" {
		t.Fatalf("coordinator model: %+v", cm)
	}
	// The project setting still wins over the role.
	b.must(200, "PATCH", "/api/projects/"+tm.projectID, map[string]any{"coordinator_model": "claude-coord-p"}, nil)
	b.must(200, "GET", coordConv, nil, &cm)
	if cm.Model != "claude-coord-p" {
		t.Fatalf("project over role: %+v", cm)
	}
	b.must(200, "PATCH", "/api/projects/"+tm.projectID, map[string]any{"coordinator_model": ""}, nil)
	setModels("", "")
	if got := tm.lead.expectSetMode(); got != "claude-opus-5-5" {
		t.Fatalf("lead cleared set_mode %q", got)
	}
	b.must(200, "GET", conv, nil, &cm)
	if cm.Model != "claude-opus-5-5" {
		t.Fatalf("lead cleared: %+v", cm)
	}
	b.must(200, "GET", coordConv, nil, &cm)
	if cm.Model != "claude-opus-5-5" {
		t.Fatalf("coordinator cleared: %+v", cm)
	}
}

// TestModelCommand: /model in a thread (scope thread) changes every member of the thread with the
// lead's runtime and sends their running sessions session.set_mode; the lead keeps its model and
// is listed as skipped. The lead scope and the coordinator chat are refused (409 model_fixed):
// their models are set in the role or the project settings. A turn_ended model shows as the session's model.
func TestModelCommand(t *testing.T) {
	tm := startTeam(t)
	b := tm.b
	conv := "/api/conversations/" + tm.thread.ID + "/model"
	refused := func(path string, body map[string]any) {
		t.Helper()
		code, raw := b.do("PUT", path, body)
		if code != 409 || !strings.Contains(string(raw), "统筹和组长不能用 /model 切换模型，请在岗位配置或项目设置里改") || !strings.Contains(string(raw), "model_fixed") {
			t.Fatalf("PUT %s %v = %d %s", path, body, code, raw)
		}
	}
	refused(conv, map[string]any{"model": "claude-sonnet-x"})
	refused(conv, map[string]any{"model": "claude-sonnet-x", "scope": "lead"})

	var res orchestrator.ModelResult
	b.must(200, "PUT", conv, map[string]any{"model": "claude-haiku-y", "scope": "thread"}, &res)
	// dev-1 is the project member working here (dev-2 never did); the lead is left alone.
	if len(res.Applied) != 1 || res.Applied[0].Employee != "dev-1" || !res.Applied[0].Live || res.Applied[0].Model != "claude-haiku-y" ||
		len(res.Skipped) != 1 || res.Skipped[0] != "lead-1" {
		t.Fatalf("members only: %+v", res)
	}
	if tm.dev.expectSetMode() != "claude-haiku-y" {
		t.Fatal("member set_mode")
	}
	var cm orchapi.ConversationModel
	b.must(200, "GET", conv, nil, &cm)
	if cm.Scope != "thread" || cm.Model != "claude-opus-5-5" || cm.Override != "" || cm.Inherited != "claude-opus-5-5" || !cm.Live || len(cm.Members) != 2 {
		t.Fatalf("thread model: %+v", cm)
	}
	for _, m := range cm.Members {
		if (m.Name == "lead-1" && (m.Model != "claude-opus-5-5" || m.Override != "")) ||
			(m.Name == "dev-1" && (m.Model != "claude-haiku-y" || m.Override != "claude-haiku-y")) {
			t.Fatalf("members: %+v", cm.Members)
		}
	}
	b.must(422, "PUT", conv, map[string]any{"model": "bad model", "scope": "thread"}, nil)
	b.must(200, "PUT", conv, map[string]any{"model": "", "scope": "thread"}, &res)
	if len(res.Applied) != 1 || res.Applied[0].Model != "claude-opus-5-5" || tm.dev.expectSetMode() != "claude-opus-5-5" {
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
	coordConv := "/api/conversations/" + coord.ConversationID + "/model"
	refused(coordConv, map[string]any{"model": "claude-sonnet-x"})
	refused(coordConv, map[string]any{"model": "claude-sonnet-x", "scope": "thread"})
	b.must(200, "GET", coordConv, nil, &cm)
	if cm.Scope != "coordinator" || cm.Model != "claude-opus-5-5" || cm.Override != "" || cm.Inherited != "claude-opus-5-5" || len(cm.Members) != 0 {
		t.Fatalf("coordinator model: %+v", cm)
	}
	other := tm.w.signUp("other-model@example.com")
	if code, _ := other.do("PUT", conv, map[string]any{"model": "x", "scope": "thread"}); code != 404 {
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
