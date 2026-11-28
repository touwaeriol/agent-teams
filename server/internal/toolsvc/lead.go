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
	return okText(fmt.Sprintf("plan updated (%d steps)", len(steps))), tx.EmitThread(ctx, t.ID)
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
	files, err := s.useAttachments(ctx, tx, c, in.Attachments)
	if err != nil {
		return Result{}, err
	}
	task, pooled, err := s.sched.Assign(ctx, tx, scheduler.Assignment{Thread: t, Assignee: in.Assignee, Instruction: instruction,
		Main: in.Main, DependsOn: deps, Kind: kind, CreatedBy: "lead", Attachments: files})
	if err != nil {
		return Result{}, err
	}
	out := protocol.AssignTaskResult{TaskID: protocol.TaskID(task.ID), Status: protocol.TaskStatus(task.Status), Pooled: pooled}
	where := "is in the task pool; an idle " + task.Role + " will claim it"
	if !pooled {
		where = "is assigned to " + in.Assignee
	}
	return okJSON(fmt.Sprintf("task %s created and %s%s", task.ID, where, carried(files)), out), nil
}

// repoInstruction records an explicit base for the task's repository and names the repository
// (and base) in front of the instruction.
func (s *Service) repoInstruction(ctx context.Context, tx *core.Tx, t core.Thread, text, repo, base string) (string, error) {
	if repo == "" {
		if base != "" {
			return "", refuse("base needs repo too")
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
		lines = append(lines, fmt.Sprintf("%s [%s] %s → %s: %s", task.ID, task.Status, task.Role, orDash(task.ClaimedBy),
			clip(task.Instruction, 80)))
	}
	if len(lines) == 0 {
		return okJSON("no tasks yet", out), nil
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
		return Result{}, refuse("no task %s", in.TaskID)
	}
	reason := ""
	if in.Reason != nil {
		reason = *in.Reason
	}
	if _, err := s.sched.Cancel(ctx, tx, task, reason); err != nil {
		return Result{}, err
	}
	return okText("cancelled " + task.ID), nil
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
		return Result{}, refuse("base needs repo too")
	}
	entries, pos, err := s.deliv.Enqueue(ctx, tx, t, task, delivery.MergeRequest{Repo: deref(in.Repo), Base: deref(in.Base)})
	if err != nil {
		return Result{}, refuseMerge(err)
	}
	out := protocol.EnqueueMergeResult{EntryID: entries[0].ID, Position: pos, Entries: entryRefs(entries)}
	if pos == 0 {
		return okJSON(fmt.Sprintf("%s's task %s has no commits to merge and is recorded as done (the entry is already merged and no merge notice will follow: do not wait for it, carry on, e.g. with open_pr)",
			task.ClaimedBy, task.ID), out), nil
	}
	return okJSON(fmt.Sprintf("%s's branch is in the merge queue (position %d%s)", task.ClaimedBy, pos, reposOf(entries)), out), nil
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
	return ", repositories " + strings.Join(repos, ", ")
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
			return task, refuse("no task %s", *in.TaskID)
		}
		return task, nil
	}
	if in.Member == nil {
		return core.Task{}, refuse("give task_id or member")
	}
	var task core.Task
	err := tx.Gorm(ctx).Select("id").Where("thread_id = ? AND claimed_by = ? AND status IN ('review','decision','running','blocked')",
		t.ID, *in.Member).Order("updated_at DESC").Take(&task).Error
	if err != nil {
		return core.Task{}, refuse("%s has no task to merge", *in.Member)
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
	if err := questionsAnswered(ctx, tx, t, "open_pr"); err != nil {
		return Result{}, err
	}
	by := c.emp().Name
	req := delivery.PRRequest{Title: in.Title, Body: in.Body, By: by, Repo: deref(in.Repo), Base: deref(in.Base)}
	return Result{Later: func(ctx context.Context) Result {
		th, again, err := s.deliv.OpenOrResubmit(ctx, t.ID, req)
		if err != nil {
			return failure(err)
		}
		repos, err := core.ThreadRepos(ctx, s.core.Pool, th.ID)
		if err != nil {
			return failure(err)
		}
		out := protocol.OpenPRResult{PRNumber: deref0(th.PRNumber), PRURL: deref(th.PRURL), Prs: repoPRs(repos)}
		if again {
			return okJSON("the PR was already open; the thread is resubmitted for acceptance (review) and the checks are evaluated again: "+prLinks(out.Prs), out)
		}
		return okJSON("PR opened: "+prLinks(out.Prs), out)
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
	if err := openSubthreadsRefusal(ctx, tx, t); err != nil {
		return Result{}, err
	}
	dropped, err := s.dropIdleTasks(ctx, tx, t)
	if err != nil {
		return Result{}, err
	}
	end, err := finishMode(ctx, tx, t)
	if err != nil {
		return Result{}, err
	}
	answer := ""
	if end == finishWithPR {
		// A thread back in running with its PRs open (failed checks, a reject) is resubmitted
		// for review; refused (nothing recorded) while work is still open.
		if answer, err = s.deliv.FinishWithPR(ctx, tx, t); err != nil {
			return Result{}, err
		}
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
	// After the commit, in its own transaction: the end waits for this turn (the lead's final
	// reply after finish is recorded first), so the tool result always goes out.
	noChanges := end == finishNow || end == finishDelivered
	tx.AfterCommit(func(ctx context.Context) {
		if err := s.deliv.FinishCalled(context.WithoutCancel(ctx), t.ID, noChanges); err != nil {
			s.log.Warn("finish", "thread_id", t.ID, "err", err)
		}
	})
	note := ""
	if len(dropped) > 0 {
		note = "\nCancelled the tasks nobody was working on: " + strings.Join(dropped, ", ") + "."
	}
	switch end {
	case finishNow:
		return okText(note + "Summary recorded. This thread has no code changes and needs no PR: now write the final report to the user as the last reply of this turn; when the turn ends the thread ends (done) and your and the members' sessions close."), nil
	case finishDelivered:
		return okText(note + "Summary recorded. The changes are on the thread branch " + t.Branch + " of a standalone Git repository (no PRs there: pushing the thread branch delivers them): now write the final report to the user (branch name, what changed, test results); when the turn ends the thread ends (done)."), nil
	case finishNeedsPR:
		return okText(note + "Summary recorded. The thread has code changes and no PR yet: call open_pr(title, body); the platform then wraps up as the project is set up (done when the PR checks pass, automatic merge, or the user's acceptance)."), nil
	}
	return okText(strings.TrimPrefix(note+"\n"+answer, "\n")), nil
}

// idleStatus are the unfinished task states nobody is working in: finish cancels them, since
// the lead calling finish decides the thread is done and they would only hold its end. Claimed
// and running tasks (a member is on them) and tasks in review or decision (work done, waiting
// for its gate or the merge queue) are left alone.
var idleStatus = map[string]bool{"pending": true, "blocked": true}

// dropIdleTasks cancels the thread's idle tasks (idleStatus) and names them.
func (s *Service) dropIdleTasks(ctx context.Context, tx *core.Tx, t core.Thread) ([]string, error) {
	tasks, err := core.ThreadTasks(ctx, tx, t.ID, core.TaskFilter{Status: []string{"pending", "blocked"}})
	if err != nil {
		return nil, err
	}
	var out []string
	for _, task := range tasks {
		task, err := core.LockTask(ctx, tx, t.ID, task.ID)
		if err != nil || !idleStatus[task.Status] {
			continue // gone, or cancelled with an earlier one (a gate of a cancelled task)
		}
		if _, err := s.sched.Cancel(ctx, tx, task, "组长已调用 finish 结束讨论串"); err != nil {
			return nil, err
		}
		out = append(out, task.ID)
	}
	return out, nil
}

// What finish does besides recording the summary.
const (
	finishWithPR    = iota // the thread has PRs: resubmitted for review when back in running (FinishWithPR)
	finishNeedsPR          // changes are on a thread branch without a PR yet
	finishNow              // nothing was changed in any repository: the thread is done now
	finishDelivered        // changes only in standalone repositories (no PRs): done now, on their thread branches
)

// finishMode decides how finish ends the thread. A thread without repository changes (no
// thread branch, no PR, nothing in the merge queue) ends right away; its plan and tasks must be
// finished first, which is refused with what is still open.
func finishMode(ctx context.Context, tx *core.Tx, t core.Thread) (int, error) {
	if err := questionsAnswered(ctx, tx, t, "finish"); err != nil {
		return 0, err
	}
	repos, err := core.ThreadRepos(ctx, tx, t.ID)
	if err != nil {
		return 0, err
	}
	for _, r := range repos {
		if r.PRNumber != nil {
			return finishWithPR, nil
		}
	}
	standalone, err := core.StandaloneRepos(ctx, tx, t.ProjectID)
	if err != nil {
		return 0, err
	}
	delivered := false
	for _, r := range repos {
		if r.NeedsPR(standalone) {
			return finishNeedsPR, nil
		}
		delivered = delivered || r.Delivered(standalone)
	}
	if t.Status != "running" && t.Status != "waiting" {
		return 0, refuse("the thread is %s and cannot end now", t.Status)
	}
	if queued, err := core.MergeQueue(ctx, tx, t.ID, true); err != nil || len(queued) > 0 {
		if err != nil {
			return 0, err
		}
		return 0, refuse("branches are still merging in the merge queue; open the PR (open_pr) once they are merged")
	}
	if delivered {
		return finishDelivered, openWork(ctx, tx, t)
	}
	return finishNow, openWork(ctx, tx, t)
}

// questionsAnswered refuses wrapping a thread up (open_pr, finish) while question cards still
// wait on the user: ending the thread would cancel them and the user's decisions would be lost.
func questionsAnswered(ctx context.Context, tx *core.Tx, t core.Thread, tool string) error {
	n, err := core.ThreadQuestionsWaiting(ctx, tx, t.ID)
	if err != nil || n == 0 {
		return err
	}
	return refuse("%d questions are still waiting for the user, and ending the thread would cancel them. Wait for the answers, act on them, then call %s", n, tool)
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
			open = append(open, fmt.Sprintf("%s (%s)", task.ID, task.Status))
		}
	}
	if len(open) > 0 {
		return refuse("work is still open: %s. Finish it (or wrap it up with update_plan / cancel_task) before calling finish", strings.Join(open, ", "))
	}
	return nil
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
		return Result{}, refuse("text must not be empty")
	}
	if err := s.orch.RecordReply(ctx, tx, c.emp(), in.Text); err != nil {
		return Result{}, err
	}
	return okText("sent to the user"), nil
}
