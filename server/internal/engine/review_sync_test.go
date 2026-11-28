package engine_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/engine"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw/nodetest"
	"github.com/touwaeriol/agent-teams/server/internal/scheduler"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

const (
	reviewedSHA = "1111111111111111111111111111111111111111"
	otherSHA    = "2222222222222222222222222222222222222222"
)

var reviewFeatures = []string{"sessions", "git_worktree", "cred_helper", "file_download", "git_sync", "git_review_sha"}

// reviewWorld: a project with one dev and one reviewer (the review gate on), a thread whose t1
// dev-1 completed with reviewedSHA pushed; t2 is the review gate. The reviewer never had a
// session in the thread.
type reviewWorld struct {
	*team
	thread core.ThreadView
	lead   *fakeAgent
	dev1   *fakeAgent
	gh     *fakeGitHub
}

func newReviewWorld(t *testing.T, features []string, tune func(*engine.Engine)) *reviewWorld {
	t.Helper()
	return newReviewWorldN(t, features, tune, 1)
}

// newReviewWorldN is newReviewWorld with reviewers members of the reviewer role.
func newReviewWorldN(t *testing.T, features []string, tune func(*engine.Engine), reviewers int) *reviewWorld {
	t.Helper()
	gh, ghSrv := newFakeGitHub(t)
	w := newWorldWith(t, ghSrv, tune)
	tm := &team{w: w, b: w.signUp("review-sync@example.com")}
	var node nodeCreated
	tm.b.must(201, "POST", "/api/nodes", map[string]string{"name": "fake"}, &node)
	var roles struct{ Roles []store.Role }
	tm.b.must(200, "GET", "/api/roles", nil, &roles)
	reviewer := roles.Roles[2]
	reviewer.ID, reviewer.Role, reviewer.Title, reviewer.Count, reviewer.Gate = "", "reviewer", "审查", reviewers, "review"
	tm.b.must(200, "PUT", "/api/roles", map[string]any{"roles": append(roles.Roles, reviewer)}, nil)
	var conn struct{ ID string }
	tm.b.must(201, "POST", "/api/git/connections", map[string]string{"name": "gh", "token": "ghp_good"}, &conn)
	var project struct{ ID string }
	tm.b.must(201, "POST", "/api/projects", map[string]any{"name": "web",
		"repos": []map[string]any{{"repo": "o/r", "git_connection_id": conn.ID}}}, &project)
	tm.projectID = project.ID
	tm.n = w.connect(node.Token, nodetest.Options{Features: features})
	n := tm.n

	rw := &reviewWorld{team: tm, gh: gh}
	tm.b.must(201, "POST", "/api/projects/"+project.ID+"/threads", map[string]any{"title": "Login", "brief": "登录页"}, &rw.thread)
	rw.lead, _ = expectOpen(t, w, n, "lead-1")
	answerPrepares(t, w, n)
	rw.lead.mustCall("update_plan", map[string]any{"steps": []map[string]string{{"title": "页面", "status": "doing"}}})
	rw.lead.mustCall("assign_task", map[string]any{"assignee": "dev", "instruction": "写表单", "main": "页面"})
	dev1, open := expectOpen(t, w, n, "dev-1")
	answerPrepares(t, w, n)
	dev1.send(protocol.TypeSessionDelivered, protocol.SessionDelivered{SessionID: protocol.SessionID(dev1.session), MsgID: open.FirstMessage.MsgID})
	completeWithPush(t, w, gh, n, dev1, "t1", reviewedSHA)
	rw.dev1 = dev1
	return rw
}

// reviewSync waits for the reviewer's pre-task sync (git.prepare with sync) and returns it.
func (rw *reviewWorld) reviewSync(t *testing.T) protocol.GitPrepare {
	t.Helper()
	ctx, cancel := rw.w.waitCtx()
	defer cancel()
	f, err := rw.n.Wait(ctx, protocol.TypeGitPrepare, func(f nodetest.Frame) bool {
		b := string(f.Body)
		return strings.Contains(b, `"sync"`) && strings.Contains(b, "reviewer-1")
	})
	if err != nil {
		t.Fatalf("no review sync before the reviewer's brief: %v", err)
	}
	var p protocol.GitPrepare
	_ = f.Decode(&p)
	return p
}

// answer sends the git.result of a sync job: ok with HEAD at head, or failed (head "").
func (rw *reviewWorld) answer(t *testing.T, p protocol.GitPrepare, head string) {
	t.Helper()
	ctx, cancel := rw.w.waitCtx()
	defer cancel()
	r := protocol.GitResult{JobID: p.JobID, Ok: true, HeadSHA: &head,
		Sync: &protocol.GitResultSync{Action: protocol.GitResultSyncActionCheckout, HeadSHA: head}}
	if head == "" {
		code, msg, actual := protocol.GitResultErrorCodeSHAMismatch, "commit not found", otherSHA
		r = protocol.GitResult{JobID: p.JobID, Ok: false, ErrorCode: &code, Error: &msg, HeadSHA: &actual}
	}
	if _, err := rw.n.Send(ctx, protocol.TypeGitResult, r); err != nil {
		t.Fatal(err)
	}
}

// noSessionSend asserts no session.send containing needle arrives within a moment.
func noSessionSend(t *testing.T, n *nodetest.Node, needle string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if f, err := n.Wait(ctx, protocol.TypeSessionSend, func(f nodetest.Frame) bool { return strings.Contains(string(f.Body), needle) }); err == nil {
		t.Fatalf("unexpected session.send: %s", f.Body)
	}
}

// TestReviewFirstClaimChecksOutReviewedCommit reproduces the reviewer that never worked in the
// thread (its last session was elsewhere, or none): its first review used to open a session on a
// worktree created from the thread branch, with no sync at all. Now the review sync goes out
// first, creating the worktree at the commit under review, and the brief names the verified sha.
func TestReviewFirstClaimChecksOutReviewedCommit(t *testing.T) {
	rw := newReviewWorld(t, reviewFeatures, nil)
	p := rw.reviewSync(t)
	if p.Sync.Mode != protocol.GitPrepareSyncModeReview || p.Sync.Ref != "thread/1/dev-1" || p.Sync.Deadline == nil ||
		p.Sync.ExpectSHA == nil || *p.Sync.ExpectSHA != reviewedSHA || !strings.Contains(string(p.Cwd), "threads/1/reviewer-1") {
		t.Fatalf("review sync %+v (sync %+v)", p, p.Sync)
	}
	if f, ok := takeNow(rw.n, protocol.TypeSessionOpen); ok && strings.Contains(string(f.Body), "reviewer-1") {
		t.Fatalf("reviewer opened before its worktree was ready: %s", f.Body)
	}
	rw.answer(t, p, reviewedSHA)
	_, open := expectOpen(t, rw.w, rw.n, "reviewer-1")
	answerPrepares(t, rw.w, rw.n)
	brief := open.FirstMessage.Text
	for _, want := range []string{"审查 dev-1 的任务 t1", "被审提交：o/r " + reviewedSHA, "o/r：平台已检出并核对，HEAD = " + reviewedSHA, "platform_error"} {
		if !strings.Contains(brief, want) {
			t.Errorf("review brief misses %q:\n%s", want, brief)
		}
	}
	if strings.Contains(brief, "平台没能核对") {
		t.Errorf("verified review asks the reviewer to check HEAD:\n%s", brief)
	}
	if errs := rw.n.Errors(); len(errs) > 0 {
		t.Fatalf("protocol errors: %+v", errs)
	}
}

// TestReviewBlockedByTurnsTakesVerdict: a gate blocked because the reviewer ended two turns
// without complete_task (TurnEnded) is not a platform error: it stays the reviewer's, and once
// the lead nudges it the verdict counts as usual — done passes, failed sends the parent back
// for rework.
func TestReviewBlockedByTurnsTakesVerdict(t *testing.T) {
	ended := func(e *protocol.SessionEvent) {
		sub, isErr, turns, ms := "success", false, 1, 10
		usage := json.RawMessage(`{"input_tokens":10,"output_tokens":5}`)
		cost := 0.01
		e.ResultSubtype, e.IsError, e.NumTurns, e.DurationMS, e.Usage, e.CostUSD = &sub, &isErr, &turns, &ms, &usage, &cost
	}
	for _, verdict := range []string{"done", "failed"} {
		t.Run(verdict, func(t *testing.T) {
			rw := newReviewWorld(t, reviewFeatures, nil)
			rw.answer(t, rw.reviewSync(t), reviewedSHA)
			rev, open := expectOpen(t, rw.w, rw.n, "reviewer-1")
			answerPrepares(t, rw.w, rw.n)
			rev.send(protocol.TypeSessionDelivered, protocol.SessionDelivered{SessionID: protocol.SessionID(rev.session), MsgID: open.FirstMessage.MsgID})
			rev.event("turn_ended", ended)
			rev.expectSend("[平台提醒] 任务 t2 还没有完成")
			rev.event("turn_ended", ended)
			rw.lead.expectSend("[Task t2 · reviewer-1 · 需要关注]")
			if gate := rw.taskRow(t, rw.thread.ID, "t2"); gate.Status != "blocked" || gate.ClaimedBy != "reviewer-1" {
				t.Fatalf("gate after two ended turns: %s claimed by %q", gate.Status, gate.ClaimedBy)
			}

			rev.mustCall("complete_task", map[string]any{"task_id": "t2", "status": verdict, "summary": "缺少输入校验"})
			parent := func() core.Task { return rw.taskRow(t, rw.thread.ID, "t1") }
			if verdict == "done" {
				rw.lead.expectSend("审查（reviewer-1）：通过")
				if p := parent(); p.Status != "review" {
					t.Fatalf("passed parent: %s", p.Status)
				}
				return
			}
			rw.lead.expectSend("预留给 dev-1 返工")
			if p := parent(); p.Status == "review" || p.ReservedFor != "dev-1" {
				t.Fatalf("rejected parent: %s reserved %q", p.Status, p.ReservedFor)
			}
		})
	}
}

// TestReviewSyncMismatchIsPlatformError: a review worktree that never reaches the commit under
// review (HEAD elsewhere, the job failed, the result never came) is prepared again; after
// ReviewSyncRetries the claim is a platform error and the gate is claimed afresh; after
// MaxAttempts it is blocked and the lead told. The parent task is never sent back to its
// developer, no rejection is reported and the reviewer never gets a brief. A daemon without
// git_review_sha gets no expect_sha, and the console still checks the HEAD it reports.
func TestReviewSyncMismatchIsPlatformError(t *testing.T) {
	rw := newReviewWorld(t, []string{"sessions", "git_worktree", "cred_helper", "file_download", "git_sync"},
		func(e *engine.Engine) { e.Sched.Interval = 200 * time.Millisecond })
	rounds := map[string]bool{}
	for claim := 0; claim < scheduler.MaxAttempts; claim++ {
		for try := 0; try <= scheduler.ReviewSyncRetries; try++ {
			p := rw.reviewSync(t)
			if p.Sync.ExpectSHA != nil {
				t.Fatalf("expect_sha sent to a daemon without %s", "git_review_sha")
			}
			gate := rw.taskRow(t, rw.thread.ID, "t2")
			if gate.Status != "claimed" || gate.ClaimedBy != "reviewer-1" || gate.Attempt != claim || rounds[gate.SyncRound] {
				t.Fatalf("claim %d try %d: gate %s by %q attempt %d round %s", claim, try, gate.Status, gate.ClaimedBy, gate.Attempt, gate.SyncRound)
			}
			rounds[gate.SyncRound] = true
			switch {
			case claim == 1 && try == 1:
				// The result never comes back: the stale-sync sweep times it out.
				rw.sql(t, `UPDATE at_tasks SET wait_since = now() - interval '3 minutes' WHERE thread_id = $1 AND id = 't2'`, rw.thread.ID)
			case try%2 == 0:
				rw.answer(t, p, otherSHA) // checked out, but not the commit under review
			default:
				rw.answer(t, p, "") // sha_mismatch
			}
		}
	}
	rw.lead.expectSend("[审查 t2 · 平台错误] 审查工作树没准备好")
	gate, parent := rw.taskRow(t, rw.thread.ID, "t2"), rw.taskRow(t, rw.thread.ID, "t1")
	if gate.Status != "blocked" || gate.ClaimedBy != "" || parent.Status != "review" || parent.ReservedFor != "" || parent.ClaimedBy != "dev-1" {
		t.Fatalf("gate %s, parent %s reserved %q claimed %q", gate.Status, parent.Status, parent.ReservedFor, parent.ClaimedBy)
	}
	noSessionSend(t, rw.n, "rejected")
	noSessionSend(t, rw.n, "退回队列")
	if f, ok := takeNow(rw.n, protocol.TypeSessionOpen); ok && strings.Contains(string(f.Body), "reviewer-1") {
		t.Fatalf("reviewer got a brief on an unprepared worktree: %s", f.Body)
	}

	// The user retries the review from the task page: prepared right this time, the brief goes out.
	rw.b.must(200, "POST", "/api/threads/"+rw.thread.ID+"/tasks/t2/retry", nil, nil)
	rw.answer(t, rw.reviewSync(t), reviewedSHA)
	_, open := expectOpen(t, rw.w, rw.n, "reviewer-1")
	if !strings.Contains(open.FirstMessage.Text, "平台已检出并核对，HEAD = "+reviewedSHA) {
		t.Fatalf("brief after retry:\n%s", open.FirstMessage.Text)
	}
	if errs := rw.n.Errors(); len(errs) > 0 {
		t.Fatalf("protocol errors: %+v", errs)
	}
}

// TestReviewPlatformErrorReport: a daemon that cannot sync (no git_sync) gets no review sync; the
// brief names the commit under review and asks the reviewer to check HEAD itself. Reporting
// complete_task(status=platform_error) is not a rejection: the review is claimed again, and the
// second report blocks it and tells the lead. Other tasks cannot use the status.
func TestReviewPlatformErrorReport(t *testing.T) {
	rw := newReviewWorld(t, nil, nil)
	rev, open := expectOpen(t, rw.w, rw.n, "reviewer-1")
	answerPrepares(t, rw.w, rw.n)
	rev.send(protocol.TypeSessionDelivered, protocol.SessionDelivered{SessionID: protocol.SessionID(rev.session), MsgID: open.FirstMessage.MsgID})
	for _, want := range []string{"被审提交：o/r " + reviewedSHA, "平台没能核对", "用 Grep / Read 查开发者总结", `status="platform_error"`} {
		if !strings.Contains(open.FirstMessage.Text, want) {
			t.Errorf("degraded review brief misses %q:\n%s", want, open.FirstMessage.Text)
		}
	}
	if strings.Contains(open.FirstMessage.Text, "gitdir") || strings.Contains(open.FirstMessage.Text, "rev-parse") {
		t.Errorf("degraded brief asks for a check a read-only session cannot do:\n%s", open.FirstMessage.Text)
	}
	res := rev.mustCall("complete_task", map[string]any{"task_id": "t2", "status": "platform_error", "summary": "HEAD 是 develop 的提交"})
	if !strings.Contains(text(res), "不算驳回") {
		t.Fatalf("platform_error result: %s", text(res))
	}
	rev.expectSend("[Task t2")
	if parent := rw.taskRow(t, rw.thread.ID, "t1"); parent.Status != "review" || parent.ReservedFor != "" {
		t.Fatalf("parent after platform_error: %s reserved %q", parent.Status, parent.ReservedFor)
	}
	rev.mustCall("complete_task", map[string]any{"task_id": "t2", "status": "platform_error", "summary": "还是不对"})
	rw.lead.expectSend("[审查 t2 · 平台错误]")
	if gate := rw.taskRow(t, rw.thread.ID, "t2"); gate.Status != "blocked" || gate.ClaimedBy != "" {
		t.Fatalf("gate after two reports: %s claimed by %q", gate.Status, gate.ClaimedBy)
	}
	// The blocked gate is nobody's: a late verdict from the reviewer cannot turn it into a rejection.
	if res := rev.call("complete_task", map[string]any{"task_id": "t2", "status": "failed", "summary": "缺少输入校验"}); res.Ok || !strings.Contains(text(res), "不是你的") {
		t.Fatalf("verdict on a blocked gate: ok=%v %s", res.Ok, text(res))
	}
	noSessionSend(t, rw.n, "rejected")
	if parent := rw.taskRow(t, rw.thread.ID, "t1"); parent.Status != "review" {
		t.Fatalf("parent: %s", parent.Status)
	}

	rw.lead.mustCall("assign_task", map[string]any{"assignee": "dev", "instruction": "加记住我", "main": "页面"})
	answerPrepares(t, rw.w, rw.n)
	rw.dev1.expectSend("加记住我")
	if res := rw.dev1.call("complete_task", map[string]any{"task_id": "t3", "status": "platform_error", "summary": "x"}); res.Ok {
		t.Fatalf("platform_error accepted on a work task: %s", text(res))
	}
}

// TestCancelClosesOpenGate: cancelling a task under review cancels its open review gate. A gate
// left behind (blocked, nobody holding it) used to keep the thread from ever ending, since every
// task must be finished first.
func TestCancelClosesOpenGate(t *testing.T) {
	rw := newReviewWorld(t, reviewFeatures, nil)
	rw.reviewSync(t) // t2, the review gate, is being claimed
	rw.lead.mustCall("cancel_task", map[string]any{"task_id": "t1", "reason": "换个做法"})
	eventually(t, "gate cancelled", func() bool {
		gate, err := core.TaskByID(rw.w.ctx, rw.w.e.Core.Pool, rw.thread.ID, "t2")
		return err == nil && gate.Status == "cancelled" && gate.FinishedAt != nil
	})
}

// TestThreeReviewersOneGate: with three reviewers a completed task gets one review gate, claimed
// by one reviewer (the others get no session); its verdict reaches the lead once and no second
// gate opens.
func TestThreeReviewersOneGate(t *testing.T) {
	rw := newReviewWorldN(t, reviewFeatures, nil, 3)
	reviewers := func(where string) int {
		return rw.count(t, `SELECT count(*) FROM at_employees WHERE project_id = ? AND thread_id IS NULL AND role = 'reviewer' AND active `+where, rw.projectID)
	}
	if n := reviewers(""); n != 3 {
		t.Fatalf("%d active reviewers", n)
	}
	rw.answer(t, rw.reviewSync(t), reviewedSHA)
	rev, open := expectOpen(t, rw.w, rw.n, "reviewer-1")
	answerPrepares(t, rw.w, rw.n)
	rev.send(protocol.TypeSessionDelivered, protocol.SessionDelivered{SessionID: protocol.SessionID(rev.session), MsgID: open.FirstMessage.MsgID})
	gates := func() int {
		return rw.count(t, `SELECT count(*) FROM at_tasks WHERE thread_id = ? AND kind = 'review' AND parent = 't1'`, rw.thread.ID)
	}
	if n := gates(); n != 1 {
		t.Fatalf("%d review gates for t1", n)
	}
	if g := rw.taskRow(t, rw.thread.ID, "t2"); g.Kind != "review" || g.ClaimedBy != "reviewer-1" || g.Assignee != "reviewer" {
		t.Fatalf("gate %+v", g)
	}
	rw.kick()
	noOpen(t, rw.n, "reviewer-2")
	noOpen(t, rw.n, "reviewer-3")
	rev.mustCall("complete_task", map[string]any{"task_id": "t2", "status": "done", "summary": "没问题"})
	rw.lead.expectSend("审查（reviewer-1）：通过")
	if n := gates(); n != 1 {
		t.Fatalf("%d review gates for t1 after the verdict", n)
	}
	if p := rw.taskRow(t, rw.thread.ID, "t1"); p.Status != "review" {
		t.Fatalf("parent after review: %s", p.Status)
	}

	// The existing project follows later count changes (RolesSaved → EnsureStaff).
	rw.setRoleCount(t, "reviewer", 1)
	eventually(t, "reviewers 2 and 3 retired", func() bool { return reviewers("") == 1 && reviewers("AND name = 'reviewer-1'") == 1 })
	rw.setRoleCount(t, "reviewer", 3)
	if n := reviewers("AND instance_no IN (2, 3) AND node_id IS NOT NULL"); n != 2 {
		t.Fatalf("raising the count again: %d of reviewer-2/3 back with a node", n)
	}
}

// sendUsage reports the node's Claude five-hour window at used percent.
func sendUsage(t *testing.T, w *world, n *nodetest.Node, used float64) {
	t.Helper()
	ctx, cancel := w.waitCtx()
	defer cancel()
	resets := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	body := map[string]any{"runtimes": map[string]any{"claude": map[string]any{"auth": "subscription", "status": "ok",
		"windows": []any{map[string]any{"key": "five_hour", "used_percent": used, "window_minutes": 300, "resets_at": resets}}}}}
	if err := n.SendUnreliable(ctx, "node.usage", body); err != nil {
		t.Fatal(err)
	}
}

// TestReviewSyncFollowsThresholdMove: a reviewer that worked in the thread on node a, now over the
// user's five-hour threshold while node b is not, opens its next review session on b (a staying
// session honors the thresholds), so the pre-task review sync must create the worktree at the
// commit under review on b as well, not on a where the session no longer goes.
func TestReviewSyncFollowsThresholdMove(t *testing.T) {
	rw := newReviewWorld(t, reviewFeatures, nil)
	rw.answer(t, rw.reviewSync(t), reviewedSHA)
	rev, open := expectOpen(t, rw.w, rw.n, "reviewer-1")
	answerPrepares(t, rw.w, rw.n)
	rev.send(protocol.TypeSessionDelivered, protocol.SessionDelivered{SessionID: protocol.SessionID(rev.session), MsgID: open.FirstMessage.MsgID})
	rev.mustCall("complete_task", map[string]any{"task_id": "t2", "status": "failed", "summary": "缺少输入校验"})
	rw.lead.expectSend("预留给 dev-1 返工")
	// The reviewer's session ended after the review (context_clear: a gate role starts each review fresh).
	rw.sql(t, `UPDATE at_agent_sessions SET state = 'closed', closed_at = now(), close_reason = 'context_clear' WHERE id = $1`, rev.session)

	var b nodeCreated
	rw.b.must(201, "POST", "/api/nodes", map[string]string{"name": "b"}, &b)
	nb := rw.w.connect(b.Token, nodetest.Options{Features: reviewFeatures})
	sendUsage(t, rw.w, rw.n, 90)
	sendUsage(t, rw.w, nb, 5)
	eventually(t, "usage reported", func() bool {
		var nodes []usageNode
		rw.b.must(200, "GET", "/api/nodes", nil, &nodes)
		seen := 0
		for _, n := range nodes {
			if string(n.Usage) != "null" && len(n.Usage) > 0 {
				seen++
			}
		}
		return seen == 2
	})

	answerPrepares(t, rw.w, rw.n)
	rw.dev1.expectSend("[Task t1")
	gen := protocol.OpenGen(rw.dev1.gen)
	rw.dev1.send(protocol.TypeToolCall, protocol.ToolCall{CallID: "tc_rework_t1", SessionID: protocol.SessionID(rw.dev1.session), OpenGen: &gen,
		Tool: "complete_task", Args: []byte(`{"task_id":"t1","summary":"加了输入校验"}`)})
	push(t, rw.w, rw.gh, rw.n, "thread/1/dev-1", otherSHA)
	if res := rw.dev1.result("tc_rework_t1"); !res.Ok {
		t.Fatalf("rework complete: %+v", res)
	}

	ctx, cancel := rw.w.waitCtx()
	defer cancel()
	f, err := nb.Wait(ctx, protocol.TypeGitPrepare, func(f nodetest.Frame) bool {
		s := string(f.Body)
		return strings.Contains(s, `"sync"`) && strings.Contains(s, "reviewer-1")
	})
	if err != nil {
		t.Fatalf("no review sync on node b: %v", err)
	}
	var p protocol.GitPrepare
	_ = f.Decode(&p)
	if p.Sync == nil || p.Sync.ExpectSHA == nil || *p.Sync.ExpectSHA != otherSHA {
		t.Fatalf("review sync on b %+v", p.Sync)
	}
	head := otherSHA
	r := protocol.GitResult{JobID: p.JobID, Ok: true, HeadSHA: &head,
		Sync: &protocol.GitResultSync{Action: protocol.GitResultSyncActionCheckout, HeadSHA: otherSHA}}
	if _, err := nb.Send(ctx, protocol.TypeGitResult, r); err != nil {
		t.Fatal(err)
	}
	expectOpen(t, rw.w, nb, "reviewer-1")
}
