package engine_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw/nodetest"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// turnRunning reports the dev's turn as running and waits until the console saw it.
func (tm *team) turnRunning(t *testing.T, a *fakeAgent) {
	t.Helper()
	gen := protocol.OpenGen(a.gen)
	a.send(protocol.TypeSessionState, protocol.SessionState{SessionID: protocol.SessionID(a.session), OpenGen: &gen, State: "turn_running"})
	eventually(t, "turn running", func() bool {
		s, err := core.SessionByID(tm.w.ctx, tm.w.e.Core.Pool, a.session)
		return err == nil && s.State == "turn_running"
	})
}

// restarts numbers the epochs of restarted daemons: each restart is a new process (a
// reconnect with the same epoch is only a dropped link, whose frames dedupe by seq).
var restarts atomic.Int64

// restart drops the node link and connects the "restarted" daemon.
func (tm *team) restart(t *testing.T) *nodetest.Node {
	t.Helper()
	tm.n.Close()
	n2 := tm.w.connect(tm.token, nodetest.Options{Epoch: fmt.Sprintf("e-restarted-%d", restarts.Add(1))})
	tm.dev.node, tm.lead.node, tm.n = n2, n2, n2
	return n2
}

// report sends sessions.report with every session parked and nothing pending.
func (tm *team) report(t *testing.T) {
	t.Helper()
	items := []protocol.SessionReportItem{
		{SessionID: protocol.SessionID(tm.dev.session), OpenGen: protocol.OpenGen(tm.dev.gen), State: "parked", Kind: "claude"},
		{SessionID: protocol.SessionID(tm.lead.session), OpenGen: protocol.OpenGen(tm.lead.gen), State: "parked", Kind: "claude"},
	}
	tm.dev.send(protocol.TypeSessionsReport, protocol.SessionsReport{Sessions: items})
}

func askBash(a *fakeAgent, id string, input map[string]string) {
	gen := protocol.OpenGen(a.gen)
	raw, _ := json.Marshal(input)
	a.send(protocol.TypeApprovalRequest, protocol.ApprovalRequest{ApprovalID: protocol.ApprovalID(id), SessionID: protocol.SessionID(a.session),
		OpenGen: &gen, Source: "claude_can_use_tool", ToolName: "Bash", Input: raw,
		Available: []protocol.ApprovalDecision{"allow", "allow_always", "deny"}})
}

func (tm *team) askUserFirst(t *testing.T) {
	t.Helper()
	tm.b.must(200, "PATCH", "/api/projects/"+tm.projectID, map[string]any{"delivery": map[string]any{"approval_routing": "user"}}, nil)
}

func (tm *team) approvalCards(t *testing.T) []core.QuestionView {
	t.Helper()
	var cards, out []core.QuestionView
	tm.b.must(200, "GET", "/api/conversations/"+tm.thread.ID+"/questions?status=all", nil, &cards)
	for _, c := range cards {
		if c.Kind == "approval" {
			out = append(out, c)
		}
	}
	return out
}

// TestShutdownKeepsUserCard: the dev waits on the user's card when the daemon shuts down
// (approval.cancel process_restarted + parked, replayed after the restart). The card stays
// open, the dev is resumed with a note naming the request, its re-issued request (new
// approval_id, reworded description) is attached to the same card and the user's answer
// reaches it.
func TestShutdownKeepsUserCard(t *testing.T) {
	tm := startTeam(t)
	tm.askUserFirst(t)
	tm.turnRunning(t, tm.dev)
	askBash(tm.dev, "ap_before", map[string]string{"command": "make test", "description": "Run the tests"})
	eventually(t, "user card", func() bool { return len(tm.userCards(t)) == 1 })
	qid := tm.userCards(t)[0].ID

	tm.restart(t)
	sid := protocol.SessionID(tm.dev.session)
	gen := protocol.OpenGen(tm.dev.gen)
	tm.dev.send(protocol.TypeApprovalCancel, protocol.ApprovalCancel{ApprovalID: "ap_before", SessionID: &sid, Reason: "process_restarted"})
	tm.dev.send(protocol.TypeSessionState, protocol.SessionState{SessionID: sid, OpenGen: &gen, State: "parked"})
	tm.report(t)

	note := tm.dev.expectSend("守护进程重启")
	if !strings.Contains(note.Text, qid+" Bash make test") || !strings.Contains(note.Text, "原样重新发起") {
		t.Fatalf("resume note %q", note.Text)
	}
	cards := tm.approvalCards(t)
	if len(cards) != 1 || cards[0].Status != "open" || cards[0].Detail["detached_at"] == nil {
		t.Fatalf("card must stay open and detached: %+v", cards)
	}
	var th core.ThreadView
	tm.b.must(200, "GET", "/api/threads/"+tm.thread.ID, nil, &th)
	if th.Status != "waiting" {
		t.Fatalf("thread %s, want waiting on the card", th.Status)
	}

	tm.turnRunning(t, tm.dev)
	askBash(tm.dev, "ap_after", map[string]string{"command": "make test", "description": "Re-run the test suite"})
	eventually(t, "card reattached", func() bool {
		c := tm.card(t, qid)
		return c.Detail["detached_at"] == nil && c.Detail["reattached_at"] != nil
	})
	if cards := tm.approvalCards(t); len(cards) != 1 {
		t.Fatalf("re-issued request must reuse the card: %+v", cards)
	}
	tm.b.must(200, "POST", "/api/conversations/"+tm.thread.ID+"/questions/"+qid+"/answer", map[string]string{"answer": "allow"}, nil)
	if r := waitResolve(t, tm.w, tm.dev, "ap_after"); r.Decision != "allow" {
		t.Fatalf("resolve %+v", r)
	}
	ctx, cancel := context.WithTimeout(tm.w.ctx, time.Second)
	defer cancel()
	if f, err := tm.n.Wait(ctx, protocol.TypeSessionSend, func(f nodetest.Frame) bool {
		return tm.dev.mine(f) && strings.Contains(string(f.Body), "守护进程重启")
	}); err == nil {
		t.Fatalf("the restart must resume the dev once: %s", f.Body)
	}
}

// TestKilledDaemonLeadCard: the daemon died (no cancel frames); the report lacks the dev's
// request, which waited on the lead. The lead decides the detached card before the dev
// re-issues; the re-issued request gets that decision at once, on the same card.
func TestKilledDaemonLeadCard(t *testing.T) {
	tm := startTeam(t)
	tm.turnRunning(t, tm.dev)
	askBash(tm.dev, "ap_lead_before", map[string]string{"command": "npm test"})
	qid := leadRequest(t, tm.lead, "npm test")

	tm.restart(t)
	tm.report(t)
	tm.dev.expectSend("守护进程重启")
	eventually(t, "card detached", func() bool { return tm.card(t, qid).Detail["detached_at"] != nil })
	tm.lead.mustCall("review_permission", map[string]string{"request_id": qid, "decision": "allow", "reason": "跑测试"})

	askBash(tm.dev, "ap_lead_after", map[string]string{"command": "npm test"})
	if r := waitResolve(t, tm.w, tm.dev, "ap_lead_after"); r.Decision != "allow" || r.DecidedBy != "policy:rule:lead-review" {
		t.Fatalf("resolve %+v", r)
	}
	if cards := tm.approvalCards(t); len(cards) != 1 || cards[0].Status != "answered" {
		t.Fatalf("cards %+v", cards)
	}
}

// TestDetachedCardExpires: a request the resumed dev does not re-issue is cancelled when its
// turn ends; the thread stops waiting.
func TestDetachedCardExpires(t *testing.T) {
	tm := startTeam(t)
	tm.askUserFirst(t)
	tm.turnRunning(t, tm.dev)
	askBash(tm.dev, "ap_gone", map[string]string{"command": "make deploy"})
	eventually(t, "user card", func() bool { return len(tm.userCards(t)) == 1 })
	tm.restart(t)
	tm.report(t)
	tm.dev.expectSend("守护进程重启")
	if len(tm.userCards(t)) != 1 {
		t.Fatal("card must stay open until the dev's turn ends")
	}
	tm.dev.event("turn_ended", func(e *protocol.SessionEvent) {
		sub, isErr, turns, ms := "success", false, 1, 10
		usage := json.RawMessage(`{"input_tokens":10,"output_tokens":5}`)
		e.ResultSubtype, e.IsError, e.NumTurns, e.DurationMS, e.Usage = &sub, &isErr, &turns, &ms, &usage
	})
	eventually(t, "card cancelled", func() bool {
		c := tm.approvalCards(t)
		return len(c) == 1 && c[0].Status == "cancelled"
	})
	var th core.ThreadView
	tm.b.must(200, "GET", "/api/threads/"+tm.thread.ID, nil, &th)
	if th.Status != "running" {
		t.Fatalf("thread %s", th.Status)
	}
}

// TestRestartResumesBlockedAskUser: a dev blocked in ask_user is resumed after the restart and
// told its question is still open; the answer reaches it as a message.
func TestRestartResumesBlockedAskUser(t *testing.T) {
	tm := startTeam(t)
	tm.turnRunning(t, tm.dev)
	call := tm.dev.startCall("ask_user", `{"question":"用哪个数据库？"}`)
	tm.dev.noResult(call)
	tm.restart(t)
	sid := protocol.SessionID(tm.dev.session)
	reason := protocol.ToolCancelReason("process_restarted")
	tm.dev.send(protocol.TypeToolCancel, protocol.ToolCancel{CallID: protocol.CallID(call), SessionID: sid, Reason: &reason})
	gen := protocol.OpenGen(tm.dev.gen)
	tm.dev.send(protocol.TypeSessionState, protocol.SessionState{SessionID: sid, OpenGen: &gen, State: "parked"})
	tm.report(t)
	note := tm.dev.expectSend("守护进程重启")
	if !strings.Contains(note.Text, "用哪个数据库") || !strings.Contains(note.Text, "不要重复提问") {
		t.Fatalf("resume note %q", note.Text)
	}
	cards := tm.userCards(t)
	if len(cards) != 1 {
		t.Fatalf("question must stay open: %+v", cards)
	}
	tm.b.must(200, "POST", "/api/conversations/"+tm.thread.ID+"/questions/"+cards[0].ID+"/answer", map[string]string{"answer": "PostgreSQL"}, nil)
	tm.dev.expectSend("PostgreSQL")
}

// TestRestartLeadMidTurn: the lead's own turn was cut too; it gets the note as well.
func TestRestartLeadMidTurn(t *testing.T) {
	tm := startTeam(t)
	tm.turnRunning(t, tm.lead)
	tm.restart(t)
	tm.report(t)
	tm.lead.expectSend("守护进程重启")
}

// TestStalledThreadWakesLead: after a reconnect, a running thread with every employee idle,
// no open card and nothing on its way gets one note to its lead. The note is sent once per
// stall: the lead's reply does not re-arm it, nor do reconnect bursts or time alone; a user
// message to the thread (after the debounce) does.
func TestStalledThreadWakesLead(t *testing.T) {
	tm := startTeam(t)
	pool, ctx := tm.w.e.Core.Pool, tm.w.ctx
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := store.Exec(ctx, pool, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{
		`UPDATE at_messages SET delivered_at = now() WHERE delivered_at IS NULL`,
		`UPDATE at_agent_sessions SET state = 'active' WHERE state <> 'closed'`,
		`UPDATE at_threads SET status = 'running' WHERE id = '` + tm.thread.ID + `'`,
	} {
		exec(q)
	}
	tm.restart(t)
	tm.report(t)
	tm.lead.expectSend("没有成员在工作")
	settle := func() {
		t.Helper()
		exec(`UPDATE at_messages SET delivered_at = now() WHERE delivered_at IS NULL`)
		exec(`UPDATE at_agent_sessions SET state = 'active' WHERE state <> 'closed'`)
	}
	// The lead answers the note (its reply becomes the thread's latest message).
	exec(`INSERT INTO at_messages (conversation_id, role, author, content, delivered_at) VALUES ($1, 'leader', 'lead', '请在页面上关掉它', now())`, tm.thread.ID)
	reconnect := func() {
		t.Helper()
		settle()
		tm.restart(t)
		tm.report(t)
	}
	quiet := func(why string) {
		t.Helper()
		wctx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		if f, err := tm.n.Wait(wctx, protocol.TypeSessionSend, tm.lead.mine); err == nil {
			t.Fatalf("lead nudged again (%s): %s", why, f.Body)
		}
	}
	for i := 0; i < 3; i++ { // a burst of reconnects
		reconnect()
	}
	quiet("reconnect burst")
	// Long after, still nothing happened: no new note. (The setup's task events go, so that
	// the note can be moved an hour back without them counting as news.)
	exec(`DELETE FROM at_events WHERE thread_id = $1`, tm.thread.ID)
	exec(`UPDATE at_threads SET stalled_note_at = now() - interval '1 hour' WHERE id = $1`, tm.thread.ID)
	reconnect()
	quiet("nothing happened")
	var notes int
	if err := store.QueryRow(ctx, pool, `SELECT count(*) FROM at_messages WHERE conversation_id = $1 AND (meta->>'stalled')::boolean`, tm.thread.ID).Scan(&notes); err != nil || notes != 1 {
		t.Fatalf("stalled notes = %d (%v)", notes, err)
	}

	// The user writes to the thread: the next stall gets a note again.
	exec(`INSERT INTO at_messages (conversation_id, role, author, content, delivered_at) VALUES ($1, 'user', 'user', '还有一点', now())`, tm.thread.ID)
	reconnect()
	tm.lead.expectSend("没有成员在工作")
	// Within the debounce, even a new user message does not send another.
	exec(`INSERT INTO at_messages (conversation_id, role, author, content, delivered_at) VALUES ($1, 'user', 'user', '再补充', now())`, tm.thread.ID)
	reconnect()
	quiet("debounce")
}
