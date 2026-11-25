package engine_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

type dispatchThread struct {
	ID       string               `json:"id"`
	Dispatch *core.ThreadDispatch `json:"dispatch"`
	raw      map[string]any
}

func getDispatch(t *testing.T, tm *team, id string) dispatchThread {
	t.Helper()
	var raw json.RawMessage
	tm.b.must(200, "GET", "/api/threads/"+id, nil, &raw)
	var out dispatchThread
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(raw, &out.raw)
	return out
}

// deliveredChat posts a user message to the coordinator and has the node confirm it.
func deliveredChat(t *testing.T, tm *team, coord *fakeAgent, conv string, body map[string]any) core.MessageView {
	t.Helper()
	var m core.MessageView
	tm.b.must(202, "POST", "/api/conversations/"+conv+"/messages", body, &m)
	confirm(t, tm, coord, body["content"].(string), m.ID)
	return m
}

// confirm acks the coordinator's delivery of a message and waits until it is stamped.
func confirm(t *testing.T, tm *team, coord *fakeAgent, needle string, id int64) {
	t.Helper()
	send := coord.expectSend(needle)
	coord.send(protocol.TypeSessionDelivered, protocol.SessionDelivered{SessionID: protocol.SessionID(coord.session), MsgID: send.MsgID})
	eventually(t, "delivered", func() bool {
		var n int
		_ = store.QueryRow(tm.w.ctx, tm.w.e.Core.Pool, `SELECT count(*) FROM at_messages WHERE id = $1 AND delivered_at IS NOT NULL`, id).Scan(&n)
		return n == 1
	})
}

// coordinatorReply ends a coordinator turn with a final text (a role=leader message).
func coordinatorReply(t *testing.T, tm *team, coord *fakeAgent, conv, text string) int64 {
	t.Helper()
	coord.event("text", func(ev *protocol.SessionEvent) { f := true; ev.Text, ev.Final = &text, &f })
	var id int64
	eventually(t, "coordinator reply", func() bool {
		_ = store.QueryRow(tm.w.ctx, tm.w.e.Core.Pool, `SELECT COALESCE(max(id), 0) FROM at_messages WHERE conversation_id = $1 AND role = 'leader' AND content = $2`, conv, text).Scan(&id)
		return id > 0
	})
	return id
}

func openThread(t *testing.T, coord *fakeAgent, args map[string]any) protocol.OpenThreadResult {
	t.Helper()
	var out protocol.OpenThreadResult
	_ = json.Unmarshal(*coord.mustCall("open_thread", args).Structured, &out)
	return out
}

// TestDispatchSource: without message_id, open_thread links the user's newest own message
// delivered after the coordinator's previous reply (none → null, no images to the lead;
// scheduled posts never count); its image goes to the lead with the brief, which asks for the
// tell_user receipt. The thread view carries dispatch {opened_by, at, source} (no brief: that is
// thread.brief). message_id must be the user's own message of this project chat. A thread the
// user opened by hand has a null source; a thread opened by a forward and an empty brief have no
// dispatch.
func TestDispatchSource(t *testing.T) {
	tm := startTeam(t)
	conv := tm.coordinatorConv(t)
	img := uploadPNG(t, tm)
	msgs, coord := projectChat(t, tm, conv, map[string]any{"content": "先看看登录页"})
	replyID := coordinatorReply(t, tm, coord, conv, "好的，我先看看。")

	// The coordinator acts after its reply with no new user message (a thread report, say): no source.
	out := openThread(t, coord, map[string]any{"title": "巡检", "brief": "例行巡检"})
	_, open := expectOpen(t, tm.w, tm.n, "lead-2")
	if strings.Contains(open.FirstMessage.Text, "用户原话") || len(open.FirstMessage.Images) != 0 {
		t.Fatalf("brief without source %+v", open.FirstMessage)
	}
	v := getDispatch(t, tm, out.ThreadID)
	if v.Dispatch == nil || v.Dispatch.OpenedBy != "coordinator" || v.Dispatch.At.IsZero() || v.Dispatch.Source != nil {
		t.Fatalf("dispatch without source %+v", v.Dispatch)
	}
	if _, has := v.raw["dispatch"].(map[string]any)["brief"]; has || v.raw["brief"] != "例行巡检" {
		t.Fatalf("dispatch must not repeat the brief: %v", v.raw["dispatch"])
	}

	// The user's newest delivered message after the reply; one still in flight does not count.
	latest := deliveredChat(t, tm, coord, conv, map[string]any{"content": "按截图修登录按钮", "attachments": []string{img}})
	var pending core.MessageView
	tm.b.must(202, "POST", "/api/conversations/"+conv+"/messages", map[string]any{"content": "还没送达的消息"}, &pending)
	out = openThread(t, coord, map[string]any{"title": "登录按钮", "brief": "修复登录按钮的样式"})
	_, open = expectOpen(t, tm.w, tm.n, "lead-3")
	first := open.FirstMessage
	for _, want := range []string{"[Thread #3 · 登录按钮]", "修复登录按钮的样式", "触发这个讨论串的用户原话", "按截图修登录按钮", "tell_user"} {
		if first == nil || !strings.Contains(first.Text, want) {
			t.Fatalf("brief lacks %q: %+v", want, first)
		}
	}
	if len(first.Images) != 1 || first.Images[0].ID != img {
		t.Fatalf("brief images %+v", first.Images)
	}
	v = getDispatch(t, tm, out.ThreadID)
	d := v.Dispatch
	if d == nil || d.Source == nil || d.Source.MessageID != latest.ID || d.Source.Content != "按截图修登录按钮" || len(d.Source.Attachments) != 1 ||
		d.Source.Attachments[0].ID != img || d.Source.Attachments[0].ContentType != "image/png" || d.Source.CreatedAt.IsZero() {
		t.Fatalf("dispatch %+v", d)
	}

	// A scheduled-task post (role user, written by the platform) is never the source.
	confirm(t, tm, coord, "还没送达的消息", pending.ID)
	coordinatorReply(t, tm, coord, conv, "收到。")
	var owner string
	_ = store.QueryRow(tm.w.ctx, tm.w.e.Core.Pool, `SELECT user_id FROM at_projects WHERE id = $1`, tm.projectID).Scan(&owner)
	post, err := tm.w.e.Orch.PostToCoordinator(tm.w.ctx, owner, tm.projectID, "【定时任务：日报】\n汇总今天的进展", map[string]any{"schedule_id": "sch_1"})
	if err != nil {
		t.Fatal(err)
	}
	confirm(t, tm, coord, "汇总今天的进展", post.ID)
	out = openThread(t, coord, map[string]any{"title": "日报", "brief": "汇总进展"})
	if src := getDispatch(t, tm, out.ThreadID).Dispatch.Source; src != nil {
		t.Fatalf("scheduled post became the source: %+v", src)
	}

	// message_id names the message explicitly.
	out = openThread(t, coord, map[string]any{"title": "看登录页", "brief": "调研登录页", "message_id": msgs[0].ID})
	v = getDispatch(t, tm, out.ThreadID)
	if src := v.Dispatch.Source; src == nil || src.MessageID != msgs[0].ID || len(src.Attachments) != 0 {
		t.Fatalf("explicit source %+v", src)
	}
	if raw := v.raw["dispatch"].(map[string]any)["source"].(map[string]any)["attachments"]; raw == nil {
		t.Fatal("attachments must be [] not null")
	}
	// ...and must be the user's own words in this project chat.
	var note, leadMsg int64
	_ = store.QueryRow(tm.w.ctx, tm.w.e.Core.Pool, `SELECT max(id) FROM at_messages WHERE conversation_id = $1 AND role = 'system'`, conv).Scan(&note)
	tm.lead.mustCall("tell_user", map[string]any{"text": "收到"})
	_ = store.QueryRow(tm.w.ctx, tm.w.e.Core.Pool, `SELECT max(id) FROM at_messages WHERE conversation_id = $1 AND role = 'leader'`, tm.thread.ID).Scan(&leadMsg)
	var other struct{ ID string }
	tm.b.must(201, "POST", "/api/projects", map[string]any{"name": "q"}, &other)
	_, otherConv, err := tm.w.e.Orch.EnsureCoordinator(tm.w.ctx, owner, other.ID)
	if err != nil {
		t.Fatal(err)
	}
	var foreign int64
	_ = store.QueryRow(tm.w.ctx, tm.w.e.Core.Pool, `INSERT INTO at_messages (conversation_id, role, author, kind, content, meta)
		VALUES ($1, 'user', 'user', 'chat', '另一个项目的话', '{}') RETURNING id`, otherConv).Scan(&foreign)
	for what, id := range map[string]int64{"other project": foreign, "coordinator reply": replyID, "coordinator note": note,
		"lead message": leadMsg, "scheduled post": post.ID, "missing": msgs[0].ID + 100000} {
		if id == 0 {
			t.Fatalf("no id for %s", what)
		}
		if r := coord.call("open_thread", map[string]any{"title": "x", "brief": "y", "message_id": id}); r.Ok || !strings.Contains(text(r), "不是用户在项目对话里发的消息") {
			t.Fatalf("message_id of %s (%d) = %v %q", what, id, r.Ok, text(r))
		}
	}

	// Opened by a forward: the copies are in the conversation, no dispatch.
	var fv forwardView
	tm.b.must(200, "POST", "/api/conversations/"+conv+"/forward", map[string]any{"message_ids": []int64{latest.ID}, "new_thread": map[string]any{}}, &fv)
	if f := getDispatch(t, tm, fv.ThreadID); !fv.Created || f.Dispatch != nil || f.raw["dispatch"] != nil {
		t.Fatalf("forwarded thread dispatch %+v (%+v)", f.Dispatch, fv)
	}

	// A thread the user opened by hand (and older threads): dispatch without a source.
	h := getDispatch(t, tm, tm.thread.ID)
	if h.Dispatch == nil || h.Dispatch.OpenedBy != "user" || h.Dispatch.Source != nil {
		t.Fatalf("manual thread dispatch %+v", h.Dispatch)
	}
	if src, ok := h.raw["dispatch"].(map[string]any)["source"]; !ok || src != nil {
		t.Fatalf("source must be null, got %v", h.raw["dispatch"])
	}
	// A deleted source message reads as null; an empty brief has no dispatch.
	if _, err := store.Exec(tm.w.ctx, tm.w.e.Core.Pool, `DELETE FROM at_messages WHERE id = $1`, msgs[0].ID); err != nil {
		t.Fatal(err)
	}
	if d := getDispatch(t, tm, v.ID).Dispatch; d == nil || d.Source != nil {
		t.Fatalf("deleted source %+v", d)
	}
	if _, err := store.Exec(tm.w.ctx, tm.w.e.Core.Pool, `UPDATE at_threads SET brief = '' WHERE id = $1`, tm.thread.ID); err != nil {
		t.Fatal(err)
	}
	if h := getDispatch(t, tm, tm.thread.ID); h.Dispatch != nil {
		t.Fatalf("empty brief dispatch %+v", h.Dispatch)
	}
}

// TestTellUser: the lead's tell_user posts a leader message in the thread conversation right
// away (and emits it); members cannot call it and the text has a length cap.
func TestTellUser(t *testing.T) {
	tm := startTeam(t)
	events := tm.b.stream("/api/projects/"+tm.projectID+"/events", "0")
	tm.lead.mustCall("tell_user", map[string]any{"text": "收到：我理解要修登录页，拆成 server 和 web 两块，需要你决定配色。"})
	events.wait(t, "message", `"role":"leader"`, "需要你决定配色")
	list := threadMessages(t, tm, tm.thread.ID)
	last := list[len(list)-1]
	if last.Role != "leader" || last.Author != "lead-1" || !strings.Contains(last.Content, "需要你决定配色") {
		t.Fatalf("last message %+v", last)
	}
	if r := tm.dev.call("tell_user", map[string]any{"text": "hi"}); r.Ok {
		t.Fatal("member called tell_user")
	}
	if r := tm.lead.call("tell_user", map[string]any{"text": strings.Repeat("长", 2001)}); r.Ok {
		t.Fatal("over-long tell_user accepted")
	}
}

// TestDispatchSourceTextAttachment: a text attachment of the source message reaches the lead
// inlined into the quoted message (name and content), as a normal post would; images and PDFs
// still go as files, and the text file is not sent twice.
func TestDispatchSourceTextAttachment(t *testing.T) {
	tm := startTeam(t)
	conv := tm.coordinatorConv(t)
	img := uploadPNG(t, tm)
	var note struct{ ID string }
	tm.b.must(201, "POST", "/api/projects/"+tm.projectID+"/attachments",
		map[string]string{"name": "需求.md", "data": base64.StdEncoding.EncodeToString([]byte("# 需求\n按钮改成蓝色"))}, &note)
	msgs, coord := projectChat(t, tm, conv, map[string]any{"content": "你好"},
		map[string]any{"content": "照附件改", "attachments": []string{note.ID, img}},
		map[string]any{"content": "", "attachments": []string{note.ID}})
	for i, m := range msgs[1:] {
		openThread(t, coord, map[string]any{"title": fmt.Sprintf("改按钮%d", i), "brief": "按需求改按钮", "message_id": m.ID})
		_, open := expectOpen(t, tm.w, tm.n, fmt.Sprintf("lead-%d", i+2))
		first := open.FirstMessage
		for _, want := range []string{"触发这个讨论串的用户原话", "[附件：需求.md]", "按钮改成蓝色"} {
			if first == nil || !strings.Contains(first.Text, want) {
				t.Fatalf("brief %d lacks %q: %+v", i, want, first)
			}
		}
		if i == 0 && (!strings.Contains(first.Text, "照附件改") || len(first.Images) != 1 || first.Images[0].ID != img) {
			t.Fatalf("brief with image %+v", first)
		}
		if i == 1 && len(first.Images) != 0 {
			t.Fatalf("text attachment sent as a file %+v", first.Images)
		}
	}
}
