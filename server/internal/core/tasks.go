package core

import (
	"context"
	"encoding/json"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/touwaeriol/agent-teams/server/internal/store"
)

func tasks(ctx context.Context, q DB) *gorm.DB { return q.Gorm(ctx).Model(&Task{}) }

// TaskByID loads a task of a thread conversation.
func TaskByID(ctx context.Context, q DB, conversationID, id string) (Task, error) {
	return take[Task](tasks(ctx, q).Where("conversation_id = ? AND id = ?", conversationID, id))
}

// LockTask loads a task FOR UPDATE.
func LockTask(ctx context.Context, tx *Tx, conversationID, id string) (Task, error) {
	return take[Task](tasks(ctx, tx).Clauses(ForUpdate).Where("conversation_id = ? AND id = ?", conversationID, id))
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

// NewTask is what InsertTask stores.
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
	Status      string
}

// InsertTask allocates t<n> in the thread conversation and inserts the task.
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
	if n.Status == "" {
		n.Status = "pending"
	}
	err = tx.Gorm(ctx).Table("at_tasks").Create(map[string]any{"conversation_id": n.Thread.ID, "id": id,
		"thread_id": n.Thread.ID, "main_task_id": n.MainTaskID, "assignee": n.Assignee, "role": n.Role,
		"claimed_by": n.ClaimedBy, "kind": n.Kind, "instruction": n.Instruction, "depends_on": deps, "status": n.Status,
		"parent": n.Parent, "priority": n.Priority, "created_by": n.CreatedBy}).Error
	if err != nil {
		return Task{}, err
	}
	return TaskByID(ctx, tx, n.Thread.ID, id)
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

// SetTaskStatus updates status and stamps finished_at for final states.
func SetTaskStatus(ctx context.Context, tx *Tx, t Task, status string) (Task, error) {
	finished := gorm.Expr("NULL")
	if status == "done" || status == "failed" || status == "cancelled" {
		finished = gorm.Expr("now()")
	}
	err := taskRow(ctx, tx, t).Updates(map[string]any{"status": status, "updated_at": gorm.Expr("now()"),
		"finished_at": finished}).Error
	if err != nil {
		return t, err
	}
	return TaskByID(ctx, tx, t.ConversationID, t.ID)
}

// UpdateTask applies column changes to a task.
func UpdateTask(ctx context.Context, tx *Tx, t Task, set map[string]any) (Task, error) {
	if err := taskRow(ctx, tx, t).Updates(stamped(set)).Error; err != nil {
		return t, err
	}
	return TaskByID(ctx, tx, t.ConversationID, t.ID)
}

// EmitTask emits a task event.
func (tx *Tx) EmitTask(ctx context.Context, t Task) error {
	th, err := ThreadByID(ctx, tx, t.ConversationID)
	if err != nil {
		return err
	}
	_, err = tx.Emit(ctx, th.ProjectID, &th.ID, EvTask, "", map[string]any{"thread_no": th.No, "task": TaskViewOf(t, th.No)})
	return err
}

// PendingDependents locks the pending tasks of the same thread that depend on task id.
func PendingDependents(ctx context.Context, tx *Tx, conversationID, id string) ([]Task, error) {
	dep, _ := json.Marshal([]string{id})
	return find[Task](tasks(ctx, tx).Clauses(ForUpdate).
		Where("conversation_id = ? AND status = 'pending' AND depends_on @> ?::jsonb", conversationID, string(dep)).
		Order("created_at, substring(id from 2)::int"))
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
