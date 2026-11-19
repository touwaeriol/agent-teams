package engine_test

import (
	"strings"
	"testing"

	"github.com/touwaeriol/agent-teams/server/internal/core"
)

// TestCoordinatorThreadStatusPhase: thread_status keeps the raw status and adds the state line the
// page shows (core.PhaseOf): a running thread whose task waits on an idle member reads 等成员处理任务.
func TestCoordinatorThreadStatusPhase(t *testing.T) {
	tm := startTeam(t)
	_, coord := projectChat(t, tm, tm.coordinatorConv(t), map[string]any{"content": "#1 怎么样了"})
	for _, a := range []*fakeAgent{tm.lead, tm.dev} { // both idle: the task waits on the member
		turnStarted(a)
		turnEnded(a, 0, 1, 1)
	}
	var got string
	eventually(t, "phase line", func() bool {
		got = text(coord.mustCall("thread_status", map[string]any{"no": 1}))
		return strings.Contains(got, "状态说明（和页面一致）：进行中：等成员处理任务（1 个未完成）")
	})
	if !strings.Contains(got, "#1 Work [running]") {
		t.Fatalf("raw status lost: %q", got)
	}
}

// TestCoordinatorReopensDoneThread: message_thread to a done thread reopens it (as a user's
// message on the page does), the lead gets the message and the chat says why; an archived thread
// is refused.
func TestCoordinatorReopensDoneThread(t *testing.T) {
	tm := startTeam(t)
	_, coord := projectChat(t, tm, tm.coordinatorConv(t), map[string]any{"content": "#1 再补一个"})
	tm.b.must(200, "POST", "/api/threads/"+tm.thread.ID+"/complete", map[string]any{}, nil)
	if st, _ := threadStatus(tm); st != "done" {
		t.Fatalf("status %s", st)
	}
	res := coord.mustCall("message_thread", map[string]any{"no": 1, "text": "用户要再补一个导出按钮"})
	if !strings.Contains(text(res), "已重新打开") || !strings.Contains(text(res), "已转给 #1 的组长") {
		t.Fatalf("message_thread result %q", text(res))
	}
	if st, _ := threadStatus(tm); st != "running" && st != "queued" {
		t.Fatalf("after message_thread: %s", st)
	}
	_, open := expectOpen(t, tm.w, tm.n, "lead-1")
	if open.FirstMessage == nil || !strings.Contains(open.FirstMessage.Text, "用户要再补一个导出按钮") {
		t.Fatalf("lead got %+v", open.FirstMessage)
	}
	var note bool
	for _, m := range threadMessages(t, tm, tm.thread.ID) {
		note = note || strings.Contains(m.Content, "统筹发来新消息，讨论串重新打开。")
	}
	if !note {
		t.Fatal("no reopen note in the thread chat")
	}

	tm.b.must(200, "POST", "/api/threads/"+tm.thread.ID+"/archive", nil, nil)
	res = coord.call("message_thread", map[string]any{"no": 1, "text": "还在吗"})
	if res.Ok || !strings.Contains(text(res), "已归档") {
		t.Fatalf("message to an archived thread: %+v", res)
	}
	if v, _ := core.ThreadByID(tm.w.ctx, tm.w.e.Core.Pool, tm.thread.ID); v.Status != "archived" {
		t.Fatalf("status %s", v.Status)
	}
}
