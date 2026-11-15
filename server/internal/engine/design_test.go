package engine_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	protocol "github.com/touwaeriol/agent-teams/protocol"

	"github.com/touwaeriol/agent-teams/server/internal/artifacts"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw/nodetest"
	"github.com/touwaeriol/agent-teams/server/internal/orchapi"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// startDesignTeam is startTeam with the designer preset (design + docs skills) as the only member role.
func startDesignTeam(t *testing.T) (*team, protocol.SessionOpen) {
	t.Helper()
	return startDesignTeamWith(t, nil)
}

// startDesignTeamWith lets prep change the designer role (and upload what it needs) before saving.
func startDesignTeamWith(t *testing.T, prep func(tm *team, designer *store.Role)) (*team, protocol.SessionOpen) {
	t.Helper()
	w := newWorld(t, nil)
	tm := &team{w: w, b: w.signUp("design@example.com")}
	var node nodeCreated
	tm.b.must(201, "POST", "/api/nodes", map[string]string{"name": "fake"}, &node)
	tm.token = node.Token
	var roles struct{ Roles []store.Role }
	tm.b.must(200, "GET", "/api/roles", nil, &roles)
	var presets struct{ Roles []store.Role }
	tm.b.must(200, "GET", "/api/roles/presets", nil, &presets)
	if prep != nil {
		prep(tm, &presets.Roles[0])
	}
	tm.b.must(200, "PUT", "/api/roles", map[string]any{"roles": []store.Role{roles.Roles[0], roles.Roles[1], presets.Roles[0]}}, nil)
	var project struct{ ID string }
	tm.b.must(201, "POST", "/api/projects", map[string]any{"name": "p"}, &project)
	tm.projectID = project.ID
	tm.n = w.connect(node.Token, nodetest.Options{})
	tm.b.must(201, "POST", "/api/projects/"+project.ID+"/threads", map[string]any{"title": "Design", "brief": "login page"}, &tm.thread)
	tm.lead, _ = expectOpen(t, w, tm.n, "lead-1")
	tm.lead.mustCall("update_plan", map[string]any{"steps": []map[string]string{{"title": "稿", "status": "todo"}}})
	tm.lead.mustCall("assign_task", map[string]any{"assignee": "designer", "instruction": "draw the login page", "main": "稿"})
	var open protocol.SessionOpen
	tm.dev, open = expectOpen(t, w, tm.n, "designer-1")
	tm.dev.send(protocol.TypeSessionDelivered, protocol.SessionDelivered{SessionID: protocol.SessionID(tm.dev.session), MsgID: open.FirstMessage.MsgID})
	return tm, open
}

type upFile struct{ path, body string }

// publish sends the files as file.chunk frames (chunk bytes each) and calls publish_file.
func (a *fakeAgent) publish(args map[string]any, chunk int, files ...upFile) protocol.ToolResult {
	a.t.Helper()
	uploadID := fmt.Sprintf("up_%026d", time.Now().UnixNano())
	var all []byte
	decl := []protocol.UploadFile{}
	for _, f := range files {
		sum := sha256.Sum256([]byte(f.body))
		decl = append(decl, protocol.UploadFile{Path: f.path, Size: len(f.body), SHA256: hex.EncodeToString(sum[:]), ContentType: "x/ignored"})
		all = append(all, f.body...)
	}
	n := 0
	gen := protocol.OpenGen(a.gen)
	for off := 0; off < len(all); off += chunk {
		end := min(off+chunk, len(all))
		a.send(protocol.TypeFileChunk, protocol.FileChunk{SessionID: protocol.SessionID(a.session), OpenGen: &gen,
			UploadID: protocol.UploadID(uploadID), Index: n, Data: base64.StdEncoding.EncodeToString(all[off:end])})
		n++
	}
	callID := fmt.Sprintf("tc_publish_%d", time.Now().UnixNano())
	raw, _ := json.Marshal(args)
	a.send(protocol.TypeToolCall, protocol.ToolCall{CallID: protocol.CallID(callID), SessionID: protocol.SessionID(a.session), OpenGen: &gen,
		Tool: "publish_file", Args: raw, Upload: &protocol.ToolUpload{UploadID: protocol.UploadID(uploadID), Chunks: n, Files: decl}})
	return a.result(callID)
}

func fetch(t *testing.T, w *world, url string) (*http.Response, string) {
	t.Helper()
	if strings.HasPrefix(url, "/") {
		url = w.srv.URL + url
	}
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp, string(raw)
}

// TestDesignerSkillsAndPublishFile: the designer's session.open carries its skills and the Skill
// tool; publish_file stores versioned files served sandboxed through signed URLs; comments go
// back to the designer.
func TestDesignerSkillsAndPublishFile(t *testing.T) {
	tm, open := startDesignTeam(t)
	if len(open.Skills) != 2 || open.Skills[0].Name != "design" || open.Skills[1].Name != "docs" ||
		!strings.Contains(open.Skills[0].Files[0].Content, "publish_file") || !contains(open.Claude.Tools, "Skill") {
		t.Fatalf("designer open skills %+v tools %v", open.Skills, open.Claude.Tools)
	}
	page := upFile{"index.html", "<!doctype html><title>Login</title><link rel=stylesheet href=style.css>"}
	style := upFile{"style.css", "body{color:red}"}
	res := tm.dev.publish(map[string]any{"path": "design", "kind": "design", "title": "登录页"}, 16, page, style)
	if !res.Ok {
		t.Fatalf("publish failed %+v", res.Content)
	}
	var out protocol.PublishFileResult
	_ = json.Unmarshal(*res.Structured, &out)
	if out.Version != 1 || !strings.HasPrefix(out.DeliverableID, "dl_") {
		t.Fatalf("result %+v", out)
	}

	var files orchapi.DeliverableFiles
	tm.b.must(200, "GET", "/api/deliverables/"+out.DeliverableID+"/files", nil, &files)
	if files.Entry != "index.html" || files.Render != "html" || len(files.Files) != 2 || files.Isolated || len(files.Versions) != 1 {
		t.Fatalf("files %+v", files)
	}
	resp, body := fetch(t, tm.w, files.EntryURL)
	csp := resp.Header.Get("Content-Security-Policy")
	if resp.StatusCode != 200 || body != page.body || !strings.HasPrefix(csp, "sandbox allow-scripts") ||
		strings.Contains(csp, "allow-same-origin") || resp.Header.Get("X-Content-Type-Options") != "nosniff" ||
		resp.Header.Get("Content-Type") != "text/html; charset=utf-8" || resp.Header.Get("Set-Cookie") != "" {
		t.Fatalf("served %d %q headers %v", resp.StatusCode, body, resp.Header)
	}
	if resp, body := fetch(t, tm.w, files.BaseURL+"style.css"); resp.StatusCode != 200 || body != style.body ||
		!strings.HasPrefix(resp.Header.Get("Content-Type"), "text/css") {
		t.Fatalf("relative asset %d %q", resp.StatusCode, body)
	}
	tampered := strings.Replace(files.BaseURL, ".1.", ".2.", 1)
	if resp, _ := fetch(t, tm.w, tampered+"index.html"); resp.StatusCode != 403 {
		t.Fatalf("tampered token = %d", resp.StatusCode)
	}

	var list struct{ Items []core.DeliverableView }
	tm.b.must(200, "GET", "/api/projects/"+tm.projectID+"/deliverables", nil, &list)
	if len(list.Items) != 1 || list.Items[0].Detail["hosted"] != true || list.Items[0].URL != "" || list.Items[0].PublishedBy != "designer-1" {
		t.Fatalf("deliverables %+v", list.Items)
	}

	// A new version of the same deliverable.
	res = tm.dev.publish(map[string]any{"path": "design/login.md", "kind": "design", "title": "登录页 v2", "deliverable_id": out.DeliverableID},
		1<<20, upFile{"login.md", "# 登录\n"})
	_ = json.Unmarshal(*res.Structured, &out)
	if !res.Ok || out.Version != 2 {
		t.Fatalf("second version %+v %+v", res.Content, out)
	}
	tm.b.must(200, "GET", "/api/deliverables/"+out.DeliverableID+"/files", nil, &files)
	if files.Version != 2 || files.Render != "markdown" || len(files.Versions) != 2 {
		t.Fatalf("latest %+v", files)
	}
	tm.b.must(200, "GET", "/api/deliverables/"+out.DeliverableID+"/files?version=1", nil, &files)
	if files.Version != 1 || files.Entry != "index.html" {
		t.Fatalf("version 1 %+v", files)
	}

	// Comments are stored and delivered to the publisher.
	var c orchapi.CommentView
	tm.b.must(201, "POST", "/api/deliverables/"+out.DeliverableID+"/comments", map[string]any{"text": "按钮再大一点", "version": 2}, &c)
	if c.SentTo != "designer-1" {
		t.Fatalf("comment %+v", c)
	}
	send := tm.dev.expectSend("按钮再大一点")
	if !strings.Contains(send.Text, "deliverable_id="+out.DeliverableID) || !strings.Contains(send.Text, "第 2 版") {
		t.Fatalf("comment message %q", send.Text)
	}
	var comments struct{ Items []orchapi.CommentView }
	tm.b.must(200, "GET", "/api/deliverables/"+out.DeliverableID+"/comments", nil, &comments)
	if len(comments.Items) != 1 || comments.Items[0].Text != "按钮再大一点" {
		t.Fatalf("comments %+v", comments)
	}
	other := tm.w.signUp("other@example.com")
	if code, _ := other.do("GET", "/api/deliverables/"+out.DeliverableID+"/files", nil); code != 404 {
		t.Fatalf("another user's files = %d", code)
	}
}

// TestPublishFileRefusals: bad checksums, missing chunks, unsupported types, a directory
// without index.html and foreign deliverable ids are explained, not stored.
// Chunks no tool.call claims may not pile up: past MaxPending a session's chunks are dropped,
// and its next publish fails as incomplete instead of filling the database.
func TestUnclaimedChunksAreCapped(t *testing.T) {
	tm, _ := startDesignTeam(t)
	gen := protocol.OpenGen(tm.dev.gen)
	piece := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("a"), artifacts.ChunkBytes))
	for i := 0; i < artifacts.MaxPending/artifacts.ChunkBytes; i++ {
		tm.dev.send(protocol.TypeFileChunk, protocol.FileChunk{SessionID: protocol.SessionID(tm.dev.session), OpenGen: &gen,
			UploadID: protocol.UploadID(fmt.Sprintf("up_%026d", 100+i/5)), Index: i % 5, Data: piece})
	}
	res := tm.dev.publish(map[string]any{"path": "a.md", "kind": "doc", "title": "x"}, 1<<20, upFile{"a.md", "# hi"})
	if res.Ok || !strings.Contains(text(res), "不完整") {
		t.Fatalf("publish over the pending cap: %+v", res)
	}
}

func TestPublishFileRefusals(t *testing.T) {
	tm, _ := startDesignTeam(t)
	cases := []struct {
		name  string
		args  map[string]any
		files []upFile
		want  string
	}{
		{"exe", map[string]any{"path": "a.exe", "kind": "data", "title": "x"}, []upFile{{"a.exe", "MZ"}}, "不支持的文件"},
		{"no index", map[string]any{"path": "d", "kind": "design", "title": "x"}, []upFile{{"a.html", "a"}, {"b.html", "b"}}, "index.html"},
		{"foreign id", map[string]any{"path": "a.md", "kind": "doc", "title": "x", "deliverable_id": "dl_nope"}, []upFile{{"a.md", "a"}}, "dl_nope"},
	}
	for _, c := range cases {
		res := tm.dev.publish(c.args, 1<<20, c.files...)
		if res.Ok || !strings.Contains(text(res), c.want) {
			t.Errorf("%s: %+v", c.name, res)
		}
	}
	// Checksum mismatch: declare one body, send another.
	gen := protocol.OpenGen(tm.dev.gen)
	tm.dev.send(protocol.TypeFileChunk, protocol.FileChunk{SessionID: protocol.SessionID(tm.dev.session), OpenGen: &gen,
		UploadID: "up_00000000000000000000000001", Index: 0, Data: base64.StdEncoding.EncodeToString([]byte("evil"))})
	sum := sha256.Sum256([]byte("good"))
	raw, _ := json.Marshal(map[string]any{"path": "a.md", "kind": "doc", "title": "x"})
	tm.dev.send(protocol.TypeToolCall, protocol.ToolCall{CallID: "tc_bad_sum", SessionID: protocol.SessionID(tm.dev.session), OpenGen: &gen,
		Tool: "publish_file", Args: raw, Upload: &protocol.ToolUpload{UploadID: "up_00000000000000000000000001", Chunks: 1,
			Files: []protocol.UploadFile{{Path: "a.md", Size: 4, SHA256: hex.EncodeToString(sum[:]), ContentType: "text/markdown"}}}})
	if res := tm.dev.result("tc_bad_sum"); res.Ok || !strings.Contains(text(res), "校验失败") {
		t.Fatalf("checksum %+v", res)
	}
	if res := tm.dev.call("publish_file", map[string]any{"path": "a.md", "kind": "doc", "title": "x"}); res.Ok {
		t.Fatalf("call without upload accepted")
	}
	var list struct{ Items []core.DeliverableView }
	tm.b.must(200, "GET", "/api/projects/"+tm.projectID+"/deliverables", nil, &list)
	if len(list.Items) != 0 {
		t.Fatalf("refused publishes left deliverables: %+v", list.Items)
	}
}
