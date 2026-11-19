package engine_test

import (
	"context"
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
}

func newReviewWorld(t *testing.T, features []string, tune func(*engine.Engine)) *reviewWorld {
	t.Helper()
	gh, ghSrv := newFakeGitHub(t)
	w := newWorldWith(t, ghSrv, tune)
	tm := &team{w: w, b: w.signUp("review-sync@example.com")}
	var node nodeCreated
	tm.b.must(201, "POST", "/api/nodes", map[string]string{"name": "fake"}, &node)
	var roles struct{ Roles []store.Role }
	tm.b.must(200, "GET", "/api/roles", nil, &roles)
	reviewer := roles.Roles[2]
	reviewer.ID, reviewer.Role, reviewer.Title, reviewer.Count, reviewer.Gate = "", "reviewer", "审查", 1, "review"
	tm.b.must(200, "PUT", "/api/roles", map[string]any{"roles": append(roles.Roles, reviewer)}, nil)
	var conn struct{ ID string }
	tm.b.must(201, "POST", "/api/git/connections", map[string]string{"name": "gh", "token": "ghp_good"}, &conn)
	var project struct{ ID string }
	tm.b.must(201, "POST", "/api/projects", map[string]any{"name": "web",
		"repos": []map[string]any{{"repo": "o/r", "git_connection_id": conn.ID}}}, &project)
	tm.projectID = project.ID
	tm.n = w.connect(node.Token, nodetest.Options{Features: features})
	n := tm.n

	rw := &reviewWorld{team: tm}
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
	rev, open := expectOpen(t, rw.w, rw.n, "reviewer-1")
	answerPrepares(t, rw.w, rw.n)
	rev.send(protocol.TypeSessionDelivered, protocol.SessionDelivered{SessionID: protocol.SessionID(rev.session), MsgID: open.FirstMessage.MsgID})
	brief := open.FirstMessage.Text
	for _, want := range []string{"审查 dev-1 的任务 t1", "被审提交：o/r " + reviewedSHA, "o/r：平台已检出并核对，HEAD = " + reviewedSHA, "platform_error"} {
		if !strings.Contains(brief, want) {
			t.Errorf("review brief misses %q:\n%s", want, brief)
		}
	}
	if strings.Contains(brief, "平台没能核对") {
		t.Errorf("verified review asks the reviewer to check HEAD:\n%s", brief)
	}
	// A gate blocked for another reason (turns ended without complete_task) stays the reviewer's:
	// nudged by the lead, it can still hand in its verdict.
	rw.sql(t, `UPDATE at_tasks SET status = 'blocked' WHERE thread_id = $1 AND id = 't2'`, rw.thread.ID)
	rev.mustCall("complete_task", map[string]any{"task_id": "t2", "status": "done", "summary": "看过了，没问题"})
	rw.lead.expectSend("审查（reviewer-1）：通过")
	if errs := rw.n.Errors(); len(errs) > 0 {
		t.Fatalf("protocol errors: %+v", errs)
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
