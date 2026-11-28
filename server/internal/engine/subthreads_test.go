package engine_test

import (
	"strings"
	"testing"

	"github.com/touwaeriol/agent-teams/server/internal/core"
)

// relayOf is a relay card's meta.relay ("" fields when the message is not one).
func relayOf(m core.MessageView) map[string]any {
	if m.Kind != "relay" {
		return nil
	}
	r, _ := m.Meta["relay"].(map[string]any)
	return r
}

// findRelay reports whether conv holds a relay card with these dir / kind / peer number (0: coordinator).
func findRelay(t *testing.T, tm *team, conv, dir, kind string, no int) bool {
	t.Helper()
	for _, m := range threadMessages(t, tm, conv) {
		r := relayOf(m)
		if r == nil || r["dir"] != dir || r["kind"] != kind {
			continue
		}
		got, _ := r["no"].(float64)
		if int(got) == no {
			return true
		}
	}
	return false
}

// TestSubthreads: a lead opens a subthread (one level, its own lead, relay cards in the parent
// chat and the project chat), the two leads talk with message_thread, the subthread's lead talks
// to the coordinator, the parent cannot finish while the subthread is open, the subthread's end
// is reported to the parent's lead (not the coordinator), the parent reopens its finished
// subthread by messaging it, close_subthread ends it, and archiving the parent archives it.
func TestSubthreads(t *testing.T) {
	tm := startTeam(t)
	res := tm.lead.mustCall("open_subthread", map[string]any{"title": "调研缓存", "brief": "比较两种缓存方案，给出推荐"})
	if !strings.Contains(text(res), "opened subthread #2") {
		t.Fatalf("open_subthread %q", text(res))
	}
	sub, open := expectOpen(t, tm.w, tm.n, "lead-2")
	if open.FirstMessage == nil || !strings.Contains(open.FirstMessage.Text, "讨论串 #1「Work」的组长开的子讨论串") {
		t.Fatalf("subthread brief %+v", open.FirstMessage)
	}
	child, err := core.ThreadByNo(tm.w.ctx, tm.w.e.Core.Pool, tm.projectID, 2)
	if err != nil || child.ParentID == nil || *child.ParentID != tm.thread.ID || child.OpenedBy != "lead" {
		t.Fatalf("child %+v %v", child, err)
	}
	var view core.ThreadView
	tm.b.must(200, "GET", "/api/threads/"+child.ID, nil, &view)
	if view.ParentNo == nil || *view.ParentNo != 1 {
		t.Fatalf("parent_no %v", view.ParentNo)
	}
	if !findRelay(t, tm, tm.thread.ID, "out", "opened", 2) || !findRelay(t, tm, tm.coordinatorConv(t), "in", "opened", 1) {
		t.Fatal("no opened relay cards")
	}

	if r := sub.call("open_subthread", map[string]any{"title": "x", "brief": "y"}); r.Ok || !strings.Contains(text(r), "cannot open subthreads") {
		t.Fatalf("nested subthread: %+v", r)
	}
	if r := tm.lead.call("finish", map[string]any{"summary": "done"}); r.Ok || !strings.Contains(text(r), "#2 have not ended") {
		t.Fatalf("finish with an open subthread: %+v", r)
	}
	if got := text(tm.lead.mustCall("list_threads", map[string]any{})); !strings.Contains(got, "#2 调研缓存") || !strings.Contains(got, " (subthread of #1)") {
		t.Fatalf("list_threads %q", got)
	}

	sub.mustCall("message_thread", map[string]any{"no": 1, "text": "推荐用 Valkey"})
	tm.lead.expectSend("[Message from thread #2 · 调研缓存 · lead-2]")
	if !findRelay(t, tm, tm.thread.ID, "in", "message", 2) || !findRelay(t, tm, child.ID, "out", "message", 1) {
		t.Fatal("no message relay cards")
	}
	if r := sub.call("message_thread", map[string]any{"no": 2, "text": "自己"}); r.Ok {
		t.Fatal("message to its own thread accepted")
	}
	if r := sub.call("close_subthread", map[string]any{"no": 1}); r.Ok {
		t.Fatal("closed a thread that is not its subthread")
	}

	sub.mustCall("message_coordinator", map[string]any{"text": "#3 也会改缓存"})
	coord, copen := expectOpen(t, tm.w, tm.n, "coordinator")
	if copen.FirstMessage == nil || !strings.Contains(copen.FirstMessage.Text, "[Message from thread #2") {
		t.Fatalf("coordinator got %+v", copen.FirstMessage)
	}
	_ = coord
	if !findRelay(t, tm, tm.coordinatorConv(t), "in", "message", 2) || !findRelay(t, tm, child.ID, "out", "message", 0) {
		t.Fatal("no coordinator relay cards")
	}

	tm.b.must(200, "POST", "/api/threads/"+child.ID+"/complete", map[string]any{"summary": "结论：Valkey"}, nil)
	tm.lead.expectSend("[Subthread #2 · 调研缓存 · done]")
	if !findRelay(t, tm, tm.thread.ID, "in", "report", 2) {
		t.Fatal("no report relay card in the parent chat")
	}
	for _, m := range threadMessages(t, tm, tm.coordinatorConv(t)) {
		if strings.Contains(m.Content, "[Thread #2") {
			t.Fatalf("coordinator told about the subthread: %q", m.Content)
		}
	}

	res = tm.lead.mustCall("message_thread", map[string]any{"no": 2, "text": "再看一下内存占用"})
	if !strings.Contains(text(res), "is reopened") {
		t.Fatalf("reopen by parent %q", text(res))
	}
	if r := tm.lead.mustCall("close_subthread", map[string]any{"no": 2, "reason": "不需要了"}); !strings.Contains(text(r), "closed") {
		t.Fatalf("close_subthread %q", text(r))
	}
	if v, _ := core.ThreadByID(tm.w.ctx, tm.w.e.Core.Pool, child.ID); v.Status != "done" {
		t.Fatalf("closed subthread %s", v.Status)
	}

	tm.b.must(200, "POST", "/api/threads/"+tm.thread.ID+"/archive", nil, nil)
	if v, _ := core.ThreadByID(tm.w.ctx, tm.w.e.Core.Pool, child.ID); v.Status != "archived" {
		t.Fatalf("subthread after archiving the parent: %s", v.Status)
	}
}

// TestSubthreadLimits: at most 5 unfinished subthreads, and two threads exchange at most 20
// messages an hour.
func TestSubthreadLimits(t *testing.T) {
	tm := startTeam(t)
	for i := range 5 {
		tm.lead.mustCall("open_subthread", map[string]any{"title": "子" + string(rune('A'+i)), "brief": "x"})
	}
	if r := tm.lead.call("open_subthread", map[string]any{"title": "第六个", "brief": "x"}); r.Ok || !strings.Contains(text(r), "at most 5") {
		t.Fatalf("sixth subthread: %+v", r)
	}
	for i := range 20 {
		if r := tm.lead.call("message_thread", map[string]any{"no": 2, "text": "第" + string(rune('a'+i))}); !r.Ok {
			t.Fatalf("message %d refused: %s", i, text(r))
		}
	}
	if r := tm.lead.call("message_thread", map[string]any{"no": 2, "text": "多一条"}); r.Ok || !strings.Contains(text(r), "the limit") {
		t.Fatalf("21st message: %+v", r)
	}
}
