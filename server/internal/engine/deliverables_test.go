package engine_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	protocol "github.com/touwaeriol/agent-teams/protocol"

	"github.com/touwaeriol/agent-teams/server/internal/artifacts"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw/nodetest"
	"github.com/touwaeriol/agent-teams/server/internal/orchapi"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// readCall calls read_deliverable and collects the file.download chunks queued before its result.
func (a *fakeAgent) readCall(args map[string]any) (protocol.ToolResult, map[string]string) {
	a.t.Helper()
	raw, _ := json.Marshal(args)
	callID := a.startCall("read_deliverable", string(raw))
	res := a.result(callID)
	if !res.Ok || res.Download == nil {
		return res, nil
	}
	var all []byte
	for i := 0; i < res.Download.Chunks; i++ {
		ctx, cancel := a.w.waitCtx()
		f, err := a.node.Wait(ctx, protocol.TypeFileDownload, func(f nodetest.Frame) bool {
			return strings.Contains(string(f.Body), string(res.Download.DownloadID)) && strings.Contains(string(f.Body), fmt.Sprintf(`"index":%d,`, i))
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
	files := map[string]string{}
	off := 0
	for _, f := range res.Download.Files {
		body := all[off : off+f.Size]
		sum := sha256.Sum256(body)
		if hex.EncodeToString(sum[:]) != f.SHA256 {
			a.t.Fatalf("checksum of %s", f.Path)
		}
		files[f.Path] = string(body)
		off += f.Size
	}
	return res, files
}

// openSecondThread opens thread #2 and returns its lead and its designer-1 (same name as thread #1's).
func openSecondThread(t *testing.T, tm *team) (*fakeAgent, *fakeAgent, core.ThreadView) {
	t.Helper()
	var th core.ThreadView
	tm.b.must(201, "POST", "/api/projects/"+tm.projectID+"/threads", map[string]any{"title": "Polish", "brief": "change the title"}, &th)
	lead, _ := expectOpen(t, tm.w, tm.n, "lead-2")
	lead.mustCall("update_plan", map[string]any{"steps": []map[string]string{{"title": "改", "status": "todo"}}})
	lead.mustCall("assign_task", map[string]any{"assignee": "designer", "instruction": "change the page title", "main": "改"})
	dev, open := expectOpen(t, tm.w, tm.n, "designer-1")
	dev.send(protocol.TypeSessionDelivered, protocol.SessionDelivered{SessionID: protocol.SessionID(dev.session), MsgID: open.FirstMessage.MsgID})
	return lead, dev, th
}

// TestReviseDeliverableAcrossThreads: designer-1 in thread #1 publishes a site; designer-1 of thread #2
// lists it, downloads it (chunked file.download frames with checksums, comments and history in the
// result), publishes v2 with the same id; the version history records who published each version
// and comments go to that version's publisher, or to the lead of its thread once it is gone.
func TestReviseDeliverableAcrossThreads(t *testing.T) {
	tm, _ := startDesignTeam(t)
	big := "<!doctype html><title>Login</title><!--" + strings.Repeat("x", 1<<20+5000) + "-->"
	res := tm.dev.publish(map[string]any{"path": "design", "kind": "design", "title": "登录页"}, 1<<20,
		upFile{"index.html", big}, upFile{"assets/style.css", "body{color:red}"})
	var pub protocol.PublishFileResult
	_ = json.Unmarshal(*res.Structured, &pub)
	id := pub.DeliverableID
	var c orchapi.CommentView
	tm.b.must(201, "POST", "/api/deliverables/"+id+"/comments", map[string]any{"text": "主色换成蓝色", "version": 1}, &c)
	if c.SentTo != "designer-1" {
		t.Fatalf("v1 comment %+v", c)
	}
	tm.dev.expectSend("主色换成蓝色")

	lead2, dev2, th2 := openSecondThread(t, tm)
	list := dev2.mustCall("list_deliverables", map[string]any{})
	var summaries protocol.ListDeliverablesResult
	_ = json.Unmarshal(*list.Structured, &summaries)
	if len(summaries.Deliverables) != 1 || summaries.Deliverables[0].ID != id || summaries.Deliverables[0].Versions != 1 ||
		*summaries.Deliverables[0].Thread != 1 || summaries.Deliverables[0].PublishedBy != "designer-1" || !strings.Contains(text(list), "read_deliverable") {
		t.Fatalf("list %+v %s", summaries, text(list))
	}
	if r := dev2.mustCall("list_deliverables", map[string]any{"thread": 2}); !strings.Contains(text(r), "还没有交付物") {
		t.Fatalf("thread filter %s", text(r))
	}

	got, files := dev2.readCall(map[string]any{"deliverable_id": id})
	if !got.Ok || got.Download.Chunks != 2 || got.Download.Version != 1 || files["index.html"] != big || files["assets/style.css"] != "body{color:red}" {
		t.Fatalf("read %+v %d files", got.Content, len(files))
	}
	var read protocol.ReadDeliverableResult
	_ = json.Unmarshal(*got.Structured, &read)
	if read.Dir != ".agent-deliverables/"+id+"/v1" || read.Entry != "index.html" || len(read.Versions) != 1 ||
		read.Versions[0].PublishedBy != "designer-1" || len(read.Comments) != 1 || read.Comments[0].Text != "主色换成蓝色" ||
		!strings.Contains(text(got), "publish_file(path=\".agent-deliverables/"+id+"/v1\"") {
		t.Fatalf("read result %+v\n%s", read, text(got))
	}

	res = dev2.publish(map[string]any{"path": ".agent-deliverables/" + id + "/v1", "kind": "design", "title": "登录页", "deliverable_id": id}, 1<<20,
		upFile{"index.html", "<!doctype html><title>Sign in</title>"}, upFile{"assets/style.css", "body{color:blue}"})
	_ = json.Unmarshal(*res.Structured, &pub)
	if !res.Ok || pub.Version != 2 || pub.DeliverableID != id {
		t.Fatalf("v2 %+v", res.Content)
	}
	var vf orchapi.DeliverableFiles
	tm.b.must(200, "GET", "/api/deliverables/"+id+"/files", nil, &vf)
	if len(vf.Versions) != 2 || vf.Versions[0].PublishedBy != "designer-1" || vf.Versions[0].PublisherThreadNo == nil || *vf.Versions[0].PublisherThreadNo != 2 ||
		vf.Versions[1].PublishedBy != "designer-1" || *vf.Versions[1].PublisherThreadNo != 1 {
		t.Fatalf("versions %+v", vf.Versions)
	}
	_, v2 := dev2.readCall(map[string]any{"deliverable_id": id, "version": 2})
	if v2["index.html"] != "<!doctype html><title>Sign in</title>" {
		t.Fatalf("v2 content %q", v2["index.html"])
	}

	// Comments follow the version's publisher (by employee id, not name): v2 → thread #2's designer-1.
	tm.b.must(201, "POST", "/api/deliverables/"+id+"/comments", map[string]any{"text": "标题再短一点", "version": 2}, &c)
	if c.SentTo != "designer-1" {
		t.Fatalf("v2 comment %+v", c)
	}
	if send := dev2.expectSend("标题再短一点"); !strings.Contains(send.Text, "read_deliverable(deliverable_id="+id) {
		t.Fatalf("comment text %q", send.Text)
	}
	// The publisher is gone: the lead of its thread gets the comment with how to fetch and revise.
	if _, err := store.Exec(tm.w.ctx, tm.w.e.Core.Pool, `UPDATE at_employees SET active = false WHERE thread_id = $1 AND name = 'designer-1'`, th2.ID); err != nil {
		t.Fatal(err)
	}
	tm.b.must(201, "POST", "/api/deliverables/"+id+"/comments", map[string]any{"text": "按钮圆角"}, &c)
	if c.SentTo != "lead-2" {
		t.Fatalf("fallback comment %+v", c)
	}
	send := lead2.expectSend("按钮圆角")
	for _, part := range []string{"designer-1 已不在", "read_deliverable(deliverable_id=" + id, "publish_file(path="} {
		if !strings.Contains(send.Text, part) {
			t.Fatalf("lead comment lacks %q: %s", part, send.Text)
		}
	}
}

// TestReadDeliverableScope: read_deliverable only reaches hosted deliverables of the caller's project
// (not another project of the same user, not another user's), unknown versions are explained, and a
// daemon without file_download is asked to upgrade.
func TestReadDeliverableScope(t *testing.T) {
	tm, _ := startDesignTeam(t)
	res := tm.dev.publish(map[string]any{"path": "a.md", "kind": "doc", "title": "说明"}, 1<<20, upFile{"a.md", "# 说明"})
	var pub protocol.PublishFileResult
	_ = json.Unmarshal(*res.Structured, &pub)
	var link core.DeliverableView
	tm.b.must(201, "POST", "/api/projects/"+tm.projectID+"/deliverables", map[string]any{"kind": "doc", "title": "外链", "url": "https://docs/x"}, &link)

	other := tm.w.signUp("someone@example.com")
	var theirs struct{ ID string }
	other.must(201, "POST", "/api/projects", map[string]any{"name": "x"}, &theirs)
	var foreign core.DeliverableView
	other.must(201, "POST", "/api/projects/"+theirs.ID+"/deliverables", map[string]any{"kind": "doc", "title": "别人的", "url": "https://x/y"}, &foreign)

	var p2 struct{ ID string }
	tm.b.must(201, "POST", "/api/projects", map[string]any{"name": "second"}, &p2)
	tm.b.must(201, "POST", "/api/projects/"+p2.ID+"/threads", map[string]any{"title": "Other", "brief": "x"}, nil)
	lead3, _ := expectOpen(t, tm.w, tm.n, "lead-1")

	cases := []struct {
		who  *fakeAgent
		args map[string]any
		want string
	}{
		{lead3, map[string]any{"deliverable_id": pub.DeliverableID}, "这个项目没有交付物"},
		{tm.dev, map[string]any{"deliverable_id": foreign.ID}, "这个项目没有交付物"},
		{tm.dev, map[string]any{"deliverable_id": link.ID}, "外部链接"},
		{tm.dev, map[string]any{"deliverable_id": pub.DeliverableID, "version": 9}, "没有第 9 版"},
	}
	for _, c := range cases {
		if r, _ := c.who.readCall(c.args); r.Ok || !strings.Contains(text(r), c.want) {
			t.Errorf("%v: %+v", c.args, r)
		}
	}
	if r := lead3.mustCall("list_deliverables", map[string]any{}); strings.Contains(text(r), pub.DeliverableID) {
		t.Fatalf("other project lists %s", text(r))
	}
	if r := tm.lead.mustCall("list_deliverables", map[string]any{"kind": "doc"}); !strings.Contains(text(r), "外部链接") || !strings.Contains(text(r), pub.DeliverableID) {
		t.Fatalf("lead list %s", text(r))
	}
	if r, files := tm.lead.readCall(map[string]any{"deliverable_id": pub.DeliverableID}); !r.Ok || files["a.md"] != "# 说明" {
		t.Fatalf("lead read %+v", r)
	}
	if _, err := store.Exec(tm.w.ctx, tm.w.e.Core.Pool, `UPDATE at_nodes SET last_hello = jsonb_set(last_hello, '{features}', '["sessions"]')`); err != nil {
		t.Fatal(err)
	}
	if r, _ := tm.dev.readCall(map[string]any{"deliverable_id": pub.DeliverableID}); r.Ok || !strings.Contains(text(r), "升级") {
		t.Fatalf("old daemon %+v", r)
	}
}

// TestDeliverableReferenceInline: @ referencing a hosted deliverable puts its entry file's text in the
// delivered message (cut at the limit) with the read_deliverable hint; an image is only described.
func TestDeliverableReferenceInline(t *testing.T) {
	tm, _ := startDesignTeam(t)
	res := tm.dev.publish(map[string]any{"path": "p.html", "kind": "design", "title": "落地页"}, 1<<20,
		upFile{"p.html", "<!doctype html><title>暗号 77</title>" + strings.Repeat("y", 40000)})
	var page protocol.PublishFileResult
	_ = json.Unmarshal(*res.Structured, &page)
	png := string(append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, 16)...))
	// Images are not deliverables any more (publish_file keeps them as attachments); one published
	// before that is still stored, and an @ reference to it still describes it.
	var logo core.Deliverable
	err := tm.w.e.Core.InTx(tm.w.ctx, func(tx *core.Tx) error {
		sum := sha256.Sum256([]byte(png))
		files := []artifacts.ArtifactFile{{Path: "logo.png", ContentType: "image/png", Size: len(png), SHA256: hex.EncodeToString(sum[:]), Data: []byte(png)}}
		var err error
		logo, err = core.InsertDeliverable(tm.w.ctx, tx, core.NewDeliverable{ProjectID: tm.projectID, ThreadID: &tm.thread.ID, Kind: "design",
			Title: "标志", Detail: artifacts.Detail("designer-1", 1, "logo.png", files, nil), PublishedBy: "designer-1"})
		if err != nil {
			return err
		}
		return artifacts.Save(tm.w.ctx, tx, logo.ID, 1, artifacts.Publisher{Name: "designer-1"}, files)
	})
	if err != nil {
		t.Fatal(err)
	}

	refs := []map[string]any{{"kind": "deliverable", "id": page.DeliverableID}, {"kind": "deliverable", "id": logo.ID}}
	tm.b.must(202, "POST", "/api/conversations/"+tm.thread.ID+"/messages", map[string]any{"content": "改一下标题", "refs": refs}, nil)
	send := tm.lead.expectSend("暗号 77")
	for _, part := range []string{"### 交付物 落地页", "id：" + page.DeliverableID, "第 1 版（designer-1 发布）", "（只取前 30000 字节）",
		"read_deliverable(deliverable_id=" + page.DeliverableID, "logo.png 是图片", "read_deliverable(deliverable_id=" + logo.ID} {
		if !strings.Contains(send.Text, part) {
			t.Fatalf("delivered text lacks %q:\n%.2000s", part, send.Text)
		}
	}
	if strings.Contains(send.Text, "PNG") {
		t.Fatalf("image bytes inlined")
	}
}
