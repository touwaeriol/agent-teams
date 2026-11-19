package engine_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/core"
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
