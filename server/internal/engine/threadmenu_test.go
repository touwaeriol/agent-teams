package engine_test

import (
	"testing"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/orchapi"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// TestThreadPinOrdering: pinned threads list first (newest pin first), unpinning puts a
// thread back in number order; another user cannot pin.
func TestThreadPinOrdering(t *testing.T) {
	tm := startTeam(t)
	base := "/api/projects/" + tm.projectID + "/threads"
	var second, third core.ThreadView
	tm.b.must(201, "POST", base, map[string]any{"title": "Second", "brief": "b"}, &second)
	tm.b.must(201, "POST", base, map[string]any{"title": "Third", "brief": "c"}, &third)
	order := func() []int {
		var list []core.ThreadView
		tm.b.must(200, "GET", base, nil, &list)
		nos := make([]int, 0, len(list))
		for _, v := range list {
			nos = append(nos, v.No)
		}
		return nos
	}
	if got := order(); !sameInts(got, []int{3, 2, 1}) {
		t.Fatalf("unpinned order %v", got)
	}
	var v core.ThreadView
	tm.b.must(200, "PATCH", "/api/threads/"+tm.thread.ID, map[string]any{"pinned": true}, &v)
	if v.PinnedAt == nil {
		t.Fatal("pinned_at not set")
	}
	time.Sleep(5 * time.Millisecond)
	tm.b.must(200, "PATCH", "/api/threads/"+second.ID, map[string]any{"pinned": true}, nil)
	if got := order(); !sameInts(got, []int{2, 1, 3}) {
		t.Fatalf("pinned order %v", got)
	}
	tm.b.must(200, "PATCH", "/api/threads/"+second.ID, map[string]any{"pinned": false}, &v)
	if v.PinnedAt != nil {
		t.Fatal("unpin kept pinned_at")
	}
	if got := order(); !sameInts(got, []int{1, 3, 2}) {
		t.Fatalf("after unpin %v", got)
	}
	other := tm.w.signUp("pin-stranger@example.com")
	if code, _ := other.do("PATCH", "/api/threads/"+third.ID, map[string]any{"pinned": true}); code != 404 {
		t.Fatalf("stranger pin = %d", code)
	}
}

// TestThreadReminderFiresOnce: a due reminder moves to reminded_at on one timers tick and never
// fires again; a cancelled reminder never fires; clearing a fired one empties both.
func TestThreadReminderFiresOnce(t *testing.T) {
	tm := startTeam(t)
	events := tm.b.stream("/api/projects/"+tm.projectID+"/events", "0")
	path := "/api/threads/" + tm.thread.ID + "/reminder"
	tm.b.must(422, "PUT", path, map[string]any{"at": time.Now().Add(-time.Minute)}, nil)
	tm.b.must(422, "PUT", path, map[string]any{"at": time.Now().Add(400 * 24 * time.Hour)}, nil)

	var v core.ThreadView
	tm.b.must(200, "PUT", path, map[string]any{"at": time.Now().Add(time.Hour)}, &v)
	if v.RemindAt == nil || v.RemindedAt != nil {
		t.Fatalf("set: %+v %+v", v.RemindAt, v.RemindedAt)
	}
	tm.w.e.Timers.Tick(tm.w.ctx) // not due yet
	if got := threadView(t, tm); got.RemindedAt != nil {
		t.Fatal("fired early")
	}
	makeDue(t, tm)
	tm.w.e.Timers.Tick(tm.w.ctx)
	fired := threadView(t, tm)
	if fired.RemindAt != nil || fired.RemindedAt == nil {
		t.Fatalf("not fired: %+v %+v", fired.RemindAt, fired.RemindedAt)
	}
	events.wait(t, "thread", `"reminded_at":"`)
	makeDue(t, tm)
	tm.w.e.Timers.Tick(tm.w.ctx)
	if again := threadView(t, tm); !again.RemindedAt.Equal(*fired.RemindedAt) {
		t.Fatalf("fired twice: %v then %v", fired.RemindedAt, again.RemindedAt)
	}

	tm.b.must(200, "DELETE", path, nil, &v)
	if v.RemindAt != nil || v.RemindedAt != nil {
		t.Fatal("clear left a reminder")
	}
	tm.b.must(200, "PUT", path, map[string]any{"at": time.Now().Add(time.Hour)}, nil)
	tm.b.must(200, "DELETE", path, nil, nil)
	makeDue(t, tm)
	tm.w.e.Timers.Tick(tm.w.ctx)
	if got := threadView(t, tm); got.RemindedAt != nil || got.RemindAt != nil {
		t.Fatal("cancelled reminder fired")
	}
	other := tm.w.signUp("remind-stranger@example.com")
	if code, _ := other.do("PUT", path, map[string]any{"at": time.Now().Add(time.Hour)}); code != 404 {
		t.Fatalf("stranger reminder = %d", code)
	}
}

// TestThreadFeedback: a user sends feedback (with or without the conversation) about their own
// thread; only administrators list it and mark it handled.
func TestThreadFeedback(t *testing.T) {
	tm := startTeam(t) // tm.b registered first: an administrator
	user := tm.w.signUp("feedback-user@example.com")
	var p struct{ ID string }
	user.must(201, "POST", "/api/projects", map[string]any{"name": "mine"}, &p)
	var th core.ThreadView
	user.must(201, "POST", "/api/projects/"+p.ID+"/threads", map[string]any{"title": "反馈用", "brief": "做点事"}, &th)

	var f orchapi.FeedbackView
	user.must(201, "POST", "/api/threads/"+th.ID+"/feedback", map[string]any{"text": "组长回复太慢", "include_content": true}, &f)
	if f.ThreadNo != th.No || f.Content == "" || f.UserEmail != "feedback-user@example.com" {
		t.Fatalf("feedback %+v", f)
	}
	user.must(201, "POST", "/api/threads/"+th.ID+"/feedback", map[string]any{"text": "不带内容"}, nil)
	user.must(422, "POST", "/api/threads/"+th.ID+"/feedback", map[string]any{"text": "   "}, nil)
	if code, _ := tm.b.do("POST", "/api/threads/"+th.ID+"/feedback", map[string]any{"text": "冒充"}); code != 404 {
		t.Fatalf("feedback on someone else's thread = %d", code)
	}
	for _, req := range [][2]string{{"GET", "/api/admin/feedback"}, {"PATCH", "/api/admin/feedback/" + f.ID}} {
		if code, _ := user.do(req[0], req[1], map[string]any{"handled": true}); code != 403 {
			t.Fatalf("non-admin %s %s = %d", req[0], req[1], code)
		}
	}
	var list []orchapi.FeedbackView
	tm.b.must(200, "GET", "/api/admin/feedback", nil, &list)
	if len(list) != 2 || list[0].Text != "不带内容" || list[0].Content != "" || list[1].ID != f.ID {
		t.Fatalf("admin list %+v", list)
	}
	var handled orchapi.FeedbackView
	tm.b.must(200, "PATCH", "/api/admin/feedback/"+f.ID, map[string]any{"handled": true}, &handled)
	if handled.HandledAt == nil || handled.HandledBy != "team@example.com" {
		t.Fatalf("handled %+v", handled)
	}
	tm.b.must(200, "GET", "/api/admin/feedback?status=open", nil, &list)
	if len(list) != 1 || list[0].ID == f.ID {
		t.Fatalf("open list %+v", list)
	}
	tm.b.must(404, "PATCH", "/api/admin/feedback/nope", map[string]any{"handled": true}, nil)
	user.must(204, "DELETE", "/api/threads/"+th.ID, nil, nil)
	tm.b.must(200, "GET", "/api/admin/feedback?status=handled", nil, &list)
	if len(list) != 1 || list[0].ThreadTitle != "反馈用" {
		t.Fatalf("feedback lost with the thread: %+v", list)
	}
}

func threadView(t *testing.T, tm *team) core.ThreadView {
	t.Helper()
	var v core.ThreadView
	tm.b.must(200, "GET", "/api/threads/"+tm.thread.ID, nil, &v)
	return v
}

func makeDue(t *testing.T, tm *team) {
	t.Helper()
	if _, err := store.Exec(tm.w.ctx, tm.w.e.Core.Pool, `UPDATE at_threads SET remind_at = now() - interval '1 second'
		WHERE remind_at IS NOT NULL`); err != nil {
		t.Fatal(err)
	}
}

func sameInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
