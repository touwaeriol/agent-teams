package engine_test

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw/nodetest"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// TestThreadArchiveUnarchiveDelete: archiving ends a running thread and closes its sessions,
// unarchiving brings it back paused, deleting purges the thread directory on the node,
// removes the thread and keeps its deliverables in the library.
func TestThreadArchiveUnarchiveDelete(t *testing.T) {
	tm := startTeam(t)
	events := tm.b.stream("/api/projects/"+tm.projectID+"/events", "0")
	tid := tm.thread.ID
	var d core.DeliverableView
	tm.b.must(201, "POST", "/api/projects/"+tm.projectID+"/deliverables",
		map[string]any{"kind": "doc", "title": "调研", "url": "https://example.com/doc", "thread_no": tm.thread.No}, &d)

	var got core.ThreadView
	tm.b.must(200, "POST", "/api/threads/"+tid+"/archive", nil, &got)
	if got.Status != "archived" {
		t.Fatalf("archive: %s", got.Status)
	}
	ctx, cancel := tm.w.waitCtx()
	defer cancel()
	if _, err := tm.n.Wait(ctx, protocol.TypeSessionClose, func(f nodetest.Frame) bool { return strings.Contains(string(f.Body), tm.dev.session) }); err != nil {
		t.Fatal(err)
	}
	tm.b.must(200, "POST", "/api/threads/"+tid+"/unarchive", nil, &got)
	if got.Status != "paused" {
		t.Fatalf("unarchive of a running thread should pause it: %s", got.Status)
	}
	tm.b.must(409, "POST", "/api/threads/"+tid+"/unarchive", nil, nil)

	other := tm.w.signUp("stranger-lifecycle@example.com")
	for _, req := range [][2]string{{"DELETE", "/api/threads/" + tid}, {"POST", "/api/threads/" + tid + "/archive"}} {
		if code, _ := other.do(req[0], req[1], nil); code != 404 {
			t.Fatalf("another user's %s %s = %d", req[0], req[1], code)
		}
	}
	img := uploadPNG(t, tm)
	tm.b.must(202, "POST", "/api/conversations/"+tid+"/messages", map[string]any{"content": "看图", "attachments": []string{img}}, nil)

	tm.b.must(204, "DELETE", "/api/threads/"+tid, nil, nil)
	tm.b.must(404, "GET", "/api/attachments/"+img, nil, nil)
	tm.b.must(404, "GET", "/api/threads/"+tid, nil, nil)
	events.wait(t, "thread", `"deleted"`, `"id":"`+tid+`"`)
	if _, err := tm.n.Wait(ctx, protocol.TypeGitPurge, func(f nodetest.Frame) bool {
		return strings.Contains(string(f.Body), "projects/"+tm.projectID+"/threads/1")
	}); err != nil {
		t.Fatal(err)
	}
	var page struct{ Items []core.DeliverableView }
	tm.b.must(200, "GET", "/api/projects/"+tm.projectID+"/deliverables", nil, &page)
	if len(page.Items) != 1 || page.Items[0].ID != d.ID || page.Items[0].ThreadNo != nil {
		t.Fatalf("deliverable should stay without its thread: %+v", page.Items)
	}
}

// TestFinishedThreadUnarchivesFinished: a done thread comes back done.
func TestFinishedThreadUnarchivesFinished(t *testing.T) {
	tm := startTeam(t)
	if _, err := store.Exec(tm.w.ctx, tm.w.e.Core.Pool, `UPDATE at_threads SET status = 'done' WHERE id = $1`, tm.thread.ID); err != nil {
		t.Fatal(err)
	}
	var got core.ThreadView
	tm.b.must(200, "POST", "/api/threads/"+tm.thread.ID+"/archive", nil, &got)
	tm.b.must(200, "POST", "/api/threads/"+tm.thread.ID+"/unarchive", nil, &got)
	if got.Status != "done" {
		t.Fatalf("got %s", got.Status)
	}
}

var onePixelPNG, _ = base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==")

// TestImageMessage: an uploaded image goes with the message to the lead's session, the
// message keeps it in meta.attachments, and the daemon can fetch it with its node token.
func TestImageMessage(t *testing.T) {
	tm := startTeam(t)
	var att struct {
		ID          string `json:"id"`
		ContentType string `json:"content_type"`
		URL         string `json:"url"`
	}
	tm.b.must(201, "POST", "/api/projects/"+tm.projectID+"/attachments",
		map[string]string{"name": "截图.png", "data": base64.StdEncoding.EncodeToString(onePixelPNG)}, &att)
	other := tm.w.signUp("stranger-images@example.com")
	if code, _ := other.do("GET", att.URL, nil); code != 404 {
		t.Fatalf("another user's image = %d", code)
	}
	if code, _ := other.do("POST", "/api/projects/"+tm.projectID+"/attachments",
		map[string]string{"name": "x.png", "data": base64.StdEncoding.EncodeToString(onePixelPNG)}); code != 404 {
		t.Fatalf("upload into another user's project = %d", code)
	}
	resp, err := tm.b.http.Get(tm.w.srv.URL + att.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if headers := resp.Header; headers.Get("Content-Type") != "image/png" || headers.Get("X-Content-Type-Options") != "nosniff" ||
		!strings.Contains(headers.Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("image headers: %v", headers)
	}

	if att.ContentType != "image/png" || att.URL != "/api/attachments/"+att.ID {
		t.Fatalf("upload: %+v", att)
	}
	tm.b.must(422, "POST", "/api/projects/"+tm.projectID+"/attachments",
		map[string]string{"name": "x.png", "data": base64.StdEncoding.EncodeToString([]byte("<svg/>"))}, nil)
	code, raw := tm.b.do("GET", att.URL, nil)
	if code != 200 || string(raw) != string(onePixelPNG) {
		t.Fatalf("get attachment: %d", code)
	}

	var msg core.MessageView
	tm.b.must(202, "POST", "/api/conversations/"+tm.thread.ID+"/messages",
		map[string]any{"content": "按截图改", "attachments": []string{att.ID}}, &msg)
	send := tm.lead.expectSend("按截图改")
	if len(send.Images) != 1 || send.Images[0].ID != att.ID || send.Images[0].MediaType != "image/png" {
		t.Fatalf("session.send images: %+v", send.Images)
	}
	if !strings.Contains(string(mustJSON(msg.Meta)), att.ID) {
		t.Fatalf("message meta: %+v", msg.Meta)
	}
	tm.b.must(422, "POST", "/api/conversations/"+tm.thread.ID+"/messages", map[string]any{"content": " "}, nil)
	tm.b.must(404, "POST", "/api/conversations/"+tm.thread.ID+"/messages",
		map[string]any{"content": "x", "attachments": []string{"img_missing"}}, nil)

	for token, want := range map[string]int{tm.token: 200, "wrong": 401} {
		req, _ := http.NewRequest("GET", tm.w.srv.URL+"/nodes/attachments/"+att.ID, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("node fetch with %q: %d", token, resp.StatusCode)
		}
	}
}

func uploadPNG(t *testing.T, tm *team) string {
	t.Helper()
	var att struct{ ID string }
	tm.b.must(201, "POST", "/api/projects/"+tm.projectID+"/attachments",
		map[string]string{"name": "a.png", "data": base64.StdEncoding.EncodeToString(onePixelPNG)}, &att)
	return att.ID
}

func mustJSON(v any) []byte {
	raw, _ := json.Marshal(v)
	return raw
}

// TestCompleteThread: the user marks a running thread done from the page. It is refused while
// a PR waits for accept or reject; otherwise the thread is done with the summary kept, every
// session closes, the coordinator is told and the change is streamed.
func TestCompleteThread(t *testing.T) {
	tm := startTeam(t)
	pool, ctx := tm.w.e.Core.Pool, tm.w.ctx
	events := tm.b.stream("/api/projects/"+tm.projectID+"/events", "0")
	tid := tm.thread.ID

	for _, q := range []string{
		`UPDATE at_threads SET status = 'review' WHERE id = $1`,
		`INSERT INTO at_thread_repos (thread_id, repo, branch_created, pr_number, pr_url, pr_state) VALUES ($1, 'o/r', true, 7, 'https://github.com/o/r/pull/7', 'open')`,
	} {
		if _, err := store.Exec(ctx, pool, q, tid); err != nil {
			t.Fatal(err)
		}
	}
	code, body := tm.b.do("POST", "/api/threads/"+tid+"/complete", map[string]any{})
	if code != http.StatusConflict || !strings.Contains(string(body), "pr_awaiting") {
		t.Fatalf("complete with a PR in review = %d %s", code, body)
	}
	if _, err := store.Exec(ctx, pool, `UPDATE at_threads SET status = 'running' WHERE id = $1`, tid); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Exec(ctx, pool, `DELETE FROM at_thread_repos WHERE thread_id = $1`, tid); err != nil {
		t.Fatal(err)
	}
	other := tm.w.signUp("stranger-complete@example.com")
	if code, _ := other.do("POST", "/api/threads/"+tid+"/complete", nil); code != 404 {
		t.Fatalf("another user's complete = %d", code)
	}

	var got core.ThreadView
	tm.b.must(200, "POST", "/api/threads/"+tid+"/complete", map[string]any{"summary": "调研结论已交付"}, &got)
	if got.Status != "done" || got.Summary != "调研结论已交付" {
		t.Fatalf("complete: %s %q", got.Status, got.Summary)
	}
	wctx, cancel := tm.w.waitCtx()
	defer cancel()
	for _, s := range []string{tm.dev.session, tm.lead.session} {
		if _, err := tm.n.Wait(wctx, protocol.TypeSessionClose, func(f nodetest.Frame) bool { return strings.Contains(string(f.Body), s) }); err != nil {
			t.Fatalf("session %s not closed: %v", s, err)
		}
	}
	events.wait(t, "thread", `"status":"done"`)
	eventually(t, "coordinator told", func() bool {
		var n int
		err := store.QueryRow(ctx, pool, `SELECT count(*) FROM at_messages m JOIN at_conversations c ON c.id = m.conversation_id
			WHERE c.kind = 'coordinator' AND c.project_id = $1 AND m.content LIKE '%标记为完成%调研结论已交付%'`, tm.projectID).Scan(&n)
		return err == nil && n == 1
	})
	// Again: already done, nothing changes.
	tm.b.must(200, "POST", "/api/threads/"+tid+"/complete", nil, &got)
	if got.Status != "done" {
		t.Fatalf("second complete: %s", got.Status)
	}
}

// TestReopenThread: the user reopens a done thread from the menu (owner only): it is running
// again with its old summary kept as a note, the coordinator is told, and no session starts until
// a message: the lead then resumes its old conversation. A message to a done thread reopens it
// on its own (to the lead or straight to a member); an archived thread still refuses both
// until it is unarchived; a failed thread reopens too.
func TestReopenThread(t *testing.T) {
	tm := startTeam(t)
	pool, ctx := tm.w.e.Core.Pool, tm.w.ctx
	events := tm.b.stream("/api/projects/"+tm.projectID+"/events", "0")
	tid := tm.thread.ID
	complete := func() {
		t.Helper()
		var got core.ThreadView
		tm.b.must(200, "POST", "/api/threads/"+tid+"/complete", map[string]any{"summary": "第一轮结论：用方案 B"}, &got)
		if got.Status != "done" {
			t.Fatalf("complete: %s", got.Status)
		}
	}
	complete()
	wctx, cancel := tm.w.waitCtx()
	defer cancel()
	for _, a := range []*fakeAgent{tm.dev, tm.lead} {
		if _, err := tm.n.Wait(wctx, protocol.TypeSessionClose, a.mine); err != nil {
			t.Fatalf("session %s not closed: %v", a.session, err)
		}
	}

	other := tm.w.signUp("stranger-reopen@example.com")
	if code, _ := other.do("POST", "/api/threads/"+tid+"/reopen", nil); code != 404 {
		t.Fatalf("another user's reopen = %d", code)
	}
	var got core.ThreadView
	tm.b.must(200, "POST", "/api/threads/"+tid+"/reopen", nil, &got)
	if got.Status != "running" || got.Summary != "" || got.FinishedAt != nil {
		t.Fatalf("reopen: %s %q %v", got.Status, got.Summary, got.FinishedAt)
	}
	events.wait(t, "thread", `"status":"running"`)
	notes := ofKind(tm.threadMessages(t), "note")
	if len(notes) == 0 || !strings.Contains(notes[len(notes)-1].Content, "第一轮结论：用方案 B") {
		t.Fatalf("old summary not kept in a note: %+v", notes)
	}
	eventually(t, "coordinator told", func() bool {
		var n int
		err := store.QueryRow(ctx, pool, `SELECT count(*) FROM at_messages m JOIN at_conversations c ON c.id = m.conversation_id
			WHERE c.kind = 'coordinator' AND c.project_id = $1 AND m.content LIKE '%· running] 用户重新打开了讨论串 #1%'`, tm.projectID).Scan(&n)
		return err == nil && n == 1
	})
	if live, _ := core.ThreadLiveSessions(ctx, pool, tid); len(live) != 0 {
		t.Fatalf("reopen started sessions: %+v", live)
	}
	tm.b.must(200, "POST", "/api/threads/"+tid+"/reopen", nil, &got) // running already: nothing changes

	// A message straight to a member of a done thread reopens it and resumes the member.
	complete()
	tm.b.must(202, "POST", "/api/threads/"+tid+"/members/dev-1/message", map[string]string{"content": "方案 B 的细节再补一下"}, nil)
	_, devOpen := expectOpen(t, tm.w, tm.n, "dev-1")
	if devOpen.Resume == nil || devOpen.Resume.SDKSessionID == nil || *devOpen.Resume.SDKSessionID != "sdk-"+tm.dev.session ||
		!strings.Contains(devOpen.FirstMessage.Text, "重新打开") {
		t.Fatalf("member must resume its old conversation: %+v %q", devOpen.Resume, devOpen.FirstMessage.Text)
	}
	tm.b.must(200, "GET", "/api/threads/"+tid, nil, &got)
	if got.Status != "running" {
		t.Fatalf("member message did not reopen: %s", got.Status)
	}

	// A message to the lead of a done thread reopens it and wakes the lead with its old context.
	complete()
	tm.b.must(202, "POST", "/api/conversations/"+tid+"/messages", map[string]any{"content": "刚才说的方案是哪个？"}, nil)
	_, leadOpen := expectOpen(t, tm.w, tm.n, "lead-1")
	if leadOpen.Resume == nil || leadOpen.Resume.SDKSessionID == nil || *leadOpen.Resume.SDKSessionID != "sdk-"+tm.lead.session ||
		!strings.Contains(leadOpen.FirstMessage.Text, "刚才说的方案是哪个？") {
		t.Fatalf("lead must resume its old conversation: %+v %q", leadOpen.Resume, leadOpen.FirstMessage.Text)
	}
	tm.b.must(200, "GET", "/api/threads/"+tid, nil, &got)
	if got.Status != "running" {
		t.Fatalf("message did not reopen: %s", got.Status)
	}

	// Archived: refused until unarchived (which keeps it finished), then reopened.
	complete()
	tm.b.must(200, "POST", "/api/threads/"+tid+"/archive", nil, nil)
	for _, path := range []string{"/api/threads/" + tid + "/reopen", "/api/conversations/" + tid + "/messages"} {
		code, raw := tm.b.do("POST", path, map[string]any{"content": "继续"})
		if code != http.StatusConflict || !strings.Contains(string(raw), "thread_archived") {
			t.Fatalf("%s on an archived thread = %d %s", path, code, raw)
		}
	}
	tm.b.must(200, "POST", "/api/threads/"+tid+"/unarchive", nil, &got)
	if got.Status != "done" {
		t.Fatalf("unarchive: %s", got.Status)
	}
	tm.b.must(200, "POST", "/api/threads/"+tid+"/reopen", nil, &got)
	if got.Status != "running" {
		t.Fatalf("reopen after unarchive: %s", got.Status)
	}

	// Failed threads reopen too.
	if _, err := store.Exec(ctx, pool, `UPDATE at_threads SET status = 'failed', finished_at = now() WHERE id = $1`, tid); err != nil {
		t.Fatal(err)
	}
	tm.b.must(200, "POST", "/api/threads/"+tid+"/reopen", nil, &got)
	if got.Status != "running" {
		t.Fatalf("reopen failed thread: %s", got.Status)
	}
}
