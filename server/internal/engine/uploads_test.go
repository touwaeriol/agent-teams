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

	"github.com/touwaeriol/agent-teams/server/internal/blobstore"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/engine"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw"
	"github.com/touwaeriol/agent-teams/server/internal/orchapi"
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
	for _, kind := range []string{"attachment"} {
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

// TestUploadsWithoutStorage: with no object store configured the upload routes answer 503
// storage_unavailable.
func TestUploadsWithoutStorage(t *testing.T) {
	w := newWorldWith(t, nil, func(e *engine.Engine) { e.Core.Blobs = blobstore.None{} })
	b := w.signUp("nostore@example.com")
	var p struct{ ID string }
	b.must(201, "POST", "/api/projects", map[string]any{"name": "p"}, &p)
	for _, kind := range []string{"attachment"} {
		if code, raw := b.do("POST", "/api/projects/"+p.ID+"/uploads", map[string]any{"kind": kind, "name": "a.bin", "size": 1}); code != 503 ||
			!strings.Contains(string(raw), "storage_unavailable") {
			t.Fatalf("%s without storage = %d %s", kind, code, raw)
		}
	}
}

// TestUploadsInFlightLimit: a user's uploads in flight are capped; the next begin gets 429 too_many_uploads (#62 F2).
func TestUploadsInFlightLimit(t *testing.T) {
	w := newWorldWith(t, nil, nil)
	b := w.signUp("busy@example.com")
	var p struct{ ID string }
	b.must(201, "POST", "/api/projects", map[string]any{"name": "p"}, &p)
	for i := 0; i < core.MaxUploadsInFlight; i++ {
		b.must(201, "POST", "/api/projects/"+p.ID+"/uploads", map[string]any{"kind": "attachment", "name": fmt.Sprintf("f%d.bin", i), "size": 10}, nil)
	}
	for _, kind := range []string{"attachment"} {
		if code, raw := b.do("POST", "/api/projects/"+p.ID+"/uploads", map[string]any{"kind": kind, "name": "more.bin", "size": 10}); code != 429 ||
			!strings.Contains(string(raw), "too_many_uploads") {
			t.Fatalf("%s over the limit = %d %s", kind, code, raw)
		}
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
	routes := [][2]string{{"GET", "/nodes/attachments/img_x/url"}}
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
