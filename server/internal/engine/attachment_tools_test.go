package engine_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw/nodetest"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// v2Features are the hello features of a daemon with attachments_v2 (#58).
var v2Features = []string{"sessions", "git_worktree", "cred_helper", "file_download", "attachments_v2"}

// attTeam is a project with a thread whose lead is up; the node has attachments_v2 when v2.
func attTeam(t *testing.T, v2 bool) *team {
	t.Helper()
	w := newWorld(t, nil)
	tm := &team{w: w, b: w.signUp("att@example.com")}
	var node nodeCreated
	tm.b.must(201, "POST", "/api/nodes", map[string]string{"name": "fake"}, &node)
	tm.token = node.Token
	tm.b.must(200, "GET", "/api/roles", nil, nil)
	askMembers(t, w)
	var project struct{ ID string }
	tm.b.must(201, "POST", "/api/projects", map[string]any{"name": "p"}, &project)
	tm.projectID = project.ID
	o := nodetest.Options{}
	if v2 {
		o.Features = v2Features
	}
	tm.n = w.connect(node.Token, o)
	tm.b.must(201, "POST", "/api/projects/"+project.ID+"/threads", map[string]any{"title": "Work", "brief": "do it"}, &tm.thread)
	tm.lead, _ = expectOpen(t, w, tm.n, "lead-1")
	tm.lead.mustCall("update_plan", map[string]any{"steps": []map[string]string{{"title": "做", "status": "todo"}}})
	return tm
}

// putAttachment stores an attachment of any type straight in the project (the upload API of
// D2 is not needed for the tool tests).
func putAttachment(t *testing.T, tm *team, projectID, name, typ string, data []byte) core.Attachment {
	t.Helper()
	c := tm.w.e.Core
	var owner string
	if err := store.QueryRow(tm.w.ctx, c.Pool, `SELECT user_id FROM at_projects WHERE id = $1`, projectID).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	a, err := core.InsertAttachment(tm.w.ctx, c.Pool, c.Blobs, c.AttachmentTTL, core.Attachment{ID: core.NewID("img"),
		ProjectID: projectID, UserID: owner, Name: name, ContentType: typ, Size: len(data), SHA256: sum256(data)}, data)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func refIDs(refs []protocol.AttachmentRef) []string {
	out := make([]string, len(refs))
	for i, r := range refs {
		out[i] = r.ID
	}
	return out
}

func sameIDs(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func (tm *team) expiresIn(t *testing.T, id string) time.Duration {
	t.Helper()
	var at time.Time
	if err := store.QueryRow(tm.w.ctx, tm.w.e.Core.Pool, `SELECT expires_at FROM at_attachments WHERE id = $1`, id).Scan(&at); err != nil {
		t.Fatal(err)
	}
	return time.Until(at)
}

// TestAssignTaskAttachmentsV2: assign_task carries attachments of any type; the member's first
// message brings them all as attachments (no legacy images), small text files inlined, the task
// keeps them, and the retention restarts. Messages (send_message) carry them too.
func TestAssignTaskAttachmentsV2(t *testing.T) {
	tm := attTeam(t, true)
	png := uploadPNG(t, tm)
	small := putAttachment(t, tm, tm.projectID, "notes.md", core.TextType, []byte("# 需求\n按钮改成蓝色"))
	zip := putAttachment(t, tm, tm.projectID, "data.zip", "application/zip", []byte("PK\x03\x04 not really"))
	tm.sql(t, `UPDATE at_attachments SET expires_at = now() + interval '1 hour' WHERE id = $1`, zip.ID)

	res := tm.lead.mustCall("assign_task", map[string]any{"assignee": "dev", "instruction": "按附件改", "main": "做",
		"attachments": []string{png, small.ID, zip.ID, png}})
	if !strings.Contains(text(res), "with 3 attachments") {
		t.Fatalf("assign_task result %q", text(res))
	}
	dev, open := expectOpen(t, tm.w, tm.n, "dev-1")
	fm := open.FirstMessage
	if fm == nil || len(fm.Images) != 0 || !sameIDs(refIDs(fm.Attachments), png, small.ID, zip.ID) {
		t.Fatalf("first message images %+v attachments %+v", fm.Images, fm.Attachments)
	}
	if !strings.Contains(fm.Text, "按钮改成蓝色") || strings.Contains(fm.Text, "请升级") {
		t.Fatalf("first message text %q", fm.Text)
	}
	if fm.Attachments[2].ContentType != "application/zip" || fm.Attachments[2].Size != zip.Size || fm.Attachments[2].SHA256 == nil {
		t.Fatalf("zip ref %+v", fm.Attachments[2])
	}
	if d := tm.expiresIn(t, zip.ID); d < 23*time.Hour {
		t.Fatalf("zip retention not restarted: %v", d)
	}
	var raw []byte
	if err := store.QueryRow(tm.w.ctx, tm.w.e.Core.Pool, `SELECT attachments FROM at_tasks WHERE conversation_id = $1 AND id = 't1'`,
		tm.thread.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var stored []core.AttachmentRef
	_ = json.Unmarshal(raw, &stored)
	if len(stored) != 3 || stored[0].ID != png {
		t.Fatalf("at_tasks.attachments %s", raw)
	}
	dev.send(protocol.TypeSessionDelivered, protocol.SessionDelivered{SessionID: open.SessionID, MsgID: fm.MsgID})

	// send_message: member → lead, with an attachment.
	r := dev.mustCall("send_message", map[string]any{"to": "lead", "text": "结果见附件", "attachments": []string{zip.ID}})
	if !strings.Contains(text(r), "with 1 attachment") {
		t.Fatalf("send_message %q", text(r))
	}
	send := tm.lead.expectSend("结果见附件")
	if len(send.Images) != 0 || !sameIDs(refIDs(send.Attachments), zip.ID) {
		t.Fatalf("lead send images %+v attachments %+v", send.Images, send.Attachments)
	}
	var mail string
	if err := store.QueryRow(tm.w.ctx, tm.w.e.Core.Pool, `SELECT body FROM at_mail WHERE sender = 'dev-1' ORDER BY id DESC LIMIT 1`).Scan(&mail); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(mail, zip.ID) || !strings.Contains(mail, "read_attachment") {
		t.Fatalf("stored mail %q", mail)
	}

	// Rework: the task goes back to the queue reserved for dev-1 (as a rejected review does);
	// its new brief brings the attachments again.
	tm.sql(t, `UPDATE at_tasks SET status = 'pending', claimed_by = '', claimed_at = NULL, reserved_for = 'dev-1',
		reserved_until = now() + interval '1 hour' WHERE conversation_id = $1 AND id = 't1'`, tm.thread.ID)
	tm.kick()
	again := dev.expectSend("[Task t1")
	if !sameIDs(refIDs(again.Attachments), png, small.ID, zip.ID) || !strings.Contains(again.Text, "按钮改成蓝色") {
		t.Fatalf("rework brief attachments %+v", again.Attachments)
	}
}

// TestAssignTaskAttachmentsLegacy: a daemon without attachments_v2 only gets images and PDFs up
// to 5 MB as images (strictly within the old enum), a cut inline copy of a big text file and a
// line asking to upgrade for the rest; no attachments field.
func TestAssignTaskAttachmentsLegacy(t *testing.T) {
	tm := attTeam(t, false)
	png := uploadPNG(t, tm)
	zip := putAttachment(t, tm, tm.projectID, "data.zip", "application/zip", []byte("PK\x03\x04"))
	big := putAttachment(t, tm, tm.projectID, "log.txt", core.TextType, []byte(strings.Repeat("日志行\n", 12000)))
	tm.lead.mustCall("assign_task", map[string]any{"assignee": "dev", "instruction": "看日志", "main": "做",
		"attachments": []string{png, zip.ID, big.ID}})
	_, open := expectOpen(t, tm.w, tm.n, "dev-1")
	fm := open.FirstMessage
	if fm == nil || len(fm.Attachments) != 0 || len(fm.Images) != 1 || fm.Images[0].ID != png || fm.Images[0].MediaType != "image/png" {
		t.Fatalf("legacy first message images %+v attachments %+v", fm.Images, fm.Attachments)
	}
	for _, want := range []string{"[附件]", zip.ID, "data.zip", "请升级", "[附件：log.txt，只取前", "只放了开头部分"} {
		if !strings.Contains(fm.Text, want) {
			t.Fatalf("legacy text lacks %q: %q", want, fm.Text[max(0, len(fm.Text)-600):])
		}
	}
	if len(fm.Text) > 70000 {
		t.Fatalf("legacy text not cut: %d bytes", len(fm.Text))
	}

	// Everything delivered (one image): the ids are still listed, so the AI can pass them on.
	tm.b.must(202, "POST", "/api/conversations/"+tm.thread.ID+"/messages", map[string]any{"content": "看图", "attachments": []string{png}}, nil)
	send := tm.lead.expectSend("看图")
	if len(send.Images) != 1 || !strings.Contains(send.Text, "[附件]") || !strings.Contains(send.Text, png+" a.png") ||
		!strings.Contains(send.Text, "已随消息附上") || strings.Contains(send.Text, "请升级") {
		t.Fatalf("legacy image message %q images %+v", send.Text, send.Images)
	}

	// More small images than an old daemon takes in one message: 8 go, the rest are named with
	// the reason (a per-message limit, not something an upgrade is needed for).
	var pngs []string
	for range 10 {
		pngs = append(pngs, uploadPNG(t, tm))
	}
	tm.b.must(202, "POST", "/api/conversations/"+tm.thread.ID+"/messages", map[string]any{"content": "十张图", "attachments": pngs}, nil)
	send = tm.lead.expectSend("十张图")
	if len(send.Images) != 8 || strings.Count(send.Text, "单条消息最多 8 张图片（旧节点）") != 2 || strings.Contains(send.Text, "请升级") ||
		!strings.Contains(send.Text, pngs[9]) {
		t.Fatalf("legacy 10 images %q images %d", send.Text, len(send.Images))
	}
}

// TestLegacyHandoffToV2: a message fitted for an old daemon that never got it is handed to a
// fresh session on a daemon that now has attachments_v2 (resume_failed): it arrives as it was
// before the rewrite, with every attachment and without the upgrade line.
func TestLegacyHandoffToV2(t *testing.T) {
	tm := attTeam(t, false)
	conv := tm.coordinatorConv(t)
	_, coord := projectChat(t, tm, conv, map[string]any{"content": "你好"})
	zip := putAttachment(t, tm, tm.projectID, "data.zip", "application/zip", []byte("PK\x03\x04"))
	coord.mustCall("message_thread", map[string]any{"no": tm.thread.No, "text": "数据包给你", "attachments": []string{zip.ID}})
	ctx, cancel := tm.w.waitCtx()
	defer cancel()
	f, err := tm.n.Wait(ctx, protocol.TypeSessionSend, func(f nodetest.Frame) bool {
		return tm.lead.mine(f) && strings.Contains(string(f.Body), "数据包给你")
	})
	if err != nil {
		t.Fatal(err)
	}
	var legacy protocol.SessionSend
	_ = f.Decode(&legacy)
	if len(legacy.Attachments) != 0 || !strings.Contains(legacy.Text, "请升级") {
		t.Fatalf("old daemon send %q %+v", legacy.Text, legacy.Attachments)
	}
	tm.sql(t, `UPDATE at_nodes SET last_hello = jsonb_set(last_hello, '{features}', (last_hello->'features') || '["attachments_v2"]'::jsonb)`)
	failed, reason := true, "transcript not found"
	tm.lead.send(protocol.TypeSessionOpened, protocol.SessionOpened{SessionID: protocol.SessionID(tm.lead.session), OpenGen: protocol.OpenGen(tm.lead.gen),
		ResumeFailed: &failed, Error: &reason})
	_, open := expectOpen(t, tm.w, tm.n, "lead-1")
	fm := open.FirstMessage
	if fm == nil || !sameIDs(refIDs(fm.Attachments), zip.ID) || len(fm.Images) != 0 || !strings.Contains(fm.Text, "数据包给你") ||
		strings.Contains(fm.Text, "请升级") || strings.Contains(fm.Text, "too old") {
		t.Fatalf("handed-off first message %+v", fm)
	}
}

// TestAttachmentToolRefusals: attachments of another project and expired ones are refused with
// an explanation; nothing is delivered.
func TestAttachmentToolRefusals(t *testing.T) {
	tm := attTeam(t, true)
	var other struct{ ID string }
	tm.b.must(201, "POST", "/api/projects", map[string]any{"name": "other"}, &other)
	foreign := putAttachment(t, tm, other.ID, "x.zip", "application/zip", []byte("PK"))
	r := tm.lead.call("assign_task", map[string]any{"assignee": "dev", "instruction": "x", "main": "做", "attachments": []string{foreign.ID}})
	if r.Ok || !strings.Contains(text(r), foreign.ID) || !strings.Contains(text(r), "is not in this project") {
		t.Fatalf("foreign attachment: ok=%v %q", r.Ok, text(r))
	}
	mine := putAttachment(t, tm, tm.projectID, "y.zip", "application/zip", []byte("PK"))
	tm.sql(t, `UPDATE at_attachments SET state = 'expired' WHERE id = $1`, mine.ID)
	r = tm.lead.call("assign_task", map[string]any{"assignee": "dev", "instruction": "y", "main": "做", "attachments": []string{mine.ID}})
	if r.Ok || !strings.Contains(text(r), "附件已过期") {
		t.Fatalf("expired attachment: ok=%v %q", r.Ok, text(r))
	}
	r = tm.lead.call("read_attachment", map[string]any{"id": mine.ID})
	if r.Ok || !strings.Contains(text(r), "附件已过期") {
		t.Fatalf("read expired: ok=%v %q", r.Ok, text(r))
	}
	var n int
	_ = store.QueryRow(tm.w.ctx, tm.w.e.Core.Pool, `SELECT count(*) FROM at_tasks WHERE conversation_id = $1`, tm.thread.ID).Scan(&n)
	if n != 0 {
		t.Fatalf("refused assign_task created %d tasks", n)
	}
}

// TestSubthreadAndRelayAttachments: open_subthread's brief brings the attachments to the new
// lead (and its chat shows them); the coordinator's message_thread and the lead's
// message_coordinator carry them too.
func TestSubthreadAndRelayAttachments(t *testing.T) {
	tm := attTeam(t, true)
	pdf := putAttachment(t, tm, tm.projectID, "spec.pdf", core.PDFType, []byte("%PDF-1.4 fake"))
	md := putAttachment(t, tm, tm.projectID, "todo.md", core.TextType, []byte("暗号 osprey-9"))
	r := tm.lead.mustCall("open_subthread", map[string]any{"title": "调研", "brief": "看规格书", "attachments": []string{pdf.ID, md.ID}})
	if !strings.Contains(text(r), "with 2 attachments") {
		t.Fatalf("open_subthread %q", text(r))
	}
	_, open := expectOpen(t, tm.w, tm.n, "lead-2")
	if open.FirstMessage == nil || !sameIDs(refIDs(open.FirstMessage.Attachments), pdf.ID, md.ID) || !strings.Contains(open.FirstMessage.Text, "看规格书") ||
		!strings.Contains(open.FirstMessage.Text, "osprey-9") {
		t.Fatalf("subthread brief %+v", open.FirstMessage)
	}
	var brief string
	if err := store.QueryRow(tm.w.ctx, tm.w.e.Core.Pool, `SELECT brief FROM at_threads WHERE project_id = $1 AND no = 2`, tm.projectID).Scan(&brief); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(brief, "看规格书") || strings.Contains(brief, "osprey-9") {
		t.Fatalf("stored subthread brief %q", brief)
	}

	conv := tm.coordinatorConv(t)
	_, coord := projectChat(t, tm, conv, map[string]any{"content": "你好"})
	r = coord.mustCall("message_thread", map[string]any{"no": tm.thread.No, "text": "用户补充了规格书", "attachments": []string{pdf.ID}})
	if !strings.Contains(text(r), "with 1 attachment") {
		t.Fatalf("message_thread %q", text(r))
	}
	send := tm.lead.expectSend("用户补充了规格书")
	if !sameIDs(refIDs(send.Attachments), pdf.ID) {
		t.Fatalf("lead got attachments %+v", send.Attachments)
	}
	found := false
	for _, m := range threadMessages(t, tm, tm.thread.ID) {
		if m.Kind == "relay" && strings.Contains(m.Content, "用户补充了规格书") && m.Meta["attachments"] != nil {
			found = true
		}
	}
	if !found {
		t.Fatal("relay card in the thread lacks meta.attachments")
	}

	tm.lead.mustCall("message_coordinator", map[string]any{"text": "规格书看完了", "attachments": []string{pdf.ID}})
	if s := coord.expectSend("规格书看完了"); !sameIDs(refIDs(s.Attachments), pdf.ID) {
		t.Fatalf("coordinator got attachments %+v", s.Attachments)
	}
}

// TestReadAttachment: read_attachment answers with a presigned download (never the content)
// on attachments_v2 daemons.
func TestReadAttachment(t *testing.T) {
	tm := attTeam(t, true)
	zip := putAttachment(t, tm, tm.projectID, "data.zip", "application/zip", []byte("PK\x03\x04 payload"))
	r := tm.lead.mustCall("read_attachment", map[string]any{"id": zip.ID})
	dl := r.AttachmentDownload
	if dl == nil || dl.URL == nil || !strings.HasPrefix(*dl.URL, "http") || dl.Attachment.ID != zip.ID || dl.Attachment.Size != zip.Size ||
		dl.ExpiresAt == nil || strings.Contains(text(r), "payload") {
		t.Fatalf("read_attachment result %+v %q", dl, text(r))
	}
	var out protocol.ReadAttachmentResult
	_ = json.Unmarshal(*r.Structured, &out)
	if out.LocalPath == "" || string(out.ID) != zip.ID {
		t.Fatalf("read_attachment structured %+v", out)
	}
}

// TestReadAttachmentOldDaemon: a daemon without attachments_v2 cannot download attachments.
func TestReadAttachmentOldDaemon(t *testing.T) {
	tm := attTeam(t, false)
	png := uploadPNG(t, tm)
	r := tm.lead.call("read_attachment", map[string]any{"id": png})
	if r.Ok || !strings.Contains(text(r), "too old") {
		t.Fatalf("old daemon read_attachment: ok=%v %q", r.Ok, text(r))
	}
}

// TestRepeatedDownloadCallsResign: a repeated read_attachment (the daemon reconnected and sent
// the call again) answers from the stored result, but with a freshly signed URL: the stored one
// may have expired.
func TestRepeatedDownloadCallsResign(t *testing.T) {
	tm := attTeam(t, true)
	zip := putAttachment(t, tm, tm.projectID, "data.zip", "application/zip", []byte("PK\x03\x04 payload"))
	stale := "http://stale.invalid/expired"
	retire := func(callID string) {
		t.Helper()
		tm.sql(t, `UPDATE at_node_commands SET status = 'expired' WHERE type = 'tool.result' AND body->>'call_id' = $1`, callID)
	}
	gen := protocol.OpenGen(tm.lead.gen)
	repeat := func(tool, callID string, args any) protocol.ToolResult {
		t.Helper()
		raw, _ := json.Marshal(args)
		tm.lead.send(protocol.TypeToolCall, protocol.ToolCall{CallID: protocol.CallID(callID), SessionID: protocol.SessionID(tm.lead.session),
			OpenGen: &gen, Tool: tool, Args: raw})
		return tm.lead.result(callID)
	}
	// Ages the stored result (its URL becomes one that no longer works) and retires its result
	// command, as when the daemon was away until the command expired: a live one is just replayed.
	age := func(callID, field string) {
		t.Helper()
		tm.sql(t, `UPDATE at_tool_calls SET result = jsonb_set(result, $3::text[], to_jsonb($4::text))
			WHERE session_id = $1 AND call_id = $2`, tm.lead.session, callID, field, stale)
		retire(callID)
	}

	first := repeat("read_attachment", "tc_ra_1", map[string]any{"id": zip.ID})
	if !first.Ok || first.AttachmentDownload == nil || first.AttachmentDownload.URL == nil {
		t.Fatalf("first read_attachment %+v", first)
	}
	age("tc_ra_1", "{attachment_download,url}")
	again := repeat("read_attachment", "tc_ra_1", map[string]any{"id": zip.ID})
	if d := again.AttachmentDownload; d == nil || d.URL == nil || *d.URL == stale || !strings.HasPrefix(*d.URL, "http") || d.Attachment.ID != zip.ID {
		t.Fatalf("repeated read_attachment not re-signed %+v", d)
	}
}

// TestForwardRestartsRetention: forwarding a message is a use of its attachments.
func TestForwardRestartsRetention(t *testing.T) {
	tm := attTeam(t, true)
	conv := tm.coordinatorConv(t)
	img := uploadPNG(t, tm)
	msgs, _ := projectChat(t, tm, conv, map[string]any{"content": "看截图", "attachments": []string{img}})
	tm.sql(t, `UPDATE at_attachments SET expires_at = now() + interval '1 hour' WHERE id = $1`, img)
	tm.b.must(200, "POST", "/api/conversations/"+conv+"/forward", map[string]any{"message_ids": []int64{msgs[0].ID}, "thread_no": tm.thread.No}, nil)
	send := tm.lead.expectSend("原样转发")
	if !sameIDs(refIDs(send.Attachments), img) || len(send.Images) != 0 {
		t.Fatalf("forward to v2 lead: attachments %+v images %+v", send.Attachments, send.Images)
	}
	if d := tm.expiresIn(t, img); d < 23*time.Hour {
		t.Fatalf("forward did not restart retention: %v", d)
	}
}

// TestReplyToAttachments: an answer to a waiting ask_lead cannot carry files, so it names them
// for read_attachment.
func TestReplyToAttachments(t *testing.T) {
	tm := attTeam(t, true)
	tm.lead.mustCall("assign_task", map[string]any{"assignee": "dev", "instruction": "做", "main": "做"})
	dev, open := expectOpen(t, tm.w, tm.n, "dev-1")
	dev.send(protocol.TypeSessionDelivered, protocol.SessionDelivered{SessionID: open.SessionID, MsgID: open.FirstMessage.MsgID})
	pdf := putAttachment(t, tm, tm.projectID, "spec.pdf", core.PDFType, []byte("%PDF-1.4"))
	call := dev.startCall("ask_lead", `{"question":"规格书在哪？"}`)
	q := tm.lead.expectSend("规格书在哪")
	qid := strings.Fields(q.Text)[1] // "[Question q1 from dev-1 …]"
	tm.lead.mustCall("reply_to", map[string]any{"question_id": qid, "answer": "看附件", "attachments": []string{pdf.ID}})
	res := dev.result(call)
	if !res.Ok || !strings.Contains(text(res), pdf.ID) || !strings.Contains(text(res), "read_attachment") {
		t.Fatalf("ask_lead answer %q", text(res))
	}
}

func sum256(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
