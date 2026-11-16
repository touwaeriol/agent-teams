package engine_test

import (
	"encoding/json"
	"strings"
	"testing"

	protocol "github.com/touwaeriol/agent-teams/protocol"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw/nodetest"
)

// threadMessages lists the thread conversation's messages.
func (tm *team) threadMessages(t *testing.T) []core.MessageView {
	t.Helper()
	var page struct {
		Items []core.MessageView `json:"items"`
	}
	tm.b.must(200, "GET", "/api/conversations/"+tm.thread.ID+"/messages", nil, &page)
	return page.Items
}

func ofKind(msgs []core.MessageView, kind string) []core.MessageView {
	var out []core.MessageView
	for _, m := range msgs {
		if m.Kind == kind {
			out = append(out, m)
		}
	}
	return out
}

// TestStructuredAskUser: a member's structured ask_user becomes one card with detail.questions;
// a bad answer is refused (422), a good one closes the card with detail.answers, and the
// blocking call returns the text and the per-question choices. A lead's question routed from
// Claude's AskUserQuestion (via) blocks too, and a plain text answer counts as 其他.
func TestStructuredAskUser(t *testing.T) {
	tm := startTeam(t)
	args := `{"questions":[{"question":"会话也直接切成 Valkey 令牌吗？","header":"会话","options":[` +
		`{"label":"也直接切","description":"去掉 JWT 和 refresh","recommended":true},{"label":"先保留","description":"只改新登录"}]},` +
		`{"question":"要跑哪些检查？","header":"检查","multi_select":true,"options":[{"label":"单测"},{"label":"E2E"},{"label":"Lint"}]}]}`
	ask := tm.dev.startCall("ask_user", args)
	tm.dev.noResult(ask)
	var cards []core.QuestionView
	tm.b.must(200, "GET", "/api/conversations/"+tm.thread.ID+"/questions?status=open", nil, &cards)
	if len(cards) != 1 || !cards[0].Blocking || !strings.HasPrefix(cards[0].Text, "会话也直接切成 Valkey 令牌吗？\n") {
		t.Fatalf("card %+v", cards)
	}
	qs, _ := cards[0].Detail["questions"].([]any)
	first, _ := qs[0].(map[string]any)
	opts, _ := first["options"].([]any)
	if len(qs) != 2 || first["header"] != "会话" || opts[0].(map[string]any)["recommended"] != true || qs[1].(map[string]any)["multi_select"] != true {
		t.Fatalf("detail.questions %+v", cards[0].Detail)
	}
	path := "/api/conversations/" + tm.thread.ID + "/questions/" + cards[0].ID + "/answer"
	for _, bad := range []map[string]any{
		{"answers": []map[string]any{{"selected": []string{"也直接切", "先保留"}}, {"selected": []string{"单测"}}}}, // two on single select
		{"answers": []map[string]any{{"selected": []string{"别的"}}, {"selected": []string{"单测"}}}},          // unknown label
		{"answers": []map[string]any{{"selected": []string{"也直接切"}}}},                                      // one answer for two questions
		{"answers": []map[string]any{{"selected": []string{"也直接切"}}, {}}},                                  // nothing chosen
	} {
		if code, body := tm.b.do("POST", path, bad); code != 422 {
			t.Fatalf("bad answer %v = %d %s", bad, code, body)
		}
	}
	var answered core.QuestionView
	tm.b.must(200, "POST", path, map[string]any{"answers": []map[string]any{{"selected": []string{"也直接切"}},
		{"selected": []string{"单测", "E2E"}, "other": "再加冒烟"}}}, &answered)
	if answered.Status != "answered" || *answered.Answer != "会话：也直接切\n检查：单测、E2E、再加冒烟" || answered.Detail["answers"] == nil {
		t.Fatalf("answered %+v", answered)
	}
	res := tm.dev.result(ask)
	var out protocol.AskUserResult
	_ = json.Unmarshal(*res.Structured, &out)
	if !res.Ok || len(out.Answers) != 2 || out.Answers[0].Selected[0] != "也直接切" || *out.Answers[1].Other != "再加冒烟" ||
		strings.Join(out.Answers[1].Selected, ",") != "单测,E2E" {
		t.Fatalf("result %+v %+v", res, out)
	}

	if res := tm.lead.call("ask_user", map[string]any{}); res.Ok {
		t.Fatal("ask_user without question must fail")
	}
	via := tm.lead.startCall("ask_user", `{"via":"AskUserQuestion","questions":[{"question":"现在发布吗？","header":"发布","options":[{"label":"发布"},{"label":"等等"}]}]}`)
	tm.lead.noResult(via)
	tm.b.must(200, "GET", "/api/conversations/"+tm.thread.ID+"/questions?status=open", nil, &cards)
	if len(cards) != 1 || !cards[0].Blocking || cards[0].Detail["via"] != "AskUserQuestion" || strings.Join(cards[0].Options, ",") != "发布,等等" {
		t.Fatalf("via card %+v", cards)
	}
	tm.b.must(200, "POST", "/api/conversations/"+tm.thread.ID+"/questions/"+cards[0].ID+"/answer", map[string]any{"answer": "明天再说"}, nil)
	res = tm.lead.result(via)
	_ = json.Unmarshal(*res.Structured, &out)
	if text(res) != "明天再说" || len(out.Answers) != 1 || len(out.Answers[0].Selected) != 0 || *out.Answers[0].Other != "明天再说" {
		t.Fatalf("via result %+v %+v", res, out)
	}
}

// TestChatCards: the lead's TodoWrite list is one card updated in place until it is all done,
// then a new list starts a new card; members' lists stay out of the chat; ExitPlanMode plans
// and published deliverables become cards too.
func TestChatCards(t *testing.T) {
	tm := startTeam(t)
	todos := func(a *fakeAgent, states ...string) {
		a.event("todos", func(ev *protocol.SessionEvent) {
			for i, st := range states {
				ev.Todos = append(ev.Todos, protocol.TodoItem{Content: "步骤" + string(rune('A'+i)), Status: protocol.TodoItemStatus(st)})
			}
		})
	}
	todos(tm.lead, "in_progress", "pending")
	eventually(t, "todo card", func() bool { return len(ofKind(tm.threadMessages(t), "todos")) == 1 })
	first := ofKind(tm.threadMessages(t), "todos")[0]
	todos(tm.lead, "completed", "in_progress")
	eventually(t, "updated in place", func() bool {
		cards := ofKind(tm.threadMessages(t), "todos")
		return len(cards) == 1 && cards[0].ID == first.ID && cards[0].Content == "[x] 步骤A\n[~] 步骤B"
	})
	todos(tm.lead, "completed", "completed")
	todos(tm.lead, "pending")
	eventually(t, "new list after a finished one", func() bool { return len(ofKind(tm.threadMessages(t), "todos")) == 2 })
	todos(tm.dev, "in_progress")
	tm.dev.event("plan", func(ev *protocol.SessionEvent) { p := "# 计划\n\n1. 先写测试"; ev.Plan = &p })
	eventually(t, "plan card", func() bool { return len(ofKind(tm.threadMessages(t), "plan")) == 1 })
	if cards := ofKind(tm.threadMessages(t), "todos"); len(cards) != 2 {
		t.Fatalf("member todos must stay out of the chat: %+v", cards)
	}
	plan := ofKind(tm.threadMessages(t), "plan")[0]
	if plan.Author != "dev-1" || plan.Role != "system" || !strings.Contains(plan.Content, "先写测试") {
		t.Fatalf("plan %+v", plan)
	}
	tm.lead.mustCall("publish_deliverable", map[string]string{"kind": "doc", "title": "设计说明", "url": "https://example.com/doc"})
	eventually(t, "artifact card", func() bool { return len(ofKind(tm.threadMessages(t), "artifact")) == 1 })
	art := ofKind(tm.threadMessages(t), "artifact")[0]
	d, _ := art.Meta["deliverable"].(map[string]any)
	if d["title"] != "设计说明" || d["url"] != "https://example.com/doc" || !strings.HasPrefix(d["id"].(string), "dl_") {
		t.Fatalf("artifact %+v", art)
	}
}

// TestResumeFailedHandsOff: the lead's parked session cannot be resumed (transcript gone):
// the daemon answers session.opened{resume_failed}; the console closes it and opens a fresh
// session whose first message is the recap (task state, memory, recent talk) followed by the
// message that was on its way, and the chat gets a "会话已恢复" note.
func TestResumeFailedHandsOff(t *testing.T) {
	tm := startTeam(t)
	tm.b.must(202, "POST", "/api/conversations/"+tm.thread.ID+"/messages", map[string]any{"content": "暗号是 heron-42，记住它"}, nil)
	tm.lead.expectSend("heron-42")
	tm.lead.event("text", func(ev *protocol.SessionEvent) { s, f := "记住了：heron-42", true; ev.Text, ev.Final = &s, &f })
	tm.b.must(202, "POST", "/api/conversations/"+tm.thread.ID+"/messages", map[string]any{"content": "暗号是什么？"}, nil)
	ctx, cancel := tm.w.waitCtx()
	defer cancel()
	if _, err := tm.n.Wait(ctx, protocol.TypeSessionSend, func(f nodetest.Frame) bool {
		return tm.lead.mine(f) && strings.Contains(string(f.Body), "暗号是什么")
	}); err != nil {
		t.Fatal(err)
	}
	failed := true
	reason := "transcript not found"
	tm.lead.send(protocol.TypeSessionOpened, protocol.SessionOpened{SessionID: protocol.SessionID(tm.lead.session), OpenGen: protocol.OpenGen(tm.lead.gen),
		ResumeFailed: &failed, Error: &reason})
	old := tm.lead.session
	lead2, open := expectOpen(t, tm.w, tm.n, "lead-1")
	if lead2.session == old || open.Resume != nil || open.FirstMessage == nil {
		t.Fatalf("fresh session %+v", open)
	}
	first := open.FirstMessage.Text
	for _, want := range []string{"[会话恢复]", "主任务", "do t1", "用户：暗号是 heron-42", "你：记住了：heron-42", "---\n暗号是什么？"} {
		if !strings.Contains(first, want) {
			t.Fatalf("recap misses %q:\n%s", want, first)
		}
	}
	if strings.Count(first, "暗号是什么") != 1 {
		t.Fatalf("pending message must appear once:\n%s", first)
	}
	s, err := core.SessionByID(tm.w.ctx, tm.w.e.Core.Pool, old)
	if err != nil || s.State != "closed" {
		t.Fatalf("old session %+v %v", s, err)
	}
	eventually(t, "resumed note", func() bool {
		notes := ofKind(tm.threadMessages(t), "resumed")
		return len(notes) == 1 && notes[0].Content == "会话已恢复" && notes[0].Author == "lead-1"
	})
}

// TestCoordinatorRecapAndResume: the coordinator's recap quotes its own conversation (the
// coordinator has no thread), and an employee whose session closed with its thread resumes
// the old conversation by SDK session id on the next message.
func TestCoordinatorRecapAndResume(t *testing.T) {
	tm := startTeam(t)
	var coord struct {
		ConversationID string `json:"conversation_id"`
	}
	tm.b.must(200, "GET", "/api/projects/"+tm.projectID+"/coordinator", nil, &coord)
	tm.b.must(202, "POST", "/api/conversations/"+coord.ConversationID+"/messages", map[string]any{"content": "代号 kestrel-7"}, nil)
	c, open := expectOpen(t, tm.w, tm.n, "coordinator")
	c.send(protocol.TypeSessionDelivered, protocol.SessionDelivered{SessionID: protocol.SessionID(c.session), MsgID: open.FirstMessage.MsgID})
	c.event("text", func(ev *protocol.SessionEvent) { s, f := "好的，记下 kestrel-7", true; ev.Text, ev.Final = &s, &f })
	if _, err := tm.w.e.Core.Pool.Exec(tm.w.ctx, `INSERT INTO at_project_memory (id, project_id, text, source) VALUES ('mem_t', $1, '部署走滚动发布', 'test')`, tm.projectID); err != nil {
		t.Fatal(err)
	}
	failed := true
	reason := "transcript not found"
	c.send(protocol.TypeSessionOpened, protocol.SessionOpened{SessionID: protocol.SessionID(c.session), OpenGen: protocol.OpenGen(c.gen), ResumeFailed: &failed, Error: &reason})
	_, open = expectOpen(t, tm.w, tm.n, "coordinator")
	for _, want := range []string{"用户：代号 kestrel-7", "你：好的，记下 kestrel-7", "项目记忆", "部署走滚动发布"} {
		if !strings.Contains(open.FirstMessage.Text, want) {
			t.Fatalf("coordinator recap misses %q:\n%s", want, open.FirstMessage.Text)
		}
	}

	if _, err := tm.w.e.Core.Pool.Exec(tm.w.ctx, `UPDATE at_agent_sessions SET state = 'closed', close_reason = 'thread_done' WHERE id = $1`, tm.lead.session); err != nil {
		t.Fatal(err)
	}
	tm.b.must(202, "POST", "/api/conversations/"+tm.thread.ID+"/messages", map[string]any{"content": "还在吗？"}, nil)
	_, leadOpen := expectOpen(t, tm.w, tm.n, "lead-1")
	if leadOpen.Resume == nil || leadOpen.Resume.SDKSessionID == nil || *leadOpen.Resume.SDKSessionID != "sdk-"+tm.lead.session {
		t.Fatalf("lead must resume its old conversation: %+v", leadOpen.Resume)
	}
}
