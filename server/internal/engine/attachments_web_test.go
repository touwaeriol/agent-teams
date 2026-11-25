package engine_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	protocol "github.com/touwaeriol/agent-teams/protocol"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw/nodetest"
	"github.com/touwaeriol/agent-teams/server/internal/orchapi"
	"github.com/touwaeriol/agent-teams/server/internal/sharedfiles"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// The page's own attachment features (#58 D6): saving one as a shared file, attachments of a new
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

// TestSaveAttachmentShared: POST /api/attachments/{id}/save-shared copies an attachment into the
// project's shared files (the bytes, the uploader "user", a use of the attachment); an existing
// path is 409 file_exists unless overwrite, the quota 409 quota_exceeded with the usage, an
// expired attachment 410; other users, attachments without a project and bad paths are refused.
func TestSaveAttachmentShared(t *testing.T) {
	tm := startTeam(t)
	w, b, pid := tm.w, tm.b, tm.projectID
	ctx := context.Background()
	body := []byte("# 会议纪要\n第一条\n")
	att := b.upload(pid, map[string]any{"kind": "attachment", "name": "纪要.md"}, body).Attachment
	_, _ = store.Exec(ctx, w.db.Pool, `UPDATE at_attachments SET expires_at = now() + interval '1 minute' WHERE id = $1`, att.ID)

	var saved orchapi.UploadedSharedFile
	b.must(200, "POST", "/api/attachments/"+att.ID+"/save-shared", map[string]any{"path": "docs/纪要.md"}, &saved)
	if saved.Path != "docs/纪要.md" || saved.Size != int64(len(body)) || saved.Overwritten || saved.UploadedBy != sharedfiles.UserName {
		t.Fatalf("saved %+v", saved)
	}
	if tm.expiresIn(t, att.ID).Hours() < 23 {
		t.Fatal("saving did not push the retention")
	}
	resp, _ := b.redirect("/api/projects/" + pid + "/shared-files/download?path=docs/纪要.md")
	if _, raw := fetchURL(t, resp.Header.Get("Location")); !bytes.Equal(raw, body) {
		t.Fatalf("shared copy %q", raw)
	}

	// Same path: 409 file_exists, unless overwrite.
	if code, raw := b.do("POST", "/api/attachments/"+att.ID+"/save-shared", map[string]any{"path": "docs/纪要.md"}); code != 409 ||
		!strings.Contains(string(raw), "file_exists") {
		t.Fatalf("existing path = %d %s", code, raw)
	}
	b.must(200, "POST", "/api/attachments/"+att.ID+"/save-shared", map[string]any{"path": "docs/纪要.md", "overwrite": true}, &saved)
	if !saved.Overwritten {
		t.Fatalf("overwrite %+v", saved)
	}
	if code, _ := b.do("POST", "/api/attachments/"+att.ID+"/save-shared", map[string]any{"path": "../x"}); code != 422 {
		t.Fatalf("bad path = %d", code)
	}

	// The quota: a project nearly full refuses, saying how much is used.
	if _, err := store.Exec(ctx, w.db.Pool, `INSERT INTO at_shared_files (project_id, path, content_type, size, uploaded_by, storage_key, object_id, state)
		VALUES ($1, 'huge.bin', 'application/octet-stream', $2, 'user', 'projects/'||$1||'/shared/sf_huge', 'sf_huge', 'ready')`,
		pid, sharedfiles.MaxProjectBytes-5); err != nil {
		t.Fatal(err)
	}
	if code, raw := b.do("POST", "/api/attachments/"+att.ID+"/save-shared", map[string]any{"path": "more.md"}); code != 409 ||
		!strings.Contains(string(raw), "quota_exceeded") || !strings.Contains(string(raw), "已用") {
		t.Fatalf("over quota = %d %s", code, raw)
	}
	_, _ = store.Exec(ctx, w.db.Pool, `DELETE FROM at_shared_files WHERE project_id = $1 AND path = 'huge.bin'`, pid)

	// Only the owner; an attachment not yet in a project has nowhere to go.
	intruder := w.signUp("intruder@example.com")
	if code, _ := intruder.do("POST", "/api/attachments/"+att.ID+"/save-shared", map[string]any{"path": "x.md"}); code != 404 {
		t.Fatalf("intruder = %d", code)
	}
	loose := b.userUpload("loose.txt", []byte("hi"))
	if code, _ := b.do("POST", "/api/attachments/"+loose.ID+"/save-shared", map[string]any{"path": "loose.txt"}); code != 404 {
		t.Fatalf("attachment without a project = %d", code)
	}

	// Expired: 410, nothing written.
	_, _ = store.Exec(ctx, w.db.Pool, `UPDATE at_attachments SET state = 'expired', storage_key = NULL WHERE id = $1`, att.ID)
	if code, raw := b.do("POST", "/api/attachments/"+att.ID+"/save-shared", map[string]any{"path": "late.md"}); code != 410 ||
		!strings.Contains(string(raw), "attachment_expired") {
		t.Fatalf("expired = %d %s", code, raw)
	}
	if countRows(t, w, `SELECT count(*) FROM at_shared_files WHERE project_id = $1`, pid) != 1 {
		t.Fatal("shared files after the refusals")
	}
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
		t.Fatalf("shared without a project = %d %s", code, raw)
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
	_, _ = store.Exec(tm.w.ctx, tm.w.e.Core.Pool, `UPDATE at_attachments SET expires_at = now() + interval '1 minute' WHERE id = $1`, zip.ID)
	res := tm.lead.publish(map[string]any{"path": "design", "kind": "design", "title": "首页"}, 1<<20, upFile{"index.html", "<!doctype html><title>Home</title>"})
	var pub protocol.PublishFileResult
	_ = json.Unmarshal(*res.Structured, &pub)
	id := pub.DeliverableID

	var c orchapi.CommentView
	tm.b.must(201, "POST", "/api/deliverables/"+id+"/comments", map[string]any{"text": "照这个改配色", "attachments": []string{pdf.ID, zip.ID}}, &c)
	if c.SentTo != "lead-1" || len(c.Attachments) != 2 || c.Attachments[0].ID != pdf.ID || c.Attachments[1].Name != "素材.zip" {
		t.Fatalf("comment %+v", c)
	}
	send := tm.lead.expectSend("照这个改配色")
	if !sameIDs(refIDs(send.Attachments), pdf.ID, zip.ID) {
		t.Fatalf("comment delivery attachments %+v", send.Attachments)
	}
	if tm.expiresIn(t, zip.ID).Hours() < 23 {
		t.Fatal("comment did not push the retention")
	}
	if countRows(t, tm.w, `SELECT count(*) FROM at_messages WHERE content LIKE '%照这个改配色%' AND jsonb_array_length(meta->'attachments') = 2`) != 1 {
		t.Fatal("stored comment message lacks the attachments")
	}

	_, _ = store.Exec(tm.w.ctx, tm.w.e.Core.Pool, `UPDATE at_attachments SET state = 'expired', storage_key = NULL WHERE id = $1`, zip.ID)
	var list struct{ Items []orchapi.CommentView }
	tm.b.must(200, "GET", "/api/deliverables/"+id+"/comments", nil, &list)
	if len(list.Items) != 1 || len(list.Items[0].Attachments) != 2 || list.Items[0].Attachments[0].Expired || !list.Items[0].Attachments[1].Expired {
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
