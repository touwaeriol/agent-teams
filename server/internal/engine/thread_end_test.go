package engine_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw/nodetest"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// The end of a thread never cuts its lead off (threads.md "结束"): a due end waits for the lead's
// turn, unfinished tasks and the merge queue; a lead that never called finish is prompted once.

// endCase is an acceptCase (merge_policy pr, one open PR whose checks pass) whose lead session
// is live on the fake node; the thread is still running until review() moves it on.
func newEndCase(t *testing.T, summary string) (*acceptCase, *fakeAgent) {
	t.Helper()
	c := newAcceptCase(t, "pr", "running")
	lead, _ := expectOpen(t, c.w, c.n, "lead-1")
	c.update(t, map[string]any{"summary": summary})
	return c, lead
}

func (c *acceptCase) update(t *testing.T, set map[string]any) {
	t.Helper()
	if err := c.w.e.Core.InTx(c.w.ctx, func(tx *core.Tx) error { return core.UpdateThread(c.w.ctx, tx, c.thread.ID, set) }); err != nil {
		t.Fatal(err)
	}
}

func (c *acceptCase) row(t *testing.T) core.Thread {
	t.Helper()
	th, err := core.ThreadByID(c.w.ctx, c.w.e.Core.Pool, c.thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	return th
}

// turnStarted starts a turn in the session (the lead is in the middle of it until turn_ended).
func turnStarted(a *fakeAgent) {
	a.event("turn_started", func(ev *protocol.SessionEvent) {
		ev.TriggerMsgIds, ev.Tools = []protocol.MsgID{"m_x"}, []string{"Read"}
	})
}

// finalReply is the lead's last reply of a turn followed by turn_ended.
func finalReply(a *fakeAgent, reply string) {
	a.event("text", func(e *protocol.SessionEvent) {
		final := true
		e.Text, e.Final = &reply, &final
	})
	turnEnded(a, 0, 1, 1)
}

// doneNote is the coordinator's "[Thread #n · title · done] …" note ("" while there is none).
func doneNote(t *testing.T, w *world, projectID string) string {
	t.Helper()
	var notes []string
	err := w.e.Core.Pool.Gorm(w.ctx).Table("at_messages").Where("author = 'platform' AND content LIKE ?",
		"[Thread #% · done]%").Pluck("content", &notes).Error
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) > 1 {
		t.Fatalf("coordinator told twice: %q", notes)
	}
	if len(notes) == 0 {
		return ""
	}
	return notes[0]
}

func leadReplies(t *testing.T, w *world, threadID string) []string {
	t.Helper()
	var out []string
	if err := w.e.Core.Pool.Gorm(w.ctx).Table("at_messages").Where("conversation_id = ? AND role = 'leader'", threadID).
		Order("id").Pluck("content", &out).Error; err != nil {
		t.Fatal(err)
	}
	return out
}

// stays fails when the thread leaves status within d (a few merge loop ticks).
func (c *acceptCase) stays(t *testing.T, status string, d time.Duration) core.ThreadView {
	t.Helper()
	deadline := time.Now().Add(d)
	var v core.ThreadView
	for time.Now().Before(deadline) {
		if v = c.view(t); v.Status != status {
			t.Fatalf("thread %s, want it to stay %s", v.Status, status)
		}
		time.Sleep(50 * time.Millisecond)
	}
	return v
}

// TestEndWaitsForLeadTurn: checks passing while the lead is in a turn do not end the thread; the
// turn's end does, after the final reply is recorded, and the coordinator gets the summary.
func TestEndWaitsForLeadTurn(t *testing.T) {
	c, lead := newEndCase(t, "购物车做完了\n细节见 PR")
	turnStarted(lead)
	c.update(t, map[string]any{"status": "review"})
	c.stays(t, "review", 800*time.Millisecond)
	eventually(t, "end pending", func() bool { return c.row(t).DonePendingAt != nil })
	if note := doneNote(t, c.w, c.thread.ProjectID); note != "" {
		t.Fatalf("coordinator told before the end: %q", note)
	}
	finalReply(lead, "最终汇报：购物车做完了，PR 已开")
	v := c.waitFor(t, "done after the turn", func(v core.ThreadView) bool { return v.Status == "done" })
	if v.PRState != "open" || c.row(t).DonePendingAt != nil {
		t.Fatalf("after the end: pr_state %q pending %v", v.PRState, c.row(t).DonePendingAt)
	}
	if r := leadReplies(t, c.w, c.thread.ID); len(r) == 0 || r[len(r)-1] != "最终汇报：购物车做完了，PR 已开" {
		t.Fatalf("final reply not recorded: %q", r)
	}
	if note := doneNote(t, c.w, c.thread.ProjectID); !strings.HasSuffix(note, "] 购物车做完了") {
		t.Fatalf("coordinator note %q", note)
	}
}

// TestEndNudgesLeadOnce: a lead that never called finish is prompted once to write its final
// report; the thread ends after the prompted turn and the coordinator gets that reply.
func TestEndNudgesLeadOnce(t *testing.T) {
	c, lead := newEndCase(t, "")
	c.update(t, map[string]any{"status": "review"})
	lead.expectSend("（平台）[Thread #1 · 可以收尾]")
	c.stays(t, "review", 800*time.Millisecond) // more loop ticks: no second prompt, no end
	var prompts int64
	if err := c.w.e.Core.Pool.Gorm(c.w.ctx).Table("at_messages").Where("conversation_id = ? AND content LIKE ?",
		c.thread.ID, "（平台）[Thread #1 · 可以收尾]%").Count(&prompts).Error; err != nil || prompts != 1 {
		t.Fatalf("prompts %d %v", prompts, err)
	}
	if th := c.row(t); th.FinishNudgedAt == nil || th.DonePendingAt == nil {
		t.Fatalf("nudge not recorded: %+v", th)
	}
	turnStarted(lead)
	finalReply(lead, "最终汇报：都做完了")
	c.waitFor(t, "done after the prompted turn", func(v core.ThreadView) bool { return v.Status == "done" })
	if note := doneNote(t, c.w, c.thread.ProjectID); !strings.HasSuffix(note, "] 最终汇报：都做完了") {
		t.Fatalf("coordinator note %q", note)
	}
}

// TestEndGraceTimeout: a lead stuck in its turn does not keep the thread going forever.
func TestEndGraceTimeout(t *testing.T) {
	c, lead := newEndCase(t, "总结")
	c.w.e.Delivery.DoneGrace = time.Second
	turnStarted(lead)
	c.update(t, map[string]any{"status": "review"})
	eventually(t, "end pending", func() bool { return c.row(t).DonePendingAt != nil })
	c.waitFor(t, "done after the grace period", func(v core.ThreadView) bool { return v.Status == "done" })
	if s := sessionOf(t, c.w, lead.session); s.State == "active" && s.InTurn {
		ctx, cancel := c.w.waitCtx()
		defer cancel()
		if _, err := c.n.Wait(ctx, protocol.TypeSessionClose, lead.mine); err != nil {
			t.Fatal(err)
		}
	}
}

// TestMergedWaitsForTasks: a PR merged on GitHub while a task is unfinished records pr_state
// merged but does not end the thread; it ends once the task is.
func TestMergedWaitsForTasks(t *testing.T) {
	c, lead := newEndCase(t, "总结")
	lead.mustCall("update_plan", map[string]any{"steps": []map[string]string{{"title": "做", "status": "done"}}})
	res := lead.mustCall("assign_task", map[string]any{"assignee": "dev", "instruction": "补一个测试", "main": "做"})
	var task protocol.AssignTaskResult
	_ = json.Unmarshal(*res.Structured, &task)
	c.update(t, map[string]any{"status": "review"})
	c.stays(t, "review", 600*time.Millisecond) // checks pass, but a task is open
	c.set(func(gh *fakeGitHub) { gh.pulls["o/r#"+itoa(c.pr)].merged = true })
	payload, _ := json.Marshal(map[string]any{"action": "closed", "pull_request": map[string]any{"html_url": prURL("o/r", c.pr), "merged": true}})
	if err := c.w.e.Delivery.GitHubEvent(c.w.ctx, "pull_request", payload); err != nil {
		t.Fatal(err)
	}
	v := c.stays(t, "review", 600*time.Millisecond)
	if v.PRState != "merged" || v.Repos[0].PRState != "merged" {
		t.Fatalf("merged not recorded: %q %+v", v.PRState, v.Repos)
	}
	lead.mustCall("cancel_task", map[string]any{"task_id": string(task.TaskID), "reason": "不需要了"})
	c.waitFor(t, "done once the task is", func(v core.ThreadView) bool { return v.Status == "done" })
}

// TestMergedWaitsForMergeQueue: an active merge queue entry holds the end like a task does (the
// entry's trial checks keep running, so the merge loop leaves it in ci_running).
func TestMergedWaitsForMergeQueue(t *testing.T) {
	c, _ := newEndCase(t, "总结")
	c.set(func(gh *fakeGitHub) {
		gh.checks["sha-trial"] = []map[string]any{{"name": "verify", "status": "in_progress"}}
	})
	if _, err := store.Exec(c.w.ctx, c.w.e.Core.Pool, `INSERT INTO at_merge_queue (id, thread_id, employee, repo, head_branch, head_sha,
		trial_branch, trial_sha, status, position) VALUES ('mq_x', $1, 'dev-1', 'o/r', 'thread/1-acc/dev-1', 'sha-x',
		'trial/1-acc/mq_x', 'sha-trial', 'ci_running', 1)`, c.thread.ID); err != nil {
		t.Fatal(err)
	}
	c.update(t, map[string]any{"status": "review"})
	c.stays(t, "review", 800*time.Millisecond)
	var st string
	if err := c.w.e.Core.Pool.Gorm(c.w.ctx).Table("at_merge_queue").Select("status").Where("id = 'mq_x'").Scan(&st).Error; err != nil || st != "ci_running" {
		t.Fatalf("entry %q %v (the test needs it active)", st, err)
	}
	if _, err := store.Exec(c.w.ctx, c.w.e.Core.Pool, `UPDATE at_merge_queue SET status = 'merged' WHERE id = 'mq_x'`); err != nil {
		t.Fatal(err)
	}
	c.waitFor(t, "done once the queue is empty", func(v core.ThreadView) bool { return v.Status == "done" })
}

// TestFinishThenFinalReply: finish on a thread without changes keeps the turn going: the final
// reply written after it is recorded, then the thread is done (coordinator gets the summary).
func TestFinishThenFinalReply(t *testing.T) {
	tm := startTeam(t)
	tm.dev.mustCall("complete_task", map[string]any{"task_id": "t1", "summary": "查完了"})
	tm.lead.expectSend("[Task t1 · dev-1 · done]")
	turnStarted(tm.lead)
	res := tm.lead.mustCall("finish", map[string]string{"summary": "调研结论：方案 A\n细节"})
	if !strings.Contains(text(res), "回合结束后讨论串结束") {
		t.Fatalf("finish result %q", text(res))
	}
	time.Sleep(600 * time.Millisecond)
	if st, _ := threadStatus(tm); st == "done" {
		t.Fatal("done in the middle of the lead's turn")
	}
	finalReply(tm.lead, "最终汇报：方案 A 更好")
	eventually(t, "thread done", func() bool { st, _ := threadStatus(tm); return st == "done" })
	if r := leadReplies(t, tm.w, tm.thread.ID); len(r) == 0 || r[len(r)-1] != "最终汇报：方案 A 更好" {
		t.Fatalf("final reply not recorded: %q", r)
	}
	if note := doneNote(t, tm.w, tm.projectID); !strings.HasSuffix(note, "] 调研结论：方案 A") {
		t.Fatalf("coordinator note %q", note)
	}
}

// TestCompleteDuringLeadTurn: the user marking a thread done on the page ends it at once, even
// in the middle of the lead's turn.
func TestCompleteDuringLeadTurn(t *testing.T) {
	tm := startTeam(t)
	turnStarted(tm.lead)
	tm.b.must(200, "POST", "/api/threads/"+tm.thread.ID+"/complete", map[string]any{}, nil)
	if st, _ := threadStatus(tm); st != "done" {
		t.Fatalf("status %s", st)
	}
	ctx, cancel := tm.w.waitCtx()
	defer cancel()
	if _, err := tm.n.Wait(ctx, protocol.TypeSessionClose, tm.lead.mine); err != nil {
		t.Fatal(err)
	}
}

// A thread back in running (failed checks, a reject) with its PRs open goes back to review when
// its lead calls finish or open_pr again; a missing CI stays in review for the user.

func (c *acceptCase) checks(conclusion string) {
	c.set(func(gh *fakeGitHub) {
		gh.checks["sha-thread"] = []map[string]any{{"name": "verify", "status": "completed", "conclusion": conclusion}}
	})
}

// TestResubmitAfterCIFail: review → failed checks → running (lead told) → lead fixes and calls
// finish → review again → checks pass → done.
func TestResubmitAfterCIFail(t *testing.T) {
	c, lead := newEndCase(t, "总结")
	c.checks("failure")
	c.update(t, map[string]any{"status": "review"})
	lead.expectSend("[Thread #1 · CI 失败]")
	c.waitFor(t, "back to running", func(v core.ThreadView) bool { return v.Status == "running" })
	c.checks("success") // the lead fixed CI
	turnStarted(lead)
	res := lead.mustCall("finish", map[string]string{"summary": "修好了 CI"})
	if !strings.Contains(text(res), "重新提交验收") || !strings.Contains(text(res), "PR 留给人") {
		t.Fatalf("finish result %q", text(res))
	}
	v := c.view(t)
	if v.Status != "review" || v.PRState != "open" || v.MergeError != nil {
		t.Fatalf("after finish: %s %q %v", v.Status, v.PRState, mergeError(v))
	}
	c.stays(t, "review", 600*time.Millisecond) // checks pass, the lead is still in its turn
	finalReply(lead, "最终汇报：CI 修好了")
	c.waitFor(t, "done once the checks pass", func(v core.ThreadView) bool { return v.Status == "done" })
}

// TestResubmitAfterReject: manual acceptance rejected → running → open_pr again resubmits (no
// ErrPRExists) → review → the user accepts → merged and done.
func TestResubmitAfterReject(t *testing.T) {
	c := newAcceptCase(t, "manual", "review")
	lead, _ := expectOpen(t, c.w, c.n, "lead-1")
	c.checks("success")
	if v := c.reject(t); v.Status != "running" {
		t.Fatalf("after reject: %s", v.Status)
	}
	lead.expectSend("验收未通过")
	res := lead.mustCall("open_pr", map[string]any{"title": "Acc", "body": "改好了"})
	if !strings.Contains(text(res), "重新提交验收") {
		t.Fatalf("open_pr result %q", text(res))
	}
	if v := c.view(t); v.Status != "review" || v.Counts.Waiting != 1 {
		t.Fatalf("after open_pr: %s waiting %d", v.Status, v.Counts.Waiting)
	}
	c.reject(t)
	lead.expectSend("验收未通过")
	res = lead.mustCall("finish", map[string]string{"summary": "又改好了"})
	if !strings.Contains(text(res), "等用户在页面上验收") {
		t.Fatalf("finish result %q", text(res))
	}
	c.accept(t)
	v := c.waitFor(t, "merged", func(v core.ThreadView) bool { return v.Status == "done" })
	if v.PRState != "merged" {
		t.Fatalf("pr_state %q", v.PRState)
	}
}

// TestCIMissingStaysInReview: no check ever appears and on_missing = fail (block): the thread stays in
// review with merge_error for the user (waiting), the lead is not told to fix it.
func TestCIMissingStaysInReview(t *testing.T) {
	c, lead := newEndCase(t, "总结")
	c.b.must(200, "PATCH", "/api/projects/"+c.thread.ProjectID, map[string]any{"delivery": map[string]any{
		"ci": map[string]any{"mode": "checks", "appear_timeout_s": 1, "on_missing": "fail"}}}, nil)
	c.set(func(gh *fakeGitHub) { gh.checks["sha-thread"] = []map[string]any{} }) // no CI on the repository
	c.update(t, map[string]any{"status": "review"})
	v := c.waitFor(t, "missing CI recorded", func(v core.ThreadView) bool { return v.MergeError != nil })
	if !strings.Contains(mergeError(v), "没有出现项目要求的 CI 检查") || v.Counts.Waiting != 1 {
		t.Fatalf("merge_error %q waiting %d", mergeError(v), v.Counts.Waiting)
	}
	c.stays(t, "review", 800*time.Millisecond)
	expectNone(t, c.n, protocol.TypeSessionSend, 300*time.Millisecond, func(f nodetest.Frame) bool {
		return lead.mine(f) && strings.Contains(string(f.Body), "CI 失败")
	})
}

// TestResubmitRefusedWithOpenTasks: finish on a thread back in running is refused (nothing
// recorded) while a small task is unfinished.
func TestResubmitRefusedWithOpenTasks(t *testing.T) {
	c, lead := newEndCase(t, "")
	lead.mustCall("update_plan", map[string]any{"steps": []map[string]string{{"title": "做", "status": "done"}}})
	lead.mustCall("assign_task", map[string]any{"assignee": "dev", "instruction": "修 CI", "main": "做"})
	// A member is on it (finish cancels tasks nobody has picked up yet).
	eventually(t, "task claimed", func() bool {
		var n int
		_ = store.QueryRow(c.w.ctx, c.w.e.Core.Pool, `SELECT count(*) FROM at_tasks WHERE status NOT IN ('pending', 'blocked') AND status <> 'cancelled'`).Scan(&n)
		return n > 0
	})
	res := lead.call("finish", map[string]string{"summary": "提前收尾"})
	if res.Ok || !strings.Contains(text(res), "还有没完成的小任务") {
		t.Fatalf("finish with an open task: %+v", res)
	}
	if v := c.view(t); v.Status != "running" || v.Summary != "" {
		t.Fatalf("after the refusal: %s %q", v.Status, v.Summary)
	}
}

// TestStalePendingEndDoesNotTimeOut: the end became due during a lead's turn, then the lead
// added a task (the end is no longer due); long after, while the lead writes its final report
// for that task, the old mark must not run the grace period out under it (review of t1).
func TestStalePendingEndDoesNotTimeOut(t *testing.T) {
	c, lead := newEndCase(t, "总结")
	c.w.e.Delivery.DoneGrace = 2 * time.Second
	lead.mustCall("update_plan", map[string]any{"steps": []map[string]string{{"title": "做", "status": "done"}}})
	turnStarted(lead)
	c.update(t, map[string]any{"status": "review"})
	eventually(t, "end pending", func() bool { return c.row(t).DonePendingAt != nil })
	res := lead.mustCall("assign_task", map[string]any{"assignee": "dev", "instruction": "再审一遍", "main": "做"})
	var task protocol.AssignTaskResult
	_ = json.Unmarshal(*res.Structured, &task)
	finalReply(lead, "补派了审查，结果回来后出最终汇报")
	eventually(t, "pending dropped", func() bool { return c.row(t).DonePendingAt == nil })
	time.Sleep(2500 * time.Millisecond) // longer than the grace period
	turnStarted(lead)
	lead.mustCall("cancel_task", map[string]any{"task_id": string(task.TaskID), "reason": "审完了"})
	v := c.stays(t, "review", 1200*time.Millisecond)
	if p := c.row(t).DonePendingAt; p == nil || time.Since(*p) > 2*time.Second {
		t.Fatalf("pending mark %v (want a fresh one) status %s", p, v.Status)
	}
	finalReply(lead, "最终汇报：审查通过")
	c.waitFor(t, "done after the report", func(v core.ThreadView) bool { return v.Status == "done" })
	if r := leadReplies(t, c.w, c.thread.ID); r[len(r)-1] != "最终汇报：审查通过" {
		t.Fatalf("final reply %q", r)
	}
}

// TestMergedThenTaskDoesNotTimeOut: the PR is merged on GitHub during the lead's turn and the lead
// adds a task in that turn; past the grace period, while the lead answers the task's result, the
// thread is not ended under it; it ends after that turn.
func TestMergedThenTaskDoesNotTimeOut(t *testing.T) {
	c, lead := newEndCase(t, "总结")
	c.w.e.Delivery.DoneGrace = 2 * time.Second
	lead.mustCall("update_plan", map[string]any{"steps": []map[string]string{{"title": "做", "status": "done"}}})
	turnStarted(lead)
	c.set(func(gh *fakeGitHub) { gh.pulls["o/r#"+itoa(c.pr)].merged = true })
	payload, _ := json.Marshal(map[string]any{"action": "closed", "pull_request": map[string]any{"html_url": prURL("o/r", c.pr), "merged": true}})
	if err := c.w.e.Delivery.GitHubEvent(c.w.ctx, "pull_request", payload); err != nil {
		t.Fatal(err)
	}
	if th := c.row(t); th.Status == "done" || th.DonePendingAt == nil || th.PRState != "merged" {
		t.Fatalf("merged in the lead's turn: %s pending %v %q", th.Status, th.DonePendingAt, th.PRState)
	}
	res := lead.mustCall("assign_task", map[string]any{"assignee": "dev", "instruction": "补文档", "main": "做"})
	var task protocol.AssignTaskResult
	_ = json.Unmarshal(*res.Structured, &task)
	finalReply(lead, "PR 合并了，补派了文档任务")
	eventually(t, "pending dropped", func() bool { return c.row(t).DonePendingAt == nil })
	time.Sleep(2500 * time.Millisecond)
	turnStarted(lead)
	lead.mustCall("cancel_task", map[string]any{"task_id": string(task.TaskID), "reason": "文档写好了"})
	c.stays(t, "running", 1200*time.Millisecond)
	finalReply(lead, "最终汇报：都合并了")
	c.waitFor(t, "done after the report", func(v core.ThreadView) bool { return v.Status == "done" })
	if r := leadReplies(t, c.w, c.thread.ID); r[len(r)-1] != "最终汇报：都合并了" {
		t.Fatalf("final reply %q", r)
	}
}

// TestReadOnlyFinishThenTask: a thread without changes whose lead calls finish and then assigns a
// task in the same turn does not end with that turn (the task is open), nor while the lead answers
// the task's result; it ends when that turn ends.
func TestReadOnlyFinishThenTask(t *testing.T) {
	tm := startTeam(t)
	tm.w.e.Delivery.DoneGrace = 2 * time.Second
	tm.dev.mustCall("complete_task", map[string]any{"task_id": "t1", "summary": "查完了"})
	tm.lead.expectSend("[Task t1 · dev-1 · done]")
	turnStarted(tm.lead)
	tm.lead.mustCall("finish", map[string]string{"summary": "调研结论"})
	tm.lead.mustCall("assign_task", map[string]any{"assignee": "dev-1", "instruction": "再核对一个数据", "main": "做"})
	finalReply(tm.lead, "补派了核对，结果回来后出最终汇报")
	time.Sleep(2500 * time.Millisecond) // past the grace period
	if st, _ := threadStatus(tm); st == "done" {
		t.Fatal("done with a task open")
	}
	tm.dev.expectSend("[Task t2")
	tm.dev.mustCall("complete_task", map[string]any{"task_id": "t2", "summary": "核对过了"})
	tm.lead.expectSend("[Task t2 · dev-1 · done]")
	turnStarted(tm.lead)
	time.Sleep(800 * time.Millisecond)
	if st, _ := threadStatus(tm); st == "done" {
		t.Fatal("done in the middle of the lead's turn")
	}
	finalReply(tm.lead, "最终汇报：数据核对无误")
	eventually(t, "thread done", func() bool { st, _ := threadStatus(tm); return st == "done" })
	if r := leadReplies(t, tm.w, tm.thread.ID); r[len(r)-1] != "最终汇报：数据核对无误" {
		t.Fatalf("final reply %q", r)
	}
}

// TestMergedWithoutWebhook: a PR a person merged on GitHub is noticed by the poll alone (no
// webhook): pr_state becomes merged, an unfinished task holding the end is reported to the lead
// once, and the thread ends when the lead cancels it.
func TestMergedWithoutWebhook(t *testing.T) {
	c, lead := newEndCase(t, "总结")
	lead.mustCall("update_plan", map[string]any{"steps": []map[string]string{{"title": "做", "status": "done"}}})
	res := lead.mustCall("assign_task", map[string]any{"assignee": "dev", "instruction": "补一个测试", "main": "做"})
	var task protocol.AssignTaskResult
	_ = json.Unmarshal(*res.Structured, &task)
	c.update(t, map[string]any{"status": "review"})
	c.set(func(gh *fakeGitHub) { gh.pulls["o/r#"+itoa(c.pr)].merged = true })
	v := c.waitFor(t, "merged recorded", func(v core.ThreadView) bool { return v.PRState == "merged" })
	if v.Status != "review" || v.Repos[0].PRState != "merged" {
		t.Fatalf("after the merge: status %s repos %+v", v.Status, v.Repos)
	}
	held := func() []string {
		var out []string
		if err := c.w.e.Core.Pool.Gorm(c.w.ctx).Table("at_messages").Where("conversation_id = ? AND content LIKE ?",
			c.thread.ID, "%收尾被挡住%").Pluck("content", &out).Error; err != nil {
			t.Fatal(err)
		}
		return out
	}
	eventually(t, "lead told what holds the end", func() bool { return len(held()) > 0 })
	c.stays(t, "review", 600*time.Millisecond)
	if h := held(); len(h) != 1 || !strings.Contains(h[0], string(task.TaskID)) || !strings.Contains(h[0], "PR 已合并") {
		t.Fatalf("held notes: %q", h)
	}
	lead.mustCall("cancel_task", map[string]any{"task_id": string(task.TaskID), "reason": "不需要了"})
	c.waitFor(t, "done once the task is", func(v core.ThreadView) bool { return v.Status == "done" })
}

// TestFinishDropsIdleTasks: the lead calling finish decides the thread is done: tasks nobody
// works on (a blocked one here) are cancelled instead of holding the end, and the thread ends.
func TestFinishDropsIdleTasks(t *testing.T) {
	c, lead := newEndCase(t, "")
	lead.mustCall("update_plan", map[string]any{"steps": []map[string]string{{"title": "做", "status": "done"}}})
	res := lead.mustCall("assign_task", map[string]any{"assignee": "dev", "instruction": "补一个测试", "main": "做"})
	var task protocol.AssignTaskResult
	_ = json.Unmarshal(*res.Structured, &task)
	if _, err := store.Exec(c.w.ctx, c.w.e.Core.Pool, `UPDATE at_tasks SET status = 'blocked', claimed_by = '' WHERE conversation_id = $1 AND id = $2`,
		c.thread.ID, string(task.TaskID)); err != nil {
		t.Fatal(err)
	}
	c.update(t, map[string]any{"status": "review"})
	c.stays(t, "review", 600*time.Millisecond) // the blocked task holds the end
	out := lead.mustCall("finish", map[string]any{"summary": "做完了"})
	if got := text(out); !strings.Contains(got, "已取消没人在做的任务："+string(task.TaskID)) {
		t.Fatalf("finish result: %s", got)
	}
	c.waitFor(t, "done after finish", func(v core.ThreadView) bool { return v.Status == "done" })
	got, err := core.TaskByID(c.w.ctx, c.w.e.Core.Pool, c.thread.ID, string(task.TaskID))
	if err != nil || got.Status != "cancelled" {
		t.Fatalf("task %+v %v", got, err)
	}
}

// TestEndNoteSurvivesDeadlock: the thread's end reports to the coordinator inside the ending
// transaction (a savepoint); when that report is picked as a deadlock victim (40P01) — another
// transaction holds the coordinator's row and waits for the project row the ending transaction
// already holds — the thread still ends and the coordinator still gets its note, sent after the
// commit in a transaction of its own.
func TestEndNoteSurvivesDeadlock(t *testing.T) {
	c, lead := newEndCase(t, "购物车做完了")
	turnStarted(lead)
	c.update(t, map[string]any{"status": "review"})
	eventually(t, "end pending", func() bool { return c.row(t).DonePendingAt != nil })
	coord, _, err := c.w.e.Orch.EnsureCoordinator(c.w.ctx, c.row(t).UserID, c.thread.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	db := c.w.e.Core.Pool.SQL()
	other, err := db.BeginTx(c.w.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Rollback() }()
	var pid int
	if err := other.QueryRow(`SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	if _, err := other.Exec(`SELECT 1 FROM at_employees WHERE id = $1 FOR UPDATE`, coord.ID); err != nil {
		t.Fatal(err)
	}
	replied := make(chan struct{})
	go func() { defer close(replied); finalReply(lead, "最终汇报：购物车做完了") }()
	// The ending transaction (project row held since its first event) now waits on the coordinator's row.
	eventually(t, "end waits on the coordinator row", func() bool {
		var n int
		_ = db.QueryRow(`SELECT count(*) FROM pg_stat_activity WHERE $1 = ANY(pg_blocking_pids(pid))`, pid).Scan(&n)
		return n > 0
	})
	// Close the cycle: the other side now waits for the project row. The ending transaction waited
	// first, so its report is the deadlock victim; the other side gets the row once it commits.
	if _, err := other.Exec(`SELECT 1 FROM at_projects WHERE id = $1 FOR UPDATE`, c.thread.ProjectID); err != nil {
		t.Fatalf("other side: %v", err)
	}
	if err := other.Rollback(); err != nil {
		t.Fatal(err)
	}
	c.waitFor(t, "done", func(v core.ThreadView) bool { return v.Status == "done" })
	eventually(t, "coordinator note after the deadlock", func() bool { return strings.HasSuffix(doneNote(t, c.w, c.thread.ProjectID), "] 购物车做完了") })
	<-replied
}
