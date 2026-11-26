package engine_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	protocol "github.com/touwaeriol/agent-teams/protocol"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw/nodetest"
	"github.com/touwaeriol/agent-teams/server/internal/orchapi"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// The page's own attachment features (#58 D6): attachments of a new
// plain session (uploaded before its project exists) and of comments on deliverables.

func countRows(t *testing.T, w *world, q string, args ...any) int {
	t.Helper()
	var n int
	if err := store.QueryRow(context.Background(), w.db.Pool, q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// userUpload runs a whole upload without a project (POST /api/uploads…).
func (b *browser) userUpload(name string, body []byte) core.Attachment {
	b.w.t.Helper()
	var up orchapi.UploadSession
	b.must(201, "POST", "/api/uploads", map[string]any{"kind": "attachment", "name": name, "size": len(body)}, &up)
	var done orchapi.UploadDone
	b.must(200, "POST", "/api/uploads/"+up.UploadID+"/complete", map[string]any{"parts": b.putPartsAt("/api/uploads", up, body)}, &done)
	if done.Kind != "attachment" || done.Attachment == nil {
		b.w.t.Fatalf("user upload %+v", done)
	}
	return done.Attachment.Attachment
}

// TestSessionAttachments: attachments uploaded without a project (POST /api/uploads…, kind
// attachment only, the user's alone) join the new plain session's project when POST /api/sessions
// names them, and its first message brings them (small text files inlined, everything as files to
// a daemon with attachments_v2; the stored message lists them). Expired ones answer 410 and no
// session is made; other users' ids, unfinished ones and ones already used are refused. Messages
// read back mark expired attachments.
func TestSessionAttachments(t *testing.T) {
	w := newWorld(t, nil)
	b := w.signUp("solo@example.com")
	var node nodeCreated
	b.must(201, "POST", "/api/nodes", map[string]string{"name": "fake"}, &node)
	b.must(200, "GET", "/api/roles", nil, nil)
	n := w.connect(node.Token, nodetest.Options{Features: v2Features})
	ctx := context.Background()

	if code, raw := b.do("POST", "/api/uploads", map[string]any{"kind": "shared", "name": "a.txt", "size": 2}); code != 422 {
		t.Fatalf("kind shared = %d %s", code, raw)
	}
	png := b.userUpload("shot.png", onePixelPNG)
	md := b.userUpload("spec.md", []byte("登录页要有记住我"))
	var key string
	if err := store.QueryRow(ctx, w.db.Pool, `SELECT storage_key FROM at_attachments WHERE id = $1 AND project_id IS NULL`, png.ID).Scan(&key); err != nil ||
		!strings.HasPrefix(key, "users/") || !strings.HasSuffix(key, "/attachments/"+png.ID) {
		t.Fatalf("unowned key %q %v", key, err)
	}
	// The owner sees it before it is sent; nobody else does.
	if resp, _ := b.redirect("/api/attachments/" + png.ID); resp.StatusCode != 302 {
		t.Fatalf("owner preview = %d", resp.StatusCode)
	}
	other := w.signUp("other@example.com")
	if code, _ := other.do("GET", "/api/attachments/"+png.ID, nil); code != 404 {
		t.Fatalf("other user's preview = %d", code)
	}

	// Abort, and other users' uploads.
	var ab orchapi.UploadSession
	b.must(201, "POST", "/api/uploads", map[string]any{"kind": "attachment", "name": "a.bin", "size": 10}, &ab)
	if code, _ := other.do("POST", "/api/uploads/"+ab.UploadID+"/parts", map[string]any{"numbers": []int{1}}); code != 404 {
		t.Fatalf("other user's parts = %d", code)
	}
	if code, _ := other.do("DELETE", "/api/uploads/"+ab.UploadID, nil); code != 404 {
		t.Fatalf("other user's abort = %d", code)
	}
	b.must(204, "DELETE", "/api/uploads/"+ab.UploadID, nil, nil)
	if code, _ := b.do("POST", "/api/uploads/"+ab.UploadID+"/parts", map[string]any{"numbers": []int{1}}); code != 404 {
		t.Fatalf("parts after abort = %d", code)
	}

	// Refusals make no session.
	stale := b.userUpload("old.txt", []byte("old"))
	_, _ = store.Exec(ctx, w.db.Pool, `UPDATE at_attachments SET state = 'expired', storage_key = NULL WHERE id = $1`, stale.ID)
	theirs := other.userUpload("theirs.txt", []byte("x"))
	var pending orchapi.UploadSession
	b.must(201, "POST", "/api/uploads", map[string]any{"kind": "attachment", "name": "half.bin", "size": 10}, &pending)
	for _, c := range []struct {
		ids  []string
		code int
	}{{[]string{png.ID, stale.ID}, 410}, {[]string{theirs.ID}, 422}, {[]string{pending.UploadID}, 422}} {
		if code, raw := b.do("POST", "/api/sessions", map[string]any{"message": "看图", "role": "dev", "repos": []any{}, "attachments": c.ids}); code != c.code {
			t.Fatalf("session with %v = %d %s", c.ids, code, raw)
		}
	}
	if countRows(t, w, `SELECT count(*) FROM at_projects WHERE kind = 'session'`) != 0 ||
		countRows(t, w, `SELECT count(*) FROM at_attachments WHERE id = $1 AND project_id IS NULL`, png.ID) != 1 {
		t.Fatal("a refused session left a project or took an attachment")
	}

	var created struct {
		Project  struct{ ID string }
		ThreadID string `json:"thread_id"`
	}
	b.must(201, "POST", "/api/sessions", map[string]any{"message": "按这个改登录页", "role": "dev", "node_id": node.Node.ID,
		"repos": []any{}, "attachments": []string{png.ID, md.ID, png.ID}}, &created)
	pid := created.Project.ID
	if countRows(t, w, `SELECT count(*) FROM at_attachments WHERE id IN ($1, $2) AND project_id = $3`, png.ID, md.ID, pid) != 2 {
		t.Fatal("attachments not moved into the session's project")
	}
	_, open := expectOpen(t, w, n, "lead-1")
	fm := open.FirstMessage
	if fm == nil || !sameIDs(refIDs(fm.Attachments), png.ID, md.ID) || !strings.Contains(fm.Text, "按这个改登录页") ||
		!strings.Contains(fm.Text, "登录页要有记住我") {
		t.Fatalf("first message %+v", fm)
	}
	// Used once: another session cannot take them.
	if code, _ := b.do("POST", "/api/sessions", map[string]any{"message": "再来", "role": "dev", "repos": []any{}, "attachments": []string{md.ID}}); code != 422 {
		t.Fatalf("reused attachment = %d", code)
	}

	// The stored first message lists them; an expired one is marked when read back.
	_, _ = store.Exec(ctx, w.db.Pool, `UPDATE at_attachments SET state = 'expired', storage_key = NULL WHERE id = $1`, png.ID)
	var page struct {
		Items []core.MessageView `json:"items"`
	}
	b.must(200, "GET", "/api/conversations/"+created.ThreadID+"/messages", nil, &page)
	var refs []map[string]any
	for _, m := range page.Items {
		if m.Role == "user" && m.Content == "按这个改登录页" {
			raw, _ := json.Marshal(m.Meta["attachments"])
			_ = json.Unmarshal(raw, &refs)
		}
	}
	if len(refs) != 2 || refs[0]["id"] != png.ID || refs[0]["expired"] != true || refs[1]["expired"] != nil {
		t.Fatalf("message refs %v", refs)
	}

	// Deleting the session queues the objects kept under users/.
	b.must(204, "DELETE", "/api/projects/"+pid, nil, nil)
	if countRows(t, w, `SELECT count(*) FROM at_blob_deletions WHERE key LIKE 'users/%/attachments/' || $1`, md.ID) != 1 {
		t.Fatal("users/ object not queued with the project")
	}
}

// TestCommentAttachments: a comment on a deliverable carries attachments of its project to the
// employee it goes to (as files to an attachments_v2 daemon, in the stored message's meta), the
// comment list returns them (expired ones marked), and their retention restarts; attachments of
// another project or expired ones are refused.
func TestCommentAttachments(t *testing.T) {
	tm := attTeam(t, true)
	pdf := putAttachment(t, tm, tm.projectID, "批注.pdf", core.PDFType, []byte("%PDF-1.4 x"))
	zip := putAttachment(t, tm, tm.projectID, "素材.zip", "application/zip", []byte("PK\x03\x04zip"))
	md := putAttachment(t, tm, tm.projectID, "要点.md", core.TextType, []byte("标题用 24 号字"))
	_, _ = store.Exec(tm.w.ctx, tm.w.e.Core.Pool, `UPDATE at_attachments SET expires_at = now() + interval '1 minute' WHERE id = $1`, zip.ID)
	res := tm.lead.publish(map[string]any{"path": "design", "kind": "design", "title": "首页"}, 1<<20, upFile{"index.html", "<!doctype html><title>Home</title>"})
	var pub protocol.PublishFileResult
	_ = json.Unmarshal(*res.Structured, &pub)
	id := pub.DeliverableID

	var c orchapi.CommentView
	tm.b.must(201, "POST", "/api/deliverables/"+id+"/comments", map[string]any{"text": "照这个改配色", "attachments": []string{pdf.ID, zip.ID, md.ID}}, &c)
	if c.SentTo != "lead-1" || len(c.Attachments) != 3 || c.Attachments[0].ID != pdf.ID || c.Attachments[1].Name != "素材.zip" {
		t.Fatalf("comment %+v", c)
	}
	send := tm.lead.expectSend("照这个改配色")
	if !sameIDs(refIDs(send.Attachments), pdf.ID, zip.ID, md.ID) || !strings.Contains(send.Text, "标题用 24 号字") {
		t.Fatalf("comment delivery %q attachments %+v", send.Text, send.Attachments)
	}
	if tm.expiresIn(t, zip.ID).Hours() < 23 {
		t.Fatal("comment did not push the retention")
	}
	if countRows(t, tm.w, `SELECT count(*) FROM at_messages WHERE content LIKE '%照这个改配色%' AND jsonb_array_length(meta->'attachments') = 3
		AND content NOT LIKE '%24 号字%'`) != 1 {
		t.Fatal("stored comment message lacks the attachments or has the inlined text")
	}

	_, _ = store.Exec(tm.w.ctx, tm.w.e.Core.Pool, `UPDATE at_attachments SET state = 'expired', storage_key = NULL WHERE id = $1`, zip.ID)
	var list struct{ Items []orchapi.CommentView }
	tm.b.must(200, "GET", "/api/deliverables/"+id+"/comments", nil, &list)
	if len(list.Items) != 1 || len(list.Items[0].Attachments) != 3 || list.Items[0].Attachments[0].Expired || !list.Items[0].Attachments[1].Expired {
		t.Fatalf("comment list %+v", list.Items)
	}
	if code, raw := tm.b.do("POST", "/api/deliverables/"+id+"/comments", map[string]any{"text": "再看", "attachments": []string{zip.ID}}); code != 410 {
		t.Fatalf("expired comment attachment = %d %s", code, raw)
	}
	var p2 struct{ ID string }
	tm.b.must(201, "POST", "/api/projects", map[string]any{"name": "second"}, &p2)
	elsewhere := putAttachment(t, tm, p2.ID, "别处.png", "image/png", onePixelPNG)
	if code, raw := tm.b.do("POST", "/api/deliverables/"+id+"/comments", map[string]any{"text": "再看", "attachments": []string{elsewhere.ID}}); code != 404 {
		t.Fatalf("other project's attachment = %d %s", code, raw)
	}
	if countRows(t, tm.w, `SELECT count(*) FROM at_deliverable_comments WHERE deliverable_id = $1`, id) != 1 {
		t.Fatal("refused comments stored")
	}
}

// TestMessageAttachmentLimit: a message takes up to 20 attachments (was 8); 21 is refused.
func TestMessageAttachmentLimit(t *testing.T) {
	tm := attTeam(t, true)
	ids := make([]string, 21)
	for i := range ids {
		ids[i] = putAttachment(t, tm, tm.projectID, "f"+string(rune('a'+i))+".bin", "application/octet-stream", []byte{byte(i), 1, 2}).ID
	}
	if code, _ := tm.b.do("POST", "/api/conversations/"+tm.thread.ID+"/messages", map[string]any{"content": "太多了", "attachments": ids}); code != 422 {
		t.Fatalf("21 attachments = %d", code)
	}
	tm.b.must(202, "POST", "/api/conversations/"+tm.thread.ID+"/messages", map[string]any{"content": "二十个文件", "attachments": ids[:20]}, nil)
	if send := tm.lead.expectSend("二十个文件"); len(send.Attachments) != 20 {
		t.Fatalf("delivered %d attachments", len(send.Attachments))
	}
}

// TestCommentAttachmentsLegacy: to a daemon without attachments_v2 a comment's small text file is
// in the delivered text and an image goes as an image (legacyFit), as for any message.
func TestCommentAttachmentsLegacy(t *testing.T) {
	tm := attTeam(t, false)
	md := putAttachment(t, tm, tm.projectID, "要点.md", core.TextType, []byte("按钮要圆角 8px"))
	png := putAttachment(t, tm, tm.projectID, "图.png", "image/png", onePixelPNG)
	res := tm.lead.publish(map[string]any{"path": "design", "kind": "design", "title": "首页"}, 1<<20, upFile{"index.html", "<!doctype html><title>Home</title>"})
	var pub protocol.PublishFileResult
	_ = json.Unmarshal(*res.Structured, &pub)
	tm.b.must(201, "POST", "/api/deliverables/"+pub.DeliverableID+"/comments", map[string]any{"text": "旧节点批注", "attachments": []string{md.ID, png.ID}}, nil)
	send := tm.lead.expectSend("旧节点批注")
	if !strings.Contains(send.Text, "按钮要圆角 8px") || len(send.Attachments) != 0 || len(send.Images) != 1 || send.Images[0].ID != png.ID {
		t.Fatalf("legacy comment %q images %+v attachments %+v", send.Text, send.Images, send.Attachments)
	}
	// Deleting the thread the comment was delivered into keeps the attachments a comment shows.
	tm.b.must(204, "DELETE", "/api/threads/"+tm.thread.ID, nil, nil)
	if countRows(t, tm.w, `SELECT count(*) FROM at_deliverable_comments WHERE deliverable_id = $1`, pub.DeliverableID) != 1 ||
		countRows(t, tm.w, `SELECT count(*) FROM at_attachments WHERE id IN ($1, $2)`, md.ID, png.ID) != 2 {
		t.Fatal("thread deletion dropped a comment's attachments")
	}
}
