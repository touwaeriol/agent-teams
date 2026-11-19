package core

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// Project event types (api.md §1.1).
const (
	EvMessage     = "message"
	EvThread      = "thread"
	EvMainTask    = "main_task"
	EvTask        = "task"
	EvQuestion    = "question"
	EvMail        = "mail"
	EvActivity    = "activity"
	EvStatus      = "status"
	EvDeliverable = "deliverable"
	EvMerge       = "merge"
	EvCI          = "ci"
	EvMemory      = "memory"
	EvNode        = "node"
	EvLayout      = "layout"
	EvSession     = "session"
	EvSharedFile  = "shared_file"
)

// ProjectChannel is the pub/sub channel of a project's events.
func ProjectChannel(projectID string) string { return "project:" + projectID }

// UserChannel is the pub/sub channel of a user's events.
func UserChannel(userID string) string { return "user:" + userID }

// nextProjectSeq allocates the project event sequence. The row lock is held
// until commit, so a project's events commit in seq order.
func (tx *Tx) nextProjectSeq(ctx context.Context, projectID string) (int64, error) {
	var seq int64
	err := store.QueryRow(ctx, tx, `UPDATE at_projects SET event_seq = event_seq + 1 WHERE id = $1 RETURNING event_seq`,
		projectID).Scan(&seq)
	return seq, MapErr(err)
}

// Emit appends a project event (at_events) and returns its seq.
func (tx *Tx) Emit(ctx context.Context, projectID string, threadID *string, typ, actor string, payload any) (int64, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	seq, err := tx.nextProjectSeq(ctx, projectID)
	if err != nil {
		return 0, err
	}
	err = tx.Gorm(ctx).Table("at_events").Create(map[string]any{"project_id": projectID, "seq": seq,
		"thread_id": threadID, "type": typ, "actor": actor, "payload": raw}).Error
	if err != nil {
		return 0, err
	}
	tx.hint(ProjectChannel(projectID), strconv.FormatInt(seq, 10))
	return seq, nil
}

// StatusRow is one employee status change (at_status_events / at_employee_status).
type StatusRow struct {
	ProjectID   string
	ThreadID    *string
	EmployeeID  string
	Tier        string
	Role        string
	State       string
	Substate    string
	Bubble      string
	BubbleKind  string
	BubbleUntil *time.Time
	TaskID      string
	TaskKind    string
	Cause       string
}

// EmitStatus records a status change and the employee's latest status, sharing the project seq.
func (tx *Tx) EmitStatus(ctx context.Context, r StatusRow) (int64, error) {
	seq, err := tx.nextProjectSeq(ctx, r.ProjectID)
	if err != nil {
		return 0, err
	}
	if r.BubbleKind == "" {
		r.BubbleKind = "plain"
	}
	err = tx.Gorm(ctx).Table("at_status_events").Create(map[string]any{"project_id": r.ProjectID, "seq": seq,
		"thread_id": r.ThreadID, "employee_id": r.EmployeeID, "tier": r.Tier, "role": r.Role, "state": r.State,
		"substate": r.Substate, "bubble": r.Bubble, "bubble_kind": r.BubbleKind, "task_id": r.TaskID,
		"task_kind": r.TaskKind, "cause": r.Cause}).Error
	if err != nil {
		return 0, err
	}
	_, err = store.Exec(ctx, tx, `INSERT INTO at_employee_status (employee_id, project_id, thread_id, state, substate, bubble,
		bubble_kind, bubble_until, task_id, task_kind, since, seq) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,now(),$11)
		ON CONFLICT (employee_id) DO UPDATE SET state = EXCLUDED.state, substate = EXCLUDED.substate,
		bubble = EXCLUDED.bubble, bubble_kind = EXCLUDED.bubble_kind, bubble_until = EXCLUDED.bubble_until,
		task_id = EXCLUDED.task_id, task_kind = EXCLUDED.task_kind,
		since = CASE WHEN at_employee_status.state = EXCLUDED.state THEN at_employee_status.since ELSE now() END,
		seq = EXCLUDED.seq`,
		r.EmployeeID, r.ProjectID, r.ThreadID, r.State, r.Substate, r.Bubble, r.BubbleKind, r.BubbleUntil,
		r.TaskID, r.TaskKind, seq)
	if err != nil {
		return 0, err
	}
	tx.hint(ProjectChannel(r.ProjectID), strconv.FormatInt(seq, 10))
	return seq, nil
}

// EmitUser appends a user event (at_user_events, seq from at_users.event_seq).
func (tx *Tx) EmitUser(ctx context.Context, userID, typ string, payload any) (int64, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	var seq int64
	if err := store.QueryRow(ctx, tx, `UPDATE at_users SET event_seq = event_seq + 1 WHERE id = $1 RETURNING event_seq`,
		userID).Scan(&seq); err != nil {
		return 0, MapErr(err)
	}
	err = tx.Gorm(ctx).Table("at_user_events").Create(map[string]any{"user_id": userID, "seq": seq, "type": typ,
		"payload": raw}).Error
	if err != nil {
		return 0, err
	}
	tx.hint(UserChannel(userID), strconv.FormatInt(seq, 10))
	return seq, nil
}

// Event is one row of the merged project stream (at_events ∪ at_status_events).
type Event struct {
	Seq       int64           `json:"id"`
	Type      string          `json:"type"`
	ThreadNo  *int            `json:"-" gorm:"column:no"`
	Data      json.RawMessage `json:"data"`
	CreatedAt time.Time       `json:"at"`
}

// statusEventData renders a status row as the SSE data {employee, status}.
const statusEventData = `jsonb_build_object(
	'employee', jsonb_build_object('id', s.employee_id, 'name', COALESCE(e.name, ''), 'tier', s.tier, 'role', s.role,
		'thread_no', t.no),
	'status', jsonb_build_object('state', s.state, 'substate', s.substate, 'bubble', s.bubble,
		'bubble_kind', s.bubble_kind, 'task_id', s.task_id, 'task_kind', s.task_kind, 'tier', s.tier, 'role', s.role,
		'since', s.created_at, 'seq', s.seq))`

// EventsAfter returns the project's events with seq > after (both tables), oldest first.
func EventsAfter(ctx context.Context, q DB, projectID string, after int64, since time.Time, limit int) ([]Event, error) {
	return rawAll[Event](ctx, q, `SELECT seq, type, no, data, created_at FROM (
		SELECT e.seq, e.type, t.no, e.payload AS data, e.created_at FROM at_events e
		LEFT JOIN at_threads t ON t.id = e.thread_id
		WHERE e.project_id = $1 AND e.seq > $2 AND e.created_at >= $3
		UNION ALL
		SELECT s.seq, 'status', t.no, `+statusEventData+`, s.created_at FROM at_status_events s
		LEFT JOIN at_employees e ON e.id = s.employee_id LEFT JOIN at_threads t ON t.id = s.thread_id
		WHERE s.project_id = $1 AND s.seq > $2 AND s.created_at >= $3
	) x ORDER BY seq LIMIT $4`, projectID, after, since, limit)
}

// EventsBetween returns the project's events in a time window (office replay).
func EventsBetween(ctx context.Context, q DB, projectID string, from, to time.Time, limit int) ([]Event, error) {
	return rawAll[Event](ctx, q, `SELECT seq, type, no, data, created_at FROM (
		SELECT e.seq, e.type, t.no, e.payload AS data, e.created_at FROM at_events e
		LEFT JOIN at_threads t ON t.id = e.thread_id
		WHERE e.project_id = $1 AND e.created_at >= $2 AND e.created_at < $3
		UNION ALL
		SELECT s.seq, 'status', t.no, `+statusEventData+`, s.created_at FROM at_status_events s
		LEFT JOIN at_employees e ON e.id = s.employee_id LEFT JOIN at_threads t ON t.id = s.thread_id
		WHERE s.project_id = $1 AND s.created_at >= $2 AND s.created_at < $3
	) x ORDER BY seq LIMIT $4`, projectID, from, to, limit)
}

// UserEventsAfter returns a user's events with seq > after, oldest first.
func UserEventsAfter(ctx context.Context, q DB, userID string, after int64, since time.Time, limit int) ([]Event, error) {
	return find[Event](q.Gorm(ctx).Table("at_user_events").Select("seq, type, NULL::int AS no, payload AS data, created_at").
		Where("user_id = ? AND seq > ? AND created_at >= ?", userID, after, since).Order("seq").Limit(limit))
}

// ProjectSeq is the project's current event sequence.
func ProjectSeq(ctx context.Context, q DB, projectID string) (int64, error) {
	return eventSeq(ctx, q, "at_projects", projectID)
}

// UserSeq is the user's current event sequence.
func UserSeq(ctx context.Context, q DB, userID string) (int64, error) {
	return eventSeq(ctx, q, "at_users", userID)
}

// eventSeq reads the event_seq column of a project or user row.
func eventSeq(ctx context.Context, q DB, table, id string) (int64, error) {
	row, err := take[struct{ EventSeq int64 }](q.Gorm(ctx).Table(table).Select("event_seq").Where("id = ?", id))
	return row.EventSeq, err
}

// OldestSeqSince is the smallest project seq still kept after since (0 = none).
func OldestSeqSince(ctx context.Context, q DB, projectID string, since time.Time) (int64, error) {
	var seq int64
	err := store.QueryRow(ctx, q, `SELECT COALESCE(min(seq), 0) FROM (
		SELECT min(seq) AS seq FROM at_events WHERE project_id = $1 AND created_at >= $2
		UNION ALL SELECT min(seq) FROM at_status_events WHERE project_id = $1 AND created_at >= $2) x`,
		projectID, since).Scan(&seq)
	return seq, err
}

// EmitSharedFile records a shared_file event (api.md §1.1) after a shared file was written or
// deleted, so the web page refreshes its list: payload {op: "write"|"delete", path, by}.
func (tx *Tx) EmitSharedFile(ctx context.Context, projectID, by, op, path string) error {
	_, err := tx.Emit(ctx, projectID, nil, EvSharedFile, by, map[string]any{"op": op, "path": path, "by": by})
	return err
}
