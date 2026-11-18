package core

import (
	"context"
	"encoding/json"

	"gorm.io/gorm"

	"github.com/touwaeriol/agent-teams/server/internal/store"
)

func sessions(ctx context.Context, q DB) *gorm.DB { return q.Gorm(ctx).Model(&AgentSession{}) }

// SessionByID loads a logical session.
func SessionByID(ctx context.Context, q DB, id string) (AgentSession, error) {
	return take[AgentSession](sessions(ctx, q).Where("id = ?", id))
}

// LockSession loads a session FOR UPDATE.
func LockSession(ctx context.Context, tx *Tx, id string) (AgentSession, error) {
	return take[AgentSession](sessions(ctx, tx).Clauses(ForUpdate).Where("id = ?", id))
}

// LiveSession returns the employee's session that is not closed.
func LiveSession(ctx context.Context, q DB, employeeID string) (AgentSession, error) {
	return take[AgentSession](sessions(ctx, q).Where("employee_id = ? AND state <> 'closed'", employeeID))
}

// LatestSession returns the employee's most recent session (closed or not).
func LatestSession(ctx context.Context, q DB, employeeID string) (AgentSession, error) {
	return take[AgentSession](sessions(ctx, q).Where("employee_id = ?", employeeID).Order("opened_at DESC"))
}

// NodeLiveSessions lists the sessions that are not closed on a node.
func NodeLiveSessions(ctx context.Context, q DB, nodeID string) ([]AgentSession, error) {
	return find[AgentSession](sessions(ctx, q).Where("node_id = ? AND state <> 'closed'", nodeID))
}

// ActiveSessionCount counts sessions that are not closed or parked on a node (node selection).
func ActiveSessionCount(ctx context.Context, q DB, nodeID string) (int, error) {
	return count(sessions(ctx, q).Where("node_id = ? AND state NOT IN ('closed','parked','crashed')", nodeID))
}

// NewSession is what InsertSession stores.
type NewSession struct {
	EmployeeID     string
	NodeID         string
	Runtime        string
	Cwd            string
	Branch         string
	PermissionMode string
	ThreadID       *string // the thread whose worktree it opens in (nil: coordinator)
}

// InsertSession creates a logical session at open_gen 1.
func InsertSession(ctx context.Context, tx *Tx, n NewSession) (AgentSession, error) {
	id := NewID("s")
	err := tx.Gorm(ctx).Table("at_agent_sessions").Create(map[string]any{"id": id, "employee_id": n.EmployeeID,
		"node_id": n.NodeID, "runtime": n.Runtime, "cwd": n.Cwd, "branch": n.Branch, "state": "opening", "open_gen": 1,
		"permission_mode": n.PermissionMode, "thread_id": n.ThreadID}).Error
	if err != nil {
		return AgentSession{}, err
	}
	return SessionByID(ctx, tx, id)
}

// BumpOpenGen starts a new open generation (resume) and returns it.
func BumpOpenGen(ctx context.Context, tx *Tx, id string) (int, error) {
	var gen int
	err := store.QueryRow(ctx, tx, `UPDATE at_agent_sessions SET open_gen = open_gen + 1, state = 'opening', in_turn = false WHERE id = $1
		RETURNING open_gen`, id).Scan(&gen)
	return gen, MapErr(err)
}

// openSession selects a session that is not closed.
func openSession(ctx context.Context, tx *Tx, id string) *gorm.DB {
	return tx.Gorm(ctx).Table("at_agent_sessions").Where("id = ? AND state <> 'closed'", id)
}

// SetSessionState records the node-reported session state; a session that is not active runs
// no turn.
func SetSessionState(ctx context.Context, tx *Tx, id, state string) error {
	set := map[string]any{"state": state, "last_active_at": gorm.Expr("now()")}
	if state != "active" {
		set["in_turn"] = false
	}
	return Changed(openSession(ctx, tx, id).Updates(set))
}

// SetInTurn records whether a turn runs in the session (turn_started / turn_ended): a project
// member mid-turn takes no task of another thread (shared-members.md §4.1).
func SetInTurn(ctx context.Context, tx *Tx, id string, on bool) error {
	return openSession(ctx, tx, id).Update("in_turn", on).Error
}

// RecordOpened stores session.opened (SDK session id, init) and marks the session active.
func RecordOpened(ctx context.Context, tx *Tx, id string, sdkID, codexID *string, init json.RawMessage) error {
	if init == nil {
		init = json.RawMessage(`{}`)
	}
	return Changed(openSession(ctx, tx, id).Updates(map[string]any{"state": "active", "init": []byte(init),
		"last_active_at":  gorm.Expr("now()"),
		"sdk_session_id":  gorm.Expr("COALESCE(?, sdk_session_id)", sdkID),
		"codex_thread_id": gorm.Expr("COALESCE(?, codex_thread_id)", codexID)}))
}

// unackedCommands selects a session's node commands that were not acknowledged yet.
func unackedCommands(ctx context.Context, tx *Tx, sessionID string) *gorm.DB {
	return tx.Gorm(ctx).Table("at_node_commands").Where("session_id = ? AND status IN ('pending','sent')", sessionID)
}

// CloseSession marks a session closed; its unacked commands are superseded, except tool
// results queued before the close (the call that ended the thread still gets its answer).
func CloseSession(ctx context.Context, tx *Tx, id, reason string) error {
	err := openSession(ctx, tx, id).Updates(map[string]any{"state": "closed", "closed_at": gorm.Expr("now()"),
		"close_reason": reason}).Error
	if err != nil {
		return err
	}
	return unackedCommands(ctx, tx, id).Where("type NOT IN ('session.close','tool.result')").Update("status", "superseded").Error
}

// SupersedeOldGen marks unacked commands of older open generations superseded.
func SupersedeOldGen(ctx context.Context, tx *Tx, id string, gen int) error {
	return unackedCommands(ctx, tx, id).Where("open_gen < ?", gen).Update("status", "superseded").Error
}

// TouchSessionEvent advances last_event_no; false when the event was already seen.
func TouchSessionEvent(ctx context.Context, tx *Tx, id string, eventNo int64) (bool, error) {
	res := tx.Gorm(ctx).Table("at_agent_sessions").Where("id = ? AND last_event_no < ?", id, eventNo).
		Updates(map[string]any{"last_event_no": eventNo, "last_active_at": gorm.Expr("now()")})
	return res.RowsAffected > 0, res.Error
}

// SessionContext is a session with its employee and the project owner.
type SessionContext struct {
	Session  AgentSession
	Employee Employee
	OwnerID  string
}

// SessionContextOf loads a session, its employee and the project owner.
func SessionContextOf(ctx context.Context, q DB, sessionID string) (SessionContext, error) {
	var sc SessionContext
	var err error
	if sc.Session, err = SessionByID(ctx, q, sessionID); err != nil {
		return sc, err
	}
	if sc.Employee, err = EmployeeByID(ctx, q, sc.Session.EmployeeID); err != nil {
		return sc, err
	}
	if sc.Employee.ProjectMember() && sc.Session.ThreadID != nil {
		// A project member's session acts in the thread it was opened in, even after the member
		// moved on (the tail of a closed session): conversation, usage and activity stay there.
		sc.Employee.CurrentThreadID = sc.Session.ThreadID
	}
	sc.OwnerID, err = ProjectOwner(ctx, q, sc.Employee.ProjectID)
	return sc, err
}

// SessionTask is the employee's current task when it belongs to the session: a project member's
// session of another thread (closed when it moved) has nothing to do with the task it has now.
func SessionTask(ctx context.Context, q DB, sc SessionContext) (Task, error) {
	task, err := CurrentTaskOf(ctx, q, sc.Employee)
	if err != nil {
		return task, err
	}
	if sc.Employee.ProjectMember() && sc.Session.ThreadID != nil && *sc.Session.ThreadID != task.ConversationID {
		return task, ErrNotFound
	}
	return task, nil
}

// ProjectOwner returns the id of the user who owns a project.
func ProjectOwner(ctx context.Context, q DB, projectID string) (string, error) {
	p, err := take[struct{ UserID string }](q.Gorm(ctx).Table("at_projects").Select("user_id").Where("id = ?", projectID))
	return p.UserID, err
}

// liveSessionsJoined selects live sessions through their employee (alias e).
func liveSessionsJoined(ctx context.Context, q DB) *gorm.DB {
	return q.Gorm(ctx).Table("at_agent_sessions a").Select("a.*").Joins("JOIN at_employees e ON e.id = a.employee_id").
		Where("a.state <> 'closed'")
}

// ThreadLiveSessions lists the live sessions of a thread's employees and the sessions project
// members opened in the thread.
func ThreadLiveSessions(ctx context.Context, q DB, threadID string) ([]AgentSession, error) {
	return find[AgentSession](liveSessionsJoined(ctx, q).Where("e.thread_id = ? OR a.thread_id = ?", threadID, threadID))
}

// ProjectLiveSessions lists the live sessions of a project.
func ProjectLiveSessions(ctx context.Context, q DB, projectID string) ([]AgentSession, error) {
	return find[AgentSession](liveSessionsJoined(ctx, q).Where("e.project_id = ?", projectID))
}

// UserLiveSessions lists the live sessions of all of a user's projects.
func UserLiveSessions(ctx context.Context, q DB, userID string) ([]AgentSession, error) {
	return find[AgentSession](liveSessionsJoined(ctx, q).Joins("JOIN at_projects p ON p.id = e.project_id").
		Where("p.user_id = ?", userID))
}

// NodeHasFeature reports whether the node's last hello listed the feature.
func NodeHasFeature(ctx context.Context, q DB, nodeID, feature string) (bool, error) {
	var n int64
	err := q.Gorm(ctx).Table("at_nodes").Where("id = ? AND last_hello->'features' @> jsonb_build_array(?::text)", nodeID, feature).Count(&n).Error
	return n > 0, err
}
