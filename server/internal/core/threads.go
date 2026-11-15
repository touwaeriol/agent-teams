package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// ThreadByID loads a thread.
func ThreadByID(ctx context.Context, q DB, id string) (Thread, error) {
	return scanThread(q.QueryRow(ctx, `SELECT `+threadCols+` FROM at_threads WHERE id = $1`, id))
}

// ThreadOfUser loads a thread owned by the user (others' read as not found).
func ThreadOfUser(ctx context.Context, q DB, userID, id string) (Thread, error) {
	return scanThread(q.QueryRow(ctx, `SELECT `+threadCols+` FROM at_threads WHERE id = $1 AND user_id = $2`, id, userID))
}

// ThreadByNo loads a project's thread by number.
func ThreadByNo(ctx context.Context, q DB, projectID string, no int) (Thread, error) {
	return scanThread(q.QueryRow(ctx, `SELECT `+threadCols+` FROM at_threads WHERE project_id = $1 AND no = $2`, projectID, no))
}

// LockThread loads a thread FOR UPDATE.
func LockThread(ctx context.Context, tx *Tx, id string) (Thread, error) {
	return scanThread(tx.QueryRow(ctx, `SELECT `+threadCols+` FROM at_threads WHERE id = $1 FOR UPDATE`, id))
}

// Threads lists a project's threads; statuses filters ("" = all).
func Threads(ctx context.Context, q DB, projectID string, statuses []string) ([]Thread, error) {
	rows, err := q.Query(ctx, `SELECT `+threadCols+` FROM at_threads WHERE project_id = $1
		AND (COALESCE(cardinality($2::text[]), 0) = 0 OR status = ANY($2)) ORDER BY no DESC`, projectID, statuses)
	return collect(rows, err, scanThread)
}

// QueuedThreads lists a user's queued threads in creation order.
func QueuedThreads(ctx context.Context, q DB, userID string) ([]Thread, error) {
	rows, err := q.Query(ctx, `SELECT `+threadCols+` FROM at_threads WHERE user_id = $1 AND status = 'queued'
		ORDER BY created_at, no`, userID)
	return collect(rows, err, scanThread)
}

// RunningThreadCount counts the user's threads that hold a running slot.
func RunningThreadCount(ctx context.Context, q DB, userID string) (int, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT count(*) FROM at_threads WHERE user_id = $1 AND status IN ('running','waiting')`,
		userID).Scan(&n)
	return n, err
}

// NewThread is what CreateThread stores.
type NewThread struct {
	ProjectID   string
	UserID      string
	Title       string
	Brief       string
	BaseBranch  string
	AutoApprove bool
	OpenedBy    string
	Roles       []store.Role
}

// CreateThread inserts the thread's conversation (kind thread) and the thread row (status queued).
func CreateThread(ctx context.Context, tx *Tx, n NewThread) (Thread, error) {
	var no int
	if err := tx.QueryRow(ctx, `UPDATE at_projects SET thread_seq = thread_seq + 1, updated_at = now() WHERE id = $1
		RETURNING thread_seq`, n.ProjectID).Scan(&no); err != nil {
		return Thread{}, MapErr(err)
	}
	id := NewHex(6)
	if _, err := tx.Exec(ctx, `INSERT INTO at_conversations (id, user_id, title, project_id, kind) VALUES ($1, $2, $3, $4, 'thread')`,
		id, n.UserID, n.Title, n.ProjectID); err != nil {
		return Thread{}, err
	}
	roles, _ := json.Marshal(n.Roles)
	branch := fmt.Sprintf("thread/%d-%s", no, Slug(n.Title))
	_, err := tx.Exec(ctx, `INSERT INTO at_threads (id, project_id, user_id, no, title, brief, branch, base_branch,
		auto_approve, opened_by, roles_snapshot) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		id, n.ProjectID, n.UserID, no, n.Title, n.Brief, branch, n.BaseBranch, n.AutoApprove, n.OpenedBy, roles)
	if err != nil {
		return Thread{}, err
	}
	return ThreadByID(ctx, tx, id)
}

// Slug turns a title into a branch-safe ASCII slug (never empty).
func Slug(title string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(title) {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if ok {
			b.WriteRune(r)
			dash = false
			continue
		}
		if b.Len() > 0 && !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	s := strings.Trim(b.String(), "-")
	if len(s) > 32 {
		s = strings.Trim(s[:32], "-")
	}
	if s == "" {
		return "work"
	}
	return s
}

// SetThreadStatus changes the status and stamps started/finished; returns the previous status.
func SetThreadStatus(ctx context.Context, tx *Tx, id, status, pauseReason string) (string, error) {
	var prev string
	err := tx.QueryRow(ctx, `UPDATE at_threads t SET status = $2, pause_reason = $3, updated_at = now(),
		started_at = CASE WHEN $2 = 'running' AND t.started_at IS NULL THEN now() ELSE t.started_at END,
		finished_at = CASE WHEN $2 IN ('done','failed','archived') THEN COALESCE(t.finished_at, now()) ELSE t.finished_at END
		FROM at_threads old WHERE t.id = $1 AND old.id = t.id RETURNING old.status`, id, status, pauseReason).Scan(&prev)
	return prev, MapErr(err)
}

// UpdateThread applies column changes.
func UpdateThread(ctx context.Context, tx *Tx, id string, set map[string]any) error {
	parts := make([]string, 0, len(set))
	args := []any{id}
	for col, val := range set {
		args = append(args, val)
		parts = append(parts, fmt.Sprintf("%s = $%d", col, len(args)))
	}
	if len(parts) == 0 {
		return nil
	}
	return Affected(tx.Exec(ctx, `UPDATE at_threads SET `+strings.Join(parts, ", ")+`, updated_at = now() WHERE id = $1`, args...))
}

// ThreadCountsOf computes a thread's badges.
func ThreadCountsOf(ctx context.Context, q DB, t Thread) (ThreadCounts, error) {
	var c ThreadCounts
	err := q.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM at_questions WHERE thread_id = $1 AND status = 'open' AND audience = 'user' AND kind = 'question'),
		(SELECT count(*) FROM at_questions WHERE thread_id = $1 AND status = 'open' AND kind = 'approval'),
		(SELECT count(*) FROM at_tasks WHERE thread_id = $1 AND status <> 'cancelled'),
		(SELECT count(*) FROM at_tasks WHERE thread_id = $1 AND status = 'done'),
		(SELECT count(*) FROM at_main_tasks WHERE thread_id = $1),
		(SELECT count(*) FROM at_main_tasks WHERE thread_id = $1 AND status = 'done')`, t.ID).
		Scan(&c.QuestionsOpen, &c.ApprovalsOpen, &c.TasksTotal, &c.TasksDone, &c.MainTotal, &c.MainDone)
	c.Waiting = c.QuestionsOpen + c.ApprovalsOpen
	if t.Status == "review" {
		c.Waiting++
	}
	return c, err
}

// MainTaskViews lists the plan with per-step task counts.
func MainTaskViews(ctx context.Context, q DB, threadID string) ([]MainTaskView, error) {
	rows, err := q.Query(ctx, `SELECT m.id, m.pos, m.title, m.status, m.auto_done,
		(SELECT count(*) FROM at_tasks t WHERE t.thread_id = m.thread_id AND t.main_task_id = m.id AND t.status <> 'cancelled'),
		(SELECT count(*) FROM at_tasks t WHERE t.thread_id = m.thread_id AND t.main_task_id = m.id AND t.status = 'done')
		FROM at_main_tasks m WHERE m.thread_id = $1 ORDER BY m.pos`, threadID)
	return collect(rows, err, func(r Scanner) (MainTaskView, error) {
		var v MainTaskView
		err := r.Scan(&v.ID, &v.Pos, &v.Title, &v.Status, &v.AutoDone, &v.TasksTotal, &v.TasksDone)
		return v, err
	})
}

// ThreadViewOf builds the full ThreadView.
func (s *Store) ThreadViewOf(ctx context.Context, q DB, t Thread) (ThreadView, error) {
	v := ThreadView{ID: t.ID, ProjectID: t.ProjectID, No: t.No, Title: t.Title, Brief: t.Brief, Status: t.Status,
		PauseReason: t.PauseReason, Branch: t.Branch, BaseBranch: t.BaseBranch, PRNumber: t.PRNumber, PRURL: t.PRURL,
		PRState: t.PRState, Summary: t.Summary, AutoApprove: t.AutoApprove, OpenedBy: t.OpenedBy,
		CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt, StartedAt: t.StartedAt, FinishedAt: t.FinishedAt}
	var err error
	if v.Members, err = s.EmployeeBriefs(ctx, q, t.ProjectID, &t.ID); err != nil {
		return v, err
	}
	if v.MainTasks, err = MainTaskViews(ctx, q, t.ID); err != nil {
		return v, err
	}
	if v.Counts, err = ThreadCountsOf(ctx, q, t); err != nil {
		return v, err
	}
	if v.Repos, err = ThreadRepoViews(ctx, q, t.ID); err != nil {
		return v, err
	}
	if v.LeadLast, err = leadLast(ctx, q, t.ID); err != nil {
		return v, err
	}
	if v.CostUSDEst, err = threadCost(ctx, q, t.ID); err != nil {
		return v, err
	}
	v.QueuePosition, err = queuePosition(ctx, q, t)
	return v, err
}

func leadLast(ctx context.Context, q DB, threadID string) (*LeadLast, error) {
	var l LeadLast
	err := MapErr(q.QueryRow(ctx, `SELECT content, created_at FROM at_messages WHERE conversation_id = $1 AND role = 'leader'
		ORDER BY id DESC LIMIT 1`, threadID).Scan(&l.Text, &l.At))
	if err == ErrNotFound {
		return nil, nil
	}
	return &l, err
}

func threadCost(ctx context.Context, q DB, threadID string) (*float64, error) {
	var cost *float64
	err := q.QueryRow(ctx, `SELECT sum(cost_usd)::float8 FROM at_usage WHERE thread_id = $1`, threadID).Scan(&cost)
	return cost, err
}

func queuePosition(ctx context.Context, q DB, t Thread) (*int, error) {
	if t.Status != "queued" {
		return nil, nil
	}
	var n int
	err := q.QueryRow(ctx, `SELECT count(*) FROM at_threads WHERE user_id = $1 AND status = 'queued'
		AND (created_at, no) < ($2, $3)`, t.UserID, t.CreatedAt, t.No).Scan(&n)
	return &n, err
}

// EmitThread emits the thread's current ThreadView.
func (tx *Tx) EmitThread(ctx context.Context, threadID string) error {
	t, err := ThreadByID(ctx, tx, threadID)
	if err != nil {
		return err
	}
	v, err := tx.store.ThreadViewOf(ctx, tx, t)
	if err != nil {
		return err
	}
	_, err = tx.Emit(ctx, t.ProjectID, &t.ID, EvThread, "", map[string]any{"thread": v})
	return err
}

// EmitMainTasks emits the plan of a thread.
func (tx *Tx) EmitMainTasks(ctx context.Context, t Thread) error {
	views, err := MainTaskViews(ctx, tx, t.ID)
	if err != nil {
		return err
	}
	_, err = tx.Emit(ctx, t.ProjectID, &t.ID, EvMainTask, "", map[string]any{"thread_no": t.No, "main_tasks": views})
	return err
}

// RefreshWaiting recomputes waiting_count and returns the status the thread should move to
// (running ↔ waiting), or "" when it stays.
func RefreshWaiting(ctx context.Context, tx *Tx, threadID string) (string, error) {
	t, err := LockThread(ctx, tx, threadID)
	if err != nil {
		return "", err
	}
	c, err := ThreadCountsOf(ctx, tx, t)
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `UPDATE at_threads SET waiting_count = $2 WHERE id = $1`, threadID, c.Waiting); err != nil {
		return "", err
	}
	open := c.QuestionsOpen + c.ApprovalsOpen
	switch {
	case t.Status == "running" && open > 0:
		return "waiting", nil
	case t.Status == "waiting" && open == 0:
		return "running", nil
	}
	return "", nil
}
