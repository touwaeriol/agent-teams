package delivery

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/ghops"
	"github.com/touwaeriol/agent-teams/server/internal/orchestrator"
	"github.com/touwaeriol/agent-teams/server/internal/scheduler"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// maxFastForwardRetries rebuilds a trial branch when the thread head moved meanwhile (§8.2 step 4).
const maxFastForwardRetries = 3

// mergeable task states: the member finished (review/decision) or the lead takes it as it is.
var mergeable = map[string]bool{"review": true, "decision": true, "running": true, "blocked": true, "claimed": true}

// MergeRequest narrows enqueue_merge: Repo merges only that repository, Base records the base
// branch the thread uses there (before its thread branch exists).
type MergeRequest struct {
	Repo string
	Base string
}

// Enqueue puts a member's task into the thread's merge queue (enqueue_merge): one entry per
// repository the task pushed. It returns the entries and the queue position of the first.
func (e *Engine) Enqueue(ctx context.Context, tx *core.Tx, t core.Thread, task core.Task, req MergeRequest) ([]core.MergeEntry, int, error) {
	if !mergeable[task.Status] || task.ClaimedBy == "" {
		return nil, 0, ErrTaskNotReady
	}
	repos, err := e.mergeRepos(ctx, tx, t, task, req)
	if err != nil {
		return nil, 0, err
	}
	branch := orDefault(task.Branch, orchestrator.MemberBranch(t.No, task.ClaimedBy))
	if len(repos) == 0 {
		return e.mergedWithoutRepo(ctx, tx, t, task, branch)
	}
	entries := make([]core.MergeEntry, 0, len(repos))
	for _, repo := range repos {
		entry, err := core.InsertMerge(ctx, tx, t.ID, &task.ID, task.ClaimedBy, repo, branch)
		if err != nil {
			return nil, 0, err
		}
		if err := tx.EmitMerge(ctx, t, entry); err != nil {
			return nil, 0, err
		}
		entries = append(entries, entry)
	}
	if _, err := e.sched.SetStatus(ctx, tx, task, "merging"); err != nil {
		return nil, 0, err
	}
	pos, err := core.QueuePosition(ctx, tx, entries[0])
	tx.AfterCommit(func(ctx context.Context) { e.Kick(ctx) })
	return entries, pos, err
}

// mergeRepos are the repositories to merge for a task: those it pushed (optionally one of them).
// Tasks completed before multi-repository support recorded only a head sha: their single
// repository is the project's only one.
func (e *Engine) mergeRepos(ctx context.Context, tx *core.Tx, t core.Thread, task core.Task, req MergeRequest) ([]string, error) {
	if !e.orch.HasRepo(ctx, t.ProjectID) {
		return nil, nil
	}
	pushed := pushedRepos(task)
	if len(pushed) == 0 && task.HeadSHA != "" {
		pushed = e.legacyRepo(ctx, t.ProjectID)
	}
	if req.Repo == "" {
		return pushed, nil
	}
	slug, err := e.orch.SetBase(ctx, tx, t, req.Repo, req.Base)
	if err != nil {
		return nil, err
	}
	if !slices.ContainsFunc(pushed, func(r string) bool { return strings.EqualFold(r, slug) }) {
		return nil, fmt.Errorf("%w：任务 %s 没有在 %s 里推送改动", ErrNotPushed, task.ID, slug)
	}
	return []string{slug}, nil
}

func pushedRepos(task core.Task) []string {
	out := make([]string, 0, len(task.Pushed))
	for r := range task.Pushed {
		out = append(out, r)
	}
	slices.Sort(out)
	return out
}

func (e *Engine) legacyRepo(ctx context.Context, projectID string) []string {
	p, err := e.orch.Project(ctx, projectID)
	if err != nil || len(p.Repos) != 1 {
		return nil
	}
	return []string{p.Repos[0].Repo}
}

// mergedWithoutRepo: projects without a repository (or a task without commits) have nothing to merge.
func (e *Engine) mergedWithoutRepo(ctx context.Context, tx *core.Tx, t core.Thread, task core.Task, branch string) ([]core.MergeEntry, int, error) {
	entry, err := core.InsertMerge(ctx, tx, t.ID, &task.ID, task.ClaimedBy, "", branch)
	if err != nil {
		return nil, 0, err
	}
	if entry, err = core.UpdateMerge(ctx, tx, entry.ID, map[string]any{"status": "merged"}); err != nil {
		return nil, 0, err
	}
	if _, err := e.sched.Finish(ctx, tx, task, "done"); err != nil {
		return nil, 0, err
	}
	return []core.MergeEntry{entry}, 0, tx.EmitMerge(ctx, t, entry)
}

func orDefault(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

// Tick processes the head entry of every thread's queue, then pull requests waiting to merge.
func (e *Engine) Tick(ctx context.Context) {
	threads, err := core.ThreadsWithActiveMerges(ctx, e.core.Pool)
	if err != nil {
		e.log.Warn("merge queue", "err", err)
		return
	}
	for _, id := range threads {
		if err := e.step(ctx, id); err != nil {
			e.log.Warn("merge queue step", "thread_id", id, "err", err)
		}
	}
	e.prTick(ctx)
	e.doneTick(ctx)
}

// step advances the first active entry of each repository of the thread by one state.
func (e *Engine) step(ctx context.Context, threadID string) error {
	queue, err := core.MergeQueue(ctx, e.core.Pool, threadID, true)
	if err != nil || len(queue) == 0 {
		return err
	}
	t, err := core.ThreadByID(ctx, e.core.Pool, threadID)
	if err != nil {
		return err
	}
	var errs []error
	for _, entry := range headsByRepo(queue) {
		errs = append(errs, e.stepEntry(ctx, t, entry))
	}
	return errors.Join(errs...)
}

// headsByRepo picks the first entry of every repository (queue order).
func headsByRepo(queue []core.MergeEntry) []core.MergeEntry {
	seen := map[string]bool{}
	var heads []core.MergeEntry
	for _, m := range queue {
		if !seen[m.Repo] {
			seen[m.Repo] = true
			heads = append(heads, m)
		}
	}
	return heads
}

func (e *Engine) stepEntry(ctx context.Context, t core.Thread, entry core.MergeEntry) error {
	repo, err := e.repoIn(ctx, t.ProjectID, entry.Repo)
	if err != nil {
		return e.fail(ctx, t, entry, "项目没有可用的 GitHub 仓库 "+entry.Repo, err)
	}
	if entry.Status == "ci_running" {
		return e.checkEntry(ctx, t, repo, entry)
	}
	if err := e.orch.EnsureThreadBranch(ctx, t, repo); err != nil {
		return e.fail(ctx, t, entry, "创建讨论串分支失败", err)
	}
	return e.startEntry(ctx, t, repo, entry)
}

// startEntry builds mq/<no>/<id> from the thread head and merges the member branch into it.
func (e *Engine) startEntry(ctx context.Context, t core.Thread, repo ghops.Repo, entry core.MergeEntry) error {
	d, err := core.DeliveryOf(ctx, e.core.Pool, t.ProjectID)
	if err != nil {
		return err
	}
	if !d.Gates.MergeQueue {
		return e.directMerge(ctx, t, repo, entry)
	}
	head, err := e.gh.RefSHA(ctx, repo, t.Branch)
	if ghops.Status(err) == 404 {
		head, err = e.recreateThreadBranch(ctx, t, repo)
	}
	if err != nil {
		return e.fail(ctx, t, entry, "读取讨论串分支失败", err)
	}
	trial := fmt.Sprintf("mq/%d/%s", t.No, entry.ID)
	_ = e.gh.DeleteRef(ctx, repo, trial)
	if err := e.gh.CreateRef(ctx, repo, trial, head); err != nil {
		return e.fail(ctx, t, entry, "创建试合并分支失败", err)
	}
	res, err := e.gh.Merge(ctx, repo, trial, entry.HeadBranch, fmt.Sprintf("Merge %s into thread #%d (queue %s)", entry.HeadBranch, t.No, entry.ID))
	if err != nil || res.Conflict {
		_ = e.gh.DeleteRef(ctx, repo, trial)
		return e.mergeProblem(ctx, t, entry, res, err)
	}
	return e.toCI(ctx, t, entry, trial, orDefault(res.SHA, head), head)
}

// recreateThreadBranch cuts the thread branch again from the thread's base when GitHub no longer
// has it although the thread recorded it (deleted by hand, or with a merged pull request while
// the thread kept working), instead of cancelling the merge. The lead is always told; when the
// repository's pull request is not merged, the tasks merged into the lost branch are named: their
// changes may be missing from the new one (lostMergesNote).
func (e *Engine) recreateThreadBranch(ctx context.Context, t core.Thread, repo ghops.Repo) (string, error) {
	base := e.orch.BaseFor(ctx, e.core.Pool, t, repo)
	if base == "" {
		return "", fmt.Errorf("讨论串分支 %s 不存在：%w", t.Branch, ErrBaseUnknown)
	}
	e.log.Warn("thread branch missing, recreating it", "thread_id", t.ID, "repo", repo.Slug, "branch", t.Branch, "base", base)
	sha, err := e.gh.EnsureBranch(ctx, repo, t.Branch, base)
	if err != nil {
		return "", fmt.Errorf("讨论串分支 %s 不存在，从 %s 重新创建也失败了：%w", t.Branch, base, err)
	}
	err = e.core.InTx(ctx, func(tx *core.Tx) error {
		note, err := lostMergesNote(ctx, tx, t, repo.Slug)
		if err != nil {
			return err
		}
		return e.sched.NotifyLead(ctx, tx, t, fmt.Sprintf("[Merge · %s] 讨论串分支 %s 在仓库 %s 上不存在，已从 %s 重新切出。%s",
			repo.Slug, t.Branch, repo.Slug, base, note))
	})
	return sha, err
}

// lostMergesNote names the tasks merged into a thread branch that is gone, unless the
// repository's pull request merged (their changes are in the base the branch was cut from).
// Done tasks cannot be enqueued again (enqueue_merge takes unfinished tasks only), so the note
// says how to bring the changes back: a new task for the member that resubmits its branch.
func lostMergesNote(ctx context.Context, tx *core.Tx, t core.Thread, repo string) (string, error) {
	if tr, err := core.ThreadRepoOf(ctx, tx, t.ID, repo); err == nil && tr.PRNumber != nil && tr.PRState == "merged" {
		return "这个仓库的 PR 已经合并，之前合并的改动都在基准分支里，不需要处理。", nil
	}
	var rows []struct{ TaskID, HeadBranch, Employee string }
	err := tx.Gorm(ctx).Table("at_merge_queue").Select("task_id, head_branch, employee").
		Where("thread_id = ? AND repo = ? AND status = 'merged' AND task_id IS NOT NULL", t.ID, repo).Order("position").Find(&rows).Error
	if err != nil || len(rows) == 0 {
		return "此前没有合并进这个仓库讨论串分支的任务。", err
	}
	items := make([]string, len(rows))
	for i, r := range rows {
		items[i] = fmt.Sprintf("%s（%s，成员分支 %s）", r.TaskID, r.Employee, r.HeadBranch)
	}
	return fmt.Sprintf("这个仓库的 PR 还没有合并，之前合并进旧讨论串分支的任务改动可能不在新分支里：%s。请核对新分支；"+
		"这些任务已经是 done，不能再 enqueue_merge。缺了的改动请给对应成员派一个新任务（assign_task 点名该成员），"+
		"让他确认成员分支上这些提交还在（本地没有就从远端成员分支取回）后 complete_task，再对新任务 enqueue_merge；"+
		"成员分支也没有了的，只能重新做。", strings.Join(items, "、")), nil
}

func (e *Engine) mergeProblem(ctx context.Context, t core.Thread, entry core.MergeEntry, res ghops.MergeResult, err error) error {
	if res.Conflict {
		return e.conflict(ctx, t, entry)
	}
	return e.fail(ctx, t, entry, "试合并失败", err)
}

// directMerge merges the member branch into the thread branch without a trial (merge_queue gate off).
func (e *Engine) directMerge(ctx context.Context, t core.Thread, repo ghops.Repo, entry core.MergeEntry) error {
	base, err := e.gh.RefSHA(ctx, repo, t.Branch) // the hand-over range; a missing branch is cut again first
	if ghops.Status(err) == 404 {
		if base, err = e.recreateThreadBranch(ctx, t, repo); err != nil {
			return e.fail(ctx, t, entry, "读取讨论串分支失败", err)
		}
	}
	entry.BaseSHA = base
	res, err := e.gh.Merge(ctx, repo, t.Branch, entry.HeadBranch, fmt.Sprintf("Merge %s into thread #%d", entry.HeadBranch, t.No))
	if err != nil || res.Conflict {
		return e.mergeProblem(ctx, t, entry, res, err)
	}
	return e.merged(ctx, t, repo, entry, res.SHA)
}

func (e *Engine) toCI(ctx context.Context, t core.Thread, entry core.MergeEntry, trial, sha, base string) error {
	return e.core.InTx(ctx, func(tx *core.Tx) error {
		entry, err := core.UpdateMerge(ctx, tx, entry.ID, map[string]any{"status": "ci_running", "trial_branch": trial,
			"trial_sha": sha, "base_sha": base, "attempts": entry.Attempts + 1, "error": ""})
		if err != nil {
			return err
		}
		return tx.EmitMerge(ctx, t, entry)
	})
}

// checkEntry polls CI on the trial head and acts on the verdict.
func (e *Engine) checkEntry(ctx context.Context, t core.Thread, repo ghops.Repo, entry core.MergeEntry) error {
	verdict, failed, err := e.ciVerdict(ctx, t, repo, entry.TrialSHA, &entry.ID, entry.UpdatedAt)
	if err != nil {
		return err
	}
	switch verdict {
	case CIPass:
		return e.fastForward(ctx, t, repo, entry)
	case CIFail:
		return e.ciFailed(ctx, t, repo, entry, failed, "")
	case CIMissing:
		return e.ciFailed(ctx, t, repo, entry, nil, "等待超时，仓库上没有出现任何 CI 检查（项目设置 on_missing=block）")
	}
	return nil
}

// ciVerdict reads the checks of sha, records them and evaluates the project's CI rules.
// Without checks:read the gate degrades to off and the thread is told once.
func (e *Engine) ciVerdict(ctx context.Context, t core.Thread, repo ghops.Repo, sha string, queueID *string, since time.Time) (Verdict, []core.CheckView, error) {
	d, err := core.DeliveryOf(ctx, e.core.Pool, t.ProjectID)
	if err != nil {
		return CIPending, nil, err
	}
	rules := RulesOf(d)
	if rules.Mode == "off" {
		return CIPass, nil, nil
	}
	checks, err := e.gh.CheckRuns(ctx, repo, sha)
	if errors.Is(err, ghops.ErrNoChecks) {
		return CIPass, nil, e.degraded(ctx, t, repo)
	}
	if err != nil {
		return CIPending, nil, err
	}
	if err := e.recordChecks(ctx, t, queueID, sha, checks); err != nil {
		return CIPending, nil, err
	}
	v, failed := Evaluate(checks, rules, e.now().Sub(since))
	return v, failed, nil
}

func (e *Engine) recordChecks(ctx context.Context, t core.Thread, queueID *string, sha string, checks []core.CheckView) error {
	if len(checks) == 0 {
		return nil
	}
	return e.core.InTx(ctx, func(tx *core.Tx) error {
		if err := core.SaveChecks(ctx, tx, t.ProjectID, &t.ID, queueID, sha, checks); err != nil {
			return err
		}
		_, err := tx.Emit(ctx, t.ProjectID, &t.ID, core.EvCI, "", map[string]any{"thread_no": t.No, "sha": sha, "checks": checks})
		return err
	})
}

// degradedNote is posted once per thread when CI cannot be read.
const degradedNote = "CI 关卡已降级为 off：平台的 GitHub App 没有 checks:read 权限（请管理员在 GitHub 上给 App 加权限并批准），合并不等待检查。"

// standaloneCINote is degradedNote for a standalone Git repository (plain git has no check runs).
const standaloneCINote = "CI 关卡已降级为 off：%s 是独立 Git 仓库，平台读不到它的 CI 结果，合并不等待检查（需要时让成员在任务里自己跑测试）。"

func (e *Engine) degraded(ctx context.Context, t core.Thread, repo ghops.Repo) error {
	note := degradedNote
	if repo.Standalone() {
		note = fmt.Sprintf(standaloneCINote, repo.Slug)
	}
	var n int64
	err := e.core.Pool.Gorm(ctx).Model(&core.Message{}).Where("conversation_id = ? AND meta->>'notice' = 'ci_degraded'", t.ID).
		Count(&n).Error
	if err != nil || n > 0 {
		return err
	}
	return e.core.InTx(ctx, func(tx *core.Tx) error {
		m, err := core.InsertMessage(ctx, tx, core.NewMessage{ConversationID: t.ID, Role: "system", Author: "platform", Kind: "note",
			Content: note, Meta: map[string]any{"notice": "ci_degraded", "thread_no": t.No}})
		if err != nil {
			return err
		}
		return tx.EmitMessage(ctx, t.ProjectID, &t.ID, m)
	})
}

// fastForward moves the thread branch to the green trial head (422 = thread moved: rebuild).
func (e *Engine) fastForward(ctx context.Context, t core.Thread, repo ghops.Repo, entry core.MergeEntry) error {
	err := e.gh.UpdateRef(ctx, repo, t.Branch, entry.TrialSHA, false)
	if ghops.Status(err) == 422 && entry.Attempts < maxFastForwardRetries {
		_ = e.gh.DeleteRef(ctx, repo, entry.TrialBranch)
		return e.requeue(ctx, t, entry)
	}
	if err != nil {
		return e.fail(ctx, t, entry, "快进讨论串分支失败", err)
	}
	return e.merged(ctx, t, repo, entry, entry.TrialSHA)
}

func (e *Engine) requeue(ctx context.Context, t core.Thread, entry core.MergeEntry) error {
	return e.core.InTx(ctx, func(tx *core.Tx) error {
		entry, err := core.UpdateMerge(ctx, tx, entry.ID, map[string]any{"status": "queued", "trial_branch": "", "trial_sha": ""})
		if err != nil {
			return err
		}
		return tx.EmitMerge(ctx, t, entry)
	})
}

// merged finishes an entry: trial branch gone, task done (and the original task of a fix), lead told.
func (e *Engine) merged(ctx context.Context, t core.Thread, repo ghops.Repo, entry core.MergeEntry, sha string) error {
	if entry.TrialBranch != "" {
		_ = e.gh.DeleteRef(ctx, repo, entry.TrialBranch)
	}
	added := e.mergeRange(ctx, repo, entry.BaseSHA, sha)
	return e.core.InTx(ctx, func(tx *core.Tx) error {
		entry, err := core.UpdateMerge(ctx, tx, entry.ID, map[string]any{"status": "merged", "head_sha": sha, "base_sha": entry.BaseSHA})
		if err != nil {
			return err
		}
		if err := tx.EmitMerge(ctx, t, entry); err != nil {
			return err
		}
		if err := recordMerged(ctx, tx, entry, added); err != nil {
			return err
		}
		task, err := e.finishTasks(ctx, tx, entry)
		if err != nil {
			return err
		}
		return e.sched.NotifyLead(ctx, tx, t, fmt.Sprintf("[Merge · %s · %s · merged] %s 已合并进 %s 的 %s", task.ID, entry.Employee,
			entry.HeadBranch, entry.Repo, t.Branch))
	})
}

// mergeRange is what a merge added to the thread branch, for the follow-up tasks' briefs; a
// failed compare leaves the file list empty (the range alone still helps).
func (e *Engine) mergeRange(ctx context.Context, repo ghops.Repo, base, head string) core.TaskMerge {
	m := core.TaskMerge{Base: base, Head: head, Files: []string{}}
	if base == "" || head == "" || base == head {
		return m
	}
	if files, err := e.gh.Compare(ctx, repo, base, head); err == nil {
		m.Files = files
	} else {
		e.log.Warn("compare merge range", "repo", repo.Slug, "err", err)
	}
	return m
}

// recordMerged stores the range on the merged task, per repository.
func recordMerged(ctx context.Context, tx *core.Tx, entry core.MergeEntry, added core.TaskMerge) error {
	if entry.TaskID == nil {
		return nil
	}
	task, err := core.LockTask(ctx, tx, entry.ThreadID, *entry.TaskID)
	if err != nil {
		return ignoreMissing(err)
	}
	merged := maps.Clone(task.Merged)
	merged[entry.Repo] = added
	_, err = core.UpdateTask(ctx, tx, task, map[string]any{"merged": merged})
	return err
}

func (e *Engine) finishTasks(ctx context.Context, tx *core.Tx, entry core.MergeEntry) (core.Task, error) {
	if entry.TaskID == nil {
		return core.Task{}, nil
	}
	task, err := core.LockTask(ctx, tx, entry.ThreadID, *entry.TaskID)
	if err != nil {
		return task, ignoreMissing(err)
	}
	if open, err := core.TaskMergesOpen(ctx, tx, entry.ThreadID, task.ID, entry.Repo); err != nil || open > 0 {
		return task, err // other repositories of the task are still on their way
	}
	if lost, err := cancelledUnmerged(ctx, tx, entry.ThreadID, task.ID); err != nil || lost {
		if err != nil || task.Status != "merging" {
			return task, err
		}
		// Another repository's entry was cancelled (fail): the task is not done, it waits to be
		// enqueued again (releaseTask).
		return e.sched.SetStatus(ctx, tx, task, "blocked")
	}
	if task, err = e.sched.Finish(ctx, tx, task, "done"); err != nil {
		return task, err
	}
	if task.Kind != "fix" || task.Parent == "" {
		return task, nil
	}
	parent, err := core.LockTask(ctx, tx, entry.ThreadID, task.Parent)
	if err != nil || parent.Finished() {
		return task, ignoreMissing(err)
	}
	if open, err := core.TaskMergesOpen(ctx, tx, entry.ThreadID, parent.ID, entry.Repo); err != nil || open > 0 {
		return task, err
	}
	_, err = e.sched.Finish(ctx, tx, parent, "done")
	return task, err
}

// conflict marks the entry and gives the member a fix task (§8.2 step 2).
func (e *Engine) conflict(ctx context.Context, t core.Thread, entry core.MergeEntry) error {
	text := fmt.Sprintf("仓库 %s：把讨论串分支 %s 合并（或 rebase）进你的分支 %s 并解决冲突，提交后调用 complete_task。",
		entry.Repo, t.Branch, entry.HeadBranch)
	return e.problem(ctx, t, entry, "conflict", "合并冲突", text)
}

// ciFailed marks the entry and creates a fix task with the failed checks (§8.2 step 5).
func (e *Engine) ciFailed(ctx context.Context, t core.Thread, repo ghops.Repo, entry core.MergeEntry, failed []core.CheckView, reason string) error {
	_ = e.gh.DeleteRef(ctx, repo, entry.TrialBranch)
	var b strings.Builder
	fmt.Fprintf(&b, "仓库 %s：合并进讨论串分支前 CI 没有通过，请修复后提交并调用 complete_task。\n", entry.Repo)
	if reason != "" {
		b.WriteString(reason + "\n")
	}
	for _, c := range failed {
		fmt.Fprintf(&b, "- %s：%s %s\n  %s\n", c.Name, orDefault(c.Conclusion, c.Status), c.URL, clip(c.Summary, 400))
	}
	return e.problem(ctx, t, entry, "ci_failed", "CI 失败", b.String())
}

func (e *Engine) problem(ctx context.Context, t core.Thread, entry core.MergeEntry, status, label, instruction string) error {
	return e.core.InTx(ctx, func(tx *core.Tx) error {
		entry, err := core.UpdateMerge(ctx, tx, entry.ID, map[string]any{"status": status, "error": label})
		if err != nil {
			return err
		}
		if err := tx.EmitMerge(ctx, t, entry); err != nil {
			return err
		}
		fix, err := e.fixTask(ctx, tx, t, entry, instruction)
		if err != nil {
			return err
		}
		return e.sched.NotifyLead(ctx, tx, t, fmt.Sprintf("[Merge · %s · %s] %s 在 %s 的合并没有完成，已派修复任务 %s。", label,
			entry.Employee, entry.HeadBranch, entry.Repo, fix))
	})
}

// fixTask blocks the original task and queues a fix task for the member's role, reserved for
// the member whose branch it fixes (scheduler.ReserveFor; shared-members.md §4.1).
func (e *Engine) fixTask(ctx context.Context, tx *core.Tx, t core.Thread, entry core.MergeEntry, instruction string) (string, error) {
	var mainID, parent string
	if entry.TaskID != nil {
		task, err := core.LockTask(ctx, tx, t.ID, *entry.TaskID)
		if err != nil {
			return "", err
		}
		if _, err := e.sched.SetStatus(ctx, tx, task, "blocked"); err != nil {
			return "", err
		}
		parent = task.ID
		if task.MainTaskID != nil {
			mainID = *task.MainTaskID
		}
	}
	assignee := entry.Employee
	if m, err := core.EmployeeByName(ctx, tx, t.ProjectID, &t.ID, entry.Employee); err == nil {
		assignee = m.Role
	}
	fix, _, err := e.sched.Assign(ctx, tx, scheduler.Assignment{Thread: t, Assignee: assignee, Instruction: instruction,
		Main: mainID, Kind: "fix", Parent: parent, CreatedBy: "platform", Priority: 50, ReservedFor: entry.Employee})
	return fix.ID, err
}

// fail marks an entry failed for a non-merge reason (GitHub unavailable, no repo) and tells the lead.
func (e *Engine) fail(ctx context.Context, t core.Thread, entry core.MergeEntry, label string, cause error) error {
	if ghops.Status(cause) == 503 || ghops.Status(cause) >= 500 {
		return cause // GitHub unavailable: retry on the next tick
	}
	reason := label + "：" + cause.Error()
	return e.core.InTx(ctx, func(tx *core.Tx) error {
		entry, err := core.UpdateMerge(ctx, tx, entry.ID, map[string]any{"status": "cancelled", "error": clip(reason, 500)})
		if err != nil {
			return err
		}
		if err := tx.EmitMerge(ctx, t, entry); err != nil {
			return err
		}
		retry, err := e.releaseTask(ctx, tx, entry)
		if err != nil {
			return err
		}
		return e.sched.NotifyLead(ctx, tx, t, fmt.Sprintf("[Merge · %s · %s] %s%s", entry.Employee, entry.Repo, reason, retry))
	})
}

// cancelledUnmerged reports a task with a repository whose last entry was cancelled and that
// has no merged entry.
func cancelledUnmerged(ctx context.Context, q core.DB, threadID, taskID string) (bool, error) {
	var n int
	err := store.QueryRow(ctx, q, `SELECT count(*) FROM at_merge_queue q WHERE q.thread_id = $1 AND q.task_id = $2
		AND q.status = 'cancelled' AND NOT EXISTS (SELECT 1 FROM at_merge_queue m WHERE m.thread_id = q.thread_id
		AND m.task_id = q.task_id AND m.repo = q.repo AND (m.status <> 'cancelled' AND m.position > q.position OR m.status = 'merged'))`,
		threadID, taskID).Scan(&n)
	return n > 0, err
}

// releaseTask puts the task of a cancelled entry back from merging to blocked (as a conflict or
// failed CI does), so the lead can enqueue_merge it again once the cause is fixed, or cancel it.
// A task whose other repositories are still in the queue stays merging until they are through.
func (e *Engine) releaseTask(ctx context.Context, tx *core.Tx, entry core.MergeEntry) (string, error) {
	if entry.TaskID == nil {
		return "", nil
	}
	task, err := core.LockTask(ctx, tx, entry.ThreadID, *entry.TaskID)
	if err != nil || task.Status != "merging" {
		return "", ignoreMissing(err)
	}
	if open, err := core.TaskMergesOpen(ctx, tx, entry.ThreadID, task.ID, entry.Repo); err != nil || open > 0 {
		return "", err
	}
	if _, err := e.sched.SetStatus(ctx, tx, task, "blocked"); err != nil {
		return "", err
	}
	return fmt.Sprintf("。合并条目已取消，任务 %s 回到 blocked：原因解决后可以再调用 enqueue_merge(task_id=%s) 重新入队，不需要了就 cancel_task", task.ID, task.ID), nil
}

// Run is the merge/PR loop (advisory-lock leader), woken by hints and every PollInterval.
func (e *Engine) Run(ctx context.Context) {
	hints, cancel := e.hub.Subscribe(mergeChannel)
	defer cancel()
	scheduler.RunAsLeader(ctx, e.core.Pool, "agent-teams:merge", 5*time.Second, func(ctx context.Context) {
		t := time.NewTicker(e.PollInterval)
		defer t.Stop()
		for {
			e.Tick(ctx)
			select {
			case <-ctx.Done():
				return
			case <-hints:
			case <-t.C:
			}
		}
	})
}
