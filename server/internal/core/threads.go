package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/touwaeriol/agent-teams/server/internal/store"
)

func threads(ctx context.Context, q DB) *gorm.DB { return q.Gorm(ctx).Model(&Thread{}) }

// ThreadByID loads a thread.
func ThreadByID(ctx context.Context, q DB, id string) (Thread, error) {
	return take[Thread](threads(ctx, q).Where("id = ?", id))
}

// ThreadOfUser loads a thread owned by the user (others' read as not found).
func ThreadOfUser(ctx context.Context, q DB, userID, id string) (Thread, error) {
	return take[Thread](threads(ctx, q).Where("id = ? AND user_id = ?", id, userID))
}

// ThreadByNo loads a project's thread by number.
func ThreadByNo(ctx context.Context, q DB, projectID string, no int) (Thread, error) {
	return take[Thread](threads(ctx, q).Where("project_id = ? AND no = ?", projectID, no))
}

// LockThread loads a thread FOR UPDATE.
func LockThread(ctx context.Context, tx *Tx, id string) (Thread, error) {
	return take[Thread](threads(ctx, tx).Clauses(ForUpdate).Where("id = ?", id))
}

// Threads lists a project's threads, pinned ones first (newest pin first), then newest
// first; statuses filters ("" = all).
func Threads(ctx context.Context, q DB, projectID string, statuses []string) ([]Thread, error) {
	g := threads(ctx, q).Where("project_id = ?", projectID)
	if len(statuses) > 0 {
		g = g.Where("status IN ?", statuses)
	}
	return find[Thread](g.Order("pinned_at DESC NULLS LAST, no DESC"))
}

// QueuedThreads lists a user's queued threads in creation order.
func QueuedThreads(ctx context.Context, q DB, userID string) ([]Thread, error) {
	return find[Thread](threads(ctx, q).Where("user_id = ? AND status = 'queued'", userID).Order("created_at, no"))
}

// QueuedThreadUsers lists the users that have queued threads.
func QueuedThreadUsers(ctx context.Context, q DB) ([]string, error) {
	var out []string
	err := threads(ctx, q).Where("status = 'queued'").Distinct().Pluck("user_id", &out).Error
	return out, err
}

// RunningThreadCount counts the user's threads that hold a running slot.
func RunningThreadCount(ctx context.Context, q DB, userID string) (int, error) {
	return count(threads(ctx, q).Where("user_id = ? AND status IN ('running','waiting')", userID))
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
	// OnlyRoles are the member roles the lead may assign to (empty: all, stored as NULL).
	OnlyRoles []string
	// SourceMessageID is the user's message the thread was opened for (nil: none).
	SourceMessageID *int64
	// ForwardedBrief: the brief is the user's forwarded messages (forward into a new thread).
	ForwardedBrief bool
}

// CreateThread inserts the thread's conversation (kind thread) and the thread row (status queued).
func CreateThread(ctx context.Context, tx *Tx, n NewThread) (Thread, error) {
	var no int
	if err := store.QueryRow(ctx, tx, `UPDATE at_projects SET thread_seq = thread_seq + 1, updated_at = now() WHERE id = $1
		RETURNING thread_seq`, n.ProjectID).Scan(&no); err != nil {
		return Thread{}, MapErr(err)
	}
	id := NewHex(6)
	err := tx.Gorm(ctx).Table("at_conversations").Create(map[string]any{"id": id, "user_id": n.UserID,
		"title": n.Title, "project_id": n.ProjectID, "kind": "thread"}).Error
	if err != nil {
		return Thread{}, err
	}
	roles, _ := json.Marshal(n.Roles)
	err = tx.Gorm(ctx).Table("at_threads").Create(map[string]any{"id": id, "project_id": n.ProjectID,
		"user_id": n.UserID, "no": no, "title": n.Title, "brief": n.Brief,
		"branch": fmt.Sprintf("thread/%d-%s", no, Slug(n.Title)), "base_branch": n.BaseBranch,
		"auto_approve": n.AutoApprove, "opened_by": n.OpenedBy, "roles_snapshot": roles,
		"source_message_id": n.SourceMessageID, "forwarded_brief": n.ForwardedBrief, "only_roles": onlyRolesValue(n.OnlyRoles)}).Error
	if err != nil {
		return Thread{}, err
	}
	return ThreadByID(ctx, tx, id)
}

func onlyRolesValue(roles []string) any {
	if len(roles) == 0 {
		return nil
	}
	return jsonOf(roles)
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
	err := store.QueryRow(ctx, tx, `UPDATE at_threads t SET status = $2, pause_reason = $3, updated_at = now(),
		started_at = CASE WHEN $2 = 'running' AND t.started_at IS NULL THEN now() ELSE t.started_at END,
		finished_at = CASE WHEN $2 IN ('done','failed','archived') THEN COALESCE(t.finished_at, now()) ELSE t.finished_at END,
		done_pending_at = CASE WHEN $2 IN ('done','failed','archived') THEN NULL ELSE t.done_pending_at END
		FROM at_threads old WHERE t.id = $1 AND old.id = t.id RETURNING old.status`, id, status, pauseReason).Scan(&prev)
	return prev, MapErr(err)
}

// UpdateThread applies column changes.
func UpdateThread(ctx context.Context, tx *Tx, id string, set map[string]any) error {
	if len(set) == 0 {
		return nil
	}
	return Changed(tx.Gorm(ctx).Table("at_threads").Where("id = ?", id).Updates(stamped(set)))
}

// ThreadCountsOf computes a thread's badges.
func ThreadCountsOf(ctx context.Context, q DB, t Thread) (ThreadCounts, error) {
	var c ThreadCounts
	err := store.QueryRow(ctx, q, `SELECT
		(SELECT count(*) FROM at_questions WHERE thread_id = $1 AND status = 'open' AND audience = 'user' AND kind = 'question'),
		(SELECT count(*) FROM at_questions WHERE thread_id = $1 AND status = 'open' AND audience = 'user' AND kind = 'approval'),
		(SELECT count(*) FROM at_tasks WHERE thread_id = $1 AND status <> 'cancelled'),
		(SELECT count(*) FROM at_tasks WHERE thread_id = $1 AND status = 'done'),
		(SELECT count(*) FROM at_tasks WHERE thread_id = $1 AND status NOT IN ('done', 'failed', 'cancelled')),
		(SELECT count(*) FROM at_main_tasks WHERE thread_id = $1),
		(SELECT count(*) FROM at_main_tasks WHERE thread_id = $1 AND status = 'done'),
		(SELECT count(*) FROM at_deliverables d WHERE d.thread_id = $1 AND `+notImageDeliverable+`),
		(SELECT merge_policy FROM at_projects WHERE id = $2)`, t.ID, t.ProjectID).
		Scan(&c.QuestionsOpen, &c.ApprovalsOpen, &c.TasksTotal, &c.TasksDone, &c.TasksOpen, &c.MainTotal, &c.MainDone, &c.Deliverables, &c.MergePolicy)
	c.Waiting = c.QuestionsOpen + c.ApprovalsOpen
	if AwaitsUser(t, c.MergePolicy) {
		c.Waiting++
	}
	return c, err
}

// AwaitsUser reports whether a thread in review waits on the user: an acceptance under
// merge_policy manual, or a merge that failed. Under pr and auto it waits on its PRs' checks only.
func AwaitsUser(t Thread, policy string) bool {
	return t.Status == "review" && (policy == "manual" || t.MergeError != nil)
}

// MainTaskViews lists the plan with per-step task counts.
func MainTaskViews(ctx context.Context, q DB, threadID string) ([]MainTaskView, error) {
	return find[MainTaskView](q.Gorm(ctx).Table("at_main_tasks m").Select(`m.id, m.pos, m.title, m.status, m.auto_done,
		(SELECT count(*) FROM at_tasks t WHERE t.thread_id = m.thread_id AND t.main_task_id = m.id
		  AND t.status <> 'cancelled') AS tasks_total,
		(SELECT count(*) FROM at_tasks t WHERE t.thread_id = m.thread_id AND t.main_task_id = m.id
		  AND t.status = 'done') AS tasks_done`).Where("m.thread_id = ?", threadID).Order("m.pos"))
}

// ThreadViewOf builds the full ThreadView.
func (s *Store) ThreadViewOf(ctx context.Context, q DB, t Thread) (ThreadView, error) {
	v := ThreadView{ID: t.ID, ProjectID: t.ProjectID, No: t.No, Title: t.Title, Brief: t.Brief, Status: t.Status,
		PauseReason: t.PauseReason, Branch: t.Branch, BaseBranch: t.BaseBranch, PRNumber: t.PRNumber, PRURL: t.PRURL,
		PRState: t.PRState, MergeError: t.MergeError, Summary: t.Summary, AutoApprove: t.AutoApprove, OpenedBy: t.OpenedBy,
		CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt, StartedAt: t.StartedAt, FinishedAt: t.FinishedAt,
		PinnedAt: t.PinnedAt, RemindAt: t.RemindAt, RemindedAt: t.RemindedAt}
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
	v.MergePolicy = v.Counts.MergePolicy
	if v.Repos, err = ThreadRepoViews(ctx, q, t.ID); err != nil {
		return v, err
	}
	if v.LeadLast, err = leadLast(ctx, q, t.ID); err != nil {
		return v, err
	}
	if v.CostUSDEst, err = threadCost(ctx, q, t.ID); err != nil {
		return v, err
	}
	if err = threadActivity(ctx, q, t, &v); err != nil {
		return v, err
	}
	if v.Dispatch, err = DispatchOf(ctx, q, t); err != nil {
		return v, err
	}
	v.QueuePosition, err = queuePosition(ctx, q, t)
	return v, err
}

// DispatchOf is the thread's hand-over: who opened it, when, and the user's message it was opened
// for. Nil when the brief is empty or is the user's forwarded messages (their copies are already
// in the conversation); Source is nil without a message or when it was deleted.
func DispatchOf(ctx context.Context, q DB, t Thread) (*ThreadDispatch, error) {
	if strings.TrimSpace(t.Brief) == "" || t.ForwardedBrief {
		return nil, nil
	}
	d := &ThreadDispatch{OpenedBy: t.OpenedBy, At: t.CreatedAt}
	m, err := SourceMessage(ctx, q, t)
	if err != nil || m == nil {
		return d, err
	}
	atts := AttachmentsOf(*m)
	if atts == nil {
		atts = []AttachmentRef{}
	}
	d.Source = &DispatchSource{MessageID: m.ID, Content: m.Content, Attachments: atts, CreatedAt: m.CreatedAt}
	return d, nil
}

// SourceMessage loads the user's message the thread was opened for (nil: none or deleted).
func SourceMessage(ctx context.Context, q DB, t Thread) (*Message, error) {
	if t.SourceMessageID == nil {
		return nil, nil
	}
	m, err := take[Message](q.Gorm(ctx).Where("id = ?", *t.SourceMessageID))
	if err == ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// SystemPostMeta marks a role=user message the platform wrote for the user (scheduled tasks):
// it is never the user's own words behind a thread.
const SystemPostMeta = "system_post"

// userWords filters a conversation's messages to the user's own words (role user, not a
// platform post; schedule_id marks scheduled posts written before system_post existed).
func userWords(g *gorm.DB, conversationID string) *gorm.DB {
	return g.Where(`conversation_id = ? AND role = 'user' AND NOT jsonb_exists(meta, ?)
		AND NOT jsonb_exists(meta, 'schedule_id')`, conversationID, SystemPostMeta)
}

// LatestUserMessageID is the user's message a coordinator turn acts on: the newest of the user's
// own messages delivered to the coordinator after its previous reply (nil: none, e.g. the turn
// was started by a thread report or a scheduled task).
func LatestUserMessageID(ctx context.Context, q DB, conversationID string) (*int64, error) {
	var after int64
	if err := q.Gorm(ctx).Table("at_messages").Select("COALESCE(max(id), 0)").
		Where("conversation_id = ? AND role = 'leader'", conversationID).Row().Scan(&after); err != nil {
		return nil, err
	}
	m, err := take[Message](userWords(q.Gorm(ctx).Model(&Message{}), conversationID).
		Where("id > ? AND delivered_at IS NOT NULL", after).Order("id DESC"))
	if err == ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &m.ID, nil
}

// UserWordByID loads one of the user's own messages of the conversation (ErrNotFound otherwise:
// another conversation, an employee's message or a platform post).
func UserWordByID(ctx context.Context, q DB, conversationID string, id int64) (Message, error) {
	return take[Message](userWords(q.Gorm(ctx).Model(&Message{}), conversationID).Where("id = ?", id))
}

func leadLast(ctx context.Context, q DB, threadID string) (*LeadLast, error) {
	l, err := take[LeadLast](q.Gorm(ctx).Table("at_messages").Select("content AS text, created_at AS at").
		Where("conversation_id = ? AND role = 'leader'", threadID).Order("id DESC"))
	if err == ErrNotFound {
		return nil, nil
	}
	return &l, err
}

// threadActivity fills the thread's newest deliverable (hosted images are not deliverables any more) and when anything last happened in it.
func threadActivity(ctx context.Context, q DB, t Thread, v *ThreadView) error {
	v.LastActivityAt = t.UpdatedAt
	ds, err := Deliverables(ctx, q, t.ProjectID, DeliverableFilter{ThreadID: t.ID, Limit: 1, NoImages: true})
	if err != nil {
		return err
	}
	if len(ds) == 1 {
		d, no := ds[0], t.No
		v.LatestDeliverable = &DeliverableView{ID: d.ID, ProjectID: d.ProjectID, ThreadNo: &no, Kind: d.Kind, Title: d.Title,
			URL: d.URL, Detail: d.Detail, PublishedBy: d.PublishedBy, CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt}
		if d.UpdatedAt.After(v.LastActivityAt) {
			v.LastActivityAt = d.UpdatedAt
		}
	}
	var last *time.Time
	if err := q.Gorm(ctx).Table("at_messages").Select("max(created_at)").Where("conversation_id = ?", t.ID).Row().Scan(&last); err != nil {
		return err
	}
	if last != nil && last.After(v.LastActivityAt) {
		v.LastActivityAt = *last
	}
	return nil
}

func threadCost(ctx context.Context, q DB, threadID string) (*float64, error) {
	var cost *float64
	err := q.Gorm(ctx).Table("at_usage").Select("sum(cost_usd)::float8").Where("thread_id = ?", threadID).Row().Scan(&cost)
	return cost, err
}

func queuePosition(ctx context.Context, q DB, t Thread) (*int, error) {
	if t.Status != "queued" {
		return nil, nil
	}
	n, err := count(threads(ctx, q).Where("user_id = ? AND status = 'queued' AND (created_at, no) < (?, ?)",
		t.UserID, t.CreatedAt, t.No))
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
	if err := tx.Gorm(ctx).Table("at_threads").Where("id = ?", threadID).Update("waiting_count", c.Waiting).Error; err != nil {
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

// SetThreadPinned pins (pinned_at = now) or unpins a thread.
func SetThreadPinned(ctx context.Context, tx *Tx, threadID string, pinned bool) error {
	var at any
	if pinned {
		at = gorm.Expr("now()")
	}
	return tx.Gorm(ctx).Table("at_threads").Where("id = ?", threadID).Update("pinned_at", at).Error
}

// SetThreadReminder sets when the thread's reminder fires (nil cancels it); either way a fired
// reminder is cleared.
func SetThreadReminder(ctx context.Context, tx *Tx, threadID string, at *time.Time) error {
	return tx.Gorm(ctx).Table("at_threads").Where("id = ?", threadID).
		Updates(map[string]any{"remind_at": at, "reminded_at": nil}).Error
}

// FireDueReminders moves every reminder due at now to reminded_at in one statement, so each
// fires exactly once however many runners race, and returns the threads it fired.
func FireDueReminders(ctx context.Context, tx *Tx, now time.Time) ([]string, error) {
	var ids []string
	rows, err := tx.Gorm(ctx).Raw(`UPDATE at_threads SET reminded_at = remind_at, remind_at = NULL
		WHERE remind_at IS NOT NULL AND remind_at <= ? RETURNING id`, now).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// EndLeadSchedules cancels the scheduled wake-ups a thread's lead or its members set for
// themselves (schedule_create) when the thread finishes or is archived; schedules the user or the
// coordinator aimed at the thread stay (they reopen a finished thread when they fire).
func EndLeadSchedules(ctx context.Context, tx *Tx, threadID string) error {
	return endSchedules(tx.Gorm(ctx).Table("at_schedules").Where("thread_id = ? AND created_by IN ('lead', 'member') AND status = 'active'", threadID))
}

// EndTaskSchedules cancels a member's wake-ups bound to a task when the task ends or goes back to
// the queue.
func EndTaskSchedules(ctx context.Context, tx *Tx, t Task) error {
	return endSchedules(tx.Gorm(ctx).Table("at_schedules").Where("thread_id = ? AND task_id = ? AND status = 'active'", t.ConversationID, t.ID))
}

func endSchedules(g *gorm.DB) error {
	return g.Updates(map[string]any{"status": "cancelled", "next_run_at": nil, "ended_at": gorm.Expr("now()"), "updated_at": gorm.Expr("now()")}).Error
}
