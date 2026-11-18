package engine_test

import (
	"encoding/json"
	"strings"
	"testing"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// count runs a COUNT query against the test database.
func (tm *team) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := tm.w.e.Core.Pool.Gorm(tm.w.ctx).Raw(sql, args...).Scan(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

// TestMemberMessageAcrossThreads: the user's message to dev-1 in #2 while dev-1 works on a task
// of #1 reaches its #1 session headed with #2, is recorded (and emitted) in #2, and dev-1 does
// not switch; dev-2, idle and in no thread, is brought into #2 by such a message.
func TestMemberMessageAcrossThreads(t *testing.T) {
	tm := sharedTeam(t)
	_, lead1 := tm.openThread(t, "One", nil)
	lead1.mustCall("assign_task", map[string]any{"assignee": "dev", "instruction": "task of one", "main": "做"})
	dev1, _ := startTask(t, tm, "dev-1", "task of one")
	two, _ := tm.openThread(t, "Two", nil)

	tm.b.must(202, "POST", "/api/threads/"+two.ID+"/members/dev-1/message", map[string]string{"content": "hello from two"}, nil)
	if got := dev1.expectSend("hello from two"); got.Text != "[#2 · 来自 用户 的留言]\n[用户直接对你说] hello from two" {
		t.Fatalf("dev-1 got %q", got.Text)
	}
	if n := tm.count(t, `SELECT count(*) FROM at_messages WHERE conversation_id = ? AND content = 'hello from two'
		AND meta->>'thread_no' = '2'`, two.ID); n != 1 {
		t.Fatalf("message rows in #2: %d", n)
	}
	if _, ok := takeNow(tm.n, protocol.TypeSessionClose); ok {
		t.Fatal("dev-1 switched threads for a message")
	}

	tm.b.must(202, "POST", "/api/threads/"+two.ID+"/members/dev-2/message", map[string]string{"content": "join two"}, nil)
	_, open := expectOpen(t, tm.w, tm.n, "dev-2")
	if open.Cwd != protocol.RelPath("projects/"+tm.projectID+"/threads/2/dev-2") || open.FirstMessage == nil ||
		!strings.HasPrefix(open.FirstMessage.Text, "[用户直接对你说] join two") {
		t.Fatalf("dev-2 open: cwd %s first %+v", open.Cwd, open.FirstMessage)
	}
}

// TestMailToMemberWithoutThread: mail to a member whose thread ended (it works in none) is kept
// in the sender's thread only: no delivery row lands in the coordinator conversation.
func TestMailToMemberWithoutThread(t *testing.T) {
	tm := sharedTeam(t)
	one, lead1 := tm.openThread(t, "One", nil)
	lead1.mustCall("assign_task", map[string]any{"assignee": "dev-1", "instruction": "task of one", "main": "做"})
	dev1, _ := startTask(t, tm, "dev-1", "task of one")
	dev1.mustCall("complete_task", map[string]any{"task_id": "t1", "summary": "done"})
	lead1.expectSend("[Task t1 · dev-1 · done] done")
	tm.b.must(200, "POST", "/api/threads/"+one.ID+"/archive", nil, nil)
	eventually(t, "dev-1 leaves #1", func() bool {
		return tm.count(t, `SELECT count(*) FROM at_employees WHERE project_id = ? AND name = 'dev-1' AND current_thread_id IS NULL`,
			tm.projectID) == 1
	})
	two, lead2 := tm.openThread(t, "Two", nil)
	lead2.mustCall("send_message", map[string]any{"to": "dev-1", "text": "ping"})
	if n := tm.count(t, `SELECT count(*) FROM at_messages m JOIN at_conversations c ON c.id = m.conversation_id
		WHERE c.project_id = ? AND c.kind = 'coordinator' AND m.meta->>'to' = 'dev-1'`, tm.projectID); n != 0 {
		t.Fatalf("mail delivery rows in the coordinator conversation: %d", n)
	}
	if n := tm.count(t, `SELECT count(*) FROM at_mail WHERE conversation_id = ? AND recipient = 'dev-1' AND body = 'ping'`, two.ID); n != 1 {
		t.Fatalf("mail rows in #2: %d", n)
	}
}

// TestClosedSessionTail: dev-1 finished #1's t1 and took #2's t1, which closed its #1 session.
// A late complete_task(t1) of the closed session acts in #1 (where t1 is done) and leaves #2's
// t1 alone; its last turn's usage and reply are #1's.
func TestClosedSessionTail(t *testing.T) {
	tm := sharedTeam(t)
	one, lead1 := tm.openThread(t, "One", nil)
	lead1.mustCall("assign_task", map[string]any{"assignee": "dev-1", "instruction": "task of one", "main": "做"})
	old, _ := startTask(t, tm, "dev-1", "task of one")
	old.mustCall("complete_task", map[string]any{"task_id": "t1", "summary": "one done"})
	lead1.expectSend("[Task t1 · dev-1 · done] one done")
	two, lead2 := tm.openThread(t, "Two", nil)
	lead2.mustCall("assign_task", map[string]any{"assignee": "dev-1", "instruction": "task of two", "main": "做"})
	ctx, cancel := tm.w.waitCtx()
	defer cancel()
	if _, err := tm.n.Wait(ctx, protocol.TypeSessionClose, old.mine); err != nil {
		t.Fatal(err)
	}
	startTask(t, tm, "dev-1", "task of two")

	// The late messages passed the gateway before the close committed (it drops them afterwards).
	route := func(typ string, body any) {
		t.Helper()
		if err := tm.w.e.Route(tm.w.ctx, store.Node{}, typ, body); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := json.Marshal(map[string]any{"task_id": "t1", "summary": "late"})
	gen := protocol.OpenGen(old.gen)
	route(protocol.TypeToolCall, &protocol.ToolCall{CallID: "tc_late", SessionID: protocol.SessionID(old.session), OpenGen: &gen,
		Tool: "complete_task", Args: raw})
	if n := tm.count(t, `SELECT count(*) FROM at_tool_calls WHERE call_id = 'tc_late' AND status = 'failed'`); n != 1 {
		t.Fatal("the late complete_task did not fail in #1")
	}
	if n := tm.count(t, `SELECT count(*) FROM at_tasks WHERE thread_id = ? AND id = 't1' AND status IN ('claimed','running')`, two.ID); n != 1 {
		t.Fatal("the closed #1 session completed #2's t1")
	}

	cost, final, reply := 0.5, true, "wrapping up one"
	route(protocol.TypeSessionEvent, &protocol.SessionEvent{SessionID: protocol.SessionID(old.session), EventNo: 1000,
		Kind: "turn_ended", CostUSD: &cost})
	route(protocol.TypeSessionEvent, &protocol.SessionEvent{SessionID: protocol.SessionID(old.session), EventNo: 1001,
		Kind: "text", Final: &final, Text: &reply})
	if n := tm.count(t, `SELECT count(*) FROM at_usage WHERE session_id = ? AND thread_id = ?`, old.session, one.ID); n != 1 {
		t.Fatal("the closed session's usage is not #1's")
	}
	if n := tm.count(t, `SELECT count(*) FROM at_events WHERE thread_id = ? AND type = 'activity' AND payload->>'text' = ?`,
		one.ID, reply); n != 1 {
		t.Fatal("the closed session's reply is not #1's activity")
	}
}

// TestMidTurnMemberWaitsToSwitch: dev-1 completes #1's t1 in the middle of a turn; #2's task
// named to it waits (claiming would close the session under the running turn) and is claimed
// as soon as the turn ends.
func TestMidTurnMemberWaitsToSwitch(t *testing.T) {
	tm := sharedTeam(t)
	_, lead1 := tm.openThread(t, "One", nil)
	lead1.mustCall("assign_task", map[string]any{"assignee": "dev-1", "instruction": "task of one", "main": "做"})
	dev1, _ := startTask(t, tm, "dev-1", "task of one")
	dev1.event("turn_started", func(ev *protocol.SessionEvent) {
		ev.TriggerMsgIds, ev.Tools = []protocol.MsgID{"m_brief"}, []string{"Read"}
	})
	dev1.mustCall("complete_task", map[string]any{"task_id": "t1", "summary": "one done"})
	lead1.expectSend("[Task t1 · dev-1 · done] one done")
	_, lead2 := tm.openThread(t, "Two", nil)
	lead2.mustCall("assign_task", map[string]any{"assignee": "dev-1", "instruction": "task of two", "main": "做"})
	if _, ok := takeNow(tm.n, protocol.TypeSessionClose); ok {
		t.Fatal("dev-1 was switched to #2 in the middle of its turn")
	}
	dev1.event("turn_ended", func(ev *protocol.SessionEvent) {
		sub, isErr, turns, ms, usage := "success", false, 1, 10, json.RawMessage(`{"input_tokens":1}`)
		ev.ResultSubtype, ev.IsError, ev.NumTurns, ev.DurationMS, ev.Usage = &sub, &isErr, &turns, &ms, &usage
	})
	ctx, cancel := tm.w.waitCtx()
	defer cancel()
	if _, err := tm.n.Wait(ctx, protocol.TypeSessionClose, dev1.mine); err != nil {
		t.Fatal(err)
	}
	startTask(t, tm, "dev-1", "task of two")
	if errs := tm.n.Errors(); len(errs) > 0 {
		t.Fatalf("protocol errors: %+v", errs)
	}
}
