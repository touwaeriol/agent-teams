package engine_test

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw/nodetest"
	"github.com/touwaeriol/agent-teams/server/internal/orchapi"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// TestComposerCommandsAndContext: the "/" list comes from the role until a session reports its
// own commands (role skills shown without the daemon's plugin prefix); /context sums the latest
// session's turns from at_usage; the coordinator has no skills.
func TestComposerCommandsAndContext(t *testing.T) {
	tm := startTeam(t)
	b, ctx, pool := tm.b, tm.w.ctx, tm.w.e.Core.Pool
	conv := "/api/conversations/" + tm.thread.ID
	var cmds orchapi.ComposerCommands
	b.must(200, "GET", conv+"/commands", nil, &cmds)
	if cmds.Employee != "lead-1" || cmds.Tier != "lead" || cmds.From != "role" || cmds.Commands == nil {
		t.Fatalf("role commands: %+v", cmds)
	}
	if _, err := store.Exec(ctx, pool, `UPDATE at_agent_sessions SET init = $2::jsonb WHERE id = $1`, tm.lead.session,
		`{"tools":[],"model":"claude-opus-5-5","commands":[{"name":"agent-teams-skills:design","description":"(agent-teams-skills) Make mockups","argument_hint":"<what>"},{"name":"review:check"}]}`); err != nil {
		t.Fatal(err)
	}
	b.must(200, "GET", conv+"/commands", nil, &cmds)
	want := []orchapi.ComposerCommand{{Name: "design", Description: "Make mockups", ArgumentHint: "<what>", Source: "skill"}, {Name: "review:check", Source: "plugin"}}
	if cmds.From != "session" || len(cmds.Commands) != 2 || cmds.Commands[0] != want[0] || cmds.Commands[1] != want[1] {
		t.Fatalf("session commands: %+v", cmds)
	}

	var coord struct {
		ConversationID string `json:"conversation_id"`
	}
	b.must(200, "GET", "/api/projects/"+tm.projectID+"/coordinator", nil, &coord)
	b.must(200, "GET", "/api/conversations/"+coord.ConversationID+"/commands", nil, &cmds)
	if cmds.Tier != "coordinator" || len(cmds.Commands) != 0 {
		t.Fatalf("coordinator commands: %+v", cmds)
	}
	var cc orchapi.ComposerContext
	b.must(200, "GET", "/api/conversations/"+coord.ConversationID+"/context", nil, &cc)
	if cc.Session != nil {
		t.Fatalf("coordinator never opened a session: %+v", cc.Session)
	}

	b.must(200, "PATCH", "/api/projects/"+tm.projectID, map[string]any{"context": "用 pnpm 指令"}, nil)
	b.must(200, "PUT", "/api/projects/"+tm.projectID+"/memory/deploy", map[string]string{"description": "部署", "content": "make ship"}, nil)
	for i, turn := range []string{"t_1", "t_2"} {
		if _, err := store.Exec(ctx, pool, `INSERT INTO at_usage (session_id, project_id, thread_id, turn_id, cost_usd, input_tokens,
			output_tokens, cache_read_tokens, cache_write_tokens) VALUES ($1, $2, $3, $4, 0.5, $5, 10, 1000, 100)`,
			tm.lead.session, tm.projectID, tm.thread.ID, turn, 3+i); err != nil {
			t.Fatal(err)
		}
	}
	b.must(200, "GET", conv+"/context", nil, &cc)
	s := cc.Session
	if cc.InstructionsChars != 9 || cc.MemoryFiles != 1 || cc.Messages == 0 || cc.MemoryIndexChars == 0 || s == nil || s.Turns != 2 || s.Model != "claude-opus-5-5" ||
		s.Total.Input != 7 || s.Total.CacheRead != 2000 || s.LastTurn.Input != 4 || s.CostUSD != 1 {
		t.Fatalf("context: %+v session %+v", cc, s)
	}
}

// TestMessageReferences: @ candidates come from the repository tree, threads, deliverables and
// memory; on send each reference is resolved into the delivered text (file content from the
// contents API) while the stored message keeps the words and the chips; unknown or foreign
// references are refused. Text files attached to a message are put into the delivered text,
// PDFs travel as attachments.
func TestMessageReferences(t *testing.T) {
	gh, ghSrv := newFakeGitHub(t)
	gh.setFile("o/r", "src/app.go", "package main\n// 暗号 42\n")
	gh.setFile("o/r", "README.md", "hello")
	w := newWorld(t, ghSrv)
	b := w.signUp("refs@example.com")
	var node nodeCreated
	b.must(201, "POST", "/api/nodes", map[string]string{"name": "fake"}, &node)
	b.must(200, "GET", "/api/roles", nil, nil)
	var conn struct{ ID string }
	b.must(201, "POST", "/api/git/connections", map[string]string{"name": "gh", "token": "ghp_good"}, &conn)
	var project struct{ ID string }
	b.must(201, "POST", "/api/projects", map[string]any{"name": "web", "repos": []map[string]any{{"repo": "o/r", "git_connection_id": conn.ID}}}, &project)
	pid := project.ID
	n := w.connect(node.Token, nodetest.Options{})
	var thread core.ThreadView
	b.must(201, "POST", "/api/projects/"+pid+"/threads", map[string]any{"title": "Login", "brief": "登录页"}, &thread)
	lead, _ := expectOpen(t, w, n, "lead-1")
	answerPrepares(t, w, n)
	b.must(200, "PUT", "/api/projects/"+pid+"/memory/deploy", map[string]string{"description": "部署", "content": "部署用 make ship"}, nil)
	var d core.DeliverableView
	b.must(201, "POST", "/api/projects/"+pid+"/deliverables", map[string]any{"kind": "doc", "title": "设计稿", "url": "https://docs/x"}, &d)

	var list orchapi.RefList
	b.must(200, "GET", "/api/projects/"+pid+"/references?q=app", nil, &list)
	if len(list.Items) != 1 || list.Items[0].Kind != "file" || list.Items[0].Path != "src/app.go" || list.Items[0].Repo != "o/r" || list.FilesNote != "" {
		t.Fatalf("file candidates: %+v", list)
	}
	b.must(200, "GET", "/api/projects/"+pid+"/references?q=", nil, &list)
	kinds := map[string]int{}
	for _, it := range list.Items {
		kinds[it.Kind]++
	}
	if kinds["file"] != 2 || kinds["thread"] != 1 || kinds["deliverable"] != 1 || kinds["memory"] != 2 {
		t.Fatalf("all candidates: %+v", list.Items)
	}

	refs := []map[string]any{{"kind": "file", "repo": "o/r", "path": "src/app.go"}, {"kind": "thread", "no": thread.No},
		{"kind": "deliverable", "id": d.ID}, {"kind": "memory", "name": "deploy.md"}}
	var msg core.MessageView
	b.must(202, "POST", "/api/conversations/"+thread.ID+"/messages", map[string]any{"content": "看看这些", "refs": refs}, &msg)
	send := lead.expectSend("暗号 42")
	for _, part := range []string{"看看这些\n\n---\n[用户用 @ 引用的上下文]", "### 文件 src/app.go（仓库 o/r，分支 main）", "### 讨论串 #1 Login", "任务说明：登录页",
		"### 交付物 设计稿", "https://docs/x", "### 记忆文件 deploy.md", "部署用 make ship"} {
		if !strings.Contains(send.Text, part) {
			t.Fatalf("delivered text lacks %q:\n%s", part, send.Text)
		}
	}
	chips, _ := msg.Meta["refs"].([]any)
	if msg.Content != "看看这些" || len(chips) != 4 || !strings.Contains(string(mustJSON(chips[0])), `"label":"src/app.go"`) {
		t.Fatalf("stored message: %+v", msg)
	}

	other := w.signUp("other-refs@example.com")
	var theirs struct{ ID string }
	other.must(201, "POST", "/api/projects", map[string]any{"name": "x"}, &theirs)
	var foreign core.DeliverableView
	other.must(201, "POST", "/api/projects/"+theirs.ID+"/deliverables", map[string]any{"kind": "doc", "title": "别人的", "url": "https://x/y"}, &foreign)
	for _, bad := range []map[string]any{{"kind": "file", "repo": "x/y", "path": "a.go"}, {"kind": "thread", "no": 99},
		{"kind": "deliverable", "id": foreign.ID}, {"kind": "memory", "name": "nope.md"}, {"kind": "file", "repo": "o/r", "path": "../etc/passwd"}} {
		b.must(422, "POST", "/api/conversations/"+thread.ID+"/messages", map[string]any{"content": "x", "refs": []map[string]any{bad}}, nil)
	}
	if code, _ := other.do("GET", "/api/projects/"+pid+"/references", nil); code != 404 {
		t.Fatalf("another user's references = %d", code)
	}

	var text, pdf struct {
		ID          string `json:"id"`
		ContentType string `json:"content_type"`
	}
	b.must(201, "POST", "/api/projects/"+pid+"/attachments", map[string]string{"name": "notes.md", "data": base64.StdEncoding.EncodeToString([]byte("# 笔记\n口令是 蓝鲸"))}, &text)
	b.must(201, "POST", "/api/projects/"+pid+"/attachments", map[string]string{"name": "spec.pdf", "data": base64.StdEncoding.EncodeToString([]byte("%PDF-1.4\n%%EOF"))}, &pdf)
	if text.ContentType != "text/plain" || pdf.ContentType != "application/pdf" {
		t.Fatalf("types: %+v %+v", text, pdf)
	}
	b.must(422, "POST", "/api/projects/"+pid+"/attachments", map[string]string{"name": "tool.exe", "data": base64.StdEncoding.EncodeToString([]byte("MZ\x00\x01"))}, nil)
	b.must(202, "POST", "/api/conversations/"+thread.ID+"/messages", map[string]any{"content": "/design 照附件做", "attachments": []string{text.ID, pdf.ID}}, nil)
	send = lead.expectSend("口令是 蓝鲸")
	if !strings.HasPrefix(send.Text, "/design 照附件做\n\n[附件：notes.md]\n```\n# 笔记") || len(send.Images) != 1 || send.Images[0].ID != pdf.ID ||
		send.Images[0].MediaType != "application/pdf" {
		t.Fatalf("attachments delivered: %q %+v", send.Text, send.Images)
	}
}
