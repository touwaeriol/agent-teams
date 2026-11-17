package scheduler

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/orchestrator"
)

// Outcome is what a member reports with complete_task.
type Outcome struct {
	OK      bool
	Summary string
	HeadSHA string
	Pushed  map[string]string // repo → pushed head sha (nil: leave the task's record alone)
}

// Complete applies complete_task (after the platform pushed the branch) and returns the task.
func (s *Scheduler) Complete(ctx context.Context, tx *core.Tx, task core.Task, out Outcome) (core.Task, error) {
	set := map[string]any{"summary": out.Summary, "head_sha": out.HeadSHA, "nudges": 0}
	if out.Pushed != nil {
		set["pushed"] = out.Pushed
	}
	task, err := core.UpdateTask(ctx, tx, task, set)
	if err != nil {
		return task, err
	}
	switch {
	case isGateTask(task):
		return s.gateResult(ctx, tx, task, out)
	case !out.OK:
		return s.failOrRetry(ctx, tx, task, out.Summary)
	case isSubtask(task):
		return s.finish(ctx, tx, task, "done")
	}
	return s.completeWork(ctx, tx, task)
}

func isGateTask(t core.Task) bool {
	return (t.Kind == "review" || t.Kind == "decision") && t.CreatedBy == "platform"
}

// completeWork: without a repository the task is done; with one it waits for review and the merge queue.
func (s *Scheduler) completeWork(ctx context.Context, tx *core.Tx, task core.Task) (core.Task, error) {
	th, err := core.ThreadByID(ctx, tx, task.ConversationID)
	if err != nil {
		return task, err
	}
	if !s.orch.HasRepo(ctx, th.ProjectID) {
		task, err = s.finish(ctx, tx, task, "done")
		if err != nil {
			return task, err
		}
		return task, s.notifyLead(ctx, tx, th, fmt.Sprintf("[Task %s · %s · done] %s", task.ID, task.ClaimedBy, task.Summary))
	}
	task, err = s.setStatus(ctx, tx, task, "review")
	if err != nil {
		return task, err
	}
	return task, s.afterWork(ctx, tx, th, task)
}

// afterWork opens the review gate when a reviewer exists, else tells the lead.
func (s *Scheduler) afterWork(ctx context.Context, tx *core.Tx, th core.Thread, task core.Task) error {
	d, err := core.DeliveryOf(ctx, tx, th.ProjectID)
	if err != nil {
		return err
	}
	if role := gateRole(ctx, tx, th.ID, "review"); d.Gates.Review && role != "" {
		return s.openGate(ctx, tx, th, task, "review", role)
	}
	return s.notifyLead(ctx, tx, th, fmt.Sprintf("[Task %s · %s · done] %s\n分支：%s%s", task.ID, task.ClaimedBy, task.Summary, task.Branch, pushedNote(task)))
}

// gateRole is the role of the thread's member responsible for a gate ("" when none).
func gateRole(ctx context.Context, q core.DB, threadID, gate string) string {
	emps, err := core.ThreadEmployees(ctx, q, threadID)
	if err != nil {
		return ""
	}
	for _, e := range emps {
		if e.Tier == "member" && e.Spec.Gate == gate {
			return e.Role
		}
	}
	return ""
}

func (s *Scheduler) openGate(ctx context.Context, tx *core.Tx, th core.Thread, task core.Task, kind, role string) error {
	verb := map[string]string{"review": "审查", "decision": "决策"}[kind]
	text := fmt.Sprintf("%s %s 的任务 %s（分支 %s）。\n原任务：%s\n成员总结：%s\n通过用 complete_task(status=done) 写结论；驳回用 status=failed 写原因。",
		verb, task.ClaimedBy, task.ID, task.Branch, task.Instruction, task.Summary)
	gate, err := core.InsertTask(ctx, tx, core.NewTask{Thread: th, MainTaskID: task.MainTaskID, Assignee: role, Role: role,
		Kind: kind, Instruction: text, Parent: task.ID, Priority: 50, CreatedBy: "platform"})
	if err != nil {
		return err
	}
	if err := tx.EmitTask(ctx, gate); err != nil {
		return err
	}
	if kind == "decision" {
		if _, err := s.setStatus(ctx, tx, task, "decision"); err != nil {
			return err
		}
	}
	s.kickAfter(tx, th.ID)
	return nil
}

// gateResult handles the reviewer's or decider's verdict on the parent task.
func (s *Scheduler) gateResult(ctx context.Context, tx *core.Tx, gate core.Task, out Outcome) (core.Task, error) {
	final := "done"
	if !out.OK {
		final = "failed"
	}
	gate, err := s.finish(ctx, tx, gate, final)
	if err != nil {
		return gate, err
	}
	parent, err := core.LockTask(ctx, tx, gate.ConversationID, gate.Parent)
	if err != nil {
		return gate, ignoreMissing(err)
	}
	th, err := core.ThreadByID(ctx, tx, gate.ConversationID)
	if err != nil {
		return gate, err
	}
	if !out.OK {
		return gate, s.rejected(ctx, tx, th, parent, gate)
	}
	return gate, s.passed(ctx, tx, th, parent, gate)
}

func (s *Scheduler) passed(ctx context.Context, tx *core.Tx, th core.Thread, parent, gate core.Task) error {
	d, err := core.DeliveryOf(ctx, tx, th.ProjectID)
	if err != nil {
		return err
	}
	if role := gateRole(ctx, tx, th.ID, "decision"); gate.Kind == "review" && d.Gates.Decision && role != "" {
		return s.openGate(ctx, tx, th, parent, "decision", role)
	}
	if parent.Status == "decision" {
		if _, err := s.setStatus(ctx, tx, parent, "review"); err != nil {
			return err
		}
	}
	return s.notifyLead(ctx, tx, th, fmt.Sprintf("[Task %s · %s · done] %s\n%s（%s）：通过。%s\n分支：%s",
		parent.ID, parent.ClaimedBy, parent.Summary, gateName(gate), gate.ClaimedBy, gate.Summary, parent.Branch))
}

func gateName(t core.Task) string {
	if t.Kind == "decision" {
		return "决策"
	}
	return "审查"
}

// rejected sends a reviewed task back to its member and informs the lead.
func (s *Scheduler) rejected(ctx context.Context, tx *core.Tx, th core.Thread, parent, gate core.Task) error {
	text := fmt.Sprintf("[%s %s · %s · rejected] %s", gateName(gate), gate.ID, gate.ClaimedBy, gate.Summary)
	if gate.Kind == "decision" {
		return s.notifyLead(ctx, tx, th, text+fmt.Sprintf("\n任务 %s 等你决定。", parent.ID))
	}
	parent, err := core.UpdateTask(ctx, tx, parent, map[string]any{"status": "running", "nudges": 0})
	if err != nil {
		return err
	}
	if err := tx.EmitTask(ctx, parent); err != nil {
		return err
	}
	if member, err := core.EmployeeByName(ctx, tx, th.ProjectID, &th.ID, parent.ClaimedBy); err == nil {
		msg := text + fmt.Sprintf("\n请按审查意见修改任务 %s，然后再次调用 complete_task。", parent.ID)
		if _, err := s.orch.Deliver(ctx, tx, member, orchestrator.Delivery{Text: msg, Kind: "task", Priority: "next"}); err != nil {
			return err
		}
	}
	return s.notifyLead(ctx, tx, th, text)
}

// failOrRetry returns a failed task to the pool until MaxAttempts, then fails it and tells the lead.
func (s *Scheduler) failOrRetry(ctx context.Context, tx *core.Tx, task core.Task, reason string) (core.Task, error) {
	th, err := core.ThreadByID(ctx, tx, task.ConversationID)
	if err != nil {
		return task, err
	}
	who := task.ClaimedBy
	if task.Attempt+1 < MaxAttempts {
		task, err = core.UpdateTask(ctx, tx, task, map[string]any{"status": "pending", "claimed_by": "",
			"attempt": task.Attempt + 1, "claimed_at": nil})
		if err != nil {
			return task, err
		}
		s.kickAfter(tx, th.ID)
		return task, tx.EmitTask(ctx, task)
	}
	task, err = core.UpdateTask(ctx, tx, task, map[string]any{"attempt": task.Attempt + 1})
	if err != nil {
		return task, err
	}
	task, err = s.finish(ctx, tx, task, "failed")
	if err != nil {
		return task, err
	}
	return task, s.notifyLead(ctx, tx, th, fmt.Sprintf("[Task %s · %s · failed] %s", task.ID, who, reason))
}

// setStatus changes a task's status and emits it.
func (s *Scheduler) setStatus(ctx context.Context, tx *core.Tx, task core.Task, st string) (core.Task, error) {
	task, err := core.SetTaskStatus(ctx, tx, task, st)
	if err != nil {
		return task, err
	}
	s.touchMember(ctx, tx, task)
	return task, tx.EmitTask(ctx, task)
}

// finish moves a task to a final state: plan step auto-tick, waiters, a new pass of the pool.
func (s *Scheduler) finish(ctx context.Context, tx *core.Tx, task core.Task, final string) (core.Task, error) {
	task, err := s.setStatus(ctx, tx, task, final)
	if err != nil {
		return task, err
	}
	if err := s.tickMain(ctx, tx, task); err != nil {
		return task, err
	}
	if err := s.TaskFinished(ctx, tx, task); err != nil {
		return task, err
	}
	if final == "failed" || final == "cancelled" {
		if err := s.blockDependents(ctx, tx, task); err != nil {
			return task, err
		}
	}
	s.kickAfter(tx, task.ConversationID)
	return task, nil
}

var finalWord = map[string]string{"failed": "失败了", "cancelled": "被取消了"}

// blockDependents marks blocked the pending tasks that wait on a failed or cancelled task, and
// the ones waiting on those: they could never start (a dependency must be done), so instead of
// sitting in the pool unseen they show as blocked and the lead decides what happens to them.
func (s *Scheduler) blockDependents(ctx context.Context, tx *core.Tx, task core.Task) error {
	ids, err := s.blockChain(ctx, tx, task, nil)
	if err != nil || len(ids) == 0 {
		return err
	}
	th, err := core.ThreadByID(ctx, tx, task.ConversationID)
	if err != nil {
		return err
	}
	return s.notifyLead(ctx, tx, th, fmt.Sprintf("[Task %s · %s] 依赖它的 %s 已标为 blocked，不会自动开始。"+
		"需要的话 cancel_task 掉它们，再用 assign_task 重新拆（depends_on 写新任务）；也可以让用户在任务页重试。",
		task.ID, finalWord[task.Status], strings.Join(ids, "、")))
}

func (s *Scheduler) blockChain(ctx context.Context, tx *core.Tx, task core.Task, ids []string) ([]string, error) {
	deps, err := core.PendingDependents(ctx, tx, task.ConversationID, task.ID)
	if err != nil {
		return ids, err
	}
	for _, d := range deps {
		if d, err = s.setStatus(ctx, tx, d, "blocked"); err != nil {
			return ids, err
		}
		if ids, err = s.blockChain(ctx, tx, d, append(ids, d.ID)); err != nil {
			return ids, err
		}
	}
	return ids, nil
}

// Finish exposes finish (merge queue: merged task → done).
func (s *Scheduler) Finish(ctx context.Context, tx *core.Tx, task core.Task, final string) (core.Task, error) {
	return s.finish(ctx, tx, task, final)
}

// SetStatus exposes setStatus (merge queue: merging, conflict fix).
func (s *Scheduler) SetStatus(ctx context.Context, tx *core.Tx, task core.Task, st string) (core.Task, error) {
	return s.setStatus(ctx, tx, task, st)
}

// tickMain ticks the plan step when all its tasks are done.
func (s *Scheduler) tickMain(ctx context.Context, tx *core.Tx, task core.Task) error {
	if task.MainTaskID == nil {
		return nil
	}
	changed, err := core.AutoCompleteMain(ctx, tx, task.ConversationID, *task.MainTaskID)
	if err != nil || !changed {
		return err
	}
	th, err := core.ThreadByID(ctx, tx, task.ConversationID)
	if err != nil {
		return err
	}
	if err := tx.EmitMainTasks(ctx, th); err != nil {
		return err
	}
	return tx.EmitThread(ctx, th.ID)
}

// TickMain exposes tickMain (a task assigned under a step moves it to doing).
func (s *Scheduler) TickMain(ctx context.Context, tx *core.Tx, task core.Task) error {
	return s.tickMain(ctx, tx, task)
}

func (s *Scheduler) touchMember(ctx context.Context, tx *core.Tx, task core.Task) {
	if task.ClaimedBy == "" {
		return
	}
	th, err := core.ThreadByID(ctx, tx, task.ConversationID)
	if err != nil {
		return
	}
	if e, err := core.EmployeeByName(ctx, tx, th.ProjectID, &th.ID, task.ClaimedBy); err == nil {
		s.orch.Touch(tx, e.ID, "task:"+task.ID)
	}
}

func (s *Scheduler) kickAfter(tx *core.Tx, threadID string) {
	tx.AfterCommit(func(ctx context.Context) { s.Kick(ctx, threadID) })
}

// notifyLead sends a platform note to the thread's lead (priority later), refreshing its worktree first.
func (s *Scheduler) notifyLead(ctx context.Context, tx *core.Tx, th core.Thread, text string) error {
	lead, err := core.Lead(ctx, tx, th.ID)
	if err != nil {
		return ignoreMissing(err)
	}
	if err := s.orch.RefreshWorktree(ctx, tx, lead); err != nil {
		return err
	}
	_, err = s.orch.Deliver(ctx, tx, lead, orchestrator.Delivery{Text: text, Kind: "system", Priority: "later"})
	return err
}

// NotifyLead exposes notifyLead.
func (s *Scheduler) NotifyLead(ctx context.Context, tx *core.Tx, th core.Thread, text string) error {
	return s.notifyLead(ctx, tx, th, text)
}

// isSubtask reports a task a member created for another (assign_subtask, request_review):
// its result goes back to that member, not to the lead or the merge queue.
func isSubtask(t core.Task) bool {
	return t.Parent != "" && t.CreatedBy != "" && t.CreatedBy != "lead" && t.CreatedBy != "platform"
}

// pushedNote names the repositories a task pushed ("" when it pushed none).
func pushedNote(t core.Task) string {
	if len(t.Pushed) == 0 {
		return ""
	}
	repos := make([]string, 0, len(t.Pushed))
	for r := range t.Pushed {
		repos = append(repos, r)
	}
	sort.Strings(repos)
	return "（改动的仓库：" + strings.Join(repos, "、") + "）"
}
