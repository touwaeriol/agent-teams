package engine_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"
	"time"

	protocol "github.com/touwaeriol/agent-teams/protocol"

	"github.com/touwaeriol/agent-teams/server/internal/httpapi"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw/nodetest"
	"github.com/touwaeriol/agent-teams/server/internal/orchapi"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

var sharedFeatures = []string{"sessions", "git_worktree", "cred_helper", "file_download", "shared_files"}

// sfTeam is a project whose lead runs on node B and whose dev runs on node A (roles pinned with
// node_ids), so the two sessions of a test are on different daemons.
type sfTeam struct {
	*team
	nA, nB *nodetest.Node
	// tokA, tokB are the nodes' tokens.
	tokA, tokB string
}

func startSFTeam(t *testing.T) *sfTeam {
	t.Helper()
	w := newWorld(t, nil)
	tm := &team{w: w, b: w.signUp("shared@example.com")}
	var a, b nodeCreated
	tm.b.must(201, "POST", "/api/nodes", map[string]string{"name": "node-a"}, &a)
	tm.b.must(201, "POST", "/api/nodes", map[string]string{"name": "node-b"}, &b)
	var roles struct{ Roles []store.Role }
	tm.b.must(200, "GET", "/api/roles", nil, &roles)
	for i, r := range roles.Roles {
		switch {
		case r.Tier == "lead":
			roles.Roles[i].NodeIDs = []string{b.Node.ID}
		case r.Tier == "member" || r.Tier == "coordinator":
			roles.Roles[i].NodeIDs = []string{a.Node.ID}
		}
	}
	tm.b.must(200, "PUT", "/api/roles", map[string]any{"roles": roles.Roles}, nil)
	var project struct{ ID string }
	tm.b.must(201, "POST", "/api/projects", map[string]any{"name": "p"}, &project)
	tm.projectID = project.ID
	st := &sfTeam{team: tm, tokA: a.Token, tokB: b.Token}
	st.nA = w.connect(a.Token, nodetest.Options{Features: sharedFeatures})
	st.nB = w.connect(b.Token, nodetest.Options{Features: sharedFeatures})
	tm.n = st.nB
	tm.b.must(201, "POST", "/api/projects/"+project.ID+"/threads", map[string]any{"title": "Work", "brief": "do it"}, &tm.thread)
	tm.lead, _ = expectOpen(t, w, st.nB, "lead-1")
	tm.lead.mustCall("update_plan", map[string]any{"steps": []map[string]string{{"title": "做", "status": "todo"}}})
	tm.lead.mustCall("assign_task", map[string]any{"assignee": "dev", "instruction": "export the data", "main": "做"})
	var open protocol.SessionOpen
	tm.dev, open = expectOpen(t, w, st.nA, "dev-1")
	tm.dev.send(protocol.TypeSessionDelivered, protocol.SessionDelivered{SessionID: protocol.SessionID(tm.dev.session), MsgID: open.FirstMessage.MsgID})
	return st
}

// writeShared sends body as file.chunk frames (chunk bytes each) declared at declPath and calls
// write_shared_file with args.
func (a *fakeAgent) writeShared(args map[string]any, declPath, body string, chunk int) protocol.ToolResult {
	a.t.Helper()
	uploadID := fmt.Sprintf("up_%026d", time.Now().UnixNano())
	sum := sha256.Sum256([]byte(body))
	gen := protocol.OpenGen(a.gen)
	n := 0
	for off := 0; off < len(body); off += chunk {
		end := min(off+chunk, len(body))
		a.send(protocol.TypeFileChunk, protocol.FileChunk{SessionID: protocol.SessionID(a.session), OpenGen: &gen,
			UploadID: protocol.UploadID(uploadID), Index: n, Data: base64.StdEncoding.EncodeToString([]byte(body[off:end]))})
		n++
	}
	callID := fmt.Sprintf("tc_write_shared_%d", time.Now().UnixNano())
	raw, _ := json.Marshal(args)
	a.send(protocol.TypeToolCall, protocol.ToolCall{CallID: protocol.CallID(callID), SessionID: protocol.SessionID(a.session), OpenGen: &gen,
		Tool: "write_shared_file", Args: raw, Upload: &protocol.ToolUpload{UploadID: protocol.UploadID(uploadID), Chunks: n,
			Files: []protocol.UploadFile{{Path: declPath, Size: len(body), SHA256: hex.EncodeToString(sum[:]), ContentType: "x/ignored"}}}})
	return a.result(callID)
}

// readShared calls read_shared_file and collects the file.download chunks its node got before the result.
func (a *fakeAgent) readShared(path string) (protocol.ToolResult, []byte) {
	a.t.Helper()
	raw, _ := json.Marshal(map[string]any{"path": path})
	callID := a.startCall("read_shared_file", string(raw))
	res := a.result(callID)
	if !res.Ok || res.SharedDownload == nil {
		return res, nil
	}
	var all []byte
	for i := 0; i < res.SharedDownload.Chunks; i++ {
		ctx, cancel := a.w.waitCtx()
		f, err := a.node.Wait(ctx, protocol.TypeFileDownload, func(f nodetest.Frame) bool {
			return strings.Contains(string(f.Body), string(res.SharedDownload.DownloadID)) && strings.Contains(string(f.Body), fmt.Sprintf(`"index":%d,`, i))
		})
		cancel()
		if err != nil {
			a.t.Fatal(err)
		}
		var chunk protocol.FileDownload
		_ = f.Decode(&chunk)
		if string(chunk.CallID) != callID || string(chunk.SessionID) != a.session {
			a.t.Fatalf("chunk for another call %+v", chunk)
		}
		data, _ := base64.StdEncoding.DecodeString(chunk.Data)
		all = append(all, data...)
	}
	return res, all
}

func structured(t *testing.T, res protocol.ToolResult) map[string]any {
	t.Helper()
	if res.Structured == nil {
		t.Fatalf("no structured result: %+v", res.Content)
	}
	var out map[string]any
	if err := json.Unmarshal(*res.Structured, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// TestSharedFilesAcrossNodes is the acceptance test of shared files: dev-1 on node A writes a
// 1.5 MB file with write_shared_file (file.chunk upload); lead-1, a session on node B, lists it
// (uploader "dev-1 #1") and fetches it with read_shared_file: node B receives the bytes as
// file.download frames with the same sha256, and the tool result carries the local path only,
// never the content. The lead overwrites it, the user sees the change through the API, and a
// deleted file can no longer be read.
func TestSharedFilesAcrossNodes(t *testing.T) {
	st := startSFTeam(t)
	events := st.b.stream("/api/projects/"+st.projectID+"/events?topics=shared_file", "")
	marker := "ZXQ-SECRET-CONTENT-MARKER"
	body := marker + strings.Repeat("0123456789abcdef", (3<<19)/16) // ~1.5 MB: two download chunks
	res := st.dev.writeShared(map[string]any{"file": "out/report.csv"}, "out/report.csv", body, 1<<20)
	if !res.Ok {
		t.Fatalf("write failed %+v", res.Content)
	}
	var wrote protocol.WriteSharedFileResult
	_ = json.Unmarshal(*res.Structured, &wrote)
	if wrote.Path != "out/report.csv" || wrote.Size != len(body) || wrote.SHA256 != sha([]byte(body)) || wrote.Overwritten ||
		!strings.Contains(text(res), "不要放密钥") {
		t.Fatalf("write result %+v %s", wrote, text(res))
	}
	events.wait(t, "shared_file", `"op":"write"`, `"path":"out/report.csv"`, `"by":"dev-1 #1"`)

	list := st.lead.mustCall("list_shared_files", map[string]any{})
	var listed protocol.ListSharedFilesResult
	_ = json.Unmarshal(*list.Structured, &listed)
	if len(listed.Files) != 1 || listed.Files[0].UploadedBy != "dev-1" || listed.Files[0].UploadedByThreadNo == nil ||
		*listed.Files[0].UploadedByThreadNo != 1 || listed.Files[0].ContentType != "text/csv; charset=utf-8" ||
		listed.TotalBytes != len(body) || listed.LimitBytes != 5<<30 || !strings.Contains(text(list), "dev-1 #1") {
		t.Fatalf("list %+v\n%s", listed, text(list))
	}

	got, data := st.lead.readShared("out/report.csv")
	if !got.Ok || got.SharedDownload == nil || got.Download != nil {
		t.Fatalf("read %+v", got)
	}
	if st.lead.node != st.nB || st.dev.node != st.nA {
		t.Fatal("the sessions must run on different nodes")
	}
	dl := got.SharedDownload
	if dl.Chunks != 2 || len(dl.Files) != 1 || dl.Files[0].Path != "out/report.csv" || dl.Files[0].Size != len(body) ||
		dl.Files[0].SHA256 != wrote.SHA256 || sha(data) != wrote.SHA256 || string(data) != body {
		t.Fatalf("download %+v, %d bytes received", dl, len(data))
	}
	out := structured(t, got)
	if out["local_path"] != ".shared/out/report.csv" || out["sha256"] != wrote.SHA256 || out["path"] != "out/report.csv" {
		t.Fatalf("read result %v", out)
	}
	if _, has := out["content"]; has {
		t.Fatalf("read result carries content: %v", out)
	}
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), marker) || strings.Contains(string(raw), base64.StdEncoding.EncodeToString([]byte(marker))[:16]) {
		t.Fatalf("tool result carries the file content: %.300s", raw)
	}

	// The lead (node B) overwrites it from its local copy; the user sees the new uploader.
	res = st.lead.writeShared(map[string]any{"file": ".shared/out/report.csv"}, "out/report.csv", "a,b\n", 1<<20)
	_ = json.Unmarshal(*res.Structured, &wrote)
	if !res.Ok || !wrote.Overwritten || wrote.Size != 4 {
		t.Fatalf("overwrite %+v %s", wrote, text(res))
	}
	var page orchapi.SharedFileList
	st.b.must(200, "GET", "/api/projects/"+st.projectID+"/shared-files", nil, &page)
	if len(page.Files) != 1 || page.Files[0].UploadedBy != "lead-1" || page.TotalBytes != 4 || page.FileCount != 1 ||
		page.Limits.FileBytes != 200<<20 || page.Limits.ProjectFiles != 1000 {
		t.Fatalf("api list %+v", page)
	}

	// The coordinator (node A) has a working directory too and gets the lead's version the same way.
	var coord struct {
		ConversationID string `json:"conversation_id"`
	}
	st.b.must(200, "GET", "/api/projects/"+st.projectID+"/coordinator", nil, &coord)
	st.b.must(202, "POST", "/api/conversations/"+coord.ConversationID+"/messages", map[string]any{"content": "看看共享文件"}, nil)
	coordinator, _ := expectOpen(t, st.w, st.nA, "coordinator")
	if got, data := coordinator.readShared("out/report.csv"); !got.Ok || string(data) != "a,b\n" ||
		structured(t, got)["local_path"] != ".shared/out/report.csv" {
		t.Fatalf("coordinator read %+v %q", got.Content, data)
	}

	// Refusals: traversal in the declared path, a hidden target, an args.path the schema refuses.
	if r := st.dev.writeShared(map[string]any{"file": "x"}, "a/../../etc/passwd", "x", 1<<20); r.Ok || !strings.Contains(text(r), "..") {
		t.Fatalf("traversal accepted %s", text(r))
	}
	if r := st.dev.writeShared(map[string]any{"file": "x"}, ".env", "x", 1<<20); r.Ok {
		t.Fatalf("hidden file accepted %s", text(r))
	}
	if r := st.dev.writeShared(map[string]any{"file": "x", "path": "/abs"}, "abs", "x", 1<<20); r.Ok {
		t.Fatalf("absolute args.path accepted %s", text(r))
	}
	if r := st.dev.call("write_shared_file", map[string]any{"file": "x"}); r.Ok || !strings.Contains(text(r), "升级") {
		t.Fatalf("write without upload %s", text(r))
	}

	del := st.dev.mustCall("delete_shared_file", map[string]any{"path": "out/report.csv"})
	if out := structured(t, del); out["deleted"] != true {
		t.Fatalf("delete %v", out)
	}
	events.wait(t, "shared_file", `"op":"delete"`, `"path":"out/report.csv"`)
	if r, _ := st.lead.readShared("out/report.csv"); r.Ok || !strings.Contains(text(r), "没有共享文件") {
		t.Fatalf("read after delete %s", text(r))
	}
	if r := st.dev.mustCall("delete_shared_file", map[string]any{"path": "out/report.csv"}); structured(t, r)["deleted"] != false {
		t.Fatalf("second delete %s", text(r))
	}

	// A daemon without the shared_files feature is asked to upgrade instead of getting the content.
	st.dev.writeShared(map[string]any{"file": "n.txt"}, "n.txt", "hello", 1<<20)
	if _, err := store.Exec(st.w.ctx, st.w.e.Core.Pool, `UPDATE at_nodes SET last_hello = jsonb_set(last_hello, '{features}', '["sessions","file_download"]')`); err != nil {
		t.Fatal(err)
	}
	if r, _ := st.lead.readShared("n.txt"); r.Ok || !strings.Contains(text(r), "升级") || strings.Contains(text(r), "hello") {
		t.Fatalf("old daemon %s", text(r))
	}
}

// multipartUpload posts files to the shared-files API: parts[i] = {path?, name, body}.
func (b *browser) multipartUpload(pid, dir string, parts [][3]string) (int, []byte) {
	b.w.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if dir != "" {
		_ = mw.WriteField("dir", dir)
	}
	for _, p := range parts {
		if p[0] != "" {
			_ = mw.WriteField("path", p[0])
		}
	}
	for _, p := range parts {
		fw, _ := mw.CreateFormFile("file", p[1])
		_, _ = io.WriteString(fw, p[2])
	}
	_ = mw.Close()
	req, _ := http.NewRequest("POST", b.w.srv.URL+"/api/projects/"+pid+"/shared-files", &buf)
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

func (b *browser) get(path string) *http.Response {
	b.w.t.Helper()
	req, _ := http.NewRequest("GET", b.w.srv.URL+path, nil)
	req.Header.Set("X-Forwarded-For", b.ip)
	resp, err := b.http.Do(req)
	if err != nil {
		b.w.t.Fatal(err)
	}
	return resp
}

// TestSharedFilesAPI: the user uploads (multipart, several files, dir prefix, per-file paths),
// lists, downloads (always an attachment, nosniff, HTML never served as HTML), overwrites and
// deletes; bad paths are refused and another user's project is 404.
func TestSharedFilesAPI(t *testing.T) {
	w := newWorld(t, nil)
	b := w.signUp("owner@example.com")
	var project struct{ ID string }
	b.must(201, "POST", "/api/projects", map[string]any{"name": "p"}, &project)
	pid := project.ID
	events := b.stream("/api/projects/"+pid+"/events?topics=shared_file", "")

	code, raw := b.multipartUpload(pid, "data/", [][3]string{{"", "a.csv", "x,y\n"}, {"", "page.html", "<script>alert(1)</script>"}})
	if code != 201 {
		t.Fatalf("upload %d %s", code, raw)
	}
	var up orchapi.SharedFileUpload
	_ = json.Unmarshal(raw, &up)
	if len(up.Files) != 2 || up.Files[0].Path != "data/a.csv" || up.Files[1].Path != "data/page.html" || up.Files[0].UploadedBy != "user" ||
		up.Files[0].Overwritten || up.TotalBytes != int64(4+len("<script>alert(1)</script>")) {
		t.Fatalf("upload result %s", raw)
	}
	events.wait(t, "shared_file", `"op":"write"`, `"path":"data/a.csv"`, `"by":"user"`)
	code, raw = b.multipartUpload(pid, "", [][3]string{{"reports/2026/sum.bin", "whatever.bin", "\x00\x01"}, {"data/a.csv", "a.csv", "1,2\n"}})
	if code != 201 || !strings.Contains(string(raw), `"overwritten":true`) {
		t.Fatalf("upload with paths %d %s", code, raw)
	}

	var page orchapi.SharedFileList
	b.must(200, "GET", "/api/projects/"+pid+"/shared-files", nil, &page)
	if len(page.Files) != 3 || page.Files[0].Path != "data/a.csv" || page.Files[2].Path != "reports/2026/sum.bin" ||
		page.Files[2].ContentType != "application/octet-stream" || page.Limits.ProjectBytes != 5<<30 {
		t.Fatalf("list %+v", page)
	}
	b.must(200, "GET", "/api/projects/"+pid+"/shared-files?prefix=reports/", nil, &page)
	if len(page.Files) != 1 {
		t.Fatalf("prefix list %+v", page)
	}

	// Downloads are a 302 to the object store, which serves the bytes as an octet-stream attachment.
	if r, _ := b.redirect("/api/projects/" + pid + "/shared-files/download?path=data/page.html"); r.StatusCode != 302 ||
		r.Header.Get("Cache-Control") != "private, no-store" {
		t.Fatalf("download redirect %d %v", r.StatusCode, r.Header)
	}
	resp := b.get("/api/projects/" + pid + "/shared-files/download?path=data/page.html")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(body) != "<script>alert(1)</script>" ||
		resp.Header.Get("Content-Disposition") != `attachment; filename="page.html"; filename*=UTF-8''page.html` ||
		resp.Header.Get("Content-Type") != "application/octet-stream" {
		t.Fatalf("download %d %q %v", resp.StatusCode, body, resp.Header)
	}
	resp = b.get("/api/projects/" + pid + "/shared-files/download?path=data/a.csv")
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "1,2\n" || resp.Header.Get("Content-Type") != "application/octet-stream" ||
		!strings.HasPrefix(resp.Header.Get("Content-Disposition"), "attachment") {
		t.Fatalf("csv download %q %v", body, resp.Header)
	}
	// Rows from before #58 are still served by the control plane, with its safety headers.
	if _, err := store.Exec(w.ctx, w.db.Pool, `INSERT INTO at_shared_files (project_id, path, content_type, size, sha256, data, uploaded_by)
		VALUES ($1, 'old.html', 'text/html; charset=utf-8', 3, 'x', 'abc', 'user')`, pid); err != nil {
		t.Fatal(err)
	}
	resp = b.get("/api/projects/" + pid + "/shared-files/download?path=old.html")
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(body) != "abc" || resp.Header.Get("Content-Type") != "application/octet-stream" ||
		resp.Header.Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "sandbox") ||
		resp.Header.Get("Content-Disposition") != `attachment; filename="old.html"; filename*=UTF-8''old.html` {
		t.Fatalf("legacy download %d %q %v", resp.StatusCode, body, resp.Header)
	}
	b.must(204, "DELETE", "/api/projects/"+pid+"/shared-files?path=old.html", nil, nil)

	for _, bad := range [][3]string{{"../x", "x", "x"}, {"/etc/x", "x", "x"}, {".git/config", "x", "x"}, {`a\b`, "x", "x"}} {
		if code, raw := b.multipartUpload(pid, "", [][3]string{bad}); code != 422 {
			t.Fatalf("path %q: %d %s", bad[0], code, raw)
		}
	}
	if code, raw := b.multipartUpload(pid, "../up", [][3]string{{"", "x.txt", "x"}}); code != 422 {
		t.Fatalf("dir traversal: %d %s", code, raw)
	}
	if code, _ := b.do("GET", "/api/projects/"+pid+"/shared-files/download?path=../../etc/passwd", nil); code != 422 {
		t.Fatalf("download traversal %d", code)
	}
	if code, _ := b.do("GET", "/api/projects/"+pid+"/shared-files/download?path=nope.txt", nil); code != 404 {
		t.Fatalf("download missing %d", code)
	}

	// Another user can neither list, download, upload nor delete in this project.
	other := w.signUp("other@example.com")
	if code, _ := other.do("GET", "/api/projects/"+pid+"/shared-files", nil); code != 404 {
		t.Fatalf("other list %d", code)
	}
	if resp := other.get("/api/projects/" + pid + "/shared-files/download?path=data/a.csv"); resp.StatusCode != 404 {
		t.Fatalf("other download %d", resp.StatusCode)
	}
	if code, _ := other.multipartUpload(pid, "", [][3]string{{"", "x.txt", "x"}}); code != 404 {
		t.Fatalf("other upload %d", code)
	}
	if code, _ := other.do("DELETE", "/api/projects/"+pid+"/shared-files?path=data/a.csv", nil); code != 404 {
		t.Fatalf("other delete %d", code)
	}

	b.must(204, "DELETE", "/api/projects/"+pid+"/shared-files?path=data/a.csv", nil, nil)
	events.wait(t, "shared_file", `"op":"delete"`, `"path":"data/a.csv"`)
	if code, _ := b.do("DELETE", "/api/projects/"+pid+"/shared-files?path=data/a.csv", nil); code != 404 {
		t.Fatalf("second delete %d", code)
	}
}
