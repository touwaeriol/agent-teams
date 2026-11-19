package toolsvc

import (
	"context"
	"fmt"
	"sort"
	"strings"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/composer"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/delivery"
	"github.com/touwaeriol/agent-teams/server/internal/orchestrator"
	"github.com/touwaeriol/agent-teams/server/internal/scheduler"
	"github.com/touwaeriol/agent-teams/server/internal/store"
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
	if in.Status == protocol.CompleteTaskInputStatusPlatformError {
		return s.platformError(ctx, tx, task, in.Summary)
	}
	ok := in.Status != "failed"
	if ok {
		if res, stop, err := s.takeHandoff(ctx, tx, task, deref(in.Handoff)); err != nil || stop {
			return res, err
		}
	}
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

// platformError: a reviewer reports that the platform did not prepare what it was to check
// (its worktree is not the commit under review). Not a verdict: the review is prepared again.
func (s *Service) platformError(ctx context.Context, tx *core.Tx, task core.Task, summary string) (Result, error) {
	if !scheduler.IsGateTask(task) {
		return Result{}, refuse("status=platform_error 只用于平台派的审查 / 决策任务；做不完请用 status=failed")
	}
	task, err := s.sched.ReviewPlatformError(ctx, tx, task, "审查员上报："+summary)
	if err != nil {
		return Result{}, err
	}
	res := protocol.CompleteTaskResult{TaskID: protocol.TaskID(task.ID), Status: protocol.TaskStatus(task.Status)}
	done := okJSON(fmt.Sprintf("已上报平台错误，任务 %s 不算驳回，平台会重新准备审查（%s）", task.ID, task.Status), res)
	done.EndsTask = true
	return done, nil
}

// takeHandoff stores the hand-off note for the tasks that build on this one (shared-members.md
// §4.2). When some do and the note is missing, the first complete_task is refused with a
// reminder (stop); calling again without one goes through.
func (s *Service) takeHandoff(ctx context.Context, tx *core.Tx, task core.Task, handoff string) (Result, bool, error) {
	if handoff = strings.TrimSpace(handoff); handoff != "" {
		_, err := core.UpdateTask(ctx, tx, task, map[string]any{"handoff": handoff})
		return Result{}, false, err
	}
	if task.Handoff != "" || task.HandoffAsked || isReviewOnly(task) {
		return Result{}, false, nil
	}
	next, err := core.OpenDependents(ctx, tx, task.ConversationID, task.ID)
	if err != nil || len(next) == 0 {
		return Result{}, false, err
	}
	if _, err := core.UpdateTask(ctx, tx, task, map[string]any{"handoff_asked": true}); err != nil {
		return Result{}, false, err
	}
	return failText("%s 依赖任务 %s：请在 complete_task 里加 handoff，写给接手的人——还没做完的、要注意的坑、约定好的接口和文件。"+
		"确实没什么要交代的，再调用一次 complete_task 即可。", strings.Join(next, "、"), task.ID), true, nil
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
	if scheduler.IsGateTask(task) && task.Status == "blocked" {
		return task, refuse("任务 %s 因平台错误已暂停（blocked），不能再给结论；等用户在任务页重试后重新领取", id)
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
	if out.Pushed != nil {
		res.Pushed = pushedList(out.Pushed)
	}
	done := okJSON(fmt.Sprintf("任务 %s 已提交（%s）", task.ID, task.Status), res)
	done.EndsTask = true
	return done, nil
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

// pushDone continues complete_task / push_branch once every repository's push job of the call
// reported back (jobs of one call share a push group; the tool call row serializes them).
func (s *Service) pushDone(ctx context.Context, tx *core.Tx, meta map[string]any, r *protocol.GitResult) error {
	sessionID, callID := str(meta["session_id"]), str(meta["call_id"])
	if sessionID == "" || callID == "" {
		return nil
	}
	outcomes, done, err := s.pushOutcomes(ctx, tx, meta, r)
	if err != nil || !done {
		return err
	}
	if f, ok := firstFailure(outcomes); ok {
		return s.Complete(ctx, tx, sessionID, callID, failText("%s", pushFailText(f, len(outcomes))))
	}
	if str(meta["then"]) == "complete" {
		return s.completeAfterPush(ctx, tx, meta, outcomes)
	}
	return s.pushBranchDone(ctx, tx, meta, outcomes)
}

// pushOutcomes returns the call's push results once all are in (done=false while some are missing).
func (s *Service) pushOutcomes(ctx context.Context, tx *core.Tx, meta map[string]any, r *protocol.GitResult) ([]delivery.PushOutcome, bool, error) {
	group := str(meta["push_group"])
	if group == "" { // a push queued before multi-repository support: one job, no repository name
		return []delivery.PushOutcome{single(r)}, true, nil
	}
	var locked []struct{ CallID string }
	if err := toolCall(ctx, tx, str(meta["session_id"]), str(meta["call_id"])).Select("call_id").Clauses(core.ForUpdate).
		Find(&locked).Error; err != nil {
		return nil, false, err
	}
	outcomes, err := delivery.PushGroup(ctx, tx, str(meta["node_id"]), group)
	if err != nil {
		return nil, false, err
	}
	for _, o := range outcomes {
		if !o.Done {
			return outcomes, false, nil
		}
	}
	return outcomes, true, nil
}

func single(r *protocol.GitResult) delivery.PushOutcome {
	o := delivery.PushOutcome{Done: true, OK: r.Ok, Result: *r}
	if r.HeadSHA != nil {
		o.SHA = *r.HeadSHA
	}
	return o
}

func firstFailure(outcomes []delivery.PushOutcome) (delivery.PushOutcome, bool) {
	for _, o := range outcomes {
		if !o.OK {
			return o, true
		}
	}
	return delivery.PushOutcome{}, false
}

func pushFailText(o delivery.PushOutcome, n int) string {
	text := delivery.PushError(&o.Result)
	if n > 1 && o.Repo != "" {
		text = "仓库 " + o.Repo + "：" + text
	}
	return text
}

// pushedRepos are the repositories that had commits to deliver (repo → sha) and the first sha.
// A legacy single push without a repository name gives a nil map.
func pushedRepos(outcomes []delivery.PushOutcome) (map[string]string, string) {
	first := ""
	var out map[string]string
	for _, o := range outcomes {
		if o.Unchanged || o.SHA == "" {
			continue
		}
		if first == "" {
			first = o.SHA
		}
		if o.Repo != "" {
			if out == nil {
				out = map[string]string{}
			}
			out[o.Repo] = o.SHA
		}
	}
	if out == nil && len(outcomes) > 0 && outcomes[0].Repo != "" {
		out = map[string]string{}
	}
	return out, first
}

func pushedList(pushed map[string]string) []protocol.PushedRepo {
	out := make([]protocol.PushedRepo, 0, len(pushed))
	for repo, sha := range pushed {
		out = append(out, protocol.PushedRepo{Repo: repo, HeadSHA: sha})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Repo < out[j].Repo })
	return out
}

func (s *Service) pushBranchDone(ctx context.Context, tx *core.Tx, meta map[string]any, outcomes []delivery.PushOutcome) error {
	sessionID, callID := str(meta["session_id"]), str(meta["call_id"])
	sc, err := core.SessionContextOf(ctx, tx, sessionID)
	if err != nil {
		return ignoreMissing(err)
	}
	t, err := core.ThreadByID(ctx, tx, str(meta["thread_id"]))
	if err != nil {
		return ignoreMissing(err)
	}
	branch := orchestrator.MemberBranch(t.No, sc.Employee.Name)
	pushed, sha := pushedRepos(outcomes)
	res := protocol.PushBranchResult{Branch: branch, HeadSHA: sha, Pushed: pushedList(pushed)}
	if sha == "" {
		return s.Complete(ctx, tx, sessionID, callID, okJSON("没有新的提交需要推送", res))
	}
	return s.Complete(ctx, tx, sessionID, callID, okJSON("已推送到 "+branch+pushedText(pushed), res))
}

func pushedText(pushed map[string]string) string {
	if len(pushed) == 0 {
		return ""
	}
	repos := make([]string, 0, len(pushed))
	for r := range pushed {
		repos = append(repos, r)
	}
	sort.Strings(repos)
	return "（" + strings.Join(repos, "、") + "）"
}

func (s *Service) completeAfterPush(ctx context.Context, tx *core.Tx, meta map[string]any, outcomes []delivery.PushOutcome) error {
	sessionID, callID := str(meta["session_id"]), str(meta["call_id"])
	task, err := core.LockTask(ctx, tx, str(meta["thread_id"]), str(meta["task_id"]))
	if err != nil {
		return s.Complete(ctx, tx, sessionID, callID, failText("任务不存在了"))
	}
	if !workable[task.Status] {
		return s.Complete(ctx, tx, sessionID, callID, failText("任务 %s 已经是 %s", task.ID, task.Status))
	}
	pushed, sha := pushedRepos(outcomes)
	var res Result
	err = tx.Savepoint(ctx, func(sub *core.Tx) error {
		var cerr error
		res, cerr = s.complete(ctx, sub, task, scheduler.Outcome{OK: true, Summary: str(meta["summary"]), HeadSHA: sha, Pushed: pushed})
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

// listTeam lists the thread's team (lead, project members it can work with) and their current task.
func (s *Service) listTeam(ctx context.Context, tx *core.Tx, c *call) (Result, error) {
	t, err := requireThread(c)
	if err != nil {
		return Result{}, err
	}
	emps, err := core.ThreadTeam(ctx, tx, t)
	if err != nil {
		return Result{}, err
	}
	out := protocol.ListTeamResult{Members: []protocol.TeamMember{}}
	var lines []string
	for _, e := range emps {
		m := s.member(ctx, tx, t, e)
		out.Members = append(out.Members, m)
		lines = append(lines, fmt.Sprintf("%s（%s）%s%s", e.Name, deref(m.Title), deref(m.Status), memberCommands(ctx, tx, e)))
	}
	return okJSON(strings.Join(lines, "\n"), out), nil
}

// memberCommands names a member's custom commands for list_team: a send_message that starts
// with one is expanded for that member.
func memberCommands(ctx context.Context, tx *core.Tx, e core.Employee) string {
	if e.Tier != "member" {
		return ""
	}
	cmds, err := composer.EmployeeCommands(ctx, tx, e, composer.RoleOf(ctx, tx, e))
	if err != nil || len(cmds) == 0 {
		return ""
	}
	return "；命令（send_message 以它开头）：" + composer.Names(cmds)
}

func (s *Service) member(ctx context.Context, tx *core.Tx, t core.Thread, e core.Employee) protocol.TeamMember {
	title := e.Spec.Title
	typ := protocol.TeamMemberType("claude_code")
	if e.Runtime == "codex" {
		typ = "codex"
	}
	m := protocol.TeamMember{Name: e.Name, Role: e.Role, Title: &title, Type: &typ}
	if task, err := core.CurrentTaskOf(ctx, tx, e); err == nil && task.ConversationID != t.ID {
		busy := "在别的讨论串忙"
		m.Status = &busy
	} else if err == nil {
		st := "在做 " + task.ID
		m.TaskID, m.Status = &task.ID, &st
	} else {
		idle := "空闲"
		m.Status = &idle
	}
	if e.NodeID != nil {
		var n store.Node
		if tx.Gorm(ctx).Select("name").Where("id = ?", *e.NodeID).Take(&n).Error == nil {
			m.Node = &n.Name
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
	var ws []struct{ SessionID, CallID, Tool string }
	err := tx.Gorm(ctx).Table("at_tool_calls").Select("session_id, call_id, tool").
		Where("status = 'waiting' AND result->>'waiting_task' = ? AND result->>'thread_id' = ?", task.ID, task.ConversationID).
		Find(&ws).Error
	if err != nil {
		return err
	}
	for _, w := range ws {
		if err := s.Complete(ctx, tx, w.SessionID, w.CallID, outcomeOf(task, w.Tool)); err != nil {
			return err
		}
	}
	return nil
}
