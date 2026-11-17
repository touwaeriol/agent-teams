package engine_test

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	protocol "github.com/touwaeriol/agent-teams/protocol"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

const fakePNG = "\x89PNG\r\n\x1a\nfake screenshot"

// deliverableCount is the number of deliverables the user sees in the project library.
func (tm *team) deliverableCount(t *testing.T) int {
	t.Helper()
	var list struct{ Items []core.DeliverableView }
	tm.b.must(200, "GET", "/api/projects/"+tm.projectID+"/deliverables", nil, &list)
	return len(list.Items)
}

// TestPublishFileScreenshotIsAttachment: a lone PNG sent with publish_file is not a deliverable:
// it is stored as a project attachment, shown in the thread as an "image" card, and the result
// explains how to use it. SVG and images as a new version are refused; a site directory with
// images is still a deliverable.
func TestPublishFileScreenshotIsAttachment(t *testing.T) {
	tm, _ := startDesignTeam(t)
	res := tm.dev.publish(map[string]any{"path": "shots/login.png", "kind": "design", "title": "登录页截图"}, 1<<20,
		upFile{"login.png", fakePNG})
	if !res.Ok || res.Structured != nil || !strings.Contains(text(res), "不是交付物") || !strings.Contains(text(res), "/api/attachments/img_") ||
		!strings.Contains(text(res), "index.html") {
		t.Fatalf("png publish %+v %s", res, text(res))
	}
	if n := tm.deliverableCount(t); n != 0 {
		t.Fatalf("screenshot became %d deliverables", n)
	}
	cards := ofKind(tm.threadMessages(t), "image")
	if len(cards) != 1 || cards[0].Author != "designer-1" || cards[0].Content != "designer-1 上传了截图：登录页截图" {
		t.Fatalf("image cards %+v", cards)
	}
	raw, _ := json.Marshal(cards[0].Meta["attachments"])
	var refs []core.AttachmentRef
	_ = json.Unmarshal(raw, &refs)
	if len(refs) != 1 || refs[0].ContentType != "image/png" || refs[0].Name != "login.png" || refs[0].Size != len(fakePNG) ||
		!strings.Contains(text(res), refs[0].ID) {
		t.Fatalf("attachments %s", raw)
	}
	if code, body := tm.b.do("GET", "/api/attachments/"+refs[0].ID, nil); code != 200 || string(body) != fakePNG {
		t.Fatalf("attachment served %d %q", code, body)
	}
	if left := uploadChunks(t, tm); left != 0 {
		t.Fatalf("%d upload chunks left", left)
	}

	// A lone SVG is refused, and an image cannot be a deliverable's new version.
	res = tm.dev.publish(map[string]any{"path": "d.svg", "kind": "design", "title": "图"}, 1<<20, upFile{"d.svg", "<svg/>"})
	if res.Ok || !strings.Contains(text(res), "SVG 不是交付物") {
		t.Fatalf("svg publish %+v", res)
	}
	site := tm.dev.publish(map[string]any{"path": "report", "kind": "doc", "title": "验收报告"}, 1<<20,
		upFile{"index.html", `<!doctype html><img src="shot.png">`}, upFile{"shot.png", fakePNG})
	var pub protocol.PublishFileResult
	if !site.Ok {
		t.Fatalf("site with an image %+v", site)
	}
	_ = json.Unmarshal(*site.Structured, &pub)
	res = tm.dev.publish(map[string]any{"path": "x.png", "kind": "doc", "title": "验收报告", "deliverable_id": pub.DeliverableID}, 1<<20,
		upFile{"x.png", fakePNG})
	if res.Ok || !strings.Contains(text(res), "不能作为交付物的新版本") {
		t.Fatalf("image as new version %+v", res)
	}
	if left := uploadChunks(t, tm); left != 0 {
		t.Fatalf("%d upload chunks left after refusals", left)
	}
	var files struct {
		Version int
		Render  string
	}
	tm.b.must(200, "GET", "/api/deliverables/"+pub.DeliverableID+"/files", nil, &files)
	if files.Version != 1 || files.Render != "html" || tm.deliverableCount(t) != 1 || len(ofKind(tm.threadMessages(t), "image")) != 1 {
		t.Fatalf("site %+v, %d deliverables", files, tm.deliverableCount(t))
	}
}

// TestScreenshotsAreNotLibraryFiles: the library refuses images and SVG; list_deliverables leaves
// out hosted images published before (the rows stay).
func TestScreenshotsAreNotLibraryFiles(t *testing.T) {
	tm, _ := startDesignTeam(t)
	path := "/api/projects/" + tm.projectID + "/library"
	for _, name := range []string{"main.png", "a.jpg", "b.webp", "c.svg"} {
		code, body := tm.b.do("POST", path, map[string]any{"name": name, "data": base64.StdEncoding.EncodeToString([]byte(fakePNG))})
		if code != 422 || !strings.Contains(string(body), "不是交付物") {
			t.Fatalf("library %s: %d %s", name, code, body)
		}
	}
	tm.b.must(201, "POST", path, map[string]any{"name": "notes.md", "data": base64.StdEncoding.EncodeToString([]byte("# hi"))}, nil)
	err := tm.w.e.Core.InTx(tm.w.ctx, func(tx *core.Tx) error {
		_, err := core.InsertDeliverable(tm.w.ctx, tx, core.NewDeliverable{ProjectID: tm.projectID, Kind: "design", Title: "旧截图",
			Detail: map[string]any{"hosted": true, "render": "image", "entry": "old.png", "version": 1}, PublishedBy: "designer-1"})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if n := tm.deliverableCount(t); n != 2 {
		t.Fatalf("library has %d items, want the old image kept", n)
	}
	list := tm.dev.mustCall("list_deliverables", map[string]any{})
	var out protocol.ListDeliverablesResult
	_ = json.Unmarshal(*list.Structured, &out)
	if len(out.Deliverables) != 1 || out.Deliverables[0].Title != "notes.md" || strings.Contains(text(list), "旧截图") {
		t.Fatalf("list_deliverables %+v", out)
	}
}

func uploadChunks(t *testing.T, tm *team) int {
	t.Helper()
	var n int
	if err := store.QueryRow(tm.w.ctx, tm.w.e.Core.Pool, `SELECT count(*) FROM at_upload_chunks`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
