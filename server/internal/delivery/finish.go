package delivery

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/touwaeriol/agent-teams/server/internal/core"
)

// DefaultDoneGrace bounds how long a thread whose end is due waits on its lead (a turn still
// running, or the prompt to write the final report): past it the thread ends anyway.
const DefaultDoneGrace = 10 * time.Minute

// Why a thread's end is looked at (endThread).
type endSource int

const (
	endChecked endSource = iota // a caller found the end due (checks passed, PRs merged, finish)
	endTurn                     // the lead's turn just ended (its final reply is recorded)
	endTimeout                  // the grace period ran out
)

// finishNudge prompts a lead that never called finish(summary) once the thread can end.
const finishNudge = "（平台）[Thread #%d · 可以收尾] 讨论串的结束条件已经满足（PR 检查通过或已合并，没有未完成的任务，合并队列已清空），" +
	"但你还没有调用 finish(summary)。请现在写给用户的最终汇报（改了什么、测试结果、PR 链接、需要用户做的事），" +
	"并调用 finish(summary) 记下总结。这一回合结束后讨论串就结束（done）；%d 分钟内没有结束，平台也会结束它。"

// endDue reports whether the thread (locked) may end now as far as its work goes: it is going,
// no task is unfinished and no merge queue entry is active, and
//   - with pull requests: all of them are merged, or (merge_policy pr) they are open with
//     passing checks (ciPassed, or recorded by an earlier pass in done_pending_at);
//   - without changes in any repository: the lead called finish (summary, or done_pending_at).
func (e *Engine) endDue(ctx context.Context, tx *core.Tx, t core.Thread, ciPassed bool) (bool, error) {
	if t.Status != "running" && t.Status != "waiting" && t.Status != "review" {
		return false, nil
	}
	if busy, err := openTasksOrMerges(ctx, tx, t.ID); err != nil || busy {
		return false, err
	}
	repos, err := core.ThreadRepos(ctx, tx, t.ID)
	if err != nil {
		return false, err
	}
	hasPR, open, branched := false, false, false
	for _, r := range repos {
		hasPR = hasPR || r.PRNumber != nil
		open = open || r.HasOpenPR()
		branched = branched || r.BranchCreated
	}
	switch {
	case open:
		return t.Status == "review" && t.PRState == "open" && e.policy(ctx, t) == "pr" && (ciPassed || t.DonePendingAt != nil), nil
	case hasPR:
		return t.PRState == "merged", nil
	case branched:
		return false, nil // changes without a pull request: open_pr first
	}
	return t.DonePendingAt != nil || t.Summary != "", nil // finish was called (it records the summary)
}

// openTasksOrMerges reports a thread with an unfinished task or an active merge queue entry.
func openTasksOrMerges(ctx context.Context, q core.DB, threadID string) (bool, error) {
	tasks, err := core.ThreadTasks(ctx, q, threadID, core.TaskFilter{})
	if err != nil {
		return false, err
	}
	for _, task := range tasks {
		if !task.Finished() {
			return true, nil
		}
	}
	active, err := core.MergeQueue(ctx, q, threadID, true)
	return len(active) > 0, err
}

// endThread moves a thread (locked) whose end is due to done without cutting its lead off: while
// the lead is in a turn the end waits (done_pending_at) for that turn's end, when its final reply
// is recorded; a lead that never called finish(summary) is prompted once to write the final
// report and the thread ends after that turn. The grace period (DoneGrace) ends it regardless.
// It reports whether the thread is done.
func (e *Engine) endThread(ctx context.Context, tx *core.Tx, t core.Thread, src endSource, ciPassed bool) (bool, error) {
	due, err := e.endDue(ctx, tx, t, ciPassed)
	if err != nil {
		return false, err
	}
	if !due {
		// The end is no longer due (a task was added, a merge started): drop the pending mark so
		// its old timestamp cannot run the grace period out later, in the middle of a lead's turn.
		// It is set afresh once the end is due again.
		if t.DonePendingAt != nil {
			return false, core.UpdateThread(ctx, tx, t.ID, map[string]any{"done_pending_at": nil})
		}
		return false, nil
	}
	if src != endTimeout {
		wait, err := e.waitForLead(ctx, tx, t, src)
		if err != nil || wait {
			return false, err
		}
	}
	if err := core.UpdateThread(ctx, tx, t.ID, map[string]any{"done_pending_at": nil}); err != nil {
		return false, err
	}
	return true, e.orch.SetStatus(ctx, tx, t, "done", "")
}

// waitForLead decides whether the end waits for the lead, marking it pending (and prompting the
// lead once for its final report) when it does.
func (e *Engine) waitForLead(ctx context.Context, tx *core.Tx, t core.Thread, src endSource) (bool, error) {
	lead, err := core.Lead(ctx, tx, t.ID)
	if errors.Is(err, core.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	inTurn, err := leadInTurn(ctx, tx, lead)
	if err != nil {
		return false, err
	}
	switch {
	case inTurn:
		return true, e.markPending(ctx, tx, t)
	case t.Summary != "":
		return false, nil
	case t.FinishNudgedAt == nil:
		if err := core.UpdateThread(ctx, tx, t.ID, map[string]any{"finish_nudged_at": gorm.Expr("now()")}); err != nil {
			return false, err
		}
		if err := e.markPending(ctx, tx, t); err != nil {
			return false, err
		}
		mins := int(e.doneGrace() / time.Minute)
		return true, e.sched.NotifyLead(ctx, tx, t, fmt.Sprintf(finishNudge, t.No, max(mins, 1)))
	}
	// Prompted already: the turn that answers the prompt ends the thread (endTurn); a pass in
	// between (the merge loop) waits for it.
	return src != endTurn && t.DonePendingAt != nil, nil
}

func (e *Engine) markPending(ctx context.Context, tx *core.Tx, t core.Thread) error {
	if t.DonePendingAt != nil {
		return nil
	}
	if err := core.UpdateThread(ctx, tx, t.ID, map[string]any{"done_pending_at": gorm.Expr("now()")}); err != nil {
		return err
	}
	return tx.EmitThread(ctx, t.ID)
}

func leadInTurn(ctx context.Context, q core.DB, lead core.Employee) (bool, error) {
	s, err := core.LiveSession(ctx, q, lead.ID)
	if errors.Is(err, core.ErrNotFound) {
		return false, nil
	}
	return err == nil && s.InTurn && s.State == "active", err
}

func (e *Engine) doneGrace() time.Duration {
	if e.DoneGrace <= 0 {
		return DefaultDoneGrace
	}
	return e.DoneGrace
}

// LeadTurnEnded runs in the transaction of a lead's turn_ended (after its final reply was
// recorded): a thread whose end waited for the turn ends now.
func (e *Engine) LeadTurnEnded(ctx context.Context, tx *core.Tx, threadID string) error {
	t, err := core.LockThread(ctx, tx, threadID)
	if err != nil || t.DonePendingAt == nil {
		return ignoreMissing(err)
	}
	_, err = e.endThread(ctx, tx, t, endTurn, false)
	return err
}

// FinishCalled ends the thread after its lead called finish (in its own transaction, after the
// tool's): a thread without repository changes is marked to end (done_pending_at) and ends when
// the lead's turn does, so the final reply written after finish is kept; a thread whose pull
// requests are merged (it waited for its tasks or for finish) ends the same way.
func (e *Engine) FinishCalled(ctx context.Context, threadID string, noChanges bool) error {
	return e.core.InTx(ctx, func(tx *core.Tx) error {
		t, err := core.LockThread(ctx, tx, threadID)
		if err != nil {
			return ignoreMissing(err)
		}
		if noChanges && t.DonePendingAt == nil && (t.Status == "running" || t.Status == "waiting") {
			if err := e.markPending(ctx, tx, t); err != nil {
				return err
			}
			if t, err = core.LockThread(ctx, tx, threadID); err != nil {
				return err
			}
		}
		_, err = e.endThread(ctx, tx, t, endChecked, false)
		return err
	})
}

// doneTick ends the threads whose end waited past the grace period, and those whose pull
// requests are merged but that waited for their tasks or merge queue (they end once that clears).
func (e *Engine) doneTick(ctx context.Context) {
	var ids []string
	cutoff := e.now().Add(-e.doneGrace())
	err := e.core.Pool.Gorm(ctx).Table("at_threads").Where(`status IN ('running','waiting','review')
		AND (done_pending_at < ? OR (pr_state = 'merged' AND pr_number IS NOT NULL))`, cutoff).Pluck("id", &ids).Error
	if err != nil {
		e.log.Warn("done tick", "err", err)
		return
	}
	for _, id := range ids {
		err := e.core.InTx(ctx, func(tx *core.Tx) error {
			t, err := core.LockThread(ctx, tx, id)
			if err != nil {
				return ignoreMissing(err)
			}
			src := endChecked
			if t.DonePendingAt != nil && t.DonePendingAt.Before(cutoff) {
				src = endTimeout
			}
			_, err = e.endThread(ctx, tx, t, src, false)
			return err
		})
		if err != nil {
			e.log.Warn("done step", "thread_id", id, "err", err)
		}
	}
}
