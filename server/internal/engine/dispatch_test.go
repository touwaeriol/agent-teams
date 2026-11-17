package engine_test

import (
	"encoding/json"
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

// TestDispatchSource: open_thread links the user's latest delivered project-chat message (or the
// one named by message_id), its image goes to the lead with the brief, the brief asks for the
// tell_user receipt, and the thread view carries dispatch {brief, opened_by, at, source}.
// A thread the user opened by hand has dispatch with a null source; an empty brief has none.
func TestDispatchSource(t *testing.T) {
	tm := startTeam(t)
	conv := tm.coordinatorConv(t)
	img := uploadPNG(t, tm)
	msgs, coord := projectChat(t, tm, conv, map[string]any{"content": "先看看登录页"})
	var latest core.MessageView
	tm.b.must(202, "POST", "/api/conversations/"+conv+"/messages", map[string]any{"content": "按截图修登录按钮", "attachments": []string{img}}, &latest)
	send := coord.expectSend("按截图修登录按钮")
	coord.send(protocol.TypeSessionDelivered, protocol.SessionDelivered{SessionID: protocol.SessionID(coord.session), MsgID: send.MsgID})
	eventually(t, "latest delivered", func() bool {
		var n int
		_ = store.QueryRow(tm.w.ctx, tm.w.e.Core.Pool, `SELECT count(*) FROM at_messages WHERE id = $1 AND delivered_at IS NOT NULL`, latest.ID).Scan(&n)
		return n == 1
	})
	// A message the coordinator has not received yet is not the one it acts on.
	var pending core.MessageView
	tm.b.must(202, "POST", "/api/conversations/"+conv+"/messages", map[string]any{"content": "还没送达的消息"}, &pending)

	res := coord.mustCall("open_thread", map[string]any{"title": "登录按钮", "brief": "修复登录按钮的样式"})
	var out protocol.OpenThreadResult
	_ = json.Unmarshal(*res.Structured, &out)
	_, open := expectOpen(t, tm.w, tm.n, "lead-2")
	first := open.FirstMessage
	for _, want := range []string{"[Thread #2 · 登录按钮]", "修复登录按钮的样式", "触发这个讨论串的用户原话", "按截图修登录按钮", "tell_user"} {
		if first == nil || !strings.Contains(first.Text, want) {
			t.Fatalf("brief lacks %q: %+v", want, first)
		}
	}
	if len(first.Images) != 1 || first.Images[0].ID != img {
		t.Fatalf("brief images %+v", first.Images)
	}
	v := getDispatch(t, tm, out.ThreadID)
	d := v.Dispatch
	if d == nil || d.Brief != "修复登录按钮的样式" || d.OpenedBy != "coordinator" || d.At.IsZero() || d.Source == nil {
		t.Fatalf("dispatch %+v", d)
	}
	if d.Source.MessageID != latest.ID || d.Source.Content != "按截图修登录按钮" || len(d.Source.Attachments) != 1 ||
		d.Source.Attachments[0].ID != img || d.Source.Attachments[0].ContentType != "image/png" || d.Source.CreatedAt.IsZero() {
		t.Fatalf("dispatch source %+v", d.Source)
	}

	// message_id names the message explicitly; a foreign id is refused.
	res = coord.mustCall("open_thread", map[string]any{"title": "看登录页", "brief": "调研登录页", "message_id": msgs[0].ID})
	_ = json.Unmarshal(*res.Structured, &out)
	if src := getDispatch(t, tm, out.ThreadID).Dispatch.Source; src == nil || src.MessageID != msgs[0].ID || len(src.Attachments) != 0 {
		t.Fatalf("explicit source %+v", src)
	}
	if raw := getDispatch(t, tm, out.ThreadID).raw["dispatch"].(map[string]any)["source"].(map[string]any)["attachments"]; raw == nil {
		t.Fatal("attachments must be [] not null")
	}
	if r := coord.call("open_thread", map[string]any{"title": "x", "brief": "y", "message_id": msgs[0].ID + 100000}); r.Ok {
		t.Fatalf("foreign message_id accepted: %q", text(r))
	}

	// A thread the user opened by hand (and older threads): dispatch without a source.
	h := getDispatch(t, tm, tm.thread.ID)
	if h.Dispatch == nil || h.Dispatch.Brief != "do it" || h.Dispatch.OpenedBy != "user" || h.Dispatch.Source != nil {
		t.Fatalf("manual thread dispatch %+v", h.Dispatch)
	}
	if src, ok := h.raw["dispatch"].(map[string]any)["source"]; !ok || src != nil {
		t.Fatalf("source must be null, got %v", h.raw["dispatch"])
	}
	// A deleted source message reads as null; an empty brief has no dispatch.
	if _, err := store.Exec(tm.w.ctx, tm.w.e.Core.Pool, `DELETE FROM at_messages WHERE id = $1`, latest.ID); err != nil {
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
