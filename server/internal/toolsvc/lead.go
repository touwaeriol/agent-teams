package toolsvc

import (
	"context"
	"errors"
	"fmt"
	"strings"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/delivery"
	"github.com/touwaeriol/agent-teams/server/internal/orchestrator"
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
	instruction, err := s.repoInstruction(ctx, tx, t, in.Instruction, deref(in.Repo), deref(in.Base))
	if err != nil {
		return Result{}, err
	}
	task, pooled, err := s.sched.Assign(ctx, tx, scheduler.Assignment{Thread: t, Assignee: in.Assignee, Instruction: instruction,
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

// repoInstruction records an explicit base for the task's repository and names the repository
// (and base) in front of the instruction.
func (s *Service) repoInstruction(ctx context.Context, tx *core.Tx, t core.Thread, text, repo, base string) (string, error) {
	if repo == "" {
		if base != "" {
			return "", refuse("写 base 时请同时写 repo")
		}
		return text, nil
	}
	slug, err := s.orch.SetBase(ctx, tx, t, repo, base)
	if err != nil {
		return "", refuse("%s", err.Error())
	}
	prefix := fmt.Sprintf("[仓库 %s] ", slug)
	if base != "" {
		prefix = fmt.Sprintf("[仓库 %s · 基准分支 %s] ", slug, base)
	}
	return prefix + text, nil
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
	var m core.MainTask
	_ = q.Gorm(ctx).Select("title").Where("id = ?", *t.MainTaskID).Take(&m).Error
	return m.Title
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
	if in.Base != nil && in.Repo == nil {
		return Result{}, refuse("写 base 时请同时写 repo")
	}
	entries, pos, err := s.deliv.Enqueue(ctx, tx, t, task, delivery.MergeRequest{Repo: deref(in.Repo), Base: deref(in.Base)})
	if err != nil {
		return Result{}, refuseMerge(err)
	}
	out := protocol.EnqueueMergeResult{EntryID: entries[0].ID, Position: pos, Entries: entryRefs(entries)}
	if pos == 0 {
		return okJSON(fmt.Sprintf("%s 的任务 %s 没有需要合并的提交，已记为完成（条目已是 merged，不会再有合并通知，不用等它，直接继续，例如 open_pr）",
			task.ClaimedBy, task.ID), out), nil
	}
	return okJSON(fmt.Sprintf("%s 的分支已进合并队列（第 %d 位%s）", task.ClaimedBy, pos, reposOf(entries)), out), nil
}

func entryRefs(entries []core.MergeEntry) []protocol.MergeEntryRef {
	out := make([]protocol.MergeEntryRef, len(entries))
	for i, e := range entries {
		out[i] = protocol.MergeEntryRef{EntryID: e.ID, Repo: e.Repo}
	}
	return out
}

func reposOf(entries []core.MergeEntry) string {
	var repos []string
	for _, e := range entries {
		if e.Repo != "" {
			repos = append(repos, e.Repo)
		}
	}
	if len(repos) == 0 {
		return ""
	}
	return "，仓库 " + strings.Join(repos, "、")
}

// refuseMerge turns the merge request's own mistakes (unknown repository, nothing pushed there,
// fixed base) into tool refusals the lead can act on.
func refuseMerge(err error) error {
	if errors.Is(err, orchestrator.ErrUnknownRepo) || errors.Is(err, core.ErrBaseFixed) || errors.Is(err, delivery.ErrTaskNotReady) ||
		errors.Is(err, delivery.ErrNotPushed) {
		return refuse("%s", err.Error())
	}
	return err
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
	var task core.Task
	err := tx.Gorm(ctx).Select("id").Where("thread_id = ? AND claimed_by = ? AND status IN ('review','decision','running','blocked')",
		t.ID, *in.Member).Order("updated_at DESC").Take(&task).Error
	if err != nil {
		return core.Task{}, refuse("%s 没有可以合并的任务", *in.Member)
	}
	return core.LockTask(ctx, tx, t.ID, task.ID)
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
	req := delivery.PRRequest{Title: in.Title, Body: in.Body, By: by, Repo: deref(in.Repo), Base: deref(in.Base)}
	return Result{Later: func(ctx context.Context) Result {
		th, err := s.deliv.OpenPR(ctx, t.ID, req)
		if err != nil {
			return failure(err)
		}
		repos, err := core.ThreadRepos(ctx, s.core.Pool, th.ID)
		if err != nil {
			return failure(err)
		}
		out := protocol.OpenPRResult{PRNumber: deref0(th.PRNumber), PRURL: deref(th.PRURL), Prs: repoPRs(repos)}
		return okJSON("PR 已开："+prLinks(out.Prs), out)
	}}, nil
}

func repoPRs(rows []core.ThreadRepo) []protocol.RepoPR {
	out := []protocol.RepoPR{}
	for _, r := range rows {
		if r.PRNumber == nil {
			continue
		}
		base := r.BaseBranch
		out = append(out, protocol.RepoPR{Repo: r.Repo, PRNumber: *r.PRNumber, PRURL: deref(r.PRURL), Base: &base})
	}
	return out
}

func prLinks(prs []protocol.RepoPR) string {
	links := make([]string, len(prs))
	for i, p := range prs {
		links[i] = p.PRURL
	}
	return strings.Join(links, " ")
}

func deref0(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// finish writes the thread summary and files it under thread-summaries.md in project memory.
func (s *Service) finish(ctx context.Context, tx *core.Tx, c *call) (Result, error) {
	t, err := requireThread(c)
	if err != nil {
		return Result{}, err
	}
	var in protocol.FinishInput
	if err := c.decode(&in); err != nil {
		return Result{}, err
	}
	end, err := finishMode(ctx, tx, t)
	if err != nil {
		return Result{}, err
	}
	summary := strings.TrimSpace(in.Summary)
	if err := core.UpdateThread(ctx, tx, t.ID, map[string]any{"summary": summary}); err != nil {
		return Result{}, err
	}
	if err := s.rememberSummary(ctx, tx, t, summary); err != nil {
		return Result{}, err
	}
	if err := tx.EmitThread(ctx, t.ID); err != nil {
		return Result{}, err
	}
	switch end {
	case finishNow:
		// After the commit, so the tool result goes out before the sessions are closed.
		tx.AfterCommit(func(ctx context.Context) { s.endWithoutPR(context.WithoutCancel(ctx), t.ID) })
		return okText("总结已记录。这个讨论串没有代码改动，不需要开 PR：讨论串现在结束（done），成员和你的会话随即关闭，不用再做别的。"), nil
	case finishNeedsPR:
		return okText("总结已记录。讨论串有代码改动还没有开 PR：请调用 open_pr(title, body)，之后平台按项目设置收尾（PR 检查通过即结束、自动合并或等用户验收）。"), nil
	}
	return okText("总结已记录。平台按项目设置收尾：PR 检查通过即结束、自动合并或等用户验收；检查没过会把失败的检查发给你。"), nil
}

// What finish does besides recording the summary.
const (
	finishWithPR  = iota // a PR is open: the thread ends when it merges
	finishNeedsPR        // changes are on a thread branch without a PR yet
	finishNow            // nothing was changed in any repository: the thread is done now
)

// finishMode decides how finish ends the thread. A thread without repository changes (no
// thread branch, no PR, nothing in the merge queue) ends right away; its plan and tasks must be
// finished first, which is refused with what is still open.
func finishMode(ctx context.Context, tx *core.Tx, t core.Thread) (int, error) {
	repos, err := core.ThreadRepos(ctx, tx, t.ID)
	if err != nil {
		return 0, err
	}
	for _, r := range repos {
		if r.PRNumber != nil {
			return finishWithPR, nil
		}
	}
	for _, r := range repos {
		if r.BranchCreated {
			return finishNeedsPR, nil
		}
	}
	if t.Status != "running" && t.Status != "waiting" {
		return 0, refuse("讨论串现在是 %s，不能结束", t.Status)
	}
	if queued, err := core.MergeQueue(ctx, tx, t.ID, true); err != nil || len(queued) > 0 {
		if err != nil {
			return 0, err
		}
		return 0, refuse("合并队列里还有分支在合并，等合并完再开 PR（open_pr）")
	}
	return finishNow, openWork(ctx, tx, t)
}

// openWork refuses finishing a thread whose main tasks or small tasks are still going.
func openWork(ctx context.Context, tx *core.Tx, t core.Thread) error {
	plan, err := core.MainTasks(ctx, tx, t.ID)
	if err != nil {
		return err
	}
	var open []string
	for _, m := range plan {
		if m.Status != "done" {
			open = append(open, m.Title)
		}
	}
	tasks, err := core.ThreadTasks(ctx, tx, t.ID, core.TaskFilter{})
	if err != nil {
		return err
	}
	for _, task := range tasks {
		if !task.Finished() {
			open = append(open, fmt.Sprintf("%s（%s）", task.ID, task.Status))
		}
	}
	if len(open) > 0 {
		return refuse("还有没完成的工作：%s。先完成（或用 update_plan / cancel_task 收尾）再调用 finish", strings.Join(open, "、"))
	}
	return nil
}

// endWithoutPR moves a thread that changed nothing to done (sessions close, coordinator told).
func (s *Service) endWithoutPR(ctx context.Context, threadID string) {
	err := s.core.InTx(ctx, func(tx *core.Tx) error {
		t, err := core.LockThread(ctx, tx, threadID)
		if err != nil || (t.Status != "running" && t.Status != "waiting") {
			return ignoreMissing(err)
		}
		if mode, err := finishMode(ctx, tx, t); err != nil || mode != finishNow {
			return err
		}
		return s.orch.SetStatus(ctx, tx, t, "done", "")
	})
	if err != nil {
		s.log.Warn("finish without PR", "thread_id", threadID, "err", err)
	}
}

// tellUser posts the lead's short message to the user in the thread conversation right away
// (role leader, as a final reply would be), without ending the turn.
func (s *Service) tellUser(ctx context.Context, tx *core.Tx, c *call) (Result, error) {
	if _, err := requireThread(c); err != nil {
		return Result{}, err
	}
	var in protocol.TellUserInput
	if err := c.decode(&in); err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(in.Text) == "" {
		return Result{}, refuse("text 不能为空")
	}
	if err := s.orch.RecordReply(ctx, tx, c.emp(), in.Text); err != nil {
		return Result{}, err
	}
	return okText("已发给用户"), nil
}
