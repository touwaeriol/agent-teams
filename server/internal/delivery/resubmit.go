package delivery

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/touwaeriol/agent-teams/server/internal/core"
)

// ErrTasksOpen refuses submitting a thread for review while small tasks are unfinished.
var ErrTasksOpen = errors.New("还有没完成的小任务")

// resubmitTx puts a thread that went back to running (failed checks, a reject) with its pull
// requests still open into review again (threads.md "结束"): pr_state open, merge_error cleared,
// the checks evaluated anew by the merge loop. Same preconditions as opening a pull request: an
// empty merge queue, every main task done (force skips it, as for open_pr) and no unfinished small
// task. It reports false when there is nothing to resubmit (not going, or no open pull request).
// t must be locked.
func (e *Engine) resubmitTx(ctx context.Context, tx *core.Tx, t core.Thread, force bool) (bool, error) {
	if t.Status != "running" && t.Status != "waiting" {
		return false, nil
	}
	prs, _, err := e.openPRs(ctx, t)
	if err != nil || len(prs) == 0 {
		return false, err
	}
	if err := e.submitReady(ctx, tx, t, force); err != nil {
		return false, err
	}
	if err := core.UpdateThread(ctx, tx, t.ID, map[string]any{"pr_state": "open", "merge_error": nil, "done_pending_at": nil}); err != nil {
		return false, err
	}
	if err := core.SetThreadPRStates(ctx, tx, t.ID, "open"); err != nil {
		return false, err
	}
	if err := tx.EmitThread(ctx, t.ID); err != nil {
		return false, err
	}
	tx.AfterCommit(func(ctx context.Context) { e.Kick(ctx) })
	return true, e.orch.SetStatus(ctx, tx, t, "review", "")
}

// submitReady refuses a submission while the merge queue is busy, a main task (unless force) or a
// small task is unfinished, naming what is still open.
func (e *Engine) submitReady(ctx context.Context, tx *core.Tx, t core.Thread, force bool) error {
	if err := e.prReadyIn(ctx, tx, t, force); err != nil {
		return err
	}
	if force {
		return nil
	}
	tasks, err := core.ThreadTasks(ctx, tx, t.ID, core.TaskFilter{})
	if err != nil {
		return err
	}
	var open []string
	for _, task := range tasks {
		if !task.Finished() {
			open = append(open, fmt.Sprintf("%s（%s）", task.ID, task.Status))
		}
	}
	if len(open) > 0 {
		return fmt.Errorf("%w：%s", ErrTasksOpen, strings.Join(open, "、"))
	}
	return nil
}

// resubmit is resubmitTx in a transaction of its own (open_pr on a thread whose PRs exist).
func (e *Engine) resubmit(ctx context.Context, threadID string, force bool) (bool, error) {
	var done bool
	err := e.core.InTx(ctx, func(tx *core.Tx) error {
		t, err := core.LockThread(ctx, tx, threadID)
		if err != nil {
			return err
		}
		done, err = e.resubmitTx(ctx, tx, t, force)
		return err
	})
	return done, err
}

// reviewWait says what a thread in review waits for under its project's merge_policy.
func (e *Engine) reviewWait(ctx context.Context, t core.Thread) string {
	switch e.policy(ctx, t) {
	case "auto":
		return "PR 检查全部通过后平台自动合并，合并后讨论串结束（done）"
	case "manual":
		return "等用户在页面上验收（点验收后平台等检查通过再合并），合并后讨论串结束（done）"
	}
	return "PR 检查全部通过后讨论串结束（done），PR 留给人在 GitHub 上合并"
}

// FinishWithPR runs inside finish for a thread that has pull requests: a thread back in running
// with open PRs is resubmitted for review (refused while work is open). It returns the tool's
// answer: what the thread waits for now, or that it ends after this turn.
func (e *Engine) FinishWithPR(ctx context.Context, tx *core.Tx, t core.Thread) (string, error) {
	t, err := core.LockThread(ctx, tx, t.ID)
	if err != nil {
		return "", err
	}
	resubmitted, err := e.resubmitTx(ctx, tx, t, false)
	if err != nil {
		return "", err
	}
	const tail = "检查没过会把失败的检查发给你；修完（或验收被打回、改完）后再调用 finish 重新提交。结束条件满足后，要等你这一回合结束（最终汇报发出）讨论串才结束。"
	if resubmitted {
		return "总结已记录，讨论串已重新提交验收（review）：" + e.reviewWait(ctx, t) + "。" + tail, nil
	}
	prs, _, err := e.openPRs(ctx, t)
	if err != nil {
		return "", err
	}
	switch {
	case t.Status == "review" && len(prs) > 0:
		return "总结已记录。讨论串在 review：" + e.reviewWait(ctx, t) + "。" + tail, nil
	case len(prs) == 0 && t.PRState == "merged":
		if busy, err := openTasksOrMerges(ctx, tx, t.ID); err != nil || busy {
			return "总结已记录。PR 已合并，但还有未完成的小任务或合并队列里还有分支，它们结束后讨论串才结束。", err
		}
		return "总结已记录。PR 已合并：这一回合结束后讨论串结束（done）。请把给用户的最终汇报写成本回合的最后一条回复。", nil
	}
	return "总结已记录。讨论串现在是 " + t.Status + "、PR 状态 " + orDefault(t.PRState, "无") + "，平台不会自动结束它；需要时请用户在页面上处理。", nil
}
