package scheduler

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/orchestrator"
	"github.com/touwaeriol/agent-teams/server/internal/status"
)

// claimOne claims the best (idle member, claimable task) pair of the project's queue
// (shared-members.md §4.1); false when there is none. It takes the project row lock first and
// then the pair without waiting (SKIP LOCKED): it never holds a member or task row while waiting
// for the project row, the lock order of re-staffing and deliveries (core.LockProject).
func (s *Scheduler) claimOne(ctx context.Context, projectID string) (bool, error) {
	claimed := false
	err := s.core.InTx(ctx, func(tx *core.Tx) error {
		if err := core.LockProject(ctx, tx, projectID); err != nil {
			return err
		}
		m, task, ok, err := s.choose(ctx, tx, projectID)
		if err != nil || !ok {
			return err
		}
		t, err := core.ThreadByID(ctx, tx, task.ConversationID)
		if err != nil {
			return err
		}
		claimed = true
		return s.claim(ctx, tx, t, task, m)
	})
	return claimed, err
}

func ignoreMissing(err error) error {
	if err == core.ErrNotFound {
		return nil
	}
	return err
}

// choose loads the idle members and claimable tasks, scores them and locks the chosen pair
// (ok=false when no member can take a task, or the pair changed under us).
func (s *Scheduler) choose(ctx context.Context, tx *core.Tx, projectID string) (core.Employee, core.Task, bool, error) {
	idle, err := core.IdleMembers(ctx, tx, projectID)
	if err != nil || len(idle) == 0 {
		return core.Employee{}, core.Task{}, false, err
	}
	queue, err := core.ClaimableTasks(ctx, tx, projectID, rolesOf(idle))
	if err != nil || len(queue) == 0 {
		return core.Employee{}, core.Task{}, false, err
	}
	repos, err := core.ThreadRepoSets(ctx, tx, projectID)
	if err != nil {
		return core.Employee{}, core.Task{}, false, err
	}
	si, ji, ok := pick(seatsOf(idle, repos), jobsOf(queue, repos), time.Now())
	if !ok {
		return core.Employee{}, core.Task{}, false, nil
	}
	return lockPair(ctx, tx, idle[si].Employee, queue[ji].Task)
}

func rolesOf(idle []core.IdleMember) []string {
	var out []string
	for _, m := range idle {
		if !slices.Contains(out, m.Role) {
			out = append(out, m.Role)
		}
	}
	return out
}

// lockPair locks the chosen member and task (skipping rows another transaction holds) and
// checks both are still free: the member active with no task, the task pending.
func lockPair(ctx context.Context, tx *core.Tx, m core.Employee, task core.Task) (core.Employee, core.Task, bool, error) {
	m, err := core.TryLockEmployee(ctx, tx, m.ID)
	if err != nil || !m.Active {
		return m, task, false, ignoreMissing(err)
	}
	if _, err := core.CurrentTaskOf(ctx, tx, m); err != core.ErrNotFound {
		return m, task, false, ignoreMissing(err)
	}
	task, err = core.TryLockTask(ctx, tx, task.ConversationID, task.ID)
	if err != nil || task.Status != "pending" {
		return m, task, false, ignoreMissing(err)
	}
	return m, task, true, nil
}

func seatsOf(idle []core.IdleMember, repos map[string][]string) []seat {
	out := make([]seat, len(idle))
	for i, m := range idle {
		out[i] = seat{Name: m.Name, Role: m.Role, Legacy: deref(m.ThreadID), Thread: m.Thread, TurnThread: m.TurnThread,
			Repos: repos[m.Thread]}
	}
	return out
}

func jobsOf(queue []core.QueuedTask, repos map[string][]string) []job {
	out := make([]job, len(queue))
	for i, q := range queue {
		th := q.ConversationID
		j := job{Thread: th, Role: q.Role, Gate: q.CreatedBy == "platform", OnlyRoles: q.OnlyRoles,
			ReservedFor: q.ReservedFor, ReservedUntil: q.ReservedUntil, DepWorkers: q.DepWorkers, Repos: repos[th],
			Legacy: q.Legacy, Priority: q.Priority, Downstream: q.Downstream, Ready: q.Ready, Created: q.CreatedAt}
		if Named(q.Task) && q.NamedActive {
			j.Named = q.Assignee // a task named to a retired member goes to the role's pool
		}
		out[i] = j
	}
	return out
}

// Named reports whether a task was assigned to an instance rather than a role.
func Named(task core.Task) bool { return task.Assignee != "" && task.Assignee != task.Role }

// claim marks the task claimed, emits task.claimed and sends the member its brief; a project
// member moves to the task's thread first.
func (s *Scheduler) claim(ctx context.Context, tx *core.Tx, t core.Thread, task core.Task, m core.Employee) error {
	m, err := s.orch.MoveToThread(ctx, tx, m, t.ID)
	if err != nil {
		return err
	}
	task, err = core.UpdateTask(ctx, tx, task, core.WithAttemptReset(map[string]any{"status": "claimed", "claimed_by": m.Name,
		"claimed_at": time.Now().UTC(), "branch": orchestrator.MemberBranch(t.No, m.Name), "nudges": 0,
		"sync": map[string]core.TaskSync{}, "sync_pending": 0, "sync_round": "", "compact_id": "", "wait_since": nil}))
	if err != nil {
		return err
	}
	if err := tx.EmitTask(ctx, task); err != nil {
		return err
	}
	if err := tx.EmitActivity(ctx, t.ProjectID, &t.ID, m.Name, fmt.Sprintf("领取 %s：%s", task.ID, clip(task.Instruction, 80))); err != nil {
		return err
	}
	tx.AfterCommit(func(ctx context.Context) {
		s.status.Observe(ctx, m.ID, status.Observation{Kind: status.Claimed, Cause: "task.claimed:" + task.ID,
			TaskID: task.ID, TaskKind: task.Kind})
	})
	return s.startTask(ctx, tx, t, task, m)
}

// syncThenBrief syncs the member's worktree before the brief when the daemon can (shared-members.md
// §4.2): the task waits in claimed with sync_pending until every result is in (SyncDone) or
// SyncTimeout passed. Otherwise the brief goes out at once, after the old fetch-only refresh.
func (s *Scheduler) syncThenBrief(ctx context.Context, tx *core.Tx, t core.Thread, task core.Task, m core.Employee) error {
	jobs, err := s.queueSync(ctx, tx, t, task, m, 0)
	if err != nil || jobs > 0 {
		return err
	}
	if err := s.orch.RefreshWorktree(ctx, tx, m); err != nil {
		return err
	}
	return s.sendBrief(ctx, tx, t, task, m)
}

// queueSync queues the pre-task sync (attempt try of this claim) and marks the task waiting for
// it; 0 jobs when the member's worktree cannot be synced (the brief goes out at once).
func (s *Scheduler) queueSync(ctx context.Context, tx *core.Tx, t core.Thread, task core.Task, m core.Employee, try int) (int, error) {
	target, err := s.syncFor(ctx, tx, t, task)
	if err != nil {
		return 0, err
	}
	now := time.Now().UTC()
	deadline := now.Add(SyncTimeout)
	target.Sync.Deadline = &deadline
	round := fmt.Sprintf("%s.%d", core.NewID("sr"), try)
	jobs, err := s.orch.QueueSync(ctx, tx, m, target.Sync, target.Expect, map[string]any{"task_id": task.ID, "thread_id": t.ID, "round": round})
	if err != nil || jobs == 0 {
		return 0, err
	}
	_, err = core.UpdateTask(ctx, tx, task, map[string]any{"sync": map[string]core.TaskSync{}, "sync_pending": jobs,
		"sync_round": round, "wait_since": now})
	return jobs, err
}

// syncTry is the attempt a sync round belongs to (its ".N" suffix; 0 for rounds without one).
func syncTry(round string) int {
	i := strings.LastIndexByte(round, '.')
	if i < 0 {
		return 0
	}
	n, _ := strconv.Atoi(round[i+1:])
	return n
}

// syncTarget is what the member's worktree is brought to before a task; Expect is, for a review,
// the commit under review per repository (orchestrator.ExpectedSHA).
type syncTarget struct {
	Sync   protocol.GitPrepareSync
	Expect map[string]string
}

// syncFor is what the member's worktree is brought to before the task: the thread branch, or for
// a review / decision of another member's task the branch under review, at the commits the task
// was completed with (the repositories it pushed; nothing is expected of the others).
func (s *Scheduler) syncFor(ctx context.Context, tx *core.Tx, t core.Thread, task core.Task) (syncTarget, error) {
	member := syncTarget{Sync: protocol.GitPrepareSync{Mode: protocol.GitPrepareSyncModeMember, Ref: t.Branch}}
	if !isGateTask(task) {
		return member, nil
	}
	parent, err := core.TaskByID(ctx, tx, task.ConversationID, task.Parent)
	if err != nil || parent.Branch == "" {
		return member, ignoreMissing(err)
	}
	return syncTarget{Sync: protocol.GitPrepareSync{Mode: protocol.GitPrepareSyncModeReview, Ref: parent.Branch},
		Expect: reviewedSHAs(parent)}, nil
}

// reviewedSHAs is the commit under review per repository: what the task pushed, or (a record
// without per-repository pushes) its head under the key "" for a single-repository project.
func reviewedSHAs(parent core.Task) map[string]string {
	if parent.Pushed != nil {
		return maps.Clone(parent.Pushed)
	}
	if parent.HeadSHA != "" {
		return map[string]string{"": parent.HeadSHA}
	}
	return nil
}

// sendBrief delivers the task brief to the member (after the pre-task sync, when there was one),
// with the task anchor the daemon re-injects after a compaction during the task.
func (s *Scheduler) sendBrief(ctx context.Context, tx *core.Tx, t core.Thread, task core.Task, m core.Employee) error {
	text, err := s.taskBrief(ctx, tx, t, task, m)
	if err != nil {
		return err
	}
	anchor, err := orchestrator.TaskAnchor(ctx, tx, t, task)
	if err != nil {
		return err
	}
	_, err = s.orch.Deliver(ctx, tx, m, orchestrator.Delivery{Text: text, Kind: "task", Priority: "next", Anchor: &anchor,
		Record: &core.NewMessage{ConversationID: t.ID, Role: "system", Author: "platform", Kind: "note", Content: text,
			Meta: map[string]any{"to": m.Name, "task_id": task.ID}}})
	return err
}

// SyncDone records one repository's pre-task sync result; the last one sends the brief. Results
// of an earlier claim (round), repeated results and results after the timeout are ignored.
func (s *Scheduler) SyncDone(ctx context.Context, tx *core.Tx, meta map[string]any, r *protocol.GitResult) error {
	task, err := core.LockTask(ctx, tx, str(meta["thread_id"]), str(meta["task_id"]))
	if err != nil || !awaitingSync(task, str(meta["round"])) {
		return ignoreMissing(err)
	}
	repo := str(meta["repo"])
	if _, seen := task.Sync[repo]; seen {
		return nil
	}
	return s.finishSync(ctx, tx, task, map[string]core.TaskSync{repo: syncOf(r)}, task.SyncPending-1)
}

func awaitingSync(task core.Task, round string) bool {
	return task.Status == "claimed" && task.SyncPending > 0 && task.SyncRound == round
}

// finishSync records sync results and sends the brief once nothing is pending. A review whose
// worktree is not at the commit under review gets no brief: the platform prepares it again.
func (s *Scheduler) finishSync(ctx context.Context, tx *core.Tx, task core.Task, results map[string]core.TaskSync, pending int) error {
	synced := maps.Clone(task.Sync)
	maps.Copy(synced, results)
	task, err := core.UpdateTask(ctx, tx, task, map[string]any{"sync": synced, "sync_pending": pending})
	if err != nil || task.SyncPending > 0 {
		return err
	}
	t, err := core.ThreadByID(ctx, tx, task.ConversationID)
	if err != nil {
		return err
	}
	m, err := core.EmployeeByName(ctx, tx, t.ProjectID, &t.ID, task.ClaimedBy)
	if err != nil {
		return ignoreMissing(err)
	}
	if isGateTask(task) {
		problems, err := s.reviewProblems(ctx, tx, t, task)
		if err != nil {
			return err
		}
		if len(problems) > 0 {
			return s.retryReviewSync(ctx, tx, t, task, m, strings.Join(problems, "；"))
		}
	}
	return s.sendBrief(ctx, tx, t, task, m)
}

// ReviewSyncRetries is how many more times one claim of a review prepares the worktree when it
// is not at the commit under review, before the claim counts as a platform error.
const ReviewSyncRetries = 2

// reviewProblems lists the repositories whose worktree is not at the commit under review: the job
// failed or never reported, or HEAD is another commit. Repositories the task did not push
// expect nothing.
func (s *Scheduler) reviewProblems(ctx context.Context, tx *core.Tx, t core.Thread, task core.Task) ([]string, error) {
	target, err := s.syncFor(ctx, tx, t, task)
	if err != nil || target.Sync.Mode != protocol.GitPrepareSyncModeReview {
		return nil, err
	}
	var out []string
	for _, repo := range slices.Sorted(maps.Keys(task.Sync)) {
		got := task.Sync[repo]
		want := orchestrator.ExpectedSHA(target.Expect, repo, len(task.Sync))
		switch {
		case repo == unreported:
			out = append(out, fmt.Sprintf("%s：%s", repo, orDefault(got.Error, "超时")))
		case want == "":
		case got.Error != "":
			out = append(out, fmt.Sprintf("%s：%s", repo, got.Error))
		case got.HeadSHA != want:
			out = append(out, fmt.Sprintf("%s：HEAD 是 %s，被审提交是 %s（%s）", repo, orDefault(short(got.HeadSHA), "未知"), short(want), orDefault(got.Action, "无同步结果")))
		}
	}
	return out, nil
}

// retryReviewSync prepares the review worktree again (ReviewSyncRetries times per claim); after
// that the claim is a platform error (reviewPlatformError).
func (s *Scheduler) retryReviewSync(ctx context.Context, tx *core.Tx, t core.Thread, task core.Task, m core.Employee, why string) error {
	try := syncTry(task.SyncRound)
	s.log.Warn("review worktree not ready", "thread_id", t.ID, "task_id", task.ID, "try", try, "why", why)
	if try < ReviewSyncRetries {
		jobs, err := s.queueSync(ctx, tx, t, task, m, try+1)
		if err != nil || jobs > 0 {
			return err
		}
	}
	_, err := s.reviewPlatformError(ctx, tx, t, task, "审查工作树没准备好："+why)
	return err
}

// reviewPlatformError handles a review / decision the platform could not set up (its worktree
// never reached the commit under review, or the reviewer reported platform_error). It is not a
// verdict: the parent task keeps its state and goes back to nobody. The gate returns to the queue
// for a fresh claim until MaxAttempts; then it is blocked and the lead is told.
func (s *Scheduler) reviewPlatformError(ctx context.Context, tx *core.Tx, t core.Thread, gate core.Task, why string) (core.Task, error) {
	clear := map[string]any{"sync": map[string]core.TaskSync{}, "sync_pending": 0, "sync_round": "", "wait_since": nil,
		"attempt": gate.Attempt + 1}
	if err := tx.EmitActivity(ctx, t.ProjectID, &t.ID, "platform", fmt.Sprintf("%s %s 平台错误：%s", gateName(gate), gate.ID, clip(why, 200))); err != nil {
		return gate, err
	}
	if gate.Attempt+1 < MaxAttempts {
		set := maps.Clone(clear)
		set["claimed_by"], set["claimed_at"] = "", nil
		gate, err := core.RequeueTask(ctx, tx, gate, set)
		if err != nil {
			return gate, err
		}
		s.kickAfter(tx, t.ProjectID)
		return gate, tx.EmitTask(ctx, gate)
	}
	gate, err := core.UpdateTask(ctx, tx, gate, clear)
	if err != nil {
		return gate, err
	}
	if gate, err = s.setStatus(ctx, tx, gate, "blocked"); err != nil {
		return gate, err
	}
	return gate, s.notifyLead(ctx, tx, t, fmt.Sprintf("[%s %s · 平台错误] 审查工作树没准备好，已重试 %d 次仍不行：%s\n"+
		"这不是驳回：任务 %s 保持原状态，没有退给开发者。排查好节点后，让用户在任务页重试 %s。",
		gateName(gate), gate.ID, MaxAttempts, why, gate.Parent, gate.ID))
}

// ReviewPlatformError exposes reviewPlatformError (complete_task status=platform_error).
func (s *Scheduler) ReviewPlatformError(ctx context.Context, tx *core.Tx, gate core.Task, why string) (core.Task, error) {
	t, err := core.ThreadByID(ctx, tx, gate.ConversationID)
	if err != nil {
		return gate, err
	}
	return s.reviewPlatformError(ctx, tx, t, gate, why)
}

// syncOf turns a git.result into the task's record: a job that succeeded without a sync report
// created the worktree fresh from the thread branch.
func syncOf(r *protocol.GitResult) core.TaskSync {
	switch {
	case !r.Ok:
		return core.TaskSync{Error: orDefault(deref(r.Error), "同步失败")}
	case r.Sync == nil:
		return core.TaskSync{Action: "created", HeadSHA: deref(r.HeadSHA)}
	}
	return core.TaskSync{Action: string(r.Sync.Action), HeadSHA: r.Sync.HeadSHA, Conflicts: r.Sync.Conflicts}
}

// unreported names the repositories whose sync result never came back.
const unreported = "其余仓库"

// SyncTimeout is how long a brief waits for the pre-task sync before it goes out unsynced; the
// sync jobs carry it as their deadline, so a late one leaves the tree alone.
const SyncTimeout = 2 * time.Minute

// releaseStaleSyncs sends the briefs whose sync results did not come back in time.
func (s *Scheduler) releaseStaleSyncs(ctx context.Context) {
	var stale []core.Task
	err := s.core.Pool.Gorm(ctx).Model(&core.Task{}).Where("status = 'claimed' AND sync_pending > 0 AND COALESCE(wait_since, claimed_at) < ?",
		time.Now().Add(-SyncTimeout)).Find(&stale).Error
	if err != nil {
		s.log.Warn("stale syncs", "err", err)
		return
	}
	for _, st := range stale {
		err := s.core.InTx(ctx, func(tx *core.Tx) error {
			task, err := core.LockTask(ctx, tx, st.ConversationID, st.ID)
			if err != nil || !awaitingSync(task, st.SyncRound) {
				return ignoreMissing(err)
			}
			return s.finishSync(ctx, tx, task, map[string]core.TaskSync{unreported: {Error: "超时"}}, 0)
		})
		if err != nil {
			s.log.Warn("release stale sync", "task_id", st.ID, "err", err)
		}
	}
}

// taskBrief is the message a member gets with a task: instruction, dependency results, roster and unread mail (§7.3).
func (s *Scheduler) taskBrief(ctx context.Context, tx *core.Tx, t core.Thread, task core.Task, m core.Employee) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "[Task %s", task.ID)
	if title := mainTitle(ctx, tx, task); title != "" {
		fmt.Fprintf(&b, " · %s", title)
	}
	fmt.Fprintf(&b, " · %s]\n%s\n", task.Kind, task.Instruction)
	if err := depResults(ctx, tx, &b, task); err != nil {
		return "", err
	}
	if err := reworkNotes(ctx, tx, &b, t, task, m); err != nil {
		return "", err
	}
	target, err := s.syncFor(ctx, tx, t, task)
	if err != nil {
		return "", err
	}
	if target.Sync.Mode == protocol.GitPrepareSyncModeReview {
		reviewNote(&b, target, task)
	} else if len(task.Sync) > 0 {
		syncNote(&b, target.Sync, task)
	}
	mail, err := core.PeekUnread(ctx, tx, t.ID, m.Name)
	if err != nil {
		return "", err
	}
	if len(mail) > 0 {
		fmt.Fprintf(&b, "\n你有 %d 条未读留言，可用 read_messages 查看。\n", len(mail))
	}
	fmt.Fprintf(&b, "\n你的分支：%s。做完后调用 complete_task(task_id=\"%s\", summary=…)。", task.Branch, task.ID)
	return b.String(), nil
}

func mainTitle(ctx context.Context, q core.DB, task core.Task) string {
	if task.MainTaskID == nil {
		return ""
	}
	var m core.MainTask
	_ = q.Gorm(ctx).Select("title").Where("id = ?", *task.MainTaskID).Take(&m).Error
	return m.Title
}

// depResults hands each dependency over to the task (shared-members.md §4.2): who did it, its
// summary and hand-off note, what its merge added to the thread branch, and the gate verdicts on it.
func depResults(ctx context.Context, tx *core.Tx, b *strings.Builder, task core.Task) error {
	for i, dep := range task.DependsOn {
		d, err := core.TaskByID(ctx, tx, task.ConversationID, dep)
		if err != nil {
			continue
		}
		if i == 0 {
			b.WriteString("\n依赖任务：\n")
		}
		fmt.Fprintf(b, "- %s（%s）：%s\n", d.ID, d.ClaimedBy, clip(d.Summary, 3000))
		if d.Handoff != "" {
			fmt.Fprintf(b, "  交接说明：%s\n", d.Handoff)
		}
		mergedNote(b, d)
		if err := gateNotes(ctx, tx, b, d); err != nil {
			return err
		}
	}
	return nil
}

// mergedNote lists, per repository, the range and files the dependency's merge added.
func mergedNote(b *strings.Builder, d core.Task) {
	for _, repo := range slices.Sorted(maps.Keys(d.Merged)) {
		m := d.Merged[repo]
		if m.Head == "" {
			continue
		}
		rng := short(m.Head)
		if m.Base != "" {
			rng = short(m.Base) + ".." + short(m.Head)
		}
		fmt.Fprintf(b, "  %s 改动 %s（git log %s / git show 看细节）", repo, rng, rng)
		if len(m.Files) > 0 {
			fmt.Fprintf(b, "：%s", strings.Join(clipList(m.Files, 30), "、"))
		}
		b.WriteString("\n")
	}
}

var gateVerb = map[string]string{"review": "审查", "decision": "决策"}

// reworkNotes add what a task that comes back carries: the gate verdicts on it so far, and when
// a member takes over rework reserved for someone else, where that member's commits are.
func reworkNotes(ctx context.Context, tx *core.Tx, b *strings.Builder, t core.Thread, task core.Task, m core.Employee) error {
	gates, err := core.GateVerdicts(ctx, tx, task.ConversationID, task.ID)
	if err != nil {
		return err
	}
	if len(gates) > 0 {
		b.WriteString("\n之前的审查 / 决策：\n")
		if err := gateNotes(ctx, tx, b, task); err != nil {
			return err
		}
	}
	if task.ReservedFor != "" && task.ReservedFor != m.Name {
		prev := orchestrator.MemberBranch(t.No, task.ReservedFor)
		fmt.Fprintf(b, "\n这是 %s 的返工，它没能在预留时间内接手，改由你来做。它的提交在 origin/%s：先 git fetch，再 git merge origin/%s，在此基础上修改。\n",
			task.ReservedFor, prev, prev)
	}
	return nil
}

// gateNotes adds the review / decision verdicts on the dependency.
func gateNotes(ctx context.Context, tx *core.Tx, b *strings.Builder, d core.Task) error {
	gates, err := core.GateVerdicts(ctx, tx, d.ConversationID, d.ID)
	for _, g := range gates {
		verdict := "打回"
		if g.Status == "done" {
			verdict = "通过"
		}
		fmt.Fprintf(b, "  %s（%s · %s）：%s\n", gateVerb[g.Kind], g.ClaimedBy, verdict, clip(g.Summary, 800))
	}
	return err
}

// reviewNote tells a reviewer which commits are under review and whether the platform checked
// out and verified them. Without a verified checkout (an older daemon, full clones) it asks the
// reviewer to check HEAD itself and to report platform_error, not a rejection, when it is wrong.
func reviewNote(b *strings.Builder, target syncTarget, task core.Task) {
	ref := target.Sync.Ref
	b.WriteString("\n被审提交（分支 " + ref + "）：\n")
	if len(target.Expect) == 0 {
		b.WriteString("- 平台没有记录被审提交的 sha（任务没有推送新提交）\n")
	}
	for _, repo := range slices.Sorted(maps.Keys(target.Expect)) {
		fmt.Fprintf(b, "- %s：%s\n", orDefault(repo, "仓库"), target.Expect[repo])
	}
	verified := len(task.Sync) > 0
	if len(task.Sync) > 0 {
		b.WriteString("\n工作目录：\n")
		for _, repo := range slices.Sorted(maps.Keys(task.Sync)) {
			want := orchestrator.ExpectedSHA(target.Expect, repo, len(task.Sync))
			got := task.Sync[repo]
			if want != "" && got.HeadSHA == want {
				fmt.Fprintf(b, "- %s：平台已检出并核对，HEAD = %s\n", repo, want)
				continue
			}
			if want != "" {
				verified = false
			}
			fmt.Fprintf(b, "- %s：%s\n", repo, reviewLine(ref, got))
		}
	}
	if verified {
		return
	}
	b.WriteString("\n平台没能核对工作目录是不是被审提交（节点版本较旧或 clone 模式）。开工前先核对 HEAD：能用 git 就 git rev-parse HEAD；" +
		"不能用 git 时，worktree 的 .git 是个文件，读它里面 gitdir: 指向的目录下的 HEAD 文件（clone 模式直接读 .git/HEAD），内容就是当前提交的 sha。" +
		"如果不是上面的被审提交，不要审，也不要用 status=failed 驳回：用 complete_task(status=\"platform_error\", summary=你看到的 HEAD) 上报，平台会重新准备审查。\n")
}

// syncNote tells the member what the pre-task sync did to the worktree.
func syncNote(b *strings.Builder, sync protocol.GitPrepareSync, task core.Task) {
	b.WriteString("\n工作目录：\n")
	for _, repo := range slices.Sorted(maps.Keys(task.Sync)) {
		fmt.Fprintf(b, "- %s：%s\n", repo, syncLine(sync, task.Sync[repo]))
	}
}

func syncLine(sync protocol.GitPrepareSync, s core.TaskSync) string {
	if sync.Mode == protocol.GitPrepareSyncModeReview {
		return reviewLine(sync.Ref, s)
	}
	ref, sha := sync.Ref, short(s.HeadSHA)
	switch s.Action {
	case "reset", "current", "created":
		return fmt.Sprintf("已在讨论串分支 %s 的最新提交上（%s），包含已合并的依赖任务的改动", ref, sha)
	case "merged":
		return fmt.Sprintf("已把讨论串分支 %s 合进你的分支（%s），你还没合并的提交都保留", ref, sha)
	case "absent":
		return fmt.Sprintf("讨论串分支 %s 还没有建，工作目录基于讨论串的基准分支", ref)
	case "conflict":
		return fmt.Sprintf("同步讨论串分支 %s 时和你未合并的提交冲突，已中止：%s。先 git merge origin/%s 解决冲突再开工",
			ref, strings.Join(s.Conflicts, "、"), ref)
	case "dirty":
		return fmt.Sprintf("有未提交的改动，没有同步。先提交或处理掉它们，再 git merge origin/%s", ref)
	}
	return fmt.Sprintf("没能自动同步（%s）。开工前先 git fetch，再 git merge origin/%s", orDefault(s.Error, "超时"), ref)
}

// reviewLine: a reviewer only reads, so the advice is a checkout, never a merge.
func reviewLine(ref string, s core.TaskSync) string {
	switch s.Action {
	case "checkout", "created":
		return fmt.Sprintf("已只读检出被审分支 %s 的提交 %s", ref, s.HeadSHA)
	case "absent":
		return fmt.Sprintf("被审分支 %s 还没推到 GitHub，看到的可能不是最新提交", ref)
	case "dirty":
		return fmt.Sprintf("审查目录里有未提交的改动，没有切到被审分支：先 git stash，再 git checkout --detach origin/%s", ref)
	}
	return fmt.Sprintf("没能自动检出被审分支（%s）：先 git fetch，再 git checkout --detach origin/%s", orDefault(s.Error, "超时"), ref)
}

func short(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

func clipList(items []string, n int) []string {
	if len(items) <= n {
		return items
	}
	return append(slices.Clone(items[:n]), fmt.Sprintf("等 %d 个文件", len(items)))
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func orDefault(v, d string) string {
	if v == "" {
		return d
	}
	return v
}
