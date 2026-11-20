package delivery

import (
	"context"
	"fmt"
	"strings"

	"github.com/touwaeriol/agent-teams/server/internal/core"
)

// endHeld tells the lead, once per set of blockers, that the thread could end (why: its PRs
// passed their checks or were merged) but unfinished small tasks hold it. Without this a task
// nobody works on (blocked, waiting on a cancelled one) keeps the thread in review for good while
// the page only says it waits on checks. The lead cancels or finishes them.
func (e *Engine) endHeld(ctx context.Context, threadID, why string) error {
	return e.core.InTx(ctx, func(tx *core.Tx) error {
		t, err := core.LockThread(ctx, tx, threadID)
		if err != nil || t.Status == "done" || t.Status == "failed" || t.Status == "archived" {
			e.held.Delete(threadID)
			return ignoreMissing(err)
		}
		open, err := unfinishedTasks(ctx, tx, threadID)
		if err != nil || len(open) == 0 {
			e.held.Delete(threadID)
			return err
		}
		key := strings.Join(open, "、")
		if prev, ok := e.held.Load(threadID); ok && prev == key {
			return nil
		}
		e.held.Store(threadID, key)
		return e.sched.NotifyLead(ctx, tx, t, fmt.Sprintf("[Thread #%d · 收尾被挡住] %s，但这些小任务还没结束，讨论串不能收尾：%s。"+
			"不再需要的用 cancel_task 取消；还需要的安排完成（blocked 的可以让用户在任务页重试）。全部结束后讨论串自动收尾。", t.No, why, key))
	})
}

// unfinishedTasks names the thread's unfinished tasks as "t6（review，blocked）".
func unfinishedTasks(ctx context.Context, q core.DB, threadID string) ([]string, error) {
	tasks, err := core.ThreadTasks(ctx, q, threadID, core.TaskFilter{})
	if err != nil {
		return nil, err
	}
	var out []string
	for _, task := range tasks {
		if !task.Finished() {
			out = append(out, fmt.Sprintf("%s（%s，%s）", task.ID, task.Kind, task.Status))
		}
	}
	return out, nil
}
