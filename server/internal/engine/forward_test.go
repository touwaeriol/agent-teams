package engine_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/httpapi"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw/nodetest"
)

type forwardView struct {
	ThreadNo  int                `json:"thread_no"`
	ThreadID  string             `json:"thread_id"`
	Count     int                `json:"count"`
	Created   bool               `json:"created"`
	Duplicate bool               `json:"duplicate"`
	Messages  []core.MessageView `json:"messages"`
}

// projectChat posts the user's messages to the coordinator (the first opens its session) and
// returns the stored messages and the coordinator.
func projectChat(t *testing.T, tm *team, conv string, bodies ...map[string]any) ([]core.MessageView, *fakeAgent) {
	t.Helper()
	var out []core.MessageView
	var coord *fakeAgent
	for i, body := range bodies {
		var m core.MessageView
		tm.b.must(202, "POST", "/api/conversations/"+conv+"/messages", body, &m)
		out = append(out, m)
		tag := fmt.Sprintf("[message_id: %d]", m.ID)
		if i == 0 {
			var open protocol.SessionOpen
			coord, open = expectOpen(t, tm.w, tm.n, "coordinator")
			if !strings.HasSuffix(open.FirstMessage.Text, tag) {
				t.Fatalf("coordinator first message %q lacks %s", open.FirstMessage.Text, tag)
			}
			coord.send(protocol.TypeSessionDelivered, protocol.SessionDelivered{SessionID: open.SessionID, MsgID: open.FirstMessage.MsgID})
			continue
		}
		coord.expectSend(tag)
	}
	return out, coord
}

func threadMessages(t *testing.T, tm *team, conv string) []core.MessageView {
	t.Helper()
	var page struct{ Items []core.MessageView }
	tm.b.must(200, "GET", "/api/conversations/"+conv+"/messages", nil, &page)
	return page.Items
}

func forwardedOf(m core.MessageView) map[string]any {
	f, _ := m.Meta["forwarded_from"].(map[string]any)
	return f
}

// TestForwardFromProjectChat: the user forwards their own project-chat messages (text, an
// image, an @ reference) to a thread: the copies are user messages with meta.forwarded_from,
// the lead gets them as the user's words with the image, the project chat gets a note, a
// double click or a repeated Idempotency-Key sends nothing new, and only the caller's own
// messages of that project chat can be forwarded.
func TestForwardFromProjectChat(t *testing.T) {
	tm := startTeam(t)
	conv := tm.coordinatorConv(t)
	img := uploadPNG(t, tm)
	msgs, _ := projectChat(t, tm, conv,
		map[string]any{"content": "登录页按钮颜色不对，看截图", "attachments": []string{img}},
		map[string]any{"content": "参考这个讨论串", "refs": []map[string]any{{"kind": "thread", "no": tm.thread.No}}})
	ids := []int64{msgs[1].ID, msgs[0].ID}

	var fv forwardView
	tm.b.must(200, "POST", "/api/conversations/"+conv+"/forward", map[string]any{"message_ids": ids, "thread_no": tm.thread.No}, &fv)
	if fv.ThreadNo != tm.thread.No || fv.Count != 2 || fv.Created || fv.Duplicate || len(fv.Messages) != 2 {
		t.Fatalf("forward = %+v", fv)
	}
	send := tm.lead.expectSend("原样转发")
	for _, want := range []string{"用户本人的原话", "登录页按钮颜色不对，看截图", "参考这个讨论串", "[用户用 @ 引用的上下文]", "### 讨论串 #1", "a.png"} {
		if !strings.Contains(send.Text, want) {
			t.Fatalf("lead got %q, missing %q", send.Text, want)
		}
	}
	if strings.Index(send.Text, "登录页") > strings.Index(send.Text, "参考这个") || len(send.Images) != 1 || send.Images[0].ID != img {
		t.Fatalf("lead got %q images %+v", send.Text, send.Images)
	}

	var copies []core.MessageView
	for _, m := range threadMessages(t, tm, tm.thread.ID) {
		if forwardedOf(m) != nil {
			copies = append(copies, m)
		}
	}
	if len(copies) != 2 || copies[0].Role != "user" || copies[0].Content != msgs[0].Content || copies[1].Content != msgs[1].Content {
		t.Fatalf("thread copies %+v", copies)
	}
	att, _ := copies[0].Meta["attachments"].([]any)
	if len(att) != 1 || copies[1].Meta["refs"] == nil {
		t.Fatalf("copies lost attachments or refs: %+v", copies)
	}
	if f := forwardedOf(copies[1]); f["conversation_id"] != conv || f["count"] != float64(2) || f["index"] != float64(1) || f["by"] != "user" ||
		f["message_id"] != float64(msgs[1].ID) {
		t.Fatalf("forwarded_from %+v", f)
	}
	notes := 0
	for _, m := range threadMessages(t, tm, conv) {
		if to, ok := m.Meta["forwarded_to"].(map[string]any); ok && to["no"] == float64(tm.thread.No) && strings.Contains(m.Content, "已转发 2 条消息到 #1") {
			notes++
		}
	}
	if notes != 1 {
		t.Fatalf("project chat notes = %d", notes)
	}

	// A double click (no key) and a repeated Idempotency-Key send nothing new.
	tm.b.must(200, "POST", "/api/conversations/"+conv+"/forward", map[string]any{"message_ids": ids, "thread_no": tm.thread.No}, &fv)
	if !fv.Duplicate || fv.ThreadNo != tm.thread.No || fv.Count != 2 {
		t.Fatalf("double click = %+v", fv)
	}
	expectNone(t, tm.n, protocol.TypeSessionSend, 300*time.Millisecond, func(f nodetest.Frame) bool { return strings.Contains(string(f.Body), "原样转发") })
	keyed := func() forwardView {
		var out forwardView
		code, raw := tm.b.sendWith("POST", "/api/conversations/"+conv+"/forward", map[string]any{"message_ids": []int64{msgs[0].ID}, "thread_no": tm.thread.No},
			map[string]string{"Idempotency-Key": "fwd-1"})
		if code != 200 {
			t.Fatalf("keyed forward = %d %s", code, raw)
		}
		_ = json.Unmarshal(raw, &out)
		return out
	}
	first, second := keyed(), keyed()
	if first.Duplicate || first.Count != 1 || second.Messages[0].ID != first.Messages[0].ID {
		t.Fatalf("idempotent forward %+v / %+v", first, second)
	}
	tm.lead.expectSend("登录页按钮颜色不对")

	// Only the user's own messages of this project chat.
	var lastNote int64
	for _, m := range threadMessages(t, tm, conv) {
		if m.Role == "system" {
			lastNote = m.ID
		}
	}
	for _, bad := range [][]int64{{lastNote}, {copies[0].ID}, {msgs[0].ID, 999999}} {
		if code, raw := tm.b.do("POST", "/api/conversations/"+conv+"/forward", map[string]any{"message_ids": bad, "thread_no": tm.thread.No}); code != 422 {
			t.Fatalf("forward %v = %d %s", bad, code, raw)
		}
	}
	if code, _ := tm.b.do("POST", "/api/conversations/"+tm.thread.ID+"/forward", map[string]any{"message_ids": ids, "thread_no": tm.thread.No}); code != 422 {
		t.Fatalf("forward from a thread conversation = %d", code)
	}
	if code, _ := tm.b.do("POST", "/api/conversations/"+conv+"/forward", map[string]any{"message_ids": ids, "thread_no": 99}); code != 404 {
		t.Fatalf("forward to a missing thread = %d", code)
	}
	if code, _ := tm.b.do("POST", "/api/conversations/"+conv+"/forward", map[string]any{"message_ids": ids}); code != 422 {
		t.Fatalf("forward without a target = %d", code)
	}

	// Another user: not their conversation; their own project chat cannot reach these messages.
	other := tm.w.signUp("other@example.com")
	if code, _ := other.do("POST", "/api/conversations/"+conv+"/forward", map[string]any{"message_ids": ids, "thread_no": tm.thread.No}); code != 404 {
		t.Fatalf("other user's forward = %d", code)
	}
	var p struct{ ID string }
	other.must(201, "POST", "/api/projects", map[string]any{"name": "q"}, &p)
	var coord struct {
		ConversationID string `json:"conversation_id"`
	}
	other.must(200, "GET", "/api/projects/"+p.ID+"/coordinator", nil, &coord)
	if code, _ := other.do("POST", "/api/conversations/"+coord.ConversationID+"/forward", map[string]any{"message_ids": ids, "new_thread": map[string]any{}}); code != 422 {
		t.Fatalf("forwarding someone else's messages = %d", code)
	}
}

// TestForwardLifecycle: a finished thread is reopened by a forward (the lead resumes with the
// messages), an archived one refuses with a hint, and new_thread opens a thread whose brief
// is the messages, with their image.
func TestForwardLifecycle(t *testing.T) {
	tm := startTeam(t)
	conv := tm.coordinatorConv(t)
	img := uploadPNG(t, tm)
	msgs, _ := projectChat(t, tm, conv, map[string]any{"content": "再加一个暗色模式", "attachments": []string{img}}, map[string]any{"content": "顺便改字号"})

	tm.b.must(200, "POST", "/api/threads/"+tm.thread.ID+"/complete", map[string]any{}, nil)
	eventually(t, "thread done", func() bool {
		th, err := core.ThreadByID(tm.w.ctx, tm.w.e.Core.Pool, tm.thread.ID)
		return err == nil && th.Status == "done"
	})
	tm.b.must(200, "POST", "/api/conversations/"+conv+"/forward", map[string]any{"message_ids": []int64{msgs[0].ID}, "thread_no": tm.thread.No}, nil)
	reopened(t, tm, "lead-1", tm.lead.session, "再加一个暗色模式")
	th, _ := core.ThreadByID(tm.w.ctx, tm.w.e.Core.Pool, tm.thread.ID)
	if th.Status != "running" {
		t.Fatalf("forward left the thread %s", th.Status)
	}

	tm.b.must(200, "POST", "/api/threads/"+tm.thread.ID+"/archive", nil, nil)
	code, raw := tm.b.do("POST", "/api/conversations/"+conv+"/forward", map[string]any{"message_ids": []int64{msgs[1].ID}, "thread_no": tm.thread.No, "note": "x"})
	if code != 409 || !strings.Contains(string(raw), "取消归档") {
		t.Fatalf("forward to an archived thread = %d %s", code, raw)
	}

	var fv forwardView
	tm.b.must(200, "POST", "/api/conversations/"+conv+"/forward", map[string]any{"message_ids": []int64{msgs[0].ID}, "new_thread": map[string]any{}}, &fv)
	if !fv.Created || fv.ThreadNo != 2 || len(fv.Messages) != 1 {
		t.Fatalf("new thread forward %+v", fv)
	}
	_, open := expectOpen(t, tm.w, tm.n, "lead-2")
	if open.FirstMessage == nil || !strings.Contains(open.FirstMessage.Text, "[Thread #2 · 再加一个暗色模式]") ||
		!strings.Contains(open.FirstMessage.Text, "用户本人的原话") || len(open.FirstMessage.Images) != 1 || open.FirstMessage.Images[0].ID != img {
		t.Fatalf("new thread brief %+v", open.FirstMessage)
	}
	tm.b.must(200, "POST", "/api/conversations/"+conv+"/forward", map[string]any{"message_ids": []int64{msgs[0].ID}, "new_thread": map[string]any{}}, &fv)
	if !fv.Duplicate || fv.ThreadNo != 2 {
		t.Fatalf("double click opened another thread: %+v", fv)
	}
}

// TestForwardToThreadTool: the coordinator forwards the user's latest message (or named ids)
// with a note; refusals (archived, foreign ids, both targets) are isError results.
func TestForwardToThreadTool(t *testing.T) {
	tm := startTeam(t)
	conv := tm.coordinatorConv(t)
	msgs, coord := projectChat(t, tm, conv, map[string]any{"content": "表单校验要支持手机号"}, map[string]any{"content": "还有邮箱"})

	res := coord.mustCall("forward_to_thread", map[string]any{"thread_no": tm.thread.No, "latest": 2, "note": "用户对登录页的补充"})
	var out protocol.ForwardToThreadResult
	_ = json.Unmarshal(*res.Structured, &out)
	if int(out.No) != tm.thread.No || out.Count != 2 || out.Created || !strings.Contains(text(res), "原样转给 #1") {
		t.Fatalf("tool result %+v %q", out, text(res))
	}
	send := tm.lead.expectSend("项目统筹附言：用户对登录页的补充")
	if !strings.Contains(send.Text, "表单校验要支持手机号") || !strings.Contains(send.Text, "还有邮箱") || strings.Contains(send.Text, "[message_id:") {
		t.Fatalf("lead got %q", send.Text)
	}
	last := threadMessages(t, tm, tm.thread.ID)
	if f := forwardedOf(last[len(last)-1]); f["by"] != "coordinator" || f["note"] != "用户对登录页的补充" {
		t.Fatalf("forwarded_from %+v", f)
	}

	for _, args := range []map[string]any{
		{"thread_no": tm.thread.No, "message_ids": []int64{msgs[0].ID + 1000}},
		{"thread_no": tm.thread.No, "new_thread": map[string]any{"title": "x"}},
		{"thread_no": 42},
	} {
		if r := coord.call("forward_to_thread", args); r.Ok {
			t.Fatalf("forward_to_thread %v succeeded: %q", args, text(r))
		}
	}
	tm.b.must(200, "POST", "/api/threads/"+tm.thread.ID+"/archive", nil, nil)
	r := coord.call("forward_to_thread", map[string]any{"thread_no": tm.thread.No, "message_ids": []int64{msgs[0].ID}})
	if r.Ok || !strings.Contains(text(r), "取消归档") || !strings.Contains(text(r), "new_thread") {
		t.Fatalf("archived forward = %v %q", r.Ok, text(r))
	}
	res = coord.mustCall("forward_to_thread", map[string]any{"new_thread": map[string]any{"title": "手机号登录"}, "message_ids": []int64{msgs[0].ID}})
	_ = json.Unmarshal(*res.Structured, &out)
	if !out.Created || out.No != 2 {
		t.Fatalf("new thread via tool %+v", out)
	}
	_, open := expectOpen(t, tm.w, tm.n, "lead-2")
	if !strings.Contains(open.FirstMessage.Text, "[Thread #2 · 手机号登录]") || !strings.Contains(open.FirstMessage.Text, "表单校验要支持手机号") {
		t.Fatalf("brief %q", open.FirstMessage.Text)
	}
}

// sendWith is do with extra request headers.
func (b *browser) sendWith(method, path string, body any, headers map[string]string) (int, []byte) {
	b.w.t.Helper()
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(method, b.w.srv.URL+path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-For", b.ip)
	req.Header.Set(httpapi.CSRFHeader, b.csrf())
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := b.http.Do(req)
	if err != nil {
		b.w.t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}
