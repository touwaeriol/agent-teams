package scheduler

import (
	"context"
	"encoding/json"
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

func idleMember(ctx context.Context, tx *core.Tx, t core.Thread, task core.Task) (core.Employee, error) {
	name := ""
	if Named(task) {
		name = task.Assignee
	}
	g := tx.Gorm(ctx).Table("at_employees e").Select("e.*").Clauses(core.SkipLocked).
		Where("e.thread_id = ? AND e.active AND e.tier = 'member'", t.ID).
		Where(`NOT EXISTS (SELECT 1 FROM at_tasks x WHERE x.thread_id = e.thread_id AND x.claimed_by = e.name
			AND x.status IN ('claimed','running'))`)
	if name != "" {
		g = g.Where("e.name = ?", name)
	} else {
		g = g.Where("e.role = ?", task.Role)
	}
	var e core.Employee
	err := g.Order("e.instance_no").Take(&e).Error
	return e, core.MapErr(err)
}

// claim marks the task claimed, emits task.claimed and sends the member its brief.
func (s *Scheduler) claim(ctx context.Context, tx *core.Tx, t core.Thread, task core.Task, m core.Employee) error {
	task, err := core.UpdateTask(ctx, tx, task, map[string]any{"status": "claimed", "claimed_by": m.Name,
		"claimed_at": time.Now().UTC(), "branch": orchestrator.MemberBranch(t.No, m.Name), "nudges": 0})
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
	sync, err := s.syncFor(ctx, tx, t, task)
	if err != nil {
		return err
	}
	jobs, err := s.orch.QueueSync(ctx, tx, m, sync, task.ID)
	if err != nil || jobs == 0 {
		if err != nil {
			return err
		}
		return s.sendBrief(ctx, tx, t, task, m)
	}
	_, err = core.UpdateTask(ctx, tx, task, map[string]any{"sync_pending": jobs, "sync": "{}"})
	return err // the brief goes out once the sync results are in (SyncDone) or SyncTimeout passed
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

// SyncDone records one repository's pre-task sync result; the last one sends the brief. A result
// for a task that is no longer waiting (re-claimed, cancelled, timed out) is ignored.
func (s *Scheduler) SyncDone(ctx context.Context, tx *core.Tx, meta map[string]any, r *protocol.GitResult) error {
	task, err := core.LockTask(ctx, tx, str(meta["thread_id"]), str(meta["task_id"]))
	if err != nil || task.Status != "claimed" || task.SyncPending == 0 {
		return ignoreMissing(err)
	}
	e, err := core.EmployeeByID(ctx, tx, str(meta["employee_id"]))
	if err != nil || e.Name != task.ClaimedBy {
		return ignoreMissing(err)
	}
	synced := task.Sync
	if synced == nil {
		synced = map[string]core.TaskSync{}
	}
	synced[str(meta["repo"])] = syncOf(r)
	task, err = core.UpdateTask(ctx, tx, task, map[string]any{"sync": jsonOf(synced), "sync_pending": task.SyncPending - 1})
	if err != nil || task.SyncPending > 0 {
		return err
	}
	return s.briefOf(ctx, tx, task, e)
}

func syncOf(r *protocol.GitResult) core.TaskSync {
	if !r.Ok || r.Sync == nil {
		return core.TaskSync{Error: orDefault(deref(r.Error), "同步失败")}
	}
	return core.TaskSync{Action: string(r.Sync.Action), HeadSHA: r.Sync.HeadSHA, Conflicts: r.Sync.Conflicts}
}

func (s *Scheduler) briefOf(ctx context.Context, tx *core.Tx, task core.Task, e core.Employee) error {
	t, err := core.ThreadByID(ctx, tx, task.ConversationID)
	if err != nil {
		return err
	}
	return s.sendBrief(ctx, tx, t, task, e)
}

// unreported names the repositories whose sync result never came back.
const unreported = "其余仓库"

// SyncTimeout is how long a brief waits for the pre-task sync before it goes out unsynced.
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
			if err != nil || task.Status != "claimed" || task.SyncPending == 0 {
				return ignoreMissing(err)
			}
			synced := task.Sync
			if synced == nil {
				synced = map[string]core.TaskSync{}
			}
			synced[unreported] = core.TaskSync{Error: "超时"}
			if task, err = core.UpdateTask(ctx, tx, task, map[string]any{"sync_pending": 0, "sync": jsonOf(synced)}); err != nil {
				return err
			}
			t, err := core.ThreadByID(ctx, tx, task.ConversationID)
			if err != nil {
				return err
			}
			e, err := core.EmployeeByName(ctx, tx, t.ProjectID, &t.ID, task.ClaimedBy)
			if err != nil {
				return ignoreMissing(err)
			}
			return s.sendBrief(ctx, tx, t, task, e)
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
	syncNote(&b, t, task)
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
			b.WriteString("\n依赖任务（已合进讨论串分支）：\n")
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

// gateNotes adds the review / decision verdicts on the dependency.
func gateNotes(ctx context.Context, tx *core.Tx, b *strings.Builder, d core.Task) error {
	var gates []core.Task
	err := tx.Gorm(ctx).Model(&core.Task{}).Where("conversation_id = ? AND parent = ? AND kind IN ('review','decision') AND status IN ('done','failed')",
		d.ConversationID, d.ID).Order("finished_at").Find(&gates).Error
	for _, g := range gates {
		verdict := map[bool]string{true: "通过", false: "打回"}[g.Status == "done"]
		fmt.Fprintf(b, "  %s（%s · %s）：%s\n", map[string]string{"review": "审查", "decision": "决策"}[g.Kind], g.ClaimedBy, verdict, clip(g.Summary, 800))
	}
	return err
}

// syncNote tells the member what the pre-task sync did to the worktree.
func syncNote(b *strings.Builder, t core.Thread, task core.Task) {
	if len(task.Sync) == 0 {
		return
	}
	b.WriteString("\n工作目录：\n")
	for _, repo := range slices.Sorted(maps.Keys(task.Sync)) {
		fmt.Fprintf(b, "- %s：%s\n", repo, syncLine(t, task.Sync[repo]))
	}
}

func syncLine(t core.Thread, s core.TaskSync) string {
	switch s.Action {
	case "reset", "current":
		return fmt.Sprintf("已同步到讨论串分支 %s（%s），包含已合并的依赖任务的改动", t.Branch, short(s.HeadSHA))
	case "merged":
		return fmt.Sprintf("已把讨论串分支 %s 合进你的分支（%s），你还没合并的提交都保留", t.Branch, short(s.HeadSHA))
	case "checkout":
		return fmt.Sprintf("已只读检出被审分支的最新提交 %s", short(s.HeadSHA))
	case "conflict":
		return fmt.Sprintf("同步讨论串分支 %s 时和你未合并的提交冲突，已中止：%s。先 git merge origin/%s 解决冲突再开工",
			t.Branch, strings.Join(s.Conflicts, "、"), t.Branch)
	case "dirty":
		return fmt.Sprintf("有未提交的改动，没有同步。先提交或处理掉它们，再 git merge origin/%s", t.Branch)
	}
	return fmt.Sprintf("没能自动同步（%s）。开工前先 git fetch，再 git merge origin/%s", orDefault(s.Error, "超时"), t.Branch)
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

// jsonOf encodes a value for a JSONB column update.
func jsonOf(v any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}
