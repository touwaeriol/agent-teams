package core

import (
	"context"
	"encoding/json"

	"gorm.io/gorm"

	"github.com/touwaeriol/agent-teams/server/internal/store"
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
	return rawOne[Deliverable](ctx, tx, `INSERT INTO at_deliverables AS d (id, project_id, thread_id, kind, title, url,
		detail, published_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING `+deliverableCols,
		NewID("dl"), n.ProjectID, n.ThreadID, n.Kind, n.Title, n.URL, detail, n.PublishedBy)
}

// DeliverableByID loads a deliverable of a user.
func DeliverableByID(ctx context.Context, q DB, userID, id string) (Deliverable, error) {
	return take[Deliverable](deliverables(ctx, q).Joins("JOIN at_projects p ON p.id = d.project_id").
		Where("d.id = ? AND p.user_id = ?", id, userID))
}

// PRDeliverableByURL finds the pr deliverable of a thread for one pull request.
func PRDeliverableByURL(ctx context.Context, q DB, threadID, url string) (Deliverable, error) {
	return take[Deliverable](deliverables(ctx, q).Where("d.thread_id = ? AND d.kind = 'pr' AND d.url = ?", threadID, url).
		Order("d.created_at DESC"))
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
	g := deliverables(ctx, q).Where("d.project_id = ?", projectID)
	if f.Kind != "" {
		g = g.Where("d.kind = ?", f.Kind)
	}
	if f.ThreadID != "" {
		g = g.Where("d.thread_id = ?", f.ThreadID)
	}
	if f.Q != "" {
		g = g.Where("d.title ILIKE '%' || ? || '%' OR d.detail->>'summary' ILIKE '%' || ? || '%'", f.Q, f.Q)
	}
	if f.Before != "" {
		g = g.Where("d.id < ?", f.Before)
	}
	return find[Deliverable](g.Order("d.id DESC").Limit(f.Limit))
}

// UpdateDeliverable changes title/detail.
func UpdateDeliverable(ctx context.Context, tx *Tx, id string, title *string, detail map[string]any) (Deliverable, error) {
	var raw []byte
	if detail != nil {
		raw, _ = json.Marshal(detail)
	}
	return rawOne[Deliverable](ctx, tx, `UPDATE at_deliverables AS d SET title = COALESCE($2, d.title),
		detail = COALESCE($3::jsonb, d.detail), updated_at = now() WHERE d.id = $1 RETURNING `+deliverableCols, id, title, raw)
}

// DeleteDeliverable removes a deliverable.
func DeleteDeliverable(ctx context.Context, tx *Tx, id string) error {
	return Changed(tx.Gorm(ctx).Where("id = ?", id).Delete(&Deliverable{}))
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

// activeMerge selects queue entries still to process.
const activeMerge = "status IN ('queued','merging','ci_running')"

func deliverables(ctx context.Context, q DB) *gorm.DB {
	return q.Gorm(ctx).Table("at_deliverables d").Select("d.*")
}

// MergeByID loads a merge queue entry.
func MergeByID(ctx context.Context, q DB, id string) (MergeEntry, error) {
	return take[MergeEntry](q.Gorm(ctx).Where("id = ?", id))
}

// MergeQueue lists a thread's queue (activeOnly = queued/merging/ci_running).
func MergeQueue(ctx context.Context, q DB, threadID string, activeOnly bool) ([]MergeEntry, error) {
	g := q.Gorm(ctx).Where("thread_id = ?", threadID)
	if activeOnly {
		g = g.Where(activeMerge)
	}
	return find[MergeEntry](g.Order("position"))
}

// ThreadsWithActiveMerges lists threads that have entries to process.
func ThreadsWithActiveMerges(ctx context.Context, q DB) ([]string, error) {
	out := []string{}
	err := q.Gorm(ctx).Model(&MergeEntry{}).Where(activeMerge).Distinct().Pluck("thread_id", &out).Error
	return out, err
}

// InsertMerge enqueues an entry for one repository.
func InsertMerge(ctx context.Context, tx *Tx, threadID string, taskID *string, employee, repo, headBranch string) (MergeEntry, error) {
	return rawOne[MergeEntry](ctx, tx, `INSERT INTO at_merge_queue (id, thread_id, task_id, employee, repo, head_branch)
		VALUES ($1,$2,$3,$4,$5,$6) RETURNING `+mergeCols, NewID("mq"), threadID, taskID, employee, repo, headBranch)
}

// TaskMergesOpen counts the other repositories (not exceptRepo) the task was queued for and that
// have no merged entry of the task yet (cancelled entries do not count).
func TaskMergesOpen(ctx context.Context, q DB, threadID, taskID, exceptRepo string) (int, error) {
	var n int
	err := store.QueryRow(ctx, q, `SELECT count(DISTINCT q.repo) FROM at_merge_queue q WHERE q.thread_id = $1 AND q.task_id = $2
		AND q.repo <> $3 AND q.status <> 'cancelled' AND NOT EXISTS (SELECT 1 FROM at_merge_queue m
		WHERE m.thread_id = q.thread_id AND m.task_id = q.task_id AND m.repo = q.repo AND m.status = 'merged')`,
		threadID, taskID, exceptRepo).Scan(&n)
	return n, err
}

// UpdateMerge applies column changes and returns the entry.
func UpdateMerge(ctx context.Context, tx *Tx, id string, set map[string]any) (MergeEntry, error) {
	if err := Changed(tx.Gorm(ctx).Table("at_merge_queue").Where("id = ?", id).Updates(stamped(set))); err != nil {
		return MergeEntry{}, err
	}
	return MergeByID(ctx, tx, id)
}

// QueuePosition is how many active entries are ahead of the entry (1 = next).
func QueuePosition(ctx context.Context, q DB, m MergeEntry) (int, error) {
	return count(q.Gorm(ctx).Model(&MergeEntry{}).Where("thread_id = ? AND position <= ?", m.ThreadID, m.Position).
		Where(activeMerge))
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
			if _, err := store.Exec(ctx, tx, `DELETE FROM at_ci_runs WHERE sha = $1 AND check_name = $2 AND queue_id IS NULL`,
				sha, c.Name); err != nil {
				return err
			}
		}
		_, err := store.Exec(ctx, tx, `INSERT INTO at_ci_runs (project_id, thread_id, queue_id, sha, check_name, status, conclusion, url, summary)
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
	latest, err := take[struct{ Sha string }](q.Gorm(ctx).Table("at_ci_runs").Select("sha").Where("thread_id = ?", threadID).
		Order("updated_at DESC"))
	if err == ErrNotFound {
		return "", []CheckView{}, nil
	}
	if err != nil {
		return "", nil, err
	}
	checks, err := find[CheckView](q.Gorm(ctx).Table("at_ci_runs").
		Select("check_name AS name, status, conclusion, url, summary").
		Where("thread_id = ? AND sha = ?", threadID, latest.Sha).Order("check_name"))
	return latest.Sha, checks, err
}
