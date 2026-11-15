package core

import (
	"context"
	"encoding/json"
)

// NewDeliverable is what InsertDeliverable stores.
type NewDeliverable struct {
	ProjectID   string
	ThreadID    *string
	Kind        string
	Title       string
	URL         string
	Detail      map[string]any
	PublishedBy string
}

// InsertDeliverable stores a deliverable.
func InsertDeliverable(ctx context.Context, tx *Tx, n NewDeliverable) (Deliverable, error) {
	if n.Detail == nil {
		n.Detail = map[string]any{}
	}
	detail, _ := json.Marshal(n.Detail)
	return scanDeliverable(tx.QueryRow(ctx, `INSERT INTO at_deliverables AS d (id, project_id, thread_id, kind, title, url,
		detail, published_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING `+deliverableCols,
		NewID("dl"), n.ProjectID, n.ThreadID, n.Kind, n.Title, n.URL, detail, n.PublishedBy))
}

// DeliverableByID loads a deliverable of a user.
func DeliverableByID(ctx context.Context, q DB, userID, id string) (Deliverable, error) {
	return scanDeliverable(q.QueryRow(ctx, `SELECT `+deliverableCols+` FROM at_deliverables d
		JOIN at_projects p ON p.id = d.project_id WHERE d.id = $1 AND p.user_id = $2`, id, userID))
}

// PRDeliverableByURL finds the pr deliverable of a thread for one pull request.
func PRDeliverableByURL(ctx context.Context, q DB, threadID, url string) (Deliverable, error) {
	return scanDeliverable(q.QueryRow(ctx, `SELECT `+deliverableCols+` FROM at_deliverables d
		WHERE d.thread_id = $1 AND d.kind = 'pr' AND d.url = $2 ORDER BY d.created_at DESC LIMIT 1`, threadID, url))
}

// DeliverableFilter narrows Deliverables.
type DeliverableFilter struct {
	Kind     string
	ThreadID string
	Q        string
	Before   string
	Limit    int
}

// Deliverables pages a project's deliverables newest first (cursor = id of the last item).
func Deliverables(ctx context.Context, q DB, projectID string, f DeliverableFilter) ([]Deliverable, error) {
	rows, err := q.Query(ctx, `SELECT `+deliverableCols+` FROM at_deliverables d WHERE d.project_id = $1
		AND ($2 = '' OR d.kind = $2) AND ($3 = '' OR d.thread_id = $3)
		AND ($4 = '' OR d.title ILIKE '%' || $4 || '%' OR d.detail->>'summary' ILIKE '%' || $4 || '%')
		AND ($5 = '' OR d.id < $5) ORDER BY d.id DESC LIMIT $6`, projectID, f.Kind, f.ThreadID, f.Q, f.Before, f.Limit)
	return collect(rows, err, scanDeliverable)
}

// UpdateDeliverable changes title/detail.
func UpdateDeliverable(ctx context.Context, tx *Tx, id string, title *string, detail map[string]any) (Deliverable, error) {
	var raw []byte
	if detail != nil {
		raw, _ = json.Marshal(detail)
	}
	return scanDeliverable(tx.QueryRow(ctx, `UPDATE at_deliverables AS d SET title = COALESCE($2, d.title),
		detail = COALESCE($3::jsonb, d.detail), updated_at = now() WHERE d.id = $1 RETURNING `+deliverableCols, id, title, raw))
}

// DeleteDeliverable removes a deliverable.
func DeleteDeliverable(ctx context.Context, tx *Tx, id string) error {
	return Affected(tx.Exec(ctx, `DELETE FROM at_deliverables WHERE id = $1`, id))
}

// DeliverableView renders a deliverable.
func DeliverableViewOf(ctx context.Context, q DB, d Deliverable) (DeliverableView, error) {
	no, err := threadNoOf(ctx, q, d.ThreadID)
	return DeliverableView{ID: d.ID, ProjectID: d.ProjectID, ThreadNo: no, Kind: d.Kind, Title: d.Title, URL: d.URL,
		Detail: d.Detail, PublishedBy: d.PublishedBy, CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt}, err
}

// EmitDeliverable emits a deliverable event.
func (tx *Tx) EmitDeliverable(ctx context.Context, d Deliverable) error {
	v, err := DeliverableViewOf(ctx, tx, d)
	if err != nil {
		return err
	}
	_, err = tx.Emit(ctx, d.ProjectID, d.ThreadID, EvDeliverable, d.PublishedBy, map[string]any{"deliverable": v})
	return err
}

// MergeByID loads a merge queue entry.
func MergeByID(ctx context.Context, q DB, id string) (MergeEntry, error) {
	return scanMerge(q.QueryRow(ctx, `SELECT `+mergeCols+` FROM at_merge_queue WHERE id = $1`, id))
}

// MergeQueue lists a thread's queue (activeOnly = queued/merging/ci_running).
func MergeQueue(ctx context.Context, q DB, threadID string, activeOnly bool) ([]MergeEntry, error) {
	rows, err := q.Query(ctx, `SELECT `+mergeCols+` FROM at_merge_queue WHERE thread_id = $1
		AND (NOT $2 OR status IN ('queued','merging','ci_running')) ORDER BY position`, threadID, activeOnly)
	return collect(rows, err, scanMerge)
}

// ThreadsWithActiveMerges lists threads that have entries to process.
func ThreadsWithActiveMerges(ctx context.Context, q DB) ([]string, error) {
	rows, err := q.Query(ctx, `SELECT DISTINCT thread_id FROM at_merge_queue WHERE status IN ('queued','merging','ci_running')`)
	return collect(rows, err, func(r Scanner) (string, error) {
		var id string
		return id, r.Scan(&id)
	})
}

// InsertMerge enqueues an entry for one repository.
func InsertMerge(ctx context.Context, tx *Tx, threadID string, taskID *string, employee, repo, headBranch string) (MergeEntry, error) {
	return scanMerge(tx.QueryRow(ctx, `INSERT INTO at_merge_queue (id, thread_id, task_id, employee, repo, head_branch)
		VALUES ($1,$2,$3,$4,$5,$6) RETURNING `+mergeCols, NewID("mq"), threadID, taskID, employee, repo, headBranch))
}

// TaskMergesOpen counts the other repositories (not exceptRepo) the task was queued for and that
// have no merged entry of the task yet (cancelled entries do not count).
func TaskMergesOpen(ctx context.Context, q DB, threadID, taskID, exceptRepo string) (int, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT count(DISTINCT q.repo) FROM at_merge_queue q WHERE q.thread_id = $1 AND q.task_id = $2
		AND q.repo <> $3 AND q.status <> 'cancelled' AND NOT EXISTS (SELECT 1 FROM at_merge_queue m
		WHERE m.thread_id = q.thread_id AND m.task_id = q.task_id AND m.repo = q.repo AND m.status = 'merged')`,
		threadID, taskID, exceptRepo).Scan(&n)
	return n, err
}

// UpdateMerge applies column changes and returns the entry.
func UpdateMerge(ctx context.Context, tx *Tx, id string, set map[string]any) (MergeEntry, error) {
	cols := ""
	args := []any{id}
	for col, val := range set {
		args = append(args, val)
		cols += col + " = $" + itoa(len(args)) + ", "
	}
	return scanMerge(tx.QueryRow(ctx, `UPDATE at_merge_queue SET `+cols+`updated_at = now() WHERE id = $1 RETURNING `+mergeCols, args...))
}

// QueuePosition is how many active entries are ahead of the entry (1 = next).
func QueuePosition(ctx context.Context, q DB, m MergeEntry) (int, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT count(*) FROM at_merge_queue WHERE thread_id = $1 AND position <= $2
		AND status IN ('queued','merging','ci_running')`, m.ThreadID, m.Position).Scan(&n)
	return n, err
}

// EmitMerge emits a merge event.
func (tx *Tx) EmitMerge(ctx context.Context, t Thread, m MergeEntry) error {
	_, err := tx.Emit(ctx, t.ProjectID, &t.ID, EvMerge, "", map[string]any{"thread_no": t.No, "entry": MergeViewOf(m, t.No)})
	return err
}

// SaveChecks upserts CI checks of a sha for a queue entry (or a PR head when queueID is nil).
func SaveChecks(ctx context.Context, tx *Tx, projectID string, threadID, queueID *string, sha string, checks []CheckView) error {
	for _, c := range checks {
		if queueID == nil {
			if _, err := tx.Exec(ctx, `DELETE FROM at_ci_runs WHERE sha = $1 AND check_name = $2 AND queue_id IS NULL`,
				sha, c.Name); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `INSERT INTO at_ci_runs (project_id, thread_id, queue_id, sha, check_name, status, conclusion, url, summary)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
			ON CONFLICT (sha, check_name, queue_id) DO UPDATE SET status = EXCLUDED.status, conclusion = EXCLUDED.conclusion,
			url = EXCLUDED.url, summary = EXCLUDED.summary, updated_at = now()`,
			projectID, threadID, queueID, sha, c.Name, c.Status, c.Conclusion, c.URL, c.Summary)
		if err != nil {
			return err
		}
	}
	return nil
}

// LatestChecks returns the checks of the thread's most recent sha.
func LatestChecks(ctx context.Context, q DB, threadID string) (string, []CheckView, error) {
	var sha string
	err := MapErr(q.QueryRow(ctx, `SELECT sha FROM at_ci_runs WHERE thread_id = $1 ORDER BY updated_at DESC LIMIT 1`,
		threadID).Scan(&sha))
	if err == ErrNotFound {
		return "", []CheckView{}, nil
	}
	if err != nil {
		return "", nil, err
	}
	rows, err := q.Query(ctx, `SELECT check_name, status, conclusion, url, summary FROM at_ci_runs
		WHERE thread_id = $1 AND sha = $2 ORDER BY check_name`, threadID, sha)
	checks, err := collect(rows, err, func(r Scanner) (CheckView, error) {
		var c CheckView
		return c, r.Scan(&c.Name, &c.Status, &c.Conclusion, &c.URL, &c.Summary)
	})
	return sha, checks, err
}
