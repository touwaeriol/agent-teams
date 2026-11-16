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
