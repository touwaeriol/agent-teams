package toolsvc

import (
	"context"
	"fmt"
	"strings"

	protocol "github.com/touwaeriol/agent-teams/protocol"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/delivery"
	"github.com/touwaeriol/agent-teams/server/internal/scheduler"
)

// updatePlan replaces the plan (step ids kept by title).
func (s *Service) updatePlan(ctx context.Context, tx *core.Tx, c *call) (Result, error) {
	t, err := requireThread(c)
	if err != nil {
		return Result{}, err
	}
	var in protocol.UpdatePlanInput
	if err := c.decode(&in); err != nil {
		return Result{}, err
	}
	steps := make([]core.PlanStep, len(in.Steps))
	for i, st := range in.Steps {
		steps[i] = core.PlanStep{Title: strings.TrimSpace(st.Title), Status: string(st.Status)}
	}
	if err := core.ReplacePlan(ctx, tx, t.ID, steps); err != nil {
		return Result{}, err
	}
	if err := tx.EmitMainTasks(ctx, t); err != nil {
		return Result{}, err
	}
	return okText(fmt.Sprintf("计划已更新（%d 步）", len(steps))), tx.EmitThread(ctx, t.ID)
}

// assignTask creates a task for a role (pool) or a named member.
func (s *Service) assignTask(ctx context.Context, tx *core.Tx, c *call) (Result, error) {
	t, err := requireThread(c)
	if err != nil {
		return Result{}, err
	}
	var in protocol.AssignTaskInput
	if err := c.decode(&in); err != nil {
		return Result{}, err
	}
	deps := make([]string, len(in.DependsOn))
	for i, d := range in.DependsOn {
		deps[i] = string(d)
	}
	kind := "work"
	if in.Kind != nil {
		kind = string(*in.Kind)
	}
	task, pooled, err := s.sched.Assign(ctx, tx, scheduler.Assignment{Thread: t, Assignee: in.Assignee, Instruction: in.Instruction,
		Main: in.Main, DependsOn: deps, Kind: kind, CreatedBy: "lead"})
	if err != nil {
		return Result{}, err
	}
	out := protocol.AssignTaskResult{TaskID: protocol.TaskID(task.ID), Status: protocol.TaskStatus(task.Status), Pooled: pooled}
	where := "进了任务池，空闲的 " + task.Role + " 会领取"
	if !pooled {
		where = "指派给 " + in.Assignee
	}
	return okJSON(fmt.Sprintf("任务 %s 已创建，%s", task.ID, where), out), nil
}

// listTasks lists the thread's tasks.
func (s *Service) listTasks(ctx context.Context, tx *core.Tx, c *call) (Result, error) {
	t, err := requireThread(c)
	if err != nil {
		return Result{}, err
	}
	var in protocol.ListTasksInput
	if err := c.decode(&in); err != nil {
		return Result{}, err
	}
	f := core.TaskFilter{}
	if in.Status != nil {
		f.Status = []string{string(*in.Status)}
	}
	tasks, err := core.ThreadTasks(ctx, tx, t.ID, f)
	if err != nil {
		return Result{}, err
	}
	out := protocol.ListTasksResult{Tasks: []protocol.TaskSummary{}}
	var lines []string
	for _, task := range tasks {
		out.Tasks = append(out.Tasks, summaryOf(ctx, tx, task))
		lines = append(lines, fmt.Sprintf("%s [%s] %s → %s：%s", task.ID, task.Status, task.Role, orDash(task.ClaimedBy),
			clip(task.Instruction, 80)))
	}
	if len(lines) == 0 {
		return okJSON("还没有任务", out), nil
	}
	return okJSON(strings.Join(lines, "\n"), out), nil
}

func summaryOf(ctx context.Context, q core.DB, t core.Task) protocol.TaskSummary {
	kind := protocol.TaskKind(t.Kind)
	sum := protocol.TaskSummary{TaskID: protocol.TaskID(t.ID), Status: protocol.TaskStatus(t.Status), Instruction: t.Instruction,
		Kind: &kind, Assignee: &t.Assignee}
	if t.ClaimedBy != "" {
		sum.ClaimedBy = &t.ClaimedBy
	}
	if title := mainTitle(ctx, q, t); title != "" {
		sum.Main = &title
	}
	return sum
}

func mainTitle(ctx context.Context, q core.DB, t core.Task) string {
	if t.MainTaskID == nil {
		return ""
	}
	var title string
	_ = q.QueryRow(ctx, `SELECT title FROM at_main_tasks WHERE id = $1`, *t.MainTaskID).Scan(&title)
	return title
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// cancelTask cancels a task.
func (s *Service) cancelTask(ctx context.Context, tx *core.Tx, c *call) (Result, error) {
	t, err := requireThread(c)
	if err != nil {
		return Result{}, err
	}
	var in protocol.CancelTaskInput
	if err := c.decode(&in); err != nil {
		return Result{}, err
	}
	task, err := core.LockTask(ctx, tx, t.ID, string(in.TaskID))
	if err != nil {
		return Result{}, refuse("没有任务 %s", in.TaskID)
	}
	reason := ""
	if in.Reason != nil {
		reason = *in.Reason
	}
	if _, err := s.sched.Cancel(ctx, tx, task, reason); err != nil {
		return Result{}, err
	}
	return okText("已取消 " + task.ID), nil
}

// enqueueMerge puts a member's task into the merge queue.
func (s *Service) enqueueMerge(ctx context.Context, tx *core.Tx, c *call) (Result, error) {
	t, err := requireThread(c)
	if err != nil {
		return Result{}, err
	}
	var in protocol.EnqueueMergeInput
	if err := c.decode(&in); err != nil {
		return Result{}, err
	}
	task, err := s.mergeTask(ctx, tx, t, in)
	if err != nil {
		return Result{}, err
	}
	entry, pos, err := s.deliv.Enqueue(ctx, tx, t, task)
	if err != nil {
		return Result{}, err
	}
	out := protocol.EnqueueMergeResult{EntryID: entry.ID, Position: pos}
	return okJSON(fmt.Sprintf("%s 的分支已进合并队列（第 %d 位）", task.ClaimedBy, pos), out), nil
}

func (s *Service) mergeTask(ctx context.Context, tx *core.Tx, t core.Thread, in protocol.EnqueueMergeInput) (core.Task, error) {
	if in.TaskID != nil {
		task, err := core.LockTask(ctx, tx, t.ID, string(*in.TaskID))
		if err != nil {
			return task, refuse("没有任务 %s", *in.TaskID)
		}
		return task, nil
	}
	if in.Member == nil {
		return core.Task{}, refuse("请给出 task_id 或 member")
	}
	var id string
	err := tx.QueryRow(ctx, `SELECT id FROM at_tasks WHERE thread_id = $1 AND claimed_by = $2
		AND status IN ('review','decision','running','blocked') ORDER BY updated_at DESC LIMIT 1`, t.ID, *in.Member).Scan(&id)
	if err != nil {
		return core.Task{}, refuse("%s 没有可以合并的任务", *in.Member)
	}
	return core.LockTask(ctx, tx, t.ID, id)
}

// openPR opens the pull request after the commit (GitHub call).
func (s *Service) openPR(ctx context.Context, tx *core.Tx, c *call) (Result, error) {
	t, err := requireThread(c)
	if err != nil {
		return Result{}, err
	}
	var in protocol.OpenPRInput
	if err := c.decode(&in); err != nil {
		return Result{}, err
	}
	by := c.emp().Name
	return Result{Later: func(ctx context.Context) Result {
		th, err := s.deliv.OpenPR(ctx, t.ID, delivery.PRRequest{Title: in.Title, Body: in.Body, By: by})
		if err != nil {
			return failure(err)
		}
		out := protocol.OpenPRResult{PRNumber: deref0(th.PRNumber), PRURL: deref(th.PRURL)}
		return okJSON("PR 已开："+out.PRURL, out)
	}}, nil
}

func deref0(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// finish writes the thread summary and adds it to project memory (source #<no>).
func (s *Service) finish(ctx context.Context, tx *core.Tx, c *call) (Result, error) {
	t, err := requireThread(c)
	if err != nil {
		return Result{}, err
	}
	var in protocol.FinishInput
	if err := c.decode(&in); err != nil {
		return Result{}, err
	}
	summary := strings.TrimSpace(in.Summary)
	if err := core.UpdateThread(ctx, tx, t.ID, map[string]any{"summary": summary}); err != nil {
		return Result{}, err
	}
	if err := s.addMemory(ctx, tx, t.ProjectID, summary, fmt.Sprintf("#%d", t.No)); err != nil {
		return Result{}, err
	}
	return okText("总结已记录"), tx.EmitThread(ctx, t.ID)
}
