package engine_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/touwaeriol/agent-teams/server/internal/httpapi"
	"github.com/touwaeriol/agent-teams/server/internal/orchapi"
	"github.com/touwaeriol/agent-teams/server/internal/store"
	"github.com/touwaeriol/agent-teams/server/internal/userfs"
)

type formFile struct{ name, body string }

// multipart posts a multipart form (file parts named "file", then the other fields).
func (b *browser) multipart(path string, fields map[string][]string, files []formFile) (int, []byte) {
	b.w.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, vs := range fields {
		for _, v := range vs {
			_ = mw.WriteField(k, v)
		}
	}
	for _, f := range files {
		part, _ := mw.CreateFormFile("file", f.name)
		_, _ = part.Write([]byte(f.body))
	}
	_ = mw.Close()
	req, _ := http.NewRequest("POST", b.w.srv.URL+path, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-Forwarded-For", b.ip)
	req.Header.Set(httpapi.CSRFHeader, b.csrf())
	resp, err := b.http.Do(req)
	if err != nil {
		b.w.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

// get fetches a page path and returns the whole response.
func (b *browser) get(path string) (*http.Response, []byte) {
	b.w.t.Helper()
	req, _ := http.NewRequest("GET", b.w.srv.URL+path, nil)
	req.Header.Set("X-Forwarded-For", b.ip)
	resp, err := b.http.Do(req)
	if err != nil {
		b.w.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp, raw
}

// TestProjectFiles: the project's files are its folder in the owner's shared folder. Before the
// folder is on the page sees why and writes are 409 folder_unavailable; then uploads (into a dir,
// with per-file paths), listing, downloads (as attachments, active types as bytes), folders and
// deletes work on the volume; bad paths are 422, missing ones 404, other users 404.
func TestProjectFiles(t *testing.T) {
	tm := startTeam(t)
	w, b, pid := tm.w, tm.b, tm.projectID
	base := "/api/projects/" + pid + "/files"

	var page orchapi.ProjectFiles
	b.must(200, "GET", base, nil, &page)
	if page.Available || page.Reason == "" || len(page.Entries) != 0 {
		t.Fatalf("before the folder %+v", page)
	}
	if code, raw := b.multipart(base, nil, []formFile{{"a.txt", "hi"}}); code != 409 || !strings.Contains(string(raw), "folder_unavailable") {
		t.Fatalf("upload without a folder = %d %s", code, raw)
	}

	fakes := useFolder(t, w)
	var fs orchapi.FsView
	b.must(200, "POST", "/api/storage/fs", nil, &fs)
	if fs.Usage == nil || fs.Usage.UsedBytes != 12288 || fs.Usage.Inodes != 4 {
		t.Fatalf("usage of the ready folder %+v", fs.Usage)
	}
	b.must(200, "GET", base, nil, &page)
	if !page.Available || len(page.Entries) != 0 {
		t.Fatalf("empty project %+v", page)
	}

	var up orchapi.FilesUploaded
	code, raw := b.multipart(base, map[string][]string{"dir": {"docs"}, "path": {"纪要.md", "web/page.html"}},
		[]formFile{{"x.md", "# 纪要"}, {"y.html", "<script>alert(1)</script>"}})
	if code != 201 {
		t.Fatalf("upload = %d %s", code, raw)
	}
	decode(t, raw, &up)
	if len(up.Files) != 2 || up.Files[0].Path != "docs/纪要.md" || up.Files[1].Path != "docs/web/page.html" || up.Files[0].Size != int64(len("# 纪要")) {
		t.Fatalf("uploaded %+v", up)
	}
	onDisk, err := os.ReadFile(filepath.Join(fakes.root, userfs.NamesFor(tm.ownerID(t)).Volume, "projects", pid, "docs", "纪要.md"))
	if err != nil || string(onDisk) != "# 纪要" {
		t.Fatalf("volume copy %q %v", onDisk, err)
	}

	b.must(200, "GET", base, nil, &page)
	if len(page.Entries) != 1 || page.Entries[0].Name != "docs" || !page.Entries[0].Dir {
		t.Fatalf("root %+v", page.Entries)
	}
	b.must(200, "GET", base+"?dir=docs", nil, &page)
	if page.Dir != "docs" || len(page.Entries) != 2 || !page.Entries[0].Dir || page.Entries[1].Path != "docs/纪要.md" ||
		!strings.HasPrefix(page.Entries[1].ContentType, "text/markdown") {
		t.Fatalf("docs %+v", page.Entries)
	}

	resp, body := b.get(base + "/download?path=" + url.QueryEscape("docs/纪要.md"))
	if resp.StatusCode != 200 || string(body) != "# 纪要" || !strings.HasPrefix(resp.Header.Get("Content-Disposition"), "attachment") {
		t.Fatalf("download %d %q %v", resp.StatusCode, body, resp.Header)
	}
	resp, _ = b.get(base + "/download?path=docs/web/page.html")
	if resp.Header.Get("Content-Type") != "application/octet-stream" {
		t.Fatalf("html served as %q", resp.Header.Get("Content-Type"))
	}

	b.must(204, "POST", base+"/folders", map[string]string{"path": "inputs/raw"}, nil)
	b.must(200, "GET", base, nil, &page)
	if len(page.Entries) != 2 || page.Entries[1].Name != "inputs" {
		t.Fatalf("after mkdir %+v", page.Entries)
	}
	b.must(204, "DELETE", base+"?path=docs/web/page.html", nil, nil)
	b.must(204, "DELETE", base+"?path=inputs", nil, nil)
	b.must(200, "GET", base, nil, &page)
	if len(page.Entries) != 1 {
		t.Fatalf("after deletes %+v", page.Entries)
	}

	for _, bad := range []string{base + "?dir=..", base + "/download?path=../x", base + "/download?path=a//b"} {
		if code, _ := b.do("GET", bad, nil); code != 422 {
			t.Fatalf("%s = %d", bad, code)
		}
	}
	if code, _ := b.do("GET", base+"/download?path=nope.txt", nil); code != 404 {
		t.Fatalf("missing = %d", code)
	}
	if code, _ := b.do("DELETE", base+"?path=nope.txt", nil); code != 404 {
		t.Fatalf("delete missing = %d", code)
	}
	if code, _ := w.signUp("intruder@example.com").do("GET", base, nil); code != 404 {
		t.Fatalf("intruder = %d", code)
	}

	// The project's folder itself is never a write target.
	for _, root := range []string{"/", "//"} {
		for _, q := range []string{root, url.QueryEscape(root)} {
			if code, _ := b.do("DELETE", base+"?path="+q, nil); code != 422 {
				t.Fatalf("delete %q = %d", q, code)
			}
		}
		if code, _ := b.do("POST", base+"/folders", map[string]string{"path": root}); code != 422 {
			t.Fatalf("mkdir %q = %d", root, code)
		}
	}
	projectDir := filepath.Join(fakes.root, userfs.NamesFor(tm.ownerID(t)).Volume, "projects", pid)
	if _, err := os.Stat(filepath.Join(projectDir, "docs", "纪要.md")); err != nil {
		t.Fatalf("project folder touched: %v", err)
	}

	// An upload is not atomic: a failing file leaves the earlier ones, and the error names them.
	code, raw = b.multipart(base, map[string][]string{"path": {"p/ok.txt", "docs/纪要.md/x.txt"}}, []formFile{{"a", "1"}, {"b", "2"}})
	if code < 400 || !strings.Contains(string(raw), "upload_partial") || !strings.Contains(string(raw), "p/ok.txt") {
		t.Fatalf("partial upload = %d %s", code, raw)
	}
	if _, err := os.Stat(filepath.Join(projectDir, "p", "ok.txt")); err != nil {
		t.Fatalf("earlier file of the upload: %v", err)
	}

	// Deleting the project removes its folder from the shared folder.
	b.must(204, "DELETE", "/api/projects/"+pid, nil, nil)
	if _, err := os.Stat(projectDir); !os.IsNotExist(err) {
		t.Fatalf("project folder left after delete: %v", err)
	}
}

// TestSaveAttachmentToProjectFiles: POST /api/attachments/{id}/save-shared copies an attachment
// into the project's files (a use of the attachment); an existing path is 409 file_exists unless
// overwrite, an expired attachment 410; other users, attachments without a project and bad paths
// are refused, and without a folder it is 409 folder_unavailable.
func TestSaveAttachmentToProjectFiles(t *testing.T) {
	tm := startTeam(t)
	w, b, pid := tm.w, tm.b, tm.projectID
	ctx := context.Background()
	body := []byte("# 会议纪要\n第一条\n")
	att := b.upload(pid, map[string]any{"kind": "attachment", "name": "纪要.md"}, body).Attachment
	save := "/api/attachments/" + att.ID + "/save-shared"
	if code, raw := b.do("POST", save, map[string]any{"path": "docs/纪要.md"}); code != 409 || !strings.Contains(string(raw), "folder_unavailable") {
		t.Fatalf("without a folder = %d %s", code, raw)
	}
	useFolder(t, w)
	b.must(200, "POST", "/api/storage/fs", nil, nil)
	_, _ = store.Exec(ctx, w.db.Pool, `UPDATE at_attachments SET expires_at = now() + interval '1 minute' WHERE id = $1`, att.ID)

	var saved orchapi.FileEntry
	b.must(200, "POST", save, map[string]any{"path": "docs/纪要.md"}, &saved)
	if saved.Path != "docs/纪要.md" || saved.Size != int64(len(body)) {
		t.Fatalf("saved %+v", saved)
	}
	if tm.expiresIn(t, att.ID).Hours() < 23 {
		t.Fatal("saving did not push the retention")
	}
	if _, raw := b.get("/api/projects/" + pid + "/files/download?path=" + url.QueryEscape("docs/纪要.md")); !bytes.Equal(raw, body) {
		t.Fatalf("copy %q", raw)
	}

	if code, raw := b.do("POST", save, map[string]any{"path": "docs/纪要.md"}); code != 409 || !strings.Contains(string(raw), "file_exists") {
		t.Fatalf("existing path = %d %s", code, raw)
	}
	b.must(200, "POST", save, map[string]any{"path": "docs/纪要.md", "overwrite": true}, &saved)
	for _, bad := range []string{"../x", "/", "//"} {
		if code, _ := b.do("POST", save, map[string]any{"path": bad}); code != 422 {
			t.Fatalf("bad path %q = %d", bad, code)
		}
	}

	if code, _ := w.signUp("intruder@example.com").do("POST", save, map[string]any{"path": "x.md"}); code != 404 {
		t.Fatalf("intruder = %d", code)
	}
	loose := b.userUpload("loose.txt", []byte("hi"))
	if code, _ := b.do("POST", "/api/attachments/"+loose.ID+"/save-shared", map[string]any{"path": "loose.txt"}); code != 404 {
		t.Fatalf("attachment without a project = %d", code)
	}

	_, _ = store.Exec(ctx, w.db.Pool, `UPDATE at_attachments SET state = 'expired', storage_key = NULL WHERE id = $1`, att.ID)
	if code, raw := b.do("POST", save, map[string]any{"path": "late.md"}); code != 410 || !strings.Contains(string(raw), "attachment_expired") {
		t.Fatalf("expired = %d %s", code, raw)
	}
	if code, _ := b.do("GET", "/api/projects/"+pid+"/files/download?path=late.md", nil); code != 404 {
		t.Fatalf("expired attachment written: %d", code)
	}
}

func decode(t *testing.T, raw []byte, out any) {
	t.Helper()
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
}

func (tm *team) ownerID(t *testing.T) string {
	t.Helper()
	var id string
	if err := store.QueryRow(context.Background(), tm.w.db.Pool, `SELECT user_id FROM at_projects WHERE id = $1`, tm.projectID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}
