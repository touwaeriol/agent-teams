package engine_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	protocol "github.com/touwaeriol/agent-teams/protocol"

	"github.com/touwaeriol/agent-teams/server/internal/blobgc"
	"github.com/touwaeriol/agent-teams/server/internal/blobstore"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/engine"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw"
	"github.com/touwaeriol/agent-teams/server/internal/orchapi"
	"github.com/touwaeriol/agent-teams/server/internal/sharedfiles"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// putParts PUTs body to the presigned part URLs of an upload, part_size bytes each, and returns
// the parts for complete.
func (b *browser) putParts(pid string, up orchapi.UploadSession, body []byte) []blobstore.Part {
	b.w.t.Helper()
	return b.putPartsAt("/api/projects/"+pid+"/uploads", up, body)
}

// putPartsAt is putParts for the uploads under base (/api/uploads: without a project).
func (b *browser) putPartsAt(base string, up orchapi.UploadSession, body []byte) []blobstore.Part {
	t := b.w.t
	t.Helper()
	nums := []int32{}
	for i := int32(1); i <= up.Parts; i++ {
		nums = append(nums, i)
	}
	var urls orchapi.UploadParts
	b.must(200, "POST", base+"/"+up.UploadID+"/parts", map[string]any{"numbers": nums}, &urls)
	parts := []blobstore.Part{}
	for _, u := range urls.URLs {
		off := int64(u.Number-1) * up.PartSize
		end := min(off+up.PartSize, int64(len(body)))
		req, _ := http.NewRequest("PUT", u.URL, bytes.NewReader(body[off:end]))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || resp.Header.Get("ETag") == "" {
			t.Fatalf("part %d: %d etag %q", u.Number, resp.StatusCode, resp.Header.Get("ETag"))
		}
		parts = append(parts, blobstore.Part{Number: u.Number, ETag: resp.Header.Get("ETag")})
	}
	return parts
}

// upload runs a whole browser upload and returns the completion.
func (b *browser) upload(pid string, create map[string]any, body []byte) orchapi.UploadDone {
	b.w.t.Helper()
	create["size"] = len(body)
	var up orchapi.UploadSession
	b.must(201, "POST", "/api/projects/"+pid+"/uploads", create, &up)
	var done orchapi.UploadDone
	b.must(200, "POST", "/api/projects/"+pid+"/uploads/"+up.UploadID+"/complete", map[string]any{"parts": b.putParts(pid, up, body)}, &done)
	return done
}

// redirect GETs path without following a redirect and returns the response (body read).
func (b *browser) redirect(path string) (*http.Response, []byte) {
	b.w.t.Helper()
	c := *b.http
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, _ := http.NewRequest("GET", b.w.srv.URL+path, nil)
	req.Header.Set("X-Forwarded-For", b.ip)
	resp, err := c.Do(req)
	if err != nil {
		b.w.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp, raw
}

// fetch GETs a presigned URL with no credentials.
func fetchURL(t *testing.T, url string) (*http.Response, []byte) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp, raw
}

// nodeGet calls a node route with a node token.
func nodeCall(t *testing.T, w *world, method, path, token string, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		r = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, w.srv.URL+path, r)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

func hexSum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// TestPresignedAttachments: a browser-style multipart upload of an attachment of any type (two
// parts), the part number range, completion with the type sniffed, the 302 download (inline only
// for images and PDF, Chinese file names), node URLs (owner only), expiry (410 for page and node),
// size mismatch, incomplete parts, abort, other users and other projects.
func TestPresignedAttachments(t *testing.T) {
	tm := startTeam(t)
	w, b, pid := tm.w, tm.b, tm.projectID
	ctx := context.Background()
	count := func(q string, args ...any) int {
		t.Helper()
		var n int
		if err := store.QueryRow(ctx, w.db.Pool, q, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// Two parts: 16 MB and a few bytes.
	body := bytes.Repeat([]byte("0123456789abcdef"), (blobstore.PartSize+48)/16)
	var up orchapi.UploadSession
	b.must(201, "POST", "/api/projects/"+pid+"/uploads", map[string]any{"kind": "attachment", "name": "报告 v1.zip", "size": len(body)}, &up)
	if up.Parts != 2 || up.PartSize != blobstore.PartSize || !strings.HasPrefix(up.UploadID, "img_") || up.ID != up.UploadID {
		t.Fatalf("session %+v", up)
	}
	if code, raw := b.do("POST", "/api/projects/"+pid+"/uploads/"+up.UploadID+"/parts", map[string]any{"numbers": []int{3}}); code != 422 {
		t.Fatalf("part 3 of 2 = %d %s", code, raw)
	}
	// Not ready yet: a message cannot use it.
	if count(`SELECT count(*) FROM at_attachments WHERE id = $1 AND state = 'uploading'`, up.ID) != 1 {
		t.Fatal("row not uploading")
	}
	parts := b.putParts(pid, up, body)
	if code, raw := b.do("POST", "/api/projects/"+pid+"/uploads/"+up.UploadID+"/complete",
		map[string]any{"parts": []map[string]any{{"number": 1, "etag": `"bogus"`}}}); code != 409 || !strings.Contains(string(raw), "upload_incomplete") {
		t.Fatalf("complete with a wrong etag = %d %s", code, raw)
	}
	var done orchapi.UploadDone
	b.must(200, "POST", "/api/projects/"+pid+"/uploads/"+up.UploadID+"/complete", map[string]any{"parts": parts}, &done)
	att := done.Attachment
	if done.Kind != "attachment" || att == nil || att.Size != len(body) || att.ContentType != "application/zip" || att.Name != "报告 v1.zip" {
		t.Fatalf("done %+v %+v", done, att)
	}
	if count(`SELECT count(*) FROM at_attachments WHERE id = $1 AND state = 'ready' AND upload_id IS NULL`, att.ID) != 1 {
		t.Fatal("row not ready")
	}

	// Page download: 302 to the object, as an octet-stream attachment with the Chinese name.
	_, _ = store.Exec(ctx, w.db.Pool, `UPDATE at_attachments SET expires_at = now() + interval '1 minute' WHERE id = $1`, att.ID)
	resp, _ := b.redirect("/api/attachments/" + att.ID)
	if resp.StatusCode != 302 || resp.Header.Get("Cache-Control") != "private, max-age=240" {
		t.Fatalf("page download %d %v", resp.StatusCode, resp.Header)
	}
	if count(`SELECT count(*) FROM at_attachments WHERE id = $1 AND expires_at > now() + interval '23 hours'`, att.ID) != 1 {
		t.Fatal("download did not push the retention")
	}
	obj, raw := fetchURL(t, resp.Header.Get("Location"))
	if obj.StatusCode != 200 || !bytes.Equal(raw, body) || obj.Header.Get("Content-Type") != "application/octet-stream" ||
		!strings.HasPrefix(obj.Header.Get("Content-Disposition"), "attachment;") ||
		!strings.Contains(obj.Header.Get("Content-Disposition"), "filename*=UTF-8''%E6%8A%A5%E5%91%8A%20v1.zip") {
		t.Fatalf("object %d %d bytes %v", obj.StatusCode, len(raw), obj.Header)
	}

	// An image is shown inline, unless downloaded.
	png := b.upload(pid, map[string]any{"kind": "attachment", "name": "shot.png"}, onePixelPNG).Attachment
	if png.ContentType != "image/png" {
		t.Fatalf("png type %q", png.ContentType)
	}
	resp, _ = b.redirect("/api/attachments/" + png.ID)
	obj, raw = fetchURL(t, resp.Header.Get("Location"))
	if !bytes.Equal(raw, onePixelPNG) || obj.Header.Get("Content-Type") != "image/png" || !strings.HasPrefix(obj.Header.Get("Content-Disposition"), "inline;") {
		t.Fatalf("inline image %v", obj.Header)
	}
	resp, _ = b.redirect("/api/attachments/" + png.ID + "?download=1")
	if obj, _ = fetchURL(t, resp.Header.Get("Location")); !strings.HasPrefix(obj.Header.Get("Content-Disposition"), "attachment;") {
		t.Fatalf("download=1 %v", obj.Header)
	}
	// HTML is never inline.
	html := b.upload(pid, map[string]any{"kind": "attachment", "name": "x.html"}, []byte("<script>alert(1)</script>")).Attachment
	resp, _ = b.redirect("/api/attachments/" + html.ID)
	if obj, _ = fetchURL(t, resp.Header.Get("Location")); obj.Header.Get("Content-Type") != "application/octet-stream" ||
		!strings.HasPrefix(obj.Header.Get("Content-Disposition"), "attachment;") {
		t.Fatalf("html %v", obj.Header)
	}

	// Node URL: the owner's node gets a presigned GET, others get 404, no token 401.
	code, raw := nodeCall(t, w, "GET", "/nodes/attachments/"+att.ID+"/url", tm.token, nil)
	var nu orchapi.NodeAttachmentURL
	if code != 200 || json.Unmarshal(raw, &nu) != nil || nu.URL == nil || nu.Size != len(body) || nu.Name != "报告 v1.zip" {
		t.Fatalf("node url %d %s", code, raw)
	}
	if _, got := fetchURL(t, *nu.URL); !bytes.Equal(got, body) {
		t.Fatal("node download differs")
	}
	intruder := w.signUp("intruder@example.com")
	var other nodeCreated
	intruder.must(201, "POST", "/api/nodes", map[string]string{"name": "theirs"}, &other)
	if code, _ := nodeCall(t, w, "GET", "/nodes/attachments/"+att.ID+"/url", other.Token, nil); code != 404 {
		t.Fatalf("other user's node = %d", code)
	}
	if code, _ := nodeCall(t, w, "GET", "/nodes/attachments/"+att.ID+"/url", "", nil); code != 401 {
		t.Fatalf("no token = %d", code)
	}
	// Old attachments (bytes in the database) have no URL: the daemon uses the old route.
	if _, err := store.Exec(ctx, w.db.Pool, `INSERT INTO at_attachments (id, project_id, user_id, name, content_type, size, sha256, data)
		SELECT 'img_legacy', project_id, user_id, 'l.png', 'image/png', 3, 'x', '\x010203' FROM at_attachments WHERE id = $1`, att.ID); err != nil {
		t.Fatal(err)
	}
	code, raw = nodeCall(t, w, "GET", "/nodes/attachments/img_legacy/url", tm.token, nil)
	if code != 200 || !strings.Contains(string(raw), `"url":null`) {
		t.Fatalf("legacy node url %d %s", code, raw)
	}
	if resp, raw := b.redirect("/api/attachments/img_legacy"); resp.StatusCode != 200 || len(raw) != 3 {
		t.Fatalf("legacy page download %d %d", resp.StatusCode, len(raw))
	}

	// Expired: the page and the node get 410 with the reason.
	_, _ = store.Exec(ctx, w.db.Pool, `UPDATE at_attachments SET state = 'expired', storage_key = NULL WHERE id = $1`, att.ID)
	code, raw = nodeCall(t, w, "GET", "/nodes/attachments/"+att.ID+"/url", tm.token, nil)
	if code != 410 || !strings.Contains(string(raw), "附件已过期") || !strings.Contains(string(raw), "attachment_expired") {
		t.Fatalf("expired node url %d %s", code, raw)
	}
	if code, raw := b.do("GET", "/api/attachments/"+att.ID, nil); code != 410 || !strings.Contains(string(raw), "attachment_expired") {
		t.Fatalf("expired page download %d %s", code, raw)
	}

	// Size mismatch: declared 100 bytes, sent 50: dropped.
	var short orchapi.UploadSession
	b.must(201, "POST", "/api/projects/"+pid+"/uploads", map[string]any{"kind": "attachment", "name": "s.bin", "size": 100}, &short)
	parts = b.putParts(pid, short, bytes.Repeat([]byte("x"), 50))
	if code, raw := b.do("POST", "/api/projects/"+pid+"/uploads/"+short.UploadID+"/complete", map[string]any{"parts": parts}); code != 422 ||
		!strings.Contains(string(raw), "size_mismatch") {
		t.Fatalf("size mismatch %d %s", code, raw)
	}
	if count(`SELECT count(*) FROM at_attachments WHERE id = $1`, short.UploadID) != 0 {
		t.Fatal("mismatched upload kept")
	}
	if _, err := w.e.Core.Blobs.Head(ctx, blobstore.AttachmentKey(pid, short.UploadID)); !errors.Is(err, blobstore.ErrNotFound) {
		t.Fatalf("mismatched object kept: %v", err)
	}

	// Limits and abort.
	for _, kind := range []string{"attachment", "shared"} {
		if code, raw := b.do("POST", "/api/projects/"+pid+"/uploads", map[string]any{"kind": kind, "name": "big", "size": 200<<20 + 1}); code != 413 ||
			!strings.Contains(string(raw), "file_too_large") || !strings.Contains(string(raw), "200 MB") {
			t.Fatalf("%s over 200 MB = %d %s", kind, code, raw)
		}
	}
	var ab orchapi.UploadSession
	b.must(201, "POST", "/api/projects/"+pid+"/uploads", map[string]any{"kind": "attachment", "name": "a.bin", "size": 10}, &ab)
	b.must(204, "DELETE", "/api/projects/"+pid+"/uploads/"+ab.UploadID, nil, nil)
	if code, _ := b.do("POST", "/api/projects/"+pid+"/uploads/"+ab.UploadID+"/parts", map[string]any{"numbers": []int{1}}); code != 404 {
		t.Fatalf("parts after abort = %d", code)
	}

	// Other users and other projects never reach an upload.
	var open orchapi.UploadSession
	b.must(201, "POST", "/api/projects/"+pid+"/uploads", map[string]any{"kind": "attachment", "name": "o.bin", "size": 10}, &open)
	var p2 struct{ ID string }
	b.must(201, "POST", "/api/projects", map[string]any{"name": "second"}, &p2)
	for _, c := range []struct {
		who  *browser
		path string
	}{{intruder, "/api/projects/" + pid + "/uploads/" + open.UploadID + "/parts"}, {b, "/api/projects/" + p2.ID + "/uploads/" + open.UploadID + "/parts"}} {
		if code, raw := c.who.do("POST", c.path, map[string]any{"numbers": []int{1}}); code != 404 {
			t.Fatalf("%s = %d %s", c.path, code, raw)
		}
	}
	if code, _ := intruder.do("POST", "/api/projects/"+pid+"/uploads", map[string]any{"kind": "attachment", "name": "x", "size": 1}); code != 404 {
		t.Fatalf("intruder create = %d", code)
	}
	if code, _ := intruder.do("GET", "/api/attachments/"+png.ID, nil); code != 404 {
		t.Fatalf("intruder download = %d", code)
	}
}

// TestPresignedSharedFiles: a shared file uploaded in parts appears at its path only when it
// completes, overwrite=false refuses an existing path, uploads in flight count towards the 5 GB
// quota (Chinese message), the download is a 302; a node's presigned PUT is checked against its
// declared size and sha256 (a mismatch drops it).
func TestPresignedSharedFiles(t *testing.T) {
	tm := startTeam(t)
	w, b, pid := tm.w, tm.b, tm.projectID
	ctx := context.Background()
	body := bytes.Repeat([]byte("shared!"), 3000)
	var up orchapi.UploadSession
	b.must(201, "POST", "/api/projects/"+pid+"/uploads", map[string]any{"kind": "shared", "name": "big.bin", "path": "data/big.bin", "size": len(body)}, &up)
	if !strings.HasPrefix(up.UploadID, "up_") || !strings.HasPrefix(up.ID, "sf_") || up.Parts != 1 {
		t.Fatalf("session %+v", up)
	}
	var page orchapi.SharedFileList
	b.must(200, "GET", "/api/projects/"+pid+"/shared-files", nil, &page)
	if len(page.Files) != 0 || page.Limits.ProjectBytes != 5<<30 || page.Limits.FileBytes != 200<<20 {
		t.Fatalf("listed before completion or old limits: %+v", page)
	}
	var done orchapi.UploadDone
	b.must(200, "POST", "/api/projects/"+pid+"/uploads/"+up.UploadID+"/complete", map[string]any{"parts": b.putParts(pid, up, body)}, &done)
	if done.SharedFile == nil || done.SharedFile.Path != "data/big.bin" || done.SharedFile.Size != int64(len(body)) || done.SharedFile.Overwritten {
		t.Fatalf("done %+v", done.SharedFile)
	}
	resp, _ := b.redirect("/api/projects/" + pid + "/shared-files/download?path=data/big.bin")
	if resp.StatusCode != 302 {
		t.Fatalf("shared download %d", resp.StatusCode)
	}
	if obj, raw := fetchURL(t, resp.Header.Get("Location")); !bytes.Equal(raw, body) || !strings.HasPrefix(obj.Header.Get("Content-Disposition"), "attachment;") {
		t.Fatalf("shared object %v", obj.Header)
	}
	if code, raw := b.do("POST", "/api/projects/"+pid+"/uploads", map[string]any{"kind": "shared", "name": "big.bin", "path": "data/big.bin",
		"size": 3, "overwrite": false}); code != 409 || !strings.Contains(string(raw), "file_exists") {
		t.Fatalf("overwrite=false = %d %s", code, raw)
	}
	// overwrite=false holds at completion too: the path got a file while this upload was running.
	var late orchapi.UploadSession
	b.must(201, "POST", "/api/projects/"+pid+"/uploads", map[string]any{"kind": "shared", "name": "late.txt", "size": 4, "overwrite": false}, &late)
	latePart := b.putParts(pid, late, []byte("late"))
	b.upload(pid, map[string]any{"kind": "shared", "name": "late.txt"}, []byte("first"))
	if code, raw := b.do("POST", "/api/projects/"+pid+"/uploads/"+late.UploadID+"/complete", map[string]any{"parts": latePart}); code != 409 ||
		!strings.Contains(string(raw), "file_exists") {
		t.Fatalf("overwrite=false at completion = %d %s", code, raw)
	}
	if _, err := sharedfiles.GetUpload(ctx, w.db.Pool, pid, late.UploadID); !errors.Is(err, sharedfiles.ErrUploadNotFound) {
		t.Fatalf("refused upload kept: %v", err)
	}
	if _, got, _ := sharedfiles.Load(ctx, w.db.Pool, w.e.Core.Blobs, pid, "late.txt"); string(got) != "first" {
		t.Fatalf("late.txt = %q", got)
	}

	// Overwrite (the default): the old object is queued for deletion.
	again := b.upload(pid, map[string]any{"kind": "shared", "name": "big.bin", "path": "data/big.bin"}, []byte("v2")).SharedFile
	if again == nil || !again.Overwritten || again.Size != 2 {
		t.Fatalf("overwrite %+v", again)
	}

	// Quota: an upload in flight of almost 5 GB leaves no room.
	if _, err := store.Exec(ctx, w.db.Pool, `INSERT INTO at_shared_uploads (id, project_id, path, object_id, storage_key, size, content_type, uploaded_by)
		VALUES ('up_huge', $1, 'huge.bin', 'sf_huge', 'k', $2, 'application/octet-stream', 'user')`, pid, int64(5<<30)-10); err != nil {
		t.Fatal(err)
	}
	if code, raw := b.do("POST", "/api/projects/"+pid+"/uploads", map[string]any{"kind": "shared", "name": "more.bin", "size": 100}); code != 409 ||
		!strings.Contains(string(raw), "quota_exceeded") || !strings.Contains(string(raw), "5.0 GB") {
		t.Fatalf("quota = %d %s", code, raw)
	}
	// Attachments do not count.
	b.must(201, "POST", "/api/projects/"+pid+"/uploads", map[string]any{"kind": "attachment", "name": "a.bin", "size": 100}, nil)
	_, _ = store.Exec(ctx, w.db.Pool, `DELETE FROM at_shared_uploads WHERE id = 'up_huge'`)

	// A node's presigned PUT for write_shared_file.
	out := []byte("node output")
	ask := map[string]any{"session_id": tm.dev.session, "path": "out/r.txt", "size": len(out), "sha256": hexSum(out)}
	if code, _ := nodeCall(t, w, "POST", "/nodes/uploads", "", ask); code != 401 {
		t.Fatalf("no token = %d", code)
	}
	intruder := w.signUp("intruder@example.com")
	var other nodeCreated
	intruder.must(201, "POST", "/api/nodes", map[string]string{"name": "theirs"}, &other)
	if code, _ := nodeCall(t, w, "POST", "/nodes/uploads", other.Token, ask); code != 409 {
		t.Fatalf("session of another node = %d", code)
	}
	put := func(ask map[string]any, data []byte) orchapi.NodeUpload {
		t.Helper()
		code, raw := nodeCall(t, w, "POST", "/nodes/uploads", tm.token, ask)
		var nu orchapi.NodeUpload
		if code != 201 || json.Unmarshal(raw, &nu) != nil || nu.URL == "" {
			t.Fatalf("node upload %d %s", code, raw)
		}
		req, _ := http.NewRequest("PUT", nu.URL, bytes.NewReader(data))
		for k, v := range nu.Headers {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("put %v %v", resp, err)
		}
		resp.Body.Close()
		return nu
	}
	nu := put(ask, out)
	var f sharedfiles.SharedFile
	err := w.e.Core.InTx(ctx, func(tx *core.Tx) error {
		var err error
		f, _, err = sharedfiles.FinalizeUpload(ctx, tx, w.e.Core.Blobs, pid, nu.UploadID, sharedfiles.FinalizeOptions{})
		return err
	})
	if err != nil || f.Path != "out/r.txt" || f.SHA256 != hexSum(out) || f.UploadedBy != "dev-1" {
		t.Fatalf("finalize %+v %v", f, err)
	}
	// Replaying the presigned PUT after registration changes nothing: the file lives at another key.
	replay, _ := http.NewRequest("PUT", nu.URL, bytes.NewReader([]byte("NODE OUTPUT")))
	for k, v := range nu.Headers {
		replay.Header.Set(k, v)
	}
	if r, err := http.DefaultClient.Do(replay); err == nil {
		r.Body.Close()
	}
	if _, got, err := sharedfiles.Load(ctx, w.db.Pool, w.e.Core.Blobs, pid, "out/r.txt"); err != nil || !bytes.Equal(got, out) {
		t.Fatalf("replayed PUT changed the file: %q %v", got, err)
	}
	if f.SHA256 != hexSum(out) || strings.Contains(nu.URL, f.Path) {
		t.Fatalf("file %+v", f)
	}
	// The uploaded key goes once its URL has expired.
	gc := &blobgc.Worker{DB: w.db.Pool, Blobs: w.e.Core.Blobs, Now: func() time.Time { return time.Now().Add(blobgc.ReserveDelay + time.Minute) }}
	if _, err := gc.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	var upKey string
	_ = store.QueryRow(ctx, w.db.Pool, `SELECT storage_key FROM at_shared_files WHERE project_id = $1 AND path = 'out/r.txt'`, pid).Scan(&upKey)
	if _, got, err := sharedfiles.Load(ctx, w.db.Pool, w.e.Core.Blobs, pid, "out/r.txt"); err != nil || !bytes.Equal(got, out) || upKey == "" {
		t.Fatalf("registered object deleted by gc: %v", err)
	}

	// Changed after hashing: the declared sha256 does not match, the upload is dropped.
	bad := put(map[string]any{"session_id": tm.dev.session, "path": "out/r.txt", "size": len(out), "sha256": hexSum([]byte("other bytes"))}, out)
	err = w.e.Core.InTx(ctx, func(tx *core.Tx) error {
		_, _, err := sharedfiles.FinalizeUpload(ctx, tx, w.e.Core.Blobs, pid, bad.UploadID, sharedfiles.FinalizeOptions{})
		return err
	})
	if !errors.Is(err, sharedfiles.ErrChecksum) {
		t.Fatalf("checksum mismatch: %v", err)
	}
	if _, err := sharedfiles.GetUpload(ctx, w.db.Pool, pid, bad.UploadID); !errors.Is(err, sharedfiles.ErrUploadNotFound) {
		t.Fatalf("bad upload kept: %v", err)
	}
	var data []byte
	if _, data, err = sharedfiles.Load(ctx, w.db.Pool, w.e.Core.Blobs, pid, "out/r.txt"); err != nil || !bytes.Equal(data, out) {
		t.Fatalf("file replaced by the bad upload: %q %v", data, err)
	}
	// Wrong size: dropped as well.
	short := put(map[string]any{"session_id": tm.dev.session, "path": "out/s.txt", "size": 4, "sha256": hexSum([]byte("abcd"))}, []byte("abcd"))
	_, _ = store.Exec(ctx, w.db.Pool, `UPDATE at_shared_uploads SET size = 5 WHERE id = $1`, short.UploadID)
	_, _, err = sharedfiles.FinalizeUpload(ctx, w.db.Pool, w.e.Core.Blobs, pid, short.UploadID, sharedfiles.FinalizeOptions{})
	if !errors.Is(err, sharedfiles.ErrSizeMismatch) {
		t.Fatalf("size mismatch: %v", err)
	}
}

// TestUploadsWithoutStorage: with no object store configured the upload routes answer 503
// storage_unavailable.
func TestUploadsWithoutStorage(t *testing.T) {
	w := newWorldWith(t, nil, func(e *engine.Engine) { e.Core.Blobs = blobstore.None{} })
	b := w.signUp("nostore@example.com")
	var p struct{ ID string }
	b.must(201, "POST", "/api/projects", map[string]any{"name": "p"}, &p)
	for _, kind := range []string{"attachment", "shared"} {
		if code, raw := b.do("POST", "/api/projects/"+p.ID+"/uploads", map[string]any{"kind": kind, "name": "a.bin", "size": 1}); code != 503 ||
			!strings.Contains(string(raw), "storage_unavailable") {
			t.Fatalf("%s without storage = %d %s", kind, code, raw)
		}
	}
}

// TestWriteSharedFileS3: a daemon with presigned uploads asks POST /nodes/uploads, PUTs the file
// and calls write_shared_file with upload storage s3: the file is registered (sha256 checked);
// a PUT of other bytes than declared, another employee's upload id and an unknown one are refused.
func TestWriteSharedFileS3(t *testing.T) {
	st := startSFTeam(t)
	w := st.w
	ask := func(path string, declared []byte, sent []byte) string {
		t.Helper()
		// The daemon's body (node/src/toolhost/transfer.ts UploadRequest): the target path is name.
		code, raw := nodeCall(t, w, "POST", "/nodes/uploads", st.tokA, map[string]any{"session_id": st.dev.session, "call_id": "tc_x",
			"name": path, "size": len(declared), "sha256": hexSum(declared), "content_type": "text/csv"})
		var nu orchapi.NodeUpload
		if code != 201 || json.Unmarshal(raw, &nu) != nil {
			t.Fatalf("node upload %d %s", code, raw)
		}
		req, _ := http.NewRequest("PUT", nu.URL, bytes.NewReader(sent))
		for k, v := range nu.Headers {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("put %v %v", resp, err)
		}
		resp.Body.Close()
		return nu.UploadID
	}
	call := func(a *fakeAgent, uploadID, path string, body []byte) protocol.ToolResult {
		t.Helper()
		callID := fmt.Sprintf("tc_s3_%d", time.Now().UnixNano())
		gen := protocol.OpenGen(a.gen)
		raw, _ := json.Marshal(map[string]any{"file": path})
		a.send(protocol.TypeToolCall, protocol.ToolCall{CallID: protocol.CallID(callID), SessionID: protocol.SessionID(a.session), OpenGen: &gen,
			Tool: "write_shared_file", Args: raw, Upload: &protocol.ToolUpload{UploadID: protocol.UploadID(uploadID), Storage: protocol.ToolUploadStorageS3,
				Files: []protocol.UploadFile{{Path: path, Size: len(body), SHA256: hexSum(body), ContentType: "x/ignored"}}}})
		return a.result(callID)
	}
	body := []byte("id,value\n1,2\n")
	if code, raw := nodeCall(t, w, "POST", "/nodes/uploads", st.tokA, map[string]any{"session_id": st.dev.session, "size": 1,
		"sha256": hexSum([]byte("x"))}); code != 422 {
		t.Fatalf("no target path = %d %s", code, raw)
	}
	res := call(st.dev, ask("out/s3.csv", body, body), "out/s3.csv", body)
	if !res.Ok {
		t.Fatalf("s3 write refused: %s", text(res))
	}
	var wrote protocol.WriteSharedFileResult
	_ = json.Unmarshal(*res.Structured, &wrote)
	if wrote.Path != "out/s3.csv" || wrote.Size != len(body) || wrote.SHA256 != hexSum(body) {
		t.Fatalf("write result %+v", wrote)
	}
	f, data, err := sharedfiles.Load(w.ctx, w.db.Pool, w.e.Core.Blobs, st.projectID, "out/s3.csv")
	if err != nil || !bytes.Equal(data, body) || f.UploadedBy != "dev-1" {
		t.Fatalf("stored %+v %q %v", f, data, err)
	}

	// The file changed between hashing and the PUT: refused, the old file stays.
	changed := []byte("id,value\n9,9\n")
	changedID := ask("out/s3.csv", changed, []byte("id,value\n0,0\n"))
	changedUp, err := sharedfiles.GetUpload(w.ctx, w.db.Pool, st.projectID, changedID)
	if err != nil {
		t.Fatal(err)
	}
	changedKey := changedUp.StorageKey
	if res := call(st.dev, changedID, "out/s3.csv", changed); res.Ok ||
		!strings.Contains(text(res), "sha256") {
		t.Fatalf("checksum mismatch accepted: %s", text(res))
	}
	// Wrong size.
	if res := call(st.dev, ask("out/short.csv", body, body[:5]), "out/short.csv", body); res.Ok || !strings.Contains(text(res), "大小") {
		t.Fatalf("size mismatch accepted: %s", text(res))
	}
	if _, data, _ = sharedfiles.Load(w.ctx, w.db.Pool, w.e.Core.Blobs, st.projectID, "out/s3.csv"); !bytes.Equal(data, body) {
		t.Fatalf("old file replaced: %q", data)
	}
	// Another employee cannot finish dev-1's upload; an unknown id is refused.
	other := ask("out/other.csv", body, body)
	if res := call(st.lead, other, "out/other.csv", body); res.Ok {
		t.Fatalf("lead finished dev-1's upload: %s", text(res))
	}
	if _, err := sharedfiles.GetUpload(w.ctx, w.db.Pool, st.projectID, other); err != nil {
		t.Fatalf("another employee's refusal dropped dev-1's upload: %v", err)
	}
	// Refused for the quota: the upload is cancelled at once (it no longer holds the quota).
	big := ask("out/big.csv", body, body)
	up, err := sharedfiles.GetUpload(w.ctx, w.db.Pool, st.projectID, big)
	if err != nil {
		t.Fatal(err)
	}
	bigKey := up.StorageKey
	if _, err := store.Exec(w.ctx, w.db.Pool, `INSERT INTO at_shared_uploads (id, project_id, path, object_id, storage_key, size, content_type, uploaded_by)
		VALUES ('up_huge', $1, 'huge.bin', 'sf_huge', 'k', $2, 'application/octet-stream', 'user')`, st.projectID, int64(5<<30)-10); err != nil {
		t.Fatal(err)
	}
	if res := call(st.dev, big, "out/big.csv", body); res.Ok || !strings.Contains(text(res), "5.0 GB") {
		t.Fatalf("over quota accepted: %s", text(res))
	}
	if _, err := sharedfiles.GetUpload(w.ctx, w.db.Pool, st.projectID, big); !errors.Is(err, sharedfiles.ErrUploadNotFound) {
		t.Fatalf("refused upload still held: %v", err)
	}
	// Its object went at once, and its key is queued once more for after the PUT URL expired
	// (a replayed PUT would otherwise leave an object nothing points at).
	expectRedelete(t, w, bigKey)
	// The same for an upload dropped by its checks (the checksum one above).
	expectRedelete(t, w, changedKey)
	// A session of another node (or closed): 409, never 404 (the daemon reads 404 as an old console).
	if code, raw := nodeCall(t, w, "POST", "/nodes/uploads", st.tokB, map[string]any{"session_id": st.dev.session, "name": "a.txt",
		"size": 1, "sha256": hexSum([]byte("x"))}); code != 409 || !strings.Contains(string(raw), "session_not_on_node") {
		t.Fatalf("session on another node = %d %s", code, raw)
	}
	if res := call(st.dev, "up_00000000000000000000000000", "out/x.csv", body); res.Ok {
		t.Fatalf("unknown upload accepted: %s", text(res))
	}
}

// expectRedelete: key's object is gone and the key is queued for deletion again after the node's
// presigned PUT has expired (blobgc.RedeleteLater).
func expectRedelete(t *testing.T, w *world, key string) {
	t.Helper()
	if _, err := w.e.Core.Blobs.Head(w.ctx, key); err == nil {
		t.Fatalf("object %s still there", key)
	}
	var later bool
	if err := store.QueryRow(w.ctx, w.db.Pool, `SELECT next_at > now() + make_interval(secs => $2) FROM at_blob_deletions WHERE key = $1`,
		key, sharedfiles.UploadTTL.Seconds()).Scan(&later); err != nil || !later {
		t.Fatalf("key %s not queued after the URL expires: %v %v", key, later, err)
	}
}

// failingPresign is a store whose PresignPut fails (storage misconfigured, say).
type failingPresign struct{ blobstore.Store }

func (failingPresign) PresignPut(context.Context, string, time.Duration, int64, string) (string, map[string]string, error) {
	return "", nil, errors.New("presign failed")
}

// TestNodeUploadPresignFails: POST /nodes/uploads that cannot sign the PUT drops the upload it
// began, so it does not hold the project's quota.
func TestNodeUploadPresignFails(t *testing.T) {
	st := startSFTeam(t)
	w := st.w
	real := w.e.Core.Blobs
	w.e.Core.Blobs = failingPresign{real}
	t.Cleanup(func() { w.e.Core.Blobs = real })
	code, raw := nodeCall(t, w, "POST", "/nodes/uploads", st.tokA, map[string]any{"session_id": st.dev.session, "name": "out/a.csv",
		"size": 3, "sha256": hexSum([]byte("abc"))})
	if code < 500 {
		t.Fatalf("presign failure = %d %s", code, raw)
	}
	var n int
	if err := store.QueryRow(w.ctx, w.db.Pool, `SELECT count(*) FROM at_shared_uploads WHERE project_id = $1`, st.projectID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("uploads left: %d %v", n, err)
	}
}

// blockingOpen is a store whose Open waits until release is closed (a slow read of a big object).
type blockingOpen struct {
	blobstore.Store
	release chan struct{}
}

func (b blockingOpen) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	<-b.release
	return b.Store.Open(ctx, key)
}

// TestWriteSharedFileS3FinishesAfterTheCall: finishing a presigned write_shared_file (copy and
// sha256 of up to 200 MB) happens after the call's transaction, so the node's other messages are
// handled meanwhile (a link blocked for long misses its pings and is closed).
func TestWriteSharedFileS3FinishesAfterTheCall(t *testing.T) {
	st := startSFTeam(t)
	w := st.w
	body := []byte("slow,file\n1,2\n")
	code, raw := nodeCall(t, w, "POST", "/nodes/uploads", st.tokA, map[string]any{"session_id": st.dev.session, "name": "out/slow.csv",
		"size": len(body), "sha256": hexSum(body)})
	var nu orchapi.NodeUpload
	if code != 201 || json.Unmarshal(raw, &nu) != nil {
		t.Fatalf("node upload %d %s", code, raw)
	}
	req, _ := http.NewRequest("PUT", nu.URL, bytes.NewReader(body))
	for k, v := range nu.Headers {
		req.Header.Set(k, v)
	}
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != 200 {
		t.Fatalf("put %v %v", resp, err)
	}
	real := w.e.Core.Blobs
	slow := blockingOpen{Store: real, release: make(chan struct{})}
	w.e.Core.Blobs = slow
	t.Cleanup(func() { w.e.Core.Blobs = real })

	gen := protocol.OpenGen(st.dev.gen)
	slowID := fmt.Sprintf("tc_slow_%d", time.Now().UnixNano())
	args, _ := json.Marshal(map[string]any{"file": "out/slow.csv"})
	st.dev.send(protocol.TypeToolCall, protocol.ToolCall{CallID: protocol.CallID(slowID), SessionID: protocol.SessionID(st.dev.session), OpenGen: &gen,
		Tool: "write_shared_file", Args: args, Upload: &protocol.ToolUpload{UploadID: protocol.UploadID(nu.UploadID), Storage: protocol.ToolUploadStorageS3,
			Files: []protocol.UploadFile{{Path: "out/slow.csv", Size: len(body), SHA256: hexSum(body), ContentType: "text/csv"}}}})
	// While the object is being read, the same node's next call is answered.
	if res := st.dev.call("list_shared_files", map[string]any{}); !res.Ok {
		t.Fatalf("list_shared_files while finishing: %s", text(res))
	}
	close(slow.release)
	if res := st.dev.result(slowID); !res.Ok || !strings.Contains(text(res), "out/slow.csv") {
		t.Fatalf("slow write: %s", text(res))
	}
	if _, data, err := sharedfiles.Load(w.ctx, w.db.Pool, real, st.projectID, "out/slow.csv"); err != nil || !bytes.Equal(data, body) {
		t.Fatalf("stored %q %v", data, err)
	}
}

// TestNodeRoutesAuthStatus: the JSON node routes of #58 answer a failed node token like every
// daemon route (nodegw.AuthStatus): 401 for a wrong token, 429 with Retry-After once the client
// address used up its rejections (even with a right token).
func TestNodeRoutesAuthStatus(t *testing.T) {
	w := newWorld(t, nil)
	b := w.signUp("ops@example.com")
	var node nodeCreated
	b.must(201, "POST", "/api/nodes", map[string]string{"name": "n1"}, &node)
	call := func(method, path, token, xff string) (*http.Response, string) {
		t.Helper()
		req, _ := http.NewRequest(method, w.srv.URL+path, strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Forwarded-For", xff)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var body struct{ Code string }
		_ = json.NewDecoder(resp.Body).Decode(&body)
		return resp, body.Code
	}
	routes := [][2]string{{"GET", "/nodes/attachments/img_x/url"}, {"POST", "/nodes/uploads"}}
	for _, r := range routes {
		if resp, code := call(r[0], r[1], "wrong", "203.0.113.20"); resp.StatusCode != http.StatusUnauthorized || code != "unauthorized" {
			t.Fatalf("%s wrong token: %d %q", r[1], resp.StatusCode, code)
		}
	}
	for i := 0; i < nodegw.DefaultAuthFailLimit; i++ {
		call("GET", routes[0][1], "wrong", "203.0.113.21")
	}
	for _, r := range routes {
		resp, code := call(r[0], r[1], node.Token, "203.0.113.21")
		if resp.StatusCode != http.StatusTooManyRequests || code != "too_many_requests" || resp.Header.Get("Retry-After") == "" {
			t.Fatalf("%s throttled: %d %q Retry-After %q", r[1], resp.StatusCode, code, resp.Header.Get("Retry-After"))
		}
	}
}
