package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// ErrTaskDone refuses moving a done task to another status: done is final.
var ErrTaskDone = errors.New("任务已完成，done 是终态，不能再改状态")

func tasks(ctx context.Context, q DB) *gorm.DB { return q.Gorm(ctx).Model(&Task{}) }

// TaskByID loads a task of a thread conversation.
func TaskByID(ctx context.Context, q DB, conversationID, id string) (Task, error) {
	return take[Task](tasks(ctx, q).Where("conversation_id = ? AND id = ?", conversationID, id))
}

// LockTask loads a task FOR UPDATE.
func LockTask(ctx context.Context, tx *Tx, conversationID, id string) (Task, error) {
	return take[Task](tasks(ctx, tx).Clauses(ForUpdate).Where("conversation_id = ? AND id = ?", conversationID, id))
}

// SetTaskProgress keeps the latest report_progress of the employee's current task (if any).
func SetTaskProgress(ctx context.Context, tx *Tx, e Employee, text string) error {
	task, err := CurrentTaskOf(ctx, tx, e)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if r := []rune(text); len(r) > 2000 {
		text = string(r[:2000])
	}
	return taskRow(ctx, tx, task).Update("progress", text).Error
}

// LockTaskByCompact loads FOR UPDATE the claimed task that waits for session.compacted of compactID.
func LockTaskByCompact(ctx context.Context, tx *Tx, compactID string) (Task, error) {
	return take[Task](tasks(ctx, tx).Clauses(ForUpdate).Where("compact_id = ? AND status = 'claimed'", compactID))
}

// TryLockTask loads a task FOR UPDATE SKIP LOCKED (ErrNotFound when another transaction holds it).
func TryLockTask(ctx context.Context, tx *Tx, conversationID, id string) (Task, error) {
	return take[Task](tasks(ctx, tx).Clauses(SkipLocked).Where("conversation_id = ? AND id = ?", conversationID, id))
}

// TaskFilter narrows ThreadTasks.
type TaskFilter struct {
	Status []string
	MainID string
}

// ThreadTasks lists a thread's tasks in id order.
func ThreadTasks(ctx context.Context, q DB, threadID string, f TaskFilter) ([]Task, error) {
	g := tasks(ctx, q).Where("thread_id = ?", threadID)
	if len(f.Status) > 0 {
		g = g.Where("status IN ?", f.Status)
	}
	if f.MainID != "" {
		g = g.Where("main_task_id = ?", f.MainID)
	}
	return find[Task](g.Order("substring(id from 2)::int"))
}

// CurrentTask is the claimed/running task of an employee in a thread.
func CurrentTask(ctx context.Context, q DB, threadID, employee string) (Task, error) {
	return take[Task](tasks(ctx, q).Where("thread_id = ? AND claimed_by = ? AND status IN ('claimed','running')",
		threadID, employee).Order("claimed_at DESC"))
}

// NewTask is what InsertTask stores (a new task is always pending).
type NewTask struct {
	Thread      Thread
	MainTaskID  *string
	Assignee    string
	Role        string
	ClaimedBy   string
	Kind        string
	Instruction string
	DependsOn   []string
	Parent      string
	Priority    int
	CreatedBy   string
}

// InsertTask allocates t<n> in the thread conversation and inserts the task as pending.
func InsertTask(ctx context.Context, tx *Tx, n NewTask) (Task, error) {
	id, err := nextLocalID(ctx, tx, "at_tasks", "t", n.Thread.ID)
	if err != nil {
		return Task{}, err
	}
	deps, _ := json.Marshal(nonNilStrings(n.DependsOn))
	if n.Kind == "" {
		n.Kind = "work"
	}
	if n.Priority == 0 {
		n.Priority = 100
	}
	err = tx.Gorm(ctx).Table("at_tasks").Create(map[string]any{"conversation_id": n.Thread.ID, "id": id,
		"thread_id": n.Thread.ID, "main_task_id": n.MainTaskID, "assignee": n.Assignee, "role": n.Role,
		"claimed_by": n.ClaimedBy, "kind": n.Kind, "instruction": n.Instruction, "depends_on": deps, "status": "pending",
		"parent": n.Parent, "priority": n.Priority, "created_by": n.CreatedBy}).Error
	if err != nil {
		return Task{}, err
	}
	if err := stampReadyTask(ctx, tx, n.Thread.ID, id); err != nil {
		return Task{}, err
	}
	tx.settleAfter(n.Thread.ID)
	return TaskByID(ctx, tx, n.Thread.ID, id)
}

// stampReadyTask records that a pending task is claimable from now when it has no dependencies
// or they are all done already (NULL otherwise: MarkReady stamps it when the last one finishes).
// A dependency done by a transaction that has not committed yet is seen as not done; the
// settling after the commit (settleAfter) catches that.
func stampReadyTask(ctx context.Context, tx *Tx, conversationID, id string) error {
	_, err := store.Exec(ctx, tx, `UPDATE at_tasks t SET ready_at = CASE WHEN `+depsDone+` THEN now() END
		WHERE t.conversation_id = $1 AND t.id = $2 AND t.status = 'pending'`, conversationID, id)
	return err
}

// WithAttemptReset adds to set the clearing of a task's per-attempt fields: the progress its
// anchor quotes and the long-task metrics (shared-members.md §5.2). A requeued or (re)claimed
// task starts them afresh.
func WithAttemptReset(set map[string]any) map[string]any {
	out := map[string]any{"progress": "", "compactions": 0, "ctx_start": nil, "ctx_end": nil, "split_hinted": false}
	maps.Copy(out, set)
	return out
}

// RequeueTask puts a task back into the queue (status pending plus set): it is claimable, and
// waits for the starvation rule, from now on (shared-members.md §4.1); with a dependency not
// done yet (a blocked task retried) it waits for MarkReady like a new one. A done task is final
// (ErrTaskDone).
func RequeueTask(ctx context.Context, tx *Tx, t Task, set map[string]any) (Task, error) {
	if _, err := lockStatus(ctx, tx, t, "pending"); err != nil {
		return t, err
	}
	set = WithAttemptReset(set)
	set["status"] = "pending"
	if err := taskRow(ctx, tx, t).Updates(stamped(set)).Error; err != nil {
		return t, err
	}
	if err := stampReadyTask(ctx, tx, t.ConversationID, t.ID); err != nil {
		return t, err
	}
	tx.settleAfter(t.ConversationID) // a dependency done meanwhile; it may have left a final state
	return TaskByID(ctx, tx, t.ConversationID, t.ID)
}

// lockStatus locks a task row and returns its current status; moving a done task to another
// status is refused (ErrTaskDone): done is final, which keeps a ready task's dependencies done.
func lockStatus(ctx context.Context, tx *Tx, t Task, to string) (string, error) {
	var status string
	err := store.QueryRow(ctx, tx, `SELECT status FROM at_tasks WHERE conversation_id = $1 AND id = $2 FOR UPDATE`,
		t.ConversationID, t.ID).Scan(&status)
	if err != nil {
		return status, MapErr(err)
	}
	if status == "done" && to != "done" {
		return status, fmt.Errorf("%w：%s → %s", ErrTaskDone, t.ID, to)
	}
	return status, nil
}

// statusMoved keeps what depends on a task's status (shared-members.md §4.1). A task that became
// done stamps ready_at on those of its direct dependents whose last open dependency it was; a
// task that entered or left a final state changes the downstream counts of its ancestors. Both
// run without waiting for other task rows inside the status transaction (MarkReady skips rows
// another transaction holds), so a status change never waits on a related task's lock: the
// settling after the commit (settleAfter) stamps what was skipped or not visible yet and
// recounts downstream.
func statusMoved(ctx context.Context, tx *Tx, t Task, from, to string) error {
	if finalStatus(from) != finalStatus(to) {
		tx.settleAfter(t.ConversationID)
	}
	if to == "done" && from != "done" {
		return MarkReady(ctx, tx, t.ConversationID, t.ID)
	}
	return nil
}

func nonNilStrings(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// taskRow selects one task for a write.
func taskRow(ctx context.Context, tx *Tx, t Task) *gorm.DB {
	return tx.Gorm(ctx).Table("at_tasks").Where("conversation_id = ? AND id = ?", t.ConversationID, t.ID)
}

// SetTaskStatus updates status and stamps finished_at for final states; dependents' readiness
// and the downstream counts follow (statusMoved). A done task is final (ErrTaskDone).
func SetTaskStatus(ctx context.Context, tx *Tx, t Task, status string) (Task, error) {
	old, err := lockStatus(ctx, tx, t, status)
	if err != nil {
		return t, err
	}
	finished := gorm.Expr("NULL")
	if finalStatus(status) {
		finished = gorm.Expr("now()")
	}
	err = taskRow(ctx, tx, t).Updates(map[string]any{"status": status, "updated_at": gorm.Expr("now()"),
		"finished_at": finished}).Error
	if err != nil {
		return t, err
	}
	if err := statusMoved(ctx, tx, t, old, status); err != nil {
		return t, err
	}
	return TaskByID(ctx, tx, t.ConversationID, t.ID)
}

// UpdateTask applies column changes to a task. A status change goes through SetTaskStatus or
// RequeueTask (they keep readiness and downstream counts); claiming (pending → claimed) is the
// one status write here, and it changes neither.
func UpdateTask(ctx context.Context, tx *Tx, t Task, set map[string]any) (Task, error) {
	if err := taskRow(ctx, tx, t).Updates(stamped(set)).Error; err != nil {
		return t, err
	}
	return TaskByID(ctx, tx, t.ConversationID, t.ID)
}

// EmitTask emits a task event with the thread's counts after the change (tasks_open / tasks_done
// move with it), so lists that only hold ThreadViews stay current without a thread event.
func (tx *Tx) EmitTask(ctx context.Context, t Task) error {
	th, err := ThreadByID(ctx, tx, t.ConversationID)
	if err != nil {
		return err
	}
	counts, err := ThreadCountsOf(ctx, tx, th)
	if err != nil {
		return err
	}
	_, err = tx.Emit(ctx, th.ProjectID, &th.ID, EvTask, "", map[string]any{"thread_no": th.No, "task": TaskViewOf(t, th.No), "counts": counts})
	return err
}

// PendingDependents locks the pending tasks of the same thread that depend on task id.
func PendingDependents(ctx context.Context, tx *Tx, conversationID, id string) ([]Task, error) {
	dep, _ := json.Marshal([]string{id})
	return find[Task](tasks(ctx, tx).Clauses(ForUpdate).
		Where("conversation_id = ? AND status = 'pending' AND depends_on @> ?::jsonb", conversationID, string(dep)).
		Order("created_at, substring(id from 2)::int"))
}

// GateVerdicts lists the finished review / decision tasks on a task, oldest first.
func GateVerdicts(ctx context.Context, q DB, conversationID, taskID string) ([]Task, error) {
	return find[Task](tasks(ctx, q).Where("conversation_id = ? AND parent = ? AND kind IN ('review','decision') AND status IN ('done','failed')",
		conversationID, taskID).Order("finished_at"))
}

// OpenDependents lists the unfinished tasks of the same thread that depend on task id.
func OpenDependents(ctx context.Context, q DB, conversationID, id string) ([]string, error) {
	dep, _ := json.Marshal([]string{id})
	var ids []string
	err := tasks(ctx, q).Where("conversation_id = ? AND status NOT IN ('done','failed','cancelled') AND depends_on @> ?::jsonb",
		conversationID, string(dep)).Order("created_at, substring(id from 2)::int").Pluck("id", &ids).Error
	return ids, err
}

// DepsSatisfied reports whether every dependency of the task is done.
func DepsSatisfied(ctx context.Context, q DB, t Task) (bool, error) {
	if len(t.DependsOn) == 0 {
		return true, nil
	}
	var open int
	err := store.QueryRow(ctx, q, `SELECT count(*) FROM unnest($2::text[]) d(id)
		LEFT JOIN at_tasks t ON t.conversation_id = $1 AND t.id = d.id WHERE t.status IS DISTINCT FROM 'done'`,
		t.ConversationID, t.DependsOn).Scan(&open)
	return open == 0, err
}

// MainTasks lists a thread's plan.
func MainTasks(ctx context.Context, q DB, threadID string) ([]MainTask, error) {
	return find[MainTask](q.Gorm(ctx).Where("thread_id = ?", threadID).Order("pos"))
}

// MainTaskByTitle finds a plan step by title (exact, then case-insensitive).
func MainTaskByTitle(ctx context.Context, q DB, threadID, title string) (MainTask, error) {
	title = strings.TrimSpace(title)
	return take[MainTask](q.Gorm(ctx).Where("thread_id = ? AND (title = ? OR lower(title) = lower(?) OR id = ?)",
		threadID, title, title, title).Order(clause.OrderBy{Expression: clause.Expr{SQL: "title = ? DESC", Vars: []any{title}}}))
}

// PlanStep is one step of update_plan.
type PlanStep struct {
	Title  string
	Status string
}

// ReplacePlan stores the lead's plan, keeping ids of steps whose title is unchanged.
func ReplacePlan(ctx context.Context, tx *Tx, threadID string, steps []PlanStep) error {
	old, err := MainTasks(ctx, tx, threadID)
	if err != nil {
		return err
	}
	byTitle := map[string]MainTask{}
	for _, m := range old {
		byTitle[m.Title] = m
	}
	keep := []string{}
	for i, st := range steps {
		id, err := upsertStep(ctx, tx, threadID, i, st, byTitle)
		if err != nil {
			return err
		}
		keep = append(keep, id)
	}
	return tx.Gorm(ctx).Where("thread_id = ? AND NOT (id = ANY(?))", threadID, store.TextArray(keep)).
		Delete(&MainTask{}).Error
}

func upsertStep(ctx context.Context, tx *Tx, threadID string, pos int, st PlanStep, byTitle map[string]MainTask) (string, error) {
	status := st.Status
	if status == "" {
		status = "todo"
	}
	if m, ok := byTitle[st.Title]; ok {
		_, err := store.Exec(ctx, tx, `UPDATE at_main_tasks SET pos = $2, status = $3, auto_done = auto_done AND $3 = 'done',
			updated_at = now() WHERE id = $1`, m.ID, pos, status)
		return m.ID, err
	}
	id := NewID("mt")
	err := tx.Gorm(ctx).Table("at_main_tasks").Create(map[string]any{"id": id, "thread_id": threadID, "pos": pos,
		"title": st.Title, "status": status}).Error
	return id, err
}

// SetMainTaskStatus changes a step's status (manual or automatic).
func SetMainTaskStatus(ctx context.Context, tx *Tx, threadID, id, status string, auto bool) error {
	return Changed(tx.Gorm(ctx).Table("at_main_tasks").Where("thread_id = ? AND id = ?", threadID, id).
		Updates(map[string]any{"status": status, "auto_done": auto && status == "done", "updated_at": gorm.Expr("now()")}))
}

// AutoCompleteMain ticks a plan step when all its tasks are done; true when it changed.
func AutoCompleteMain(ctx context.Context, tx *Tx, threadID, mainID string) (bool, error) {
	tag, err := store.Exec(ctx, tx, `UPDATE at_main_tasks m SET status = 'done', auto_done = TRUE, updated_at = now()
		WHERE m.thread_id = $1 AND m.id = $2 AND m.status <> 'done'
		AND EXISTS (SELECT 1 FROM at_tasks t WHERE t.thread_id = $1 AND t.main_task_id = $2 AND t.status <> 'cancelled')
		AND NOT EXISTS (SELECT 1 FROM at_tasks t WHERE t.thread_id = $1 AND t.main_task_id = $2
			AND t.status NOT IN ('done','cancelled'))`, threadID, mainID)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() > 0 {
		return true, nil
	}
	tag, err = store.Exec(ctx, tx, `UPDATE at_main_tasks SET status = 'doing', updated_at = now()
		WHERE thread_id = $1 AND id = $2 AND status = 'todo'`, threadID, mainID)
	return tag.RowsAffected() > 0, err
}
