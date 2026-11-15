package engine_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	protocol "github.com/touwaeriol/agent-teams/protocol"

	"github.com/touwaeriol/agent-teams/server/internal/approvals"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw/nodetest"
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
	var project struct{ ID string }
	tm.b.must(201, "POST", "/api/projects", map[string]any{"name": "p"}, &project)
	tm.projectID = project.ID
	tm.n = w.connect(node.Token, nodetest.Options{})
	tm.b.must(201, "POST", "/api/projects/"+project.ID+"/threads", map[string]any{"title": "Work", "brief": "do it"}, &tm.thread)
	tm.lead, _ = expectOpen(t, w, tm.n, "lead-1")
	tm.lead.mustCall("update_plan", map[string]any{"steps": []map[string]string{{"title": "做", "status": "todo"}}})
	tm.lead.mustCall("assign_task", map[string]any{"assignee": "dev", "instruction": "do t1", "main": "做"})
	var open protocol.SessionOpen
	tm.dev, open = expectOpen(t, w, tm.n, "dev-1")
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
// the console reopens them with resume and cancels the cards the daemon no longer has.
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
	var q core.QuestionView
	eventually(t, "lost card cancelled", func() bool {
		var cards []core.QuestionView
		tm.b.must(200, "GET", "/api/conversations/"+tm.thread.ID+"/questions?status=all", nil, &cards)
		for _, c := range cards {
			if c.Kind == "approval" {
				q = c
			}
		}
		return q.Status == "cancelled"
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
}

// TestSkipRoleNeedsIsolatedNode: a skip_permissions role on a sandbox-level node opens in
// default mode, so its approvals must still become cards; on an isolated node they pass.
func TestSkipRoleNeedsIsolatedNode(t *testing.T) {
	tm := startTeam(t)
	pool, ctx := tm.w.e.Core.Pool, tm.w.ctx
	if _, err := pool.Exec(ctx, `UPDATE at_employees SET spec = jsonb_set(spec, '{skip_permissions}', 'true')
		WHERE project_id = $1 AND name = 'dev-1'`, tm.projectID); err != nil {
		t.Fatal(err)
	}
	gen := protocol.OpenGen(tm.dev.gen)
	ask := func(id string) {
		tm.dev.send(protocol.TypeApprovalRequest, protocol.ApprovalRequest{ApprovalID: protocol.ApprovalID(id), SessionID: protocol.SessionID(tm.dev.session),
			OpenGen: &gen, Source: "claude_can_use_tool", ToolName: "Bash", Input: json.RawMessage(`{"command":"make ` + id + `"}`),
			Available: []protocol.ApprovalDecision{"allow", "deny"}})
	}
	ask("ap_plain")
	eventually(t, "card shown", func() bool {
		var cards []core.QuestionView
		tm.b.must(200, "GET", "/api/conversations/"+tm.thread.ID+"/questions?status=open", nil, &cards)
		return len(cards) == 1 && cards[0].Kind == "approval"
	})
	if _, err := pool.Exec(ctx, `UPDATE at_nodes SET isolated = true WHERE user_id = (SELECT user_id FROM at_projects WHERE id = $1)`, tm.projectID); err != nil {
		t.Fatal(err)
	}
	ask("ap_isolated")
	if r := waitResolve(t, tm.w, tm.dev, "ap_isolated"); r.Decision != "allow" || r.DecidedBy != approvals.DecidedRoleSkip {
		t.Fatalf("isolated skip: %+v", r)
	}
}

// TestReadOnlyRoleAutoAllowsReads: a read_only member's read-only shell commands pass
// without a card; anything that writes still asks the user.
func TestReadOnlyRoleAutoAllowsReads(t *testing.T) {
	tm := startTeam(t)
	pool, ctx := tm.w.e.Core.Pool, tm.w.ctx
	if _, err := pool.Exec(ctx, `UPDATE at_employees SET spec = jsonb_set(spec, '{read_only}', 'true')
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
