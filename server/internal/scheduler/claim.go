package scheduler

import (
	"context"
	"database/sql"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/orchestrator"
	"github.com/touwaeriol/agent-teams/server/internal/status"
)

func (s *Scheduler) claimOne(ctx context.Context, threadID string) (bool, error) {
	claimed := false
	err := s.core.InTx(ctx, func(tx *core.Tx) error {
		t, err := core.ThreadByID(ctx, tx, threadID)
		if err != nil || (t.Status != "running" && t.Status != "waiting") {
			return ignoreMissing(err)
		}
		tasks, err := pendingTasks(ctx, tx, threadID)
		if err != nil {
			return err
		}
		for _, task := range tasks {
			ok, err := s.tryClaim(ctx, tx, t, task)
			if err != nil || ok {
				claimed = ok
				return err
			}
		}
		return nil
	})
	return claimed, err
}

func ignoreMissing(err error) error {
	if err == core.ErrNotFound {
		return nil
	}
	return err
}

// pendingTasks locks up to 50 claimable tasks of a thread, skipping rows another scheduler holds.
func pendingTasks(ctx context.Context, tx *core.Tx, threadID string) ([]core.Task, error) {
	out := []core.Task{}
	err := tx.Gorm(ctx).Model(&core.Task{}).Clauses(core.SkipLocked).Where("thread_id = ? AND status = 'pending'", threadID).
		Order("priority, created_at, substring(id from 2)::int").Limit(50).Find(&out).Error
	return out, err
}

func (s *Scheduler) tryClaim(ctx context.Context, tx *core.Tx, t core.Thread, task core.Task) (bool, error) {
	ok, err := core.DepsSatisfied(ctx, tx, task)
	if err != nil || !ok {
		return false, err
	}
	member, err := idleMember(ctx, tx, t, task)
	if err == core.ErrNotFound {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, s.claim(ctx, tx, t, task, member)
}

// Named reports whether a task was assigned to an instance rather than a role.
func Named(task core.Task) bool { return task.Assignee != "" && task.Assignee != task.Role }

// idleCandidates are the members free to claim a task of thread @t: the project's members with
// no claimed / running task anywhere in the project (not hidden by a legacy member of their
// name in the thread), and the thread's own legacy members with none in the thread.
const idleCandidates = `e.active AND e.tier = 'member' AND (
	(e.thread_id = @t AND NOT EXISTS (SELECT 1 FROM at_tasks x WHERE x.thread_id = e.thread_id AND x.claimed_by = e.name
		AND x.status IN ('claimed','running')))
	OR (e.thread_id IS NULL AND e.project_id = @p
		AND NOT EXISTS (SELECT 1 FROM at_employees l WHERE l.thread_id = @t AND l.name = e.name)
		AND NOT EXISTS (SELECT 1 FROM at_tasks x JOIN at_threads xt ON xt.id = x.thread_id
			WHERE xt.project_id = e.project_id AND x.claimed_by = e.name AND x.status IN ('claimed','running')
			AND NOT EXISTS (SELECT 1 FROM at_employees l WHERE l.thread_id = x.thread_id AND l.name = x.claimed_by))))`

// idleMember picks the member that claims the task: the named one, else the first idle member
// of the role (project members before the thread's legacy members).
func idleMember(ctx context.Context, tx *core.Tx, t core.Thread, task core.Task) (core.Employee, error) {
	name := ""
	if Named(task) {
		name = task.Assignee
	}
	g := tx.Gorm(ctx).Table("at_employees e").Select("e.*").Clauses(core.SkipLocked).
		Where(idleCandidates, sql.Named("t", t.ID), sql.Named("p", t.ProjectID))
	if name != "" {
		g = g.Where("e.name = ?", name)
	} else {
		g = g.Where("e.role = ?", task.Role)
	}
	var e core.Employee
	err := g.Order("e.thread_id NULLS FIRST, e.instance_no").Take(&e).Error
	return e, core.MapErr(err)
}

// claim marks the task claimed, emits task.claimed and sends the member its brief; a project
// member moves to the task's thread first.
func (s *Scheduler) claim(ctx context.Context, tx *core.Tx, t core.Thread, task core.Task, m core.Employee) error {
	m, err := s.orch.MoveToThread(ctx, tx, m, t.ID)
	if err != nil {
		return err
	}
	task, err = core.UpdateTask(ctx, tx, task, map[string]any{"status": "claimed", "claimed_by": m.Name,
		"claimed_at": time.Now().UTC(), "branch": orchestrator.MemberBranch(t.No, m.Name), "nudges": 0,
		"sync": map[string]core.TaskSync{}, "sync_pending": 0, "sync_round": ""})
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

// startTask syncs the member's worktree before the brief when the daemon can (shared-members.md
// §4.2): the task waits in claimed with sync_pending until every result is in (SyncDone) or
// SyncTimeout passed. Otherwise the brief goes out at once, after the old fetch-only refresh.
func (s *Scheduler) startTask(ctx context.Context, tx *core.Tx, t core.Thread, task core.Task, m core.Employee) error {
	sync, err := s.syncFor(ctx, tx, t, task)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(SyncTimeout).UTC()
	sync.Deadline = &deadline
	round := core.NewID("sr")
	jobs, err := s.orch.QueueSync(ctx, tx, m, sync, map[string]any{"task_id": task.ID, "thread_id": t.ID, "round": round})
	if err != nil {
		return err
	}
	if jobs > 0 {
		_, err = core.UpdateTask(ctx, tx, task, map[string]any{"sync_pending": jobs, "sync_round": round})
		return err
	}
	if err := s.orch.RefreshWorktree(ctx, tx, m); err != nil {
		return err
	}
	return s.sendBrief(ctx, tx, t, task, m)
}

// syncFor is what the member's worktree is brought to before the task: the thread branch, or for
// a review / decision of another member's task the branch under review.
func (s *Scheduler) syncFor(ctx context.Context, tx *core.Tx, t core.Thread, task core.Task) (protocol.GitPrepareSync, error) {
	member := protocol.GitPrepareSync{Mode: protocol.GitPrepareSyncModeMember, Ref: t.Branch}
	if !isGateTask(task) {
		return member, nil
	}
	parent, err := core.TaskByID(ctx, tx, task.ConversationID, task.Parent)
	if err != nil || parent.Branch == "" {
		return member, ignoreMissing(err)
	}
	return protocol.GitPrepareSync{Mode: protocol.GitPrepareSyncModeReview, Ref: parent.Branch}, nil
}

// sendBrief delivers the task brief to the member (after the pre-task sync, when there was one).
func (s *Scheduler) sendBrief(ctx context.Context, tx *core.Tx, t core.Thread, task core.Task, m core.Employee) error {
	text, err := s.taskBrief(ctx, tx, t, task, m)
	if err != nil {
		return err
	}
	_, err = s.orch.Deliver(ctx, tx, m, orchestrator.Delivery{Text: text, Kind: "task", Priority: "next",
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

// finishSync records sync results and sends the brief once nothing is pending.
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
	return s.sendBrief(ctx, tx, t, task, m)
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
	err := s.core.Pool.Gorm(ctx).Model(&core.Task{}).Where("status = 'claimed' AND sync_pending > 0 AND claimed_at < ?",
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
	if len(task.Sync) > 0 {
		sync, err := s.syncFor(ctx, tx, t, task)
		if err != nil {
			return "", err
		}
		syncNote(&b, sync, task)
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
		return fmt.Sprintf("已只读检出被审分支 %s 的最新提交 %s", ref, short(s.HeadSHA))
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
