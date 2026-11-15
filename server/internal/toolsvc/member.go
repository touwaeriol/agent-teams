package toolsvc

import (
	"context"
	"fmt"
	"strings"

	protocol "github.com/touwaeriol/agent-teams/protocol"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/delivery"
	"github.com/touwaeriol/agent-teams/server/internal/orchestrator"
	"github.com/touwaeriol/agent-teams/server/internal/scheduler"
)

// workable states a member may complete a task from.
var workable = map[string]bool{"claimed": true, "running": true, "blocked": true}

// completeTask finishes the member's task; with a repository the platform pushes the branch first (§7.3, §8.1).
func (s *Service) completeTask(ctx context.Context, tx *core.Tx, c *call) (Result, error) {
	t, err := requireThread(c)
	if err != nil {
		return Result{}, err
	}
	var in protocol.CompleteTaskInput
	if err := c.decode(&in); err != nil {
		return Result{}, err
	}
	task, err := s.ownTask(ctx, tx, t, c, string(in.TaskID))
	if err != nil {
		return Result{}, err
	}
	ok := in.Status != "failed"
	summary := withArtifacts(in.Summary, in.Artifacts)
	if !ok || !s.orch.HasRepo(ctx, t.ProjectID) || isReviewOnly(task) {
		return s.complete(ctx, tx, task, scheduler.Outcome{OK: ok, Summary: summary})
	}
	meta := map[string]any{"then": "complete", "session_id": c.sc.Session.ID, "call_id": c.callID, "task_id": task.ID,
		"thread_id": t.ID, "summary": summary}
	if _, err := s.deliv.Push(ctx, tx, c.emp(), t, meta); err != nil {
		return Result{}, err
	}
	return Result{Wait: true}, nil
}

// isReviewOnly: review/decision tasks produce a verdict, not commits.
func isReviewOnly(t core.Task) bool { return t.Kind == "review" || t.Kind == "decision" }

func withArtifacts(summary string, artifacts []string) string {
	if len(artifacts) == 0 {
		return summary
	}
	return summary + "\n产出：" + strings.Join(artifacts, "、")
}

func (s *Service) ownTask(ctx context.Context, tx *core.Tx, t core.Thread, c *call, id string) (core.Task, error) {
	task, err := core.LockTask(ctx, tx, t.ID, id)
	if err != nil {
		return task, refuse("没有任务 %s", id)
	}
	if task.ClaimedBy != c.emp().Name {
		return task, refuse("任务 %s 不是你的", id)
	}
	if !workable[task.Status] {
		return task, refuse("任务 %s 当前是 %s，不能完成", id, task.Status)
	}
	return task, nil
}

func (s *Service) complete(ctx context.Context, tx *core.Tx, task core.Task, out scheduler.Outcome) (Result, error) {
	task, err := s.sched.Complete(ctx, tx, task, out)
	if err != nil {
		return Result{}, err
	}
	res := protocol.CompleteTaskResult{TaskID: protocol.TaskID(task.ID), Status: protocol.TaskStatus(task.Status)}
	if out.HeadSHA != "" {
		res.HeadSHA = &out.HeadSHA
	}
	return okJSON(fmt.Sprintf("任务 %s 已提交（%s）", task.ID, task.Status), res), nil
}

// pushBranch pushes the member's worktree HEAD to its member branch.
func (s *Service) pushBranch(ctx context.Context, tx *core.Tx, c *call) (Result, error) {
	t, err := requireThread(c)
	if err != nil {
		return Result{}, err
	}
	if !s.orch.HasRepo(ctx, t.ProjectID) {
		return Result{}, refuse("项目没有关联 GitHub 仓库，无需推送")
	}
	meta := map[string]any{"then": "push_branch", "session_id": c.sc.Session.ID, "call_id": c.callID, "thread_id": t.ID}
	if _, err := s.deliv.Push(ctx, tx, c.emp(), t, meta); err != nil {
		return Result{}, err
	}
	return Result{Wait: true}, nil
}

// pushDone continues complete_task / push_branch when the push job reported back.
func (s *Service) pushDone(ctx context.Context, tx *core.Tx, meta map[string]any, r *protocol.GitResult) error {
	sessionID, callID := str(meta["session_id"]), str(meta["call_id"])
	if sessionID == "" || callID == "" {
		return nil
	}
	if !r.Ok {
		return s.Complete(ctx, tx, sessionID, callID, failText("%s", delivery.PushError(r)))
	}
	sha := ""
	if r.HeadSHA != nil {
		sha = *r.HeadSHA
	}
	if str(meta["then"]) == "complete" {
		return s.completeAfterPush(ctx, tx, meta, sha)
	}
	sc, err := core.SessionContextOf(ctx, tx, sessionID)
	if err != nil {
		return ignoreMissing(err)
	}
	t, err := core.ThreadByID(ctx, tx, str(meta["thread_id"]))
	if err != nil {
		return ignoreMissing(err)
	}
	branch := orchestrator.MemberBranch(t.No, sc.Employee.Name)
	return s.Complete(ctx, tx, sessionID, callID, okJSON("已推送到 "+branch, protocol.PushBranchResult{Branch: branch, HeadSHA: sha}))
}

func (s *Service) completeAfterPush(ctx context.Context, tx *core.Tx, meta map[string]any, sha string) error {
	sessionID, callID := str(meta["session_id"]), str(meta["call_id"])
	task, err := core.LockTask(ctx, tx, str(meta["thread_id"]), str(meta["task_id"]))
	if err != nil {
		return s.Complete(ctx, tx, sessionID, callID, failText("任务不存在了"))
	}
	if !workable[task.Status] {
		return s.Complete(ctx, tx, sessionID, callID, failText("任务 %s 已经是 %s", task.ID, task.Status))
	}
	var res Result
	err = tx.Savepoint(ctx, func(sub *core.Tx) error {
		var cerr error
		res, cerr = s.complete(ctx, sub, task, scheduler.Outcome{OK: true, Summary: str(meta["summary"]), HeadSHA: sha})
		return cerr
	})
	if err != nil {
		res = failure(err)
	}
	return s.Complete(ctx, tx, sessionID, callID, res)
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

// listTeam lists the thread's lead and members with their current task.
func (s *Service) listTeam(ctx context.Context, tx *core.Tx, c *call) (Result, error) {
	t, err := requireThread(c)
	if err != nil {
		return Result{}, err
	}
	emps, err := core.ThreadEmployees(ctx, tx, t.ID)
	if err != nil {
		return Result{}, err
	}
	out := protocol.ListTeamResult{Members: []protocol.TeamMember{}}
	var lines []string
	for _, e := range emps {
		m := s.member(ctx, tx, t, e)
		out.Members = append(out.Members, m)
		lines = append(lines, fmt.Sprintf("%s（%s）%s", e.Name, deref(m.Title), deref(m.Status)))
	}
	return okJSON(strings.Join(lines, "\n"), out), nil
}

func (s *Service) member(ctx context.Context, tx *core.Tx, t core.Thread, e core.Employee) protocol.TeamMember {
	title := e.Spec.Title
	typ := protocol.TeamMemberType("claude_code")
	if e.Runtime == "codex" {
		typ = "codex"
	}
	m := protocol.TeamMember{Name: e.Name, Role: e.Role, Title: &title, Type: &typ}
	if task, err := core.CurrentTask(ctx, tx, t.ID, e.Name); err == nil {
		st := "在做 " + task.ID
		m.TaskID, m.Status = &task.ID, &st
	} else {
		idle := "空闲"
		m.Status = &idle
	}
	if e.NodeID != nil {
		var name string
		if tx.QueryRow(ctx, `SELECT name FROM at_nodes WHERE id = $1`, *e.NodeID).Scan(&name) == nil {
			m.Node = &name
		}
	}
	return m
}

// getTask shows one task of the thread.
func (s *Service) getTask(ctx context.Context, tx *core.Tx, c *call) (Result, error) {
	t, err := requireThread(c)
	if err != nil {
		return Result{}, err
	}
	var in protocol.GetTaskInput
	if err := c.decode(&in); err != nil {
		return Result{}, err
	}
	task, err := core.TaskByID(ctx, tx, t.ID, string(in.TaskID))
	if err != nil {
		return Result{}, refuse("没有任务 %s", in.TaskID)
	}
	kind := protocol.TaskKind(task.Kind)
	deps := make([]protocol.TaskID, len(task.DependsOn))
	for i, d := range task.DependsOn {
		deps[i] = protocol.TaskID(d)
	}
	out := protocol.GetTaskResult{TaskID: protocol.TaskID(task.ID), Status: protocol.TaskStatus(task.Status),
		Instruction: task.Instruction, Kind: &kind, Assignee: &task.Assignee, DependsOn: deps, Summary: &task.Summary}
	if task.ClaimedBy != "" {
		out.ClaimedBy = &task.ClaimedBy
	}
	if title := mainTitle(ctx, tx, task); title != "" {
		out.Main = &title
	}
	text := fmt.Sprintf("%s [%s] %s\n%s", task.ID, task.Status, orDash(task.ClaimedBy), task.Instruction)
	if task.Summary != "" {
		text += "\n结果：" + task.Summary
	}
	return okJSON(text, out), nil
}

// requestReview asks a teammate to review the current task and waits for the verdict.
func (s *Service) requestReview(ctx context.Context, tx *core.Tx, c *call) (Result, error) {
	t, err := requireThread(c)
	if err != nil {
		return Result{}, err
	}
	var in protocol.RequestReviewInput
	if err := c.decode(&in); err != nil {
		return Result{}, err
	}
	if in.Reviewer == c.emp().Name {
		return Result{}, refuse("不能请自己审查")
	}
	parent := ""
	branch := orchestrator.MemberBranch(t.No, c.emp().Name)
	if cur, err := core.CurrentTask(ctx, tx, t.ID, c.emp().Name); err == nil {
		parent = cur.ID
	}
	text := fmt.Sprintf("%s 请你审查分支 %s：\n%s\n通过用 complete_task(status=done) 写结论，有问题用 status=failed 说明。", c.emp().Name, branch, in.Summary)
	task, _, err := s.sched.Assign(ctx, tx, scheduler.Assignment{Thread: t, Assignee: in.Reviewer, Instruction: text,
		Kind: "review", Parent: parent, CreatedBy: c.emp().Name, Priority: 60})
	if err != nil {
		return Result{}, err
	}
	return Result{Wait: true, WaitMeta: map[string]any{"waiting_task": task.ID, "thread_id": t.ID}}, nil
}

// assignSubtask creates a subtask under the member's current task (can_delegate).
func (s *Service) assignSubtask(ctx context.Context, tx *core.Tx, c *call) (Result, error) {
	t, err := requireThread(c)
	if err != nil {
		return Result{}, err
	}
	var in protocol.AssignSubtaskInput
	if err := c.decode(&in); err != nil {
		return Result{}, err
	}
	if in.Assignee == c.emp().Name {
		return Result{}, refuse("不能派给自己")
	}
	parent, mainID := "", ""
	if cur, err := core.CurrentTask(ctx, tx, t.ID, c.emp().Name); err == nil {
		parent = cur.ID
		if cur.MainTaskID != nil {
			mainID = *cur.MainTaskID
		}
	}
	task, _, err := s.sched.Assign(ctx, tx, scheduler.Assignment{Thread: t, Assignee: in.Assignee, Instruction: in.Instruction,
		Main: mainID, Parent: parent, CreatedBy: c.emp().Name})
	if err != nil {
		return Result{}, err
	}
	return okJSON("子任务 "+task.ID+" 已创建", protocol.AssignSubtaskResult{TaskID: protocol.TaskID(task.ID)}), nil
}

// waitTask blocks until a task finishes.
func (s *Service) waitTask(ctx context.Context, tx *core.Tx, c *call) (Result, error) {
	t, err := requireThread(c)
	if err != nil {
		return Result{}, err
	}
	var in protocol.WaitTaskInput
	if err := c.decode(&in); err != nil {
		return Result{}, err
	}
	task, err := core.TaskByID(ctx, tx, t.ID, string(in.TaskID))
	if err != nil {
		return Result{}, refuse("没有任务 %s", in.TaskID)
	}
	if task.Finished() {
		return outcomeOf(task, protocol.ToolWaitTask), nil
	}
	return Result{Wait: true, WaitMeta: map[string]any{"waiting_task": task.ID, "thread_id": t.ID}}, nil
}

func outcomeOf(task core.Task, tool string) Result {
	text := fmt.Sprintf("%s %s：%s", task.ID, task.Status, task.Summary)
	if tool == protocol.ToolRequestReview {
		return okJSON(text, protocol.RequestReviewResult{TaskID: protocol.TaskID(task.ID), Status: protocol.TaskStatus(task.Status),
			Result: task.Summary})
	}
	return okJSON(text, protocol.WaitTaskResult{TaskID: protocol.TaskID(task.ID), Status: protocol.TaskStatus(task.Status),
		Result: task.Summary})
}

// taskFinished answers wait_task / request_review calls waiting on a task (scheduler hook).
func (s *Service) taskFinished(ctx context.Context, tx *core.Tx, task core.Task) error {
	rows, err := tx.Query(ctx, `SELECT session_id, call_id, tool FROM at_tool_calls WHERE status = 'waiting'
		AND result->>'waiting_task' = $1 AND result->>'thread_id' = $2`, task.ID, task.ConversationID)
	if err != nil {
		return err
	}
	type waiter struct{ session, call, tool string }
	var ws []waiter
	for rows.Next() {
		var w waiter
		if rows.Scan(&w.session, &w.call, &w.tool) == nil {
			ws = append(ws, w)
		}
	}
	rows.Close()
	for _, w := range ws {
		if err := s.Complete(ctx, tx, w.session, w.call, outcomeOf(task, w.tool)); err != nil {
			return err
		}
	}
	return nil
}
