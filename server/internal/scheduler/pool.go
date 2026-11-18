package scheduler

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/orchestrator"
)

// Errors for tool and REST callers.
var (
	ErrUnknownAssignee = errors.New("没有这个岗位或成员")
	ErrUnknownMain     = errors.New("计划里没有这个主任务（先 update_plan）")
	ErrUnknownDep      = errors.New("依赖的任务不存在")
	ErrTaskState       = errors.New("任务当前状态不允许这个操作")
	ErrRoleExcluded    = errors.New("这个讨论串不用这个岗位（开讨论串时限定了岗位）")
)

// Assignment creates a small task.
type Assignment struct {
	Thread      core.Thread
	Assignee    string // role name (pool) or instance name (named)
	Instruction string
	Main        string // plan step title or id ("" = none)
	DependsOn   []string
	Kind        string
	Parent      string
	CreatedBy   string
	Priority    int
	// ReservedFor keeps the task for that member for ReserveFor (rework of its own work).
	ReservedFor string
}

// Assign validates and stores a task, then asks for a scheduling pass.
func (s *Scheduler) Assign(ctx context.Context, tx *core.Tx, a Assignment) (core.Task, bool, error) {
	role, named, err := resolveAssignee(ctx, tx, a.Thread, a.Assignee)
	if err != nil {
		return core.Task{}, false, err
	}
	mainID, err := resolveMain(ctx, tx, a.Thread.ID, a.Main)
	if err != nil {
		return core.Task{}, false, err
	}
	if err := checkDeps(ctx, tx, a.Thread.ID, a.DependsOn); err != nil {
		return core.Task{}, false, err
	}
	task, err := core.InsertTask(ctx, tx, core.NewTask{Thread: a.Thread, MainTaskID: mainID, Assignee: a.Assignee, Role: role,
		Kind: a.Kind, Instruction: a.Instruction, DependsOn: a.DependsOn, Parent: a.Parent, Priority: a.Priority,
		CreatedBy: a.CreatedBy})
	if err != nil {
		return task, false, err
	}
	if a.ReservedFor != "" {
		if task, err = s.Reserve(ctx, tx, task, a.ReservedFor); err != nil {
			return task, false, err
		}
	}
	if err := tx.EmitTask(ctx, task); err != nil {
		return task, false, err
	}
	if err := s.tickMain(ctx, tx, task); err != nil {
		return task, false, err
	}
	s.kickAfter(tx, a.Thread.ProjectID)
	return task, !named, nil
}

// resolveAssignee returns the role and whether the assignee names one instance: a role or an
// instance of the thread's team (project members, the thread's legacy members), within the
// roles the thread was opened with.
func resolveAssignee(ctx context.Context, tx *core.Tx, t core.Thread, assignee string) (string, bool, error) {
	every := t
	every.OnlyRoles = nil // a role outside OnlyRoles is refused below with its own reason
	emps, err := core.ThreadTeam(ctx, tx, every)
	if err != nil {
		return "", false, err
	}
	for _, e := range emps {
		if e.Tier != "member" || (e.Name != assignee && e.Role != assignee) {
			continue
		}
		if len(t.OnlyRoles) > 0 && !slices.Contains(t.OnlyRoles, e.Role) {
			return "", false, fmt.Errorf("%w：%s（可用：%s）", ErrRoleExcluded, e.Role, strings.Join(t.OnlyRoles, "、"))
		}
		return e.Role, e.Name == assignee, nil
	}
	return "", false, fmt.Errorf("%w：%s", ErrUnknownAssignee, assignee)
}

func resolveMain(ctx context.Context, tx *core.Tx, threadID, main string) (*string, error) {
	if main == "" {
		return nil, nil
	}
	m, err := core.MainTaskByTitle(ctx, tx, threadID, main)
	if errors.Is(err, core.ErrNotFound) {
		return nil, fmt.Errorf("%w：%s", ErrUnknownMain, main)
	}
	if err != nil {
		return nil, err
	}
	return &m.ID, nil
}

func checkDeps(ctx context.Context, tx *core.Tx, threadID string, deps []string) error {
	for _, d := range deps {
		if _, err := core.TaskByID(ctx, tx, threadID, d); err != nil {
			return fmt.Errorf("%w：%s", ErrUnknownDep, d)
		}
	}
	return nil
}

// Cancel cancels a task; a member working on it is told to stop.
func (s *Scheduler) Cancel(ctx context.Context, tx *core.Tx, task core.Task, reason string) (core.Task, error) {
	if task.Finished() {
		return task, ErrTaskState
	}
	worker := task.ClaimedBy
	if task.SyncPending > 0 {
		worker = "" // its brief never went out: nothing to stop
	}
	task, err := s.finish(ctx, tx, task, "cancelled")
	if err != nil || worker == "" {
		return task, err
	}
	th, err := core.ThreadByID(ctx, tx, task.ConversationID)
	if err != nil {
		return task, err
	}
	m, err := core.EmployeeByName(ctx, tx, th.ProjectID, &th.ID, worker)
	if err != nil {
		return task, ignoreMissing(err)
	}
	text := fmt.Sprintf("[Task %s · cancelled] %s 请停止这个任务。", task.ID, reason)
	_, err = s.orch.Deliver(ctx, tx, m, orchestrator.Delivery{Text: text, Kind: "system", Priority: "now"})
	return task, err
}

// Retry puts a failed or blocked task back into the pool.
func (s *Scheduler) Retry(ctx context.Context, tx *core.Tx, task core.Task) (core.Task, error) {
	if !slices.Contains([]string{"failed", "blocked", "cancelled"}, task.Status) {
		return task, ErrTaskState
	}
	task, err := core.RequeueTask(ctx, tx, task, map[string]any{"claimed_by": "", "attempt": 0,
		"nudges": 0, "finished_at": nil, "claimed_at": nil})
	if err != nil {
		return task, err
	}
	if err := s.kickTaskAfter(ctx, tx, task); err != nil {
		return task, err
	}
	return task, tx.EmitTask(ctx, task)
}

// TaskDelivered marks a claimed task running once its brief reached the member's session.
func (s *Scheduler) TaskDelivered(ctx context.Context, tx *core.Tx, m core.Message) error {
	taskID, _ := m.Meta["task_id"].(string)
	if taskID == "" {
		return nil
	}
	task, err := core.LockTask(ctx, tx, m.ConversationID, taskID)
	if err != nil || task.Status != "claimed" {
		return ignoreMissing(err)
	}
	_, err = s.setStatus(ctx, tx, task, "running")
	return err
}

// nudgeText reminds a member whose turn ended with the task still open (§7.3).
const nudgeText = "[平台提醒] 任务 %s 还没有完成：如果已经做完，请调用 complete_task(task_id=\"%s\", summary=…)；没做完请继续。"

// TurnEnded nudges once, then marks the task blocked and tells the lead. A project member's
// ended turn may free it for a task of another thread (shared-members.md §4.1): a pass follows.
func (s *Scheduler) TurnEnded(ctx context.Context, tx *core.Tx, sc core.SessionContext) error {
	e := sc.Employee
	if e.Tier != "member" {
		return nil
	}
	if e.ProjectMember() {
		s.kickAfter(tx, e.ProjectID)
	}
	task, err := core.SessionTask(ctx, tx, sc)
	if err != nil || task.Status != "running" {
		return ignoreMissing(err)
	}
	if err := s.autoPush(ctx, tx, e, task); err != nil {
		return err
	}
	task, err = core.UpdateTask(ctx, tx, task, map[string]any{"nudges": task.Nudges + 1})
	if err != nil {
		return err
	}
	if task.Nudges < 2 {
		_, err := s.orch.Deliver(ctx, tx, e, orchestrator.Delivery{Text: fmt.Sprintf(nudgeText, task.ID, task.ID),
			Kind: "system", Priority: "later"})
		return err
	}
	task, err = s.setStatus(ctx, tx, task, "blocked")
	if err != nil {
		return err
	}
	th, err := core.ThreadByID(ctx, tx, task.ConversationID)
	if err != nil {
		return err
	}
	return s.notifyLead(ctx, tx, th, fmt.Sprintf("[Task %s · %s · 需要关注] 两次回合结束都没有调用 complete_task，任务已标为 blocked。", task.ID, e.Name))
}

// autoPush pushes the member's committed work at the end of a turn, at most once per
// AutoPushEvery; review / decision tasks have nothing to push. A member without a node yet is
// skipped (nothing on any node to lose).
func (s *Scheduler) autoPush(ctx context.Context, tx *core.Tx, e core.Employee, task core.Task) error {
	if isGateTask(task) || e.NodeID == nil || !s.orch.HasRepo(ctx, e.ProjectID) || !s.pushDue(e.ID) {
		return nil
	}
	t, err := core.ThreadByID(ctx, tx, task.ConversationID)
	if err != nil {
		return err
	}
	if err := tx.Savepoint(ctx, func(sub *core.Tx) error { return s.PushWork(ctx, sub, e, t) }); err != nil {
		s.log.Warn("auto push", "employee", e.Name, "err", err)
	}
	return nil
}

func (s *Scheduler) pushDue(employeeID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if last, ok := s.lastPush[employeeID]; ok && time.Since(last) < s.AutoPushEvery {
		return false
	}
	s.lastPush[employeeID] = time.Now()
	return true
}

// SessionLost returns the employee's running task to the pool (crash) with an attempt counted.
func (s *Scheduler) SessionLost(ctx context.Context, tx *core.Tx, sc core.SessionContext) error {
	task, err := core.SessionTask(ctx, tx, sc)
	if err != nil {
		return ignoreMissing(err)
	}
	_, err = s.failOrRetry(ctx, tx, task, sc.Employee.Name+" 的会话崩溃了")
	return err
}
