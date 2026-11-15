package core

import (
	"context"
	"encoding/json"
	"strings"
)

// TaskByID loads a task of a thread conversation.
func TaskByID(ctx context.Context, q DB, conversationID, id string) (Task, error) {
	return scanTask(q.QueryRow(ctx, `SELECT `+taskCols+` FROM at_tasks WHERE conversation_id = $1 AND id = $2`, conversationID, id))
}

// LockTask loads a task FOR UPDATE.
func LockTask(ctx context.Context, tx *Tx, conversationID, id string) (Task, error) {
	return scanTask(tx.QueryRow(ctx, `SELECT `+taskCols+` FROM at_tasks WHERE conversation_id = $1 AND id = $2 FOR UPDATE`,
		conversationID, id))
}

// TaskFilter narrows ThreadTasks.
type TaskFilter struct {
	Status []string
	MainID string
}

// ThreadTasks lists a thread's tasks in id order.
func ThreadTasks(ctx context.Context, q DB, threadID string, f TaskFilter) ([]Task, error) {
	rows, err := q.Query(ctx, `SELECT `+taskCols+` FROM at_tasks WHERE thread_id = $1
		AND (COALESCE(cardinality($2::text[]), 0) = 0 OR status = ANY($2)) AND ($3 = '' OR main_task_id = $3)
		ORDER BY substring(id from 2)::int`, threadID, f.Status, f.MainID)
	return collect(rows, err, scanTask)
}

// CurrentTask is the claimed/running task of an employee in a thread.
func CurrentTask(ctx context.Context, q DB, threadID, employee string) (Task, error) {
	return scanTask(q.QueryRow(ctx, `SELECT `+taskCols+` FROM at_tasks WHERE thread_id = $1 AND claimed_by = $2
		AND status IN ('claimed','running') ORDER BY claimed_at DESC LIMIT 1`, threadID, employee))
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
	_, err = tx.Exec(ctx, `INSERT INTO at_tasks (conversation_id, id, thread_id, main_task_id, assignee, role, claimed_by,
		kind, instruction, depends_on, status, output, parent, priority, created_by)
		VALUES ($1,$2,$1,$3,$4,$5,$6,$7,$8,$9,$10,'',$11,$12,$13)`,
		n.Thread.ID, id, n.MainTaskID, n.Assignee, n.Role, n.ClaimedBy, n.Kind, n.Instruction, deps, n.Status, n.Parent,
		n.Priority, n.CreatedBy)
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

// SetTaskStatus updates status and stamps finished_at for final states.
func SetTaskStatus(ctx context.Context, tx *Tx, t Task, status string) (Task, error) {
	_, err := tx.Exec(ctx, `UPDATE at_tasks SET status = $3, updated_at = now(),
		finished_at = CASE WHEN $3 IN ('done','failed','cancelled') THEN now() ELSE NULL END
		WHERE conversation_id = $1 AND id = $2`, t.ConversationID, t.ID, status)
	if err != nil {
		return t, err
	}
	return TaskByID(ctx, tx, t.ConversationID, t.ID)
}

// UpdateTask applies column changes to a task.
func UpdateTask(ctx context.Context, tx *Tx, t Task, set map[string]any) (Task, error) {
	cols := make([]string, 0, len(set))
	args := []any{t.ConversationID, t.ID}
	for col, val := range set {
		args = append(args, val)
		cols = append(cols, col+" = $"+itoa(len(args)))
	}
	_, err := tx.Exec(ctx, `UPDATE at_tasks SET `+strings.Join(cols, ", ")+`, updated_at = now()
		WHERE conversation_id = $1 AND id = $2`, args...)
	if err != nil {
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

// DepsSatisfied reports whether every dependency of the task is done.
func DepsSatisfied(ctx context.Context, q DB, t Task) (bool, error) {
	if len(t.DependsOn) == 0 {
		return true, nil
	}
	var open int
	err := q.QueryRow(ctx, `SELECT count(*) FROM unnest($2::text[]) d(id)
		LEFT JOIN at_tasks t ON t.conversation_id = $1 AND t.id = d.id WHERE t.status IS DISTINCT FROM 'done'`,
		t.ConversationID, t.DependsOn).Scan(&open)
	return open == 0, err
}

// MainTasks lists a thread's plan.
func MainTasks(ctx context.Context, q DB, threadID string) ([]MainTask, error) {
	rows, err := q.Query(ctx, `SELECT `+mainTaskCols+` FROM at_main_tasks WHERE thread_id = $1 ORDER BY pos`, threadID)
	return collect(rows, err, scanMainTask)
}

// MainTaskByTitle finds a plan step by title (exact, then case-insensitive).
func MainTaskByTitle(ctx context.Context, q DB, threadID, title string) (MainTask, error) {
	return scanMainTask(q.QueryRow(ctx, `SELECT `+mainTaskCols+` FROM at_main_tasks WHERE thread_id = $1
		AND (title = $2 OR lower(title) = lower($2) OR id = $2) ORDER BY title = $2 DESC LIMIT 1`, threadID, strings.TrimSpace(title)))
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
	_, err = tx.Exec(ctx, `DELETE FROM at_main_tasks WHERE thread_id = $1 AND NOT (id = ANY($2))`, threadID, keep)
	return err
}

func upsertStep(ctx context.Context, tx *Tx, threadID string, pos int, st PlanStep, byTitle map[string]MainTask) (string, error) {
	status := st.Status
	if status == "" {
		status = "todo"
	}
	if m, ok := byTitle[st.Title]; ok {
		_, err := tx.Exec(ctx, `UPDATE at_main_tasks SET pos = $2, status = $3, auto_done = auto_done AND $3 = 'done',
			updated_at = now() WHERE id = $1`, m.ID, pos, status)
		return m.ID, err
	}
	id := NewID("mt")
	_, err := tx.Exec(ctx, `INSERT INTO at_main_tasks (id, thread_id, pos, title, status) VALUES ($1,$2,$3,$4,$5)`,
		id, threadID, pos, st.Title, status)
	return id, err
}

// SetMainTaskStatus changes a step's status (manual or automatic).
func SetMainTaskStatus(ctx context.Context, tx *Tx, threadID, id, status string, auto bool) error {
	return Affected(tx.Exec(ctx, `UPDATE at_main_tasks SET status = $3, auto_done = $4, updated_at = now()
		WHERE thread_id = $1 AND id = $2`, threadID, id, status, auto && status == "done"))
}

// AutoCompleteMain ticks a plan step when all its tasks are done; true when it changed.
func AutoCompleteMain(ctx context.Context, tx *Tx, threadID, mainID string) (bool, error) {
	tag, err := tx.Exec(ctx, `UPDATE at_main_tasks m SET status = 'done', auto_done = TRUE, updated_at = now()
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
	tag, err = tx.Exec(ctx, `UPDATE at_main_tasks SET status = 'doing', updated_at = now()
		WHERE thread_id = $1 AND id = $2 AND status = 'todo'`, threadID, mainID)
	return tag.RowsAffected() > 0, err
}
