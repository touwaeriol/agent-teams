package engine_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/approvals"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw/nodetest"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// team is a running thread with a lead and dev-1 working on t1 (project without a repository).
type team struct {
	w         *world
	b         *browser
	n         *nodetest.Node
	lead, dev *fakeAgent
	projectID string
	thread    core.ThreadView
	token     string
}

func startTeam(t *testing.T) *team {
	t.Helper()
	w := newWorld(t, nil)
	tm := &team{w: w, b: w.signUp("team@example.com")}
	var node nodeCreated
	tm.b.must(201, "POST", "/api/nodes", map[string]string{"name": "fake"}, &node)
	tm.token = node.Token
	tm.b.must(200, "GET", "/api/roles", nil, nil)
	askMembers(t, w)
	var project struct{ ID string }
	tm.b.must(201, "POST", "/api/projects", map[string]any{"name": "p"}, &project)
	tm.projectID = project.ID
	tm.n = w.connect(node.Token, nodetest.Options{})
	tm.b.must(201, "POST", "/api/projects/"+project.ID+"/threads", map[string]any{"title": "Work", "brief": "do it"}, &tm.thread)
	var leadOpen protocol.SessionOpen
	tm.lead, leadOpen = expectOpen(t, w, tm.n, "lead-1")
	tm.lead.mustCall("update_plan", map[string]any{"steps": []map[string]string{{"title": "做", "status": "todo"}}})
	tm.lead.mustCall("assign_task", map[string]any{"assignee": "dev", "instruction": "do t1", "main": "做"})
	var open protocol.SessionOpen
	tm.dev, open = expectOpen(t, w, tm.n, "dev-1")
	for _, o := range []protocol.SessionOpen{leadOpen, open} {
		if o.Claude == nil || o.Claude.SystemAppend == nil || !strings.Contains(*o.Claude.SystemAppend, "## 回复语言") {
			t.Fatal("session prompt lacks the reply-language rule")
		}
	}
	tm.dev.send(protocol.TypeSessionDelivered, protocol.SessionDelivered{SessionID: protocol.SessionID(tm.dev.session), MsgID: open.FirstMessage.MsgID})
	return tm
}

func (a *fakeAgent) startCall(tool string, args string) string {
	callID := "tc_" + tool + "_" + time.Now().Format("150405.000000")
	gen := protocol.OpenGen(a.gen)
	a.send(protocol.TypeToolCall, protocol.ToolCall{CallID: protocol.CallID(callID), SessionID: protocol.SessionID(a.session), OpenGen: &gen,
		Tool: tool, Args: json.RawMessage(args)})
	return callID
}

func (a *fakeAgent) noResult(callID string) {
	a.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	if _, err := a.node.Wait(ctx, protocol.TypeToolResult, func(f nodetest.Frame) bool { return strings.Contains(string(f.Body), callID) }); err == nil {
		a.t.Fatalf("blocking call %s answered too early", callID)
	}
}

// TestBlockingQuestions: a member's ask_user blocks until the user answers through the API;
// ask_lead blocks until the lead's reply_to; the lead's ask_user does not block and the answer
// comes back as "[Answer to qN]"; wait_task returns when the subtask finishes.
func TestBlockingQuestions(t *testing.T) {
	tm := startTeam(t)
	ask := tm.dev.startCall("ask_user", `{"question":"用哪个 UI 库？","options":["A","B"]}`)
	tm.dev.noResult(ask)
	var cards []core.QuestionView
	tm.b.must(200, "GET", "/api/projects/"+tm.projectID+"/waiting", nil, &struct {
		Questions *[]core.QuestionView `json:"questions"`
	}{&cards})
	if len(cards) != 1 || !cards[0].Blocking || cards[0].Asker != "dev-1" {
		t.Fatalf("waiting %+v", cards)
	}
	var coord struct {
		ConversationID string `json:"conversation_id"`
	}
	tm.b.must(200, "GET", "/api/projects/"+tm.projectID+"/coordinator", nil, &coord)
	eventually(t, "coordinator told the thread waits on the user", func() bool {
		_, msgs := tm.b.do("GET", "/api/conversations/"+coord.ConversationID+"/messages", nil)
		return strings.Contains(string(msgs), "· waiting]")
	})
	tm.b.must(200, "POST", "/api/conversations/"+tm.thread.ID+"/questions/"+cards[0].ID+"/answer", map[string]string{"answer": "B"}, nil)
	if res := tm.dev.result(ask); !res.Ok || text(res) != "B" {
		t.Fatalf("ask_user result %+v", res)
	}
	code, _ := tm.b.do("POST", "/api/conversations/"+tm.thread.ID+"/questions/"+cards[0].ID+"/answer", map[string]string{"answer": "A"})
	if code != 409 {
		t.Fatalf("second answer = %d", code)
	}

	askLead := tm.dev.startCall("ask_lead", `{"question":"字段要校验吗？"}`)
	send := tm.lead.expectSend("字段要校验吗？")
	qid := strings.Fields(send.Text)[1]
	tm.lead.mustCall("reply_to", map[string]string{"question_id": qid, "answer": "要"})
	if res := tm.dev.result(askLead); !res.Ok || text(res) != "要" {
		t.Fatalf("ask_lead result %+v", res)
	}

	res := tm.lead.mustCall("ask_user", map[string]any{"question": "要不要暗色模式？"})
	var asked protocol.AskUserResult
	_ = json.Unmarshal(*res.Structured, &asked)
	tm.b.must(200, "POST", "/api/conversations/"+tm.thread.ID+"/questions/"+string(asked.QuestionID)+"/answer", map[string]string{"answer": "要"}, nil)
	tm.lead.expectSend("[Answer to " + string(asked.QuestionID) + "] 要")
}

// TestReconcileReopensMissingSessions: after a daemon restart the report lacks the sessions;
// the console reopens them with resume, keeps the cards the daemon no longer has open
// (detached, for the re-issued requests) and resumes the dev.
func TestReconcileReopensMissingSessions(t *testing.T) {
	tm := startTeam(t)
	gen := protocol.OpenGen(tm.dev.gen)
	tm.dev.send(protocol.TypeApprovalRequest, protocol.ApprovalRequest{ApprovalID: "ap_lost", SessionID: protocol.SessionID(tm.dev.session),
		OpenGen: &gen, Source: "claude_can_use_tool", ToolName: "Bash", Input: json.RawMessage(`{"command":"make"}`),
		Available: []protocol.ApprovalDecision{"allow", "deny"}})
	tm.n.Close()
	n2 := tm.w.connect(tm.token, nodetest.Options{Epoch: "e-restarted"})
	tm.dev.node, tm.lead.node = n2, n2
	if _, err := n2.Send(tm.w.ctx, protocol.TypeSessionsReport, protocol.SessionsReport{Sessions: []protocol.SessionReportItem{}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := tm.w.waitCtx()
	defer cancel()
	f, err := n2.Wait(ctx, protocol.TypeSessionOpen, tm.dev.mine)
	if err != nil {
		t.Fatal(err)
	}
	var open protocol.SessionOpen
	_ = f.Decode(&open)
	if open.OpenGen != 2 || open.Resume == nil || open.Resume.SDKSessionID == nil || *open.Resume.SDKSessionID != "sdk-"+tm.dev.session {
		t.Fatalf("reopen %+v", open)
	}
	tm.dev.gen = 2
	tm.dev.expectSend("守护进程重启")
	var q core.QuestionView
	eventually(t, "lost card detached", func() bool {
		var cards []core.QuestionView
		tm.b.must(200, "GET", "/api/conversations/"+tm.thread.ID+"/questions?status=all", nil, &cards)
		for _, c := range cards {
			if c.Kind == "approval" {
				q = c
			}
		}
		return q.Status == "open" && q.Detail["detached_at"] != nil
	})
}

// TestRestartMidTurnResumes: the console saw the dev's turn running; after a daemon restart the
// report only knows the session as parked, so the console sends a note that resumes it.
func TestRestartMidTurnResumes(t *testing.T) {
	tm := startTeam(t)
	gen := protocol.OpenGen(tm.dev.gen)
	tm.dev.send(protocol.TypeSessionState, protocol.SessionState{SessionID: protocol.SessionID(tm.dev.session), OpenGen: &gen, State: "turn_running"})
	eventually(t, "dev turn running", func() bool {
		s, err := core.SessionByID(tm.w.ctx, tm.w.e.Core.Pool, tm.dev.session)
		return err == nil && s.State == "turn_running"
	})
	tm.n.Close()
	n2 := tm.w.connect(tm.token, nodetest.Options{Epoch: "e-restarted"})
	tm.dev.node, tm.lead.node = n2, n2
	items := []protocol.SessionReportItem{
		{SessionID: protocol.SessionID(tm.dev.session), OpenGen: gen, State: "parked", Kind: "claude"},
		{SessionID: protocol.SessionID(tm.lead.session), OpenGen: protocol.OpenGen(tm.lead.gen), State: "parked", Kind: "claude"},
	}
	if _, err := n2.Send(tm.w.ctx, protocol.TypeSessionsReport, protocol.SessionsReport{Sessions: items}); err != nil {
		t.Fatal(err)
	}
	tm.dev.expectSend("守护进程重启")
	ctx, cancel := context.WithTimeout(tm.w.ctx, 2*time.Second)
	defer cancel()
	if f, err := n2.Wait(ctx, protocol.TypeSessionSend, tm.lead.mine); err == nil {
		t.Fatalf("idle lead must not be woken: %s", f.Body)
	}
}

// TestReplayFromLastEventID: a reconnecting stream gets what it missed, filtered by topic,
// with skip frames keeping its position.
func TestReplayFromLastEventID(t *testing.T) {
	tm := startTeam(t)
	live := tm.b.stream("/api/projects/"+tm.projectID+"/events?topics=task", "")
	tm.lead.mustCall("assign_task", map[string]any{"assignee": "dev", "instruction": "second", "main": "做"})
	first := live.wait(t, "task", `"id":"t2"`)
	tm.lead.mustCall("cancel_task", map[string]any{"task_id": "t2"})
	live.wait(t, "task", `"id":"t2"`, `"status":"cancelled"`)

	resumed := tm.b.stream("/api/projects/"+tm.projectID+"/events?topics=task", first.ID)
	got := resumed.wait(t, "task", `"id":"t2"`, `"status":"cancelled"`)
	if got.ID <= first.ID && len(got.ID) <= len(first.ID) {
		t.Fatalf("replayed id %s not after %s", got.ID, first.ID)
	}
	for _, typ := range resumed.types() {
		if typ != "task" {
			t.Fatalf("filtered stream sent %s", typ)
		}
	}
	old := tm.b.stream("/api/projects/"+tm.projectID+"/events?topics=thread", "0")
	old.wait(t, "thread", `"no":1`)
}

// TestNudgeThenBlocked: a turn that ends with the task still running gets a reminder; the
// second one marks the task blocked and tells the lead (architecture.md §7.3).
func TestNudgeThenBlocked(t *testing.T) {
	tm := startTeam(t)
	ended := func(e *protocol.SessionEvent) {
		sub, isErr, turns, ms := "success", false, 1, 10
		usage := json.RawMessage(`{"input_tokens":10,"output_tokens":5}`)
		cost := 0.01
		e.ResultSubtype, e.IsError, e.NumTurns, e.DurationMS, e.Usage, e.CostUSD = &sub, &isErr, &turns, &ms, &usage, &cost
	}
	tm.dev.event("turn_ended", ended)
	tm.dev.expectSend("[平台提醒] 任务 t1 还没有完成")
	tm.dev.event("turn_ended", ended)
	tm.lead.expectSend("[Task t1 · dev-1 · 需要关注]")
	var tasks []core.TaskView
	tm.b.must(200, "GET", "/api/threads/"+tm.thread.ID+"/tasks", nil, &tasks)
	if len(tasks) != 1 || tasks[0].Status != "blocked" {
		t.Fatalf("tasks %+v", tasks)
	}
	var usage struct {
		CostUSDEst *float64              `json:"cost_usd_est"`
		Tokens     struct{ Input int64 } `json:"tokens"`
	}
	tm.b.must(200, "GET", "/api/threads/"+tm.thread.ID+"/usage", nil, &usage)
	if usage.CostUSDEst == nil || *usage.CostUSDEst < 0.019 || usage.Tokens.Input != 20 {
		t.Fatalf("usage %+v", usage)
	}
	tm.b.must(200, "POST", "/api/threads/"+tm.thread.ID+"/tasks/t1/retry", nil, nil)
	tm.dev.expectSend("[Task t1")
}

// TestThreadLimitAndPause: threads beyond max_running_threads wait in the queue and start
// when a running one is paused.
func TestThreadLimitAndPause(t *testing.T) {
	tm := startTeam(t)
	var settings map[string]any
	tm.b.must(200, "GET", "/api/admin/settings", nil, &settings)
	settings["max_running_threads"] = 1
	tm.b.must(200, "PUT", "/api/admin/settings", settings, nil)
	var second core.ThreadView
	tm.b.must(201, "POST", "/api/projects/"+tm.projectID+"/threads", map[string]any{"title": "Next", "brief": "later"}, &second)
	if second.Status != "queued" || second.QueuePosition == nil || *second.QueuePosition != 0 {
		t.Fatalf("second thread %+v", second)
	}
	tm.b.must(200, "PATCH", "/api/threads/"+tm.thread.ID, map[string]any{"status": "paused"}, nil)
	ctx, cancel := tm.w.waitCtx()
	defer cancel()
	if _, err := tm.n.Wait(ctx, protocol.TypeSessionInterrupt, tm.dev.mine); err != nil {
		t.Fatal(err)
	}
	expectOpen(t, tm.w, tm.n, "lead-2")
	var got core.ThreadView
	tm.b.must(200, "GET", "/api/threads/"+second.ID, nil, &got)
	if got.Status != "running" {
		t.Fatalf("queued thread did not start: %s", got.Status)
	}
	tm.b.must(200, "PATCH", "/api/threads/"+tm.thread.ID, map[string]any{"status": "running"}, nil)
	tm.b.must(200, "GET", "/api/threads/"+tm.thread.ID, nil, &got)
	if got.Status != "queued" {
		t.Fatalf("resumed thread should queue behind the limit: %s", got.Status)
	}
	// Lifting the limit (0 = no limit) starts the queued thread on the next sweep (server start).
	settings["max_running_threads"] = 0
	tm.b.must(200, "PUT", "/api/admin/settings", settings, nil)
	tm.w.e.Orch.StartAllQueued(tm.w.ctx)
	tm.b.must(200, "GET", "/api/threads/"+tm.thread.ID, nil, &got)
	if got.Status != "running" {
		t.Fatalf("queued thread did not start once the limit was lifted: %s", got.Status)
	}
}

// TestThreadsUnlimitedByDefault: with no limit configured, every opened thread runs at once.
func TestThreadsUnlimitedByDefault(t *testing.T) {
	tm := startTeam(t)
	for i := range 4 {
		var th core.ThreadView
		tm.b.must(201, "POST", "/api/projects/"+tm.projectID+"/threads", map[string]any{"title": fmt.Sprintf("T%d", i), "brief": "go"}, &th)
		if th.Status != "running" || th.QueuePosition != nil {
			t.Fatalf("thread %d queued without a limit: %+v", i, th)
		}
	}
}

// askMembers makes the default member roles ask (逐条问), so these tests see approval cards;
// the default bypass never raises one.
func askMembers(t *testing.T, w *world) {
	t.Helper()
	if _, err := store.Exec(w.ctx, w.e.Core.Pool, `UPDATE at_roles SET permission_mode = 'ask' WHERE tier = 'member'`); err != nil {
		t.Fatal(err)
	}
}

// TestBypassRoleApprovalsPass: a bypass member never asks (the daemon allows in canUseTool);
// an approval.request from an old daemon is allowed on any node, without a card.
func TestBypassRoleApprovalsPass(t *testing.T) {
	tm := startTeam(t)
	pool, ctx := tm.w.e.Core.Pool, tm.w.ctx
	if _, err := store.Exec(ctx, pool, `UPDATE at_employees SET spec = jsonb_set(spec, '{permission_mode}', '"bypass"')
		WHERE project_id = $1 AND name = 'dev-1'`, tm.projectID); err != nil {
		t.Fatal(err)
	}
	gen := protocol.OpenGen(tm.dev.gen)
	tm.dev.send(protocol.TypeApprovalRequest, protocol.ApprovalRequest{ApprovalID: "ap_plain", SessionID: protocol.SessionID(tm.dev.session),
		OpenGen: &gen, Source: "claude_can_use_tool", ToolName: "Bash", Input: json.RawMessage(`{"command":"make it"}`),
		Available: []protocol.ApprovalDecision{"allow", "deny"}})
	if r := waitResolve(t, tm.w, tm.dev, "ap_plain"); r.Decision != "allow" || r.DecidedBy != approvals.DecidedRoleSkip {
		t.Fatalf("bypass on a plain node: %+v", r)
	}
	var cards []core.QuestionView
	tm.b.must(200, "GET", "/api/conversations/"+tm.thread.ID+"/questions?status=open", nil, &cards)
	if len(cards) != 0 {
		t.Fatalf("open cards %+v", cards)
	}
}

// TestReadOnlyRoleAutoAllowsReads: a read_only member's read-only shell commands pass
// without a card; anything that writes still asks the user.
func TestReadOnlyRoleAutoAllowsReads(t *testing.T) {
	tm := startTeam(t)
	pool, ctx := tm.w.e.Core.Pool, tm.w.ctx
	if _, err := store.Exec(ctx, pool, `UPDATE at_employees SET spec = jsonb_set(spec, '{read_only}', 'true')
		WHERE project_id = $1 AND name = 'dev-1'`, tm.projectID); err != nil {
		t.Fatal(err)
	}
	gen := protocol.OpenGen(tm.dev.gen)
	ask := func(id, cmd string) {
		input, _ := json.Marshal(map[string]string{"command": cmd})
		tm.dev.send(protocol.TypeApprovalRequest, protocol.ApprovalRequest{ApprovalID: protocol.ApprovalID(id), SessionID: protocol.SessionID(tm.dev.session),
			OpenGen: &gen, Source: "claude_can_use_tool", ToolName: "Bash", Input: input, Available: []protocol.ApprovalDecision{"allow", "deny"}})
	}
	ask("ap_read", "git log --oneline -5")
	if r := waitResolve(t, tm.w, tm.dev, "ap_read"); r.Decision != "allow" || r.DecidedBy != approvals.DecidedReadOnly {
		t.Fatalf("read-only command: %+v", r)
	}
	ask("ap_write", "rm -rf build")
	eventually(t, "write still asks", func() bool {
		var cards []core.QuestionView
		tm.b.must(200, "GET", "/api/conversations/"+tm.thread.ID+"/questions?status=open", nil, &cards)
		return len(cards) == 1 && cards[0].Kind == "approval"
	})
}

// askApproval sends a Bash approval request from the agent.
func askApproval(a *fakeAgent, id, cmd string) {
	gen := protocol.OpenGen(a.gen)
	input, _ := json.Marshal(map[string]string{"command": cmd})
	a.send(protocol.TypeApprovalRequest, protocol.ApprovalRequest{ApprovalID: protocol.ApprovalID(id), SessionID: protocol.SessionID(a.session),
		OpenGen: &gen, Source: "claude_can_use_tool", ToolName: "Bash", Input: input,
		Available: []protocol.ApprovalDecision{"allow", "allow_always", "deny"}})
}

// leadRequest waits for the lead's "[Permission request qN …]" and returns qN.
func leadRequest(t *testing.T, lead *fakeAgent, needle string) string {
	t.Helper()
	send := lead.expectSend(needle)
	if !strings.HasPrefix(send.Text, "[Permission request ") || !strings.Contains(send.Text, "review_permission") {
		t.Fatalf("lead message %q", send.Text)
	}
	return strings.Fields(send.Text)[2]
}

func (tm *team) userCards(t *testing.T) []core.QuestionView {
	t.Helper()
	var cards []core.QuestionView
	tm.b.must(200, "GET", "/api/projects/"+tm.projectID+"/waiting", nil, &struct {
		Questions *[]core.QuestionView `json:"questions"`
	}{&cards})
	return cards
}

func (tm *team) card(t *testing.T, qid string) core.QuestionView {
	t.Helper()
	var cards []core.QuestionView
	tm.b.must(200, "GET", "/api/conversations/"+tm.thread.ID+"/questions?status=all", nil, &cards)
	for _, c := range cards {
		if c.ID == qid {
			return c
		}
	}
	t.Fatalf("no card %s in %+v", qid, cards)
	return core.QuestionView{}
}

func (tm *team) activity(t *testing.T) string {
	t.Helper()
	_, raw := tm.b.do("GET", "/api/threads/"+tm.thread.ID+"/activity?limit=50", nil)
	return string(raw)
}

// TestLeadReviewsPermissions: a member's request goes to the lead first (not to the user);
// the lead allows, denies with a reason, or hands it to the user, who decides; every
// decision names who made it on the card and in the activity feed.
func TestLeadReviewsPermissions(t *testing.T) {
	tm := startTeam(t)
	askApproval(tm.dev, "ap_allow", "npm test")
	q1 := leadRequest(t, tm.lead, "npm test")
	if cards := tm.userCards(t); len(cards) != 0 {
		t.Fatalf("the user must not see a card the lead is reviewing: %+v", cards)
	}
	tm.lead.mustCall("review_permission", map[string]string{"request_id": q1, "decision": "allow", "reason": "任务要跑测试"})
	if r := waitResolve(t, tm.w, tm.dev, "ap_allow"); r.Decision != "allow" || r.DecidedBy != "policy:rule:lead-review" {
		t.Fatalf("lead allow: %+v", r)
	}
	if c := tm.card(t, q1); c.DecidedBy != approvals.DecidedLeadPrefix+"lead-1" || c.Detail["review"] == nil {
		t.Fatalf("card %+v", c)
	}

	askApproval(tm.dev, "ap_deny", "rm -rf /srv/data")
	q2 := leadRequest(t, tm.lead, "rm -rf /srv/data")
	tm.lead.mustCall("review_permission", map[string]string{"request_id": q2, "decision": "deny", "reason": "不能删数据目录"})
	if r := waitResolve(t, tm.w, tm.dev, "ap_deny"); r.Decision != "deny" || r.Message == nil || !strings.Contains(*r.Message, "不能删数据目录") {
		t.Fatalf("lead deny: %+v", r)
	}

	askApproval(tm.dev, "ap_ask", "terraform apply")
	q3 := leadRequest(t, tm.lead, "terraform apply")
	tm.lead.mustCall("review_permission", map[string]string{"request_id": q3, "decision": "ask_user", "reason": "会改线上资源"})
	cards := tm.userCards(t)
	if len(cards) != 1 || cards[0].ID != q3 || cards[0].Audience != "user" {
		t.Fatalf("handed to the user: %+v", cards)
	}
	tm.b.must(200, "POST", "/api/conversations/"+tm.thread.ID+"/questions/"+q3+"/answer", map[string]string{"answer": "deny", "reason": "先别动"}, nil)
	if r := waitResolve(t, tm.w, tm.dev, "ap_ask"); r.Decision != "deny" || !strings.HasPrefix(r.DecidedBy, "user:") {
		t.Fatalf("user deny: %+v", r)
	}
	if res := tm.lead.call("review_permission", map[string]string{"request_id": q3, "decision": "allow", "reason": "x"}); res.Ok {
		t.Fatal("a decided request cannot be reviewed again")
	}
	feed := tm.activity(t)
	for _, want := range []string{"放行了 dev-1 的请求 Bash npm test（任务要跑测试）", "拒绝了 dev-1 的请求", "拿不准，转给你决定", "拒绝了 dev-1 的请求 Bash terraform apply（先别动）"} {
		if !strings.Contains(feed, want) {
			t.Errorf("activity lacks %q: %s", want, feed)
		}
	}
}

// TestLeadReviewTimeout: a request the lead leaves alone past lead_review_timeout_s goes to
// the user, and the user-facing timeout counts from then.
func TestLeadReviewTimeout(t *testing.T) {
	tm := startTeam(t)
	tm.b.must(200, "PATCH", "/api/projects/"+tm.projectID, map[string]any{"delivery": map[string]any{"lead_review_timeout_s": 30, "approval_timeout_s": 60}}, nil)
	askApproval(tm.dev, "ap_slow", "make deploy")
	qid := leadRequest(t, tm.lead, "make deploy")
	pool, ctx := tm.w.e.Core.Pool, tm.w.ctx
	tm.w.e.Approvals.SweepLeadReviews(ctx)
	if len(tm.userCards(t)) != 0 {
		t.Fatal("escalated before the timeout")
	}
	if _, err := store.Exec(ctx, pool, `UPDATE at_questions SET created_at = now() - interval '45 seconds' WHERE approval_id = 'ap_slow'`); err != nil {
		t.Fatal(err)
	}
	tm.w.e.Approvals.SweepLeadReviews(ctx)
	tm.w.e.Approvals.SweepTimeouts(ctx)
	cards := tm.userCards(t)
	if len(cards) != 1 || cards[0].ID != qid {
		t.Fatalf("timed-out request should ask the user (and not be denied yet): %+v", cards)
	}
	if res := tm.lead.call("review_permission", map[string]string{"request_id": qid, "decision": "allow", "reason": "x"}); res.Ok {
		t.Fatal("the lead is too late")
	}
	if !strings.Contains(tm.activity(t), "没有及时判断") {
		t.Fatal("timeout not in the activity feed")
	}
}

// TestApprovalRoutingAskUser: with 全部问我 every request is the user's card at once, and
// the lead's own requests always go to the user.
func TestApprovalRoutingAskUser(t *testing.T) {
	tm := startTeam(t)
	askApproval(tm.lead, "ap_lead", "make lint")
	eventually(t, "lead request asks the user", func() bool { return len(tm.userCards(t)) == 1 })
	tm.b.must(200, "PATCH", "/api/projects/"+tm.projectID, map[string]any{"delivery": map[string]any{"approval_routing": "user"}}, nil)
	askApproval(tm.dev, "ap_user", "npm publish")
	eventually(t, "member request asks the user", func() bool { return len(tm.userCards(t)) == 2 })
}

// TestLeadReviewGuards: only the addressed lead decides a request through review_permission:
// a member cannot (not its tier), reply_to cannot close a permission request, a card naming
// another reviewer is refused, and the thread does not wait on the user meanwhile.
func TestLeadReviewGuards(t *testing.T) {
	tm := startTeam(t)
	askApproval(tm.dev, "ap_guard", "git push --force")
	qid := leadRequest(t, tm.lead, "git push --force")
	var th core.ThreadView
	tm.b.must(200, "GET", "/api/threads/"+tm.thread.ID, nil, &th)
	if th.Counts.ApprovalsOpen != 0 || th.Counts.Waiting != 0 {
		t.Fatalf("a request with the lead is not the user's: %+v", th.Counts)
	}
	if res := tm.dev.call("review_permission", map[string]string{"request_id": qid, "decision": "allow", "reason": "mine"}); res.Ok {
		t.Fatal("a member approved its own request")
	}
	if res := tm.lead.call("reply_to", map[string]string{"question_id": qid, "answer": "allow"}); res.Ok {
		t.Fatal("reply_to closed a permission request")
	}
	pool, ctx := tm.w.e.Core.Pool, tm.w.ctx
	if _, err := store.Exec(ctx, pool, `UPDATE at_questions SET detail = detail || '{"reviewer_id":"emp_other"}' WHERE approval_id = 'ap_guard'`); err != nil {
		t.Fatal(err)
	}
	if res := tm.lead.call("review_permission", map[string]string{"request_id": qid, "decision": "allow", "reason": "x"}); res.Ok {
		t.Fatal("a card addressed to another reviewer was decided")
	}
	if c := tm.card(t, qid); c.Status != "open" {
		t.Fatalf("card closed by a refused call: %+v", c)
	}
}

// TestDependentsBlockedWhenDepEnds: tasks waiting on a task that is cancelled (or fails) could
// never start; they and the tasks waiting on them become blocked and the lead is told, instead
// of sitting in the pool unseen. Tasks with other dependencies are left alone. For the same
// reason a new task depending on the cancelled one is refused (the text names it and its
// status), and so is retrying a blocked task while that dependency is still cancelled.
func TestDependentsBlockedWhenDepEnds(t *testing.T) {
	tm := startTeam(t)
	tm.lead.mustCall("assign_task", map[string]any{"assignee": "dev", "instruction": "do t2 after t1", "main": "做", "depends_on": []string{"t1"}})
	tm.lead.mustCall("assign_task", map[string]any{"assignee": "dev", "instruction": "do t3 after t2", "main": "做", "depends_on": []string{"t2"}})
	tm.lead.mustCall("assign_task", map[string]any{"assignee": "dev", "instruction": "do t4", "main": "做"})
	tm.lead.mustCall("cancel_task", map[string]any{"task_id": "t1", "reason": "不做了"})
	tm.lead.expectSend("依赖它的 t2、t3 已标为 blocked")
	status := func(id string) string {
		task, err := core.TaskByID(tm.w.ctx, tm.w.e.Core.Pool, tm.thread.ID, id)
		if err != nil {
			t.Fatal(err)
		}
		return task.Status
	}
	if got := status("t2") + " " + status("t3"); got != "blocked blocked" {
		t.Fatalf("dependents after cancelling t1: %s", got)
	}
	if got := status("t4"); got == "blocked" {
		t.Fatalf("t4 has no dependency but is %s", got)
	}

	r := tm.lead.call("assign_task", map[string]any{"assignee": "dev", "instruction": "on a cancelled one", "main": "做", "depends_on": []string{"t4", "t1"}})
	if r.Ok || !strings.Contains(text(r), "t1（cancelled）") || !strings.Contains(text(r), "已失败或已取消") {
		t.Fatalf("assign_task on a cancelled dependency = %v %q", r.Ok, text(r))
	}
	if code, raw := tm.b.do("POST", "/api/threads/"+tm.thread.ID+"/tasks/t2/retry", nil); code != 409 || !strings.Contains(string(raw), "t1（cancelled）") {
		t.Fatalf("retrying t2 while t1 is cancelled = %d %s", code, raw)
	}
	tm.b.must(200, "POST", "/api/threads/"+tm.thread.ID+"/tasks/t3/retry", nil, nil) // its dependency t2 is blocked, not ended
	if got := status("t2") + " " + status("t3"); got != "blocked pending" {
		t.Fatalf("after the refusals: %s", got)
	}
	if _, err := core.TaskByID(tm.w.ctx, tm.w.e.Core.Pool, tm.thread.ID, "t5"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("refused task stored: %v", err)
	}
}
