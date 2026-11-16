package engine_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw/nodetest"
	"github.com/touwaeriol/agent-teams/server/internal/orchestrator"
	"github.com/touwaeriol/agent-teams/server/internal/store"
	"github.com/touwaeriol/agent-teams/server/internal/timers"
)

// setState reports a session state from the node and waits until the console recorded it.
func (a *fakeAgent) setState(t *testing.T, state string) {
	t.Helper()
	gen := protocol.OpenGen(a.gen)
	a.send(protocol.TypeSessionState, protocol.SessionState{SessionID: protocol.SessionID(a.session), OpenGen: &gen,
		State: protocol.SessionStateState(state)})
	eventually(t, a.session+" "+state, func() bool {
		s, err := core.SessionByID(a.w.ctx, a.w.e.Core.Pool, a.session)
		return err == nil && s.State == state
	})
}

func sessionOf(t *testing.T, w *world, id string) core.AgentSession {
	t.Helper()
	s, err := core.SessionByID(w.ctx, w.e.Core.Pool, id)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// statusOf is the employee's recorded state and substate.
func statusOf(w *world, employeeID string) (string, string) {
	var row struct{ State, Substate string }
	_ = w.e.Core.Pool.Gorm(w.ctx).Table("at_employee_status").Select("state, substate").Where("employee_id = ?", employeeID).Take(&row).Error
	return row.State, row.Substate
}

// expectNone fails when a frame of typ matching pred arrives within d.
func expectNone(t *testing.T, n *nodetest.Node, typ string, d time.Duration, pred func(nodetest.Frame) bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	if f, err := n.Wait(ctx, typ, pred); err == nil {
		t.Fatalf("unexpected %s: %s", typ, f.Body)
	}
}

func (tm *team) coordinatorConv(t *testing.T) string {
	t.Helper()
	var coord struct {
		ConversationID string `json:"conversation_id"`
	}
	tm.b.must(200, "GET", "/api/projects/"+tm.projectID+"/coordinator", nil, &coord)
	return coord.ConversationID
}

// reopened waits for the employee's next session.open and checks it resumes the old conversation.
func reopened(t *testing.T, tm *team, name, oldSession, needle string) *fakeAgent {
	t.Helper()
	a, open := expectOpen(t, tm.w, tm.n, name)
	if open.Resume == nil || open.Resume.SDKSessionID == nil || *open.Resume.SDKSessionID != "sdk-"+oldSession {
		t.Fatalf("%s reopened without resume: %+v", name, open.Resume)
	}
	if open.FirstMessage == nil || !strings.Contains(open.FirstMessage.Text, needle) {
		t.Fatalf("%s first message %+v, want %q", name, open.FirstMessage, needle)
	}
	a.send(protocol.TypeSessionDelivered, protocol.SessionDelivered{SessionID: open.SessionID, MsgID: open.FirstMessage.MsgID})
	return a
}

// TestWakeOnMessage: every way a message reaches an idle employee wakes its parked session
// (session.send, which the daemon resumes) or reopens its closed one with resume, and the
// employee shows as busy (thinking · waking) until its turn starts.
func TestWakeOnMessage(t *testing.T) {
	tm := startTeam(t)
	conv := tm.coordinatorConv(t)

	// user → coordinator: first message opens it, a parked one is woken, a closed one resumes.
	tm.b.must(202, "POST", "/api/conversations/"+conv+"/messages", map[string]string{"content": "你好"}, nil)
	coord, open := expectOpen(t, tm.w, tm.n, "coordinator")
	coord.send(protocol.TypeSessionDelivered, protocol.SessionDelivered{SessionID: open.SessionID, MsgID: open.FirstMessage.MsgID})
	coordID := sessionOf(t, tm.w, coord.session).EmployeeID
	coord.setState(t, "parked")
	tm.b.must(202, "POST", "/api/conversations/"+conv+"/messages", map[string]string{"content": "在吗"}, nil)
	eventually(t, "coordinator shows waking", func() bool {
		st, sub := statusOf(tm.w, coordID)
		return st == "thinking" && sub == "waking"
	})
	coord.expectSend("在吗")
	coord.event("turn_started", func(e *protocol.SessionEvent) {
		e.TriggerMsgIds, e.Tools = []protocol.MsgID{"m_x"}, []string{"mcp__platform__set_status"}
	})
	eventually(t, "coordinator thinking", func() bool {
		st, sub := statusOf(tm.w, coordID)
		return st == "thinking" && sub == ""
	})
	turnEnded(coord, 0, 1, 1)
	eventually(t, "coordinator idle", func() bool { st, _ := statusOf(tm.w, coordID); return st == "idle" })

	// scheduled task → parked coordinator.
	coord.setState(t, "parked")
	var s timers.Schedule
	tm.b.must(201, "POST", "/api/projects/"+tm.projectID+"/schedules", map[string]any{"name": "早报", "cron": "0 9 * * *", "message": "汇报"}, &s)
	tm.b.must(202, "POST", "/api/schedules/"+s.ID+"/run", nil, nil)
	coord.expectSend("【定时任务：早报】")

	// closed by the node (idle too long, daemon gone): the next message resumes the conversation.
	coord.setState(t, "closed")
	tm.b.must(202, "POST", "/api/conversations/"+conv+"/messages", map[string]string{"content": "还在吗"}, nil)
	reopened(t, tm, "coordinator", coord.session, "还在吗")

	// user → thread lead.
	tm.lead.setState(t, "parked")
	tm.b.must(202, "POST", "/api/conversations/"+tm.thread.ID+"/messages", map[string]string{"content": "进度如何"}, nil)
	tm.lead.expectSend("进度如何")

	// answer to the lead's question.
	res := tm.lead.mustCall("ask_user", map[string]any{"question": "要不要暗色模式？"})
	var asked protocol.AskUserResult
	_ = json.Unmarshal(*res.Structured, &asked)
	tm.lead.setState(t, "parked")
	tm.b.must(200, "POST", "/api/conversations/"+tm.thread.ID+"/questions/"+string(asked.QuestionID)+"/answer", map[string]string{"answer": "要"}, nil)
	tm.lead.expectSend("[Answer to " + string(asked.QuestionID) + "] 要")

	// user → member directly.
	tm.dev.setState(t, "parked")
	tm.b.must(202, "POST", "/api/threads/"+tm.thread.ID+"/members/dev-1/message", map[string]string{"content": "先做校验"}, nil)
	tm.dev.expectSend("[用户直接对你说] 先做校验")

	// lead → member mail after the member's session closed: resumed with the mail.
	tm.dev.setState(t, "closed")
	tm.lead.mustCall("send_message", map[string]string{"to": "dev-1", "text": "顺便看一下日志"})
	reopened(t, tm, "dev-1", tm.dev.session, "顺便看一下日志")
}

// TestArchiveStopsSessions: archiving closes every session of the thread (parked ones too),
// cancels the tool calls and cards they waited on, frees the node, refuses new messages with
// a hint and does not start anything on unarchive. Ended threads' employees are not woken.
func TestArchiveStopsSessions(t *testing.T) {
	tm := startTeam(t)
	tm.lead.mustCall("ask_user", map[string]any{"question": "要不要暗色模式？"})
	ask := tm.dev.startCall("ask_user", `{"question":"用哪个 UI 库？","options":["A","B"]}`)
	tm.dev.noResult(ask)
	tm.lead.setState(t, "parked")

	tm.b.must(200, "POST", "/api/threads/"+tm.thread.ID+"/archive", nil, nil)
	ctx, cancel := tm.w.waitCtx()
	defer cancel()
	for _, a := range []*fakeAgent{tm.lead, tm.dev} {
		if _, err := tm.n.Wait(ctx, protocol.TypeSessionClose, a.mine); err != nil {
			t.Fatal(err)
		}
		if s := sessionOf(t, tm.w, a.session); s.State != "closed" || s.CloseReason != "thread_done" {
			t.Fatalf("session %s: %s %s", a.session, s.State, s.CloseReason)
		}
	}
	var open, live int64
	tm.w.e.Core.Pool.Gorm(tm.w.ctx).Table("at_questions").Where("thread_id = ? AND status = 'open'", tm.thread.ID).Count(&open)
	tm.w.e.Core.Pool.Gorm(tm.w.ctx).Table("at_tool_calls").Where("session_id = ? AND status IN ('running','waiting')", tm.dev.session).Count(&live)
	if open != 0 || live != 0 {
		t.Fatalf("archive left %d open cards and %d live tool calls", open, live)
	}
	if live, _ := core.ThreadLiveSessions(tm.w.ctx, tm.w.e.Core.Pool, tm.thread.ID); len(live) != 0 {
		t.Fatalf("thread still has live sessions: %+v", live)
	}

	code, raw := tm.b.do("POST", "/api/conversations/"+tm.thread.ID+"/messages", map[string]string{"content": "继续"})
	if code != 409 || !strings.Contains(string(raw), "取消归档后再继续") || !strings.Contains(string(raw), "thread_archived") {
		t.Fatalf("message to an archived thread = %d %s", code, raw)
	}
	if code, _ := tm.b.do("POST", "/api/threads/"+tm.thread.ID+"/members/dev-1/message", map[string]string{"content": "x"}); code != 409 {
		t.Fatalf("member message to an archived thread = %d", code)
	}
	// Mail inside an archived thread is kept but wakes nobody.
	if err := tm.w.e.Core.InTx(tm.w.ctx, func(tx *core.Tx) error {
		dev, err := core.EmployeeByName(tm.w.ctx, tx, tm.projectID, &tm.thread.ID, "dev-1")
		if err != nil {
			return err
		}
		_, err = tm.w.e.Orch.Deliver(tm.w.ctx, tx, dev, orchestrator.Delivery{Text: "late mail", Kind: "mail", Priority: "later"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var th core.ThreadView
	tm.b.must(200, "POST", "/api/threads/"+tm.thread.ID+"/unarchive", nil, &th)
	if th.Status != "paused" {
		t.Fatalf("unarchive: %s", th.Status)
	}
	threadOnly := func(f nodetest.Frame) bool {
		return tm.lead.mine(f) || tm.dev.mine(f) || strings.Contains(string(f.Body), `"tier":"member"`) || strings.Contains(string(f.Body), `"tier":"lead"`)
	}
	expectNone(t, tm.n, protocol.TypeSessionOpen, 600*time.Millisecond, threadOnly)
	expectNone(t, tm.n, protocol.TypeSessionSend, 100*time.Millisecond, threadOnly)
}

// TestProjectArchiveStopsSessions: archiving a project pauses its threads, closes every
// session (coordinator included) and its schedules no longer wake the coordinator.
func TestProjectArchiveStopsSessions(t *testing.T) {
	tm := startTeam(t)
	conv := tm.coordinatorConv(t)
	tm.b.must(202, "POST", "/api/conversations/"+conv+"/messages", map[string]string{"content": "你好"}, nil)
	coord, _ := expectOpen(t, tm.w, tm.n, "coordinator")
	coord.setState(t, "parked")
	var s timers.Schedule
	tm.b.must(201, "POST", "/api/projects/"+tm.projectID+"/schedules", map[string]any{"name": "早报", "cron": "0 9 * * *", "message": "汇报"}, &s)

	tm.b.must(200, "PATCH", "/api/projects/"+tm.projectID, map[string]any{"archived": true}, nil)
	ctx, cancel := tm.w.waitCtx()
	defer cancel()
	for _, a := range []*fakeAgent{coord, tm.lead, tm.dev} {
		if _, err := tm.n.Wait(ctx, protocol.TypeSessionClose, a.mine); err != nil {
			t.Fatal(err)
		}
	}
	if st, _ := threadStatus(tm); st != "paused" {
		t.Fatalf("thread %s, want paused", st)
	}
	if _, err := store.Exec(tm.w.ctx, tm.w.e.Core.Pool, `UPDATE at_schedules SET next_run_at = now() - interval '1 minute' WHERE id = $1`, s.ID); err != nil {
		t.Fatal(err)
	}
	tm.w.e.Timers.Tick(tm.w.ctx)
	expectNone(t, tm.n, protocol.TypeSessionOpen, 500*time.Millisecond, nil)
}

// TestReconcileClosesEndedThreadSessions: sessions of an archived thread that were left parked
// (the daemon restarted before archive closed them) are closed on the next report, not resumed.
func TestReconcileClosesEndedThreadSessions(t *testing.T) {
	tm := startTeam(t)
	if _, err := store.Exec(tm.w.ctx, tm.w.e.Core.Pool, `UPDATE at_threads SET status = 'archived' WHERE id = $1`, tm.thread.ID); err != nil {
		t.Fatal(err)
	}
	tm.restart(t)
	tm.report(t)
	ctx, cancel := tm.w.waitCtx()
	defer cancel()
	for _, a := range []*fakeAgent{tm.lead, tm.dev} {
		if _, err := tm.n.Wait(ctx, protocol.TypeSessionClose, a.mine); err != nil {
			t.Fatal(err)
		}
		if s := sessionOf(t, tm.w, a.session); s.State != "closed" {
			t.Fatalf("session %s still %s", a.session, s.State)
		}
	}
	expectNone(t, tm.n, protocol.TypeSessionOpen, 500*time.Millisecond, nil)
	expectNone(t, tm.n, protocol.TypeSessionSend, 100*time.Millisecond, nil)
}

// TestReadOnlyFinish: a thread that changed no repository ends with finish alone: done, summary
// in the thread and project memory, coordinator told, sessions closed. Open work is refused.
func TestReadOnlyFinish(t *testing.T) {
	tm := startTeam(t)
	res := tm.lead.call("finish", map[string]string{"summary": "调研结论"})
	if res.Ok || !strings.Contains(text(res), "还有没完成的工作") {
		t.Fatalf("finish with open work: %+v", res)
	}
	tm.dev.mustCall("complete_task", map[string]any{"task_id": "t1", "summary": "查完了"})
	tm.lead.expectSend("[Task t1 · dev-1 · done]")
	res = tm.lead.mustCall("finish", map[string]string{"summary": "调研结论：方案 A 更好"})
	if !strings.Contains(text(res), "没有代码改动") || strings.Contains(text(res), "open_pr(") {
		t.Fatalf("finish result %q", text(res))
	}
	eventually(t, "thread done", func() bool { st, _ := threadStatus(tm); return st == "done" })
	ctx, cancel := tm.w.waitCtx()
	defer cancel()
	for _, a := range []*fakeAgent{tm.lead, tm.dev} {
		if _, err := tm.n.Wait(ctx, protocol.TypeSessionClose, a.mine); err != nil {
			t.Fatal(err)
		}
	}
	var th core.ThreadView
	tm.b.must(200, "GET", "/api/threads/"+tm.thread.ID, nil, &th)
	if th.Summary != "调研结论：方案 A 更好" {
		t.Fatalf("summary %v", th.Summary)
	}
	var mem int64
	tm.w.e.Core.Pool.Gorm(tm.w.ctx).Table("at_project_memory").Where("project_id = ? AND text LIKE ?", tm.projectID, "%方案 A 更好%").Count(&mem)
	if mem != 1 {
		t.Fatalf("memory notes %d", mem)
	}
	_, open := expectOpen(t, tm.w, tm.n, "coordinator")
	if open.FirstMessage == nil || !strings.Contains(open.FirstMessage.Text, "[Thread #1 · Work · done]") {
		t.Fatalf("coordinator told %+v", open.FirstMessage)
	}
}
