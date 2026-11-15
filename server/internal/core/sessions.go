package core

import (
	"context"
	"encoding/json"
)

// SessionByID loads a logical session.
func SessionByID(ctx context.Context, q DB, id string) (AgentSession, error) {
	return scanSession(q.QueryRow(ctx, `SELECT `+sessionCols+` FROM at_agent_sessions WHERE id = $1`, id))
}

// LockSession loads a session FOR UPDATE.
func LockSession(ctx context.Context, tx *Tx, id string) (AgentSession, error) {
	return scanSession(tx.QueryRow(ctx, `SELECT `+sessionCols+` FROM at_agent_sessions WHERE id = $1 FOR UPDATE`, id))
}

// LiveSession returns the employee's session that is not closed.
func LiveSession(ctx context.Context, q DB, employeeID string) (AgentSession, error) {
	return scanSession(q.QueryRow(ctx, `SELECT `+sessionCols+` FROM at_agent_sessions
		WHERE employee_id = $1 AND state <> 'closed'`, employeeID))
}

// LatestSession returns the employee's most recent session (closed or not).
func LatestSession(ctx context.Context, q DB, employeeID string) (AgentSession, error) {
	return scanSession(q.QueryRow(ctx, `SELECT `+sessionCols+` FROM at_agent_sessions
		WHERE employee_id = $1 ORDER BY opened_at DESC LIMIT 1`, employeeID))
}

// NodeLiveSessions lists the sessions that are not closed on a node.
func NodeLiveSessions(ctx context.Context, q DB, nodeID string) ([]AgentSession, error) {
	rows, err := q.Query(ctx, `SELECT `+sessionCols+` FROM at_agent_sessions WHERE node_id = $1 AND state <> 'closed'`, nodeID)
	return collect(rows, err, scanSession)
}

// ActiveSessionCount counts sessions that are not closed or parked on a node (node selection).
func ActiveSessionCount(ctx context.Context, q DB, nodeID string) (int, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT count(*) FROM at_agent_sessions WHERE node_id = $1
		AND state NOT IN ('closed','parked','crashed')`, nodeID).Scan(&n)
	return n, err
}

// NewSession is what InsertSession stores.
type NewSession struct {
	EmployeeID     string
	NodeID         string
	Runtime        string
	Cwd            string
	Branch         string
	PermissionMode string
}

// InsertSession creates a logical session at open_gen 1.
func InsertSession(ctx context.Context, tx *Tx, n NewSession) (AgentSession, error) {
	id := NewID("s")
	_, err := tx.Exec(ctx, `INSERT INTO at_agent_sessions (id, employee_id, node_id, runtime, cwd, branch, state, open_gen,
		permission_mode) VALUES ($1,$2,$3,$4,$5,$6,'opening',1,$7)`,
		id, n.EmployeeID, n.NodeID, n.Runtime, n.Cwd, n.Branch, n.PermissionMode)
	if err != nil {
		return AgentSession{}, err
	}
	return SessionByID(ctx, tx, id)
}

// BumpOpenGen starts a new open generation (resume) and returns it.
func BumpOpenGen(ctx context.Context, tx *Tx, id string) (int, error) {
	var gen int
	err := tx.QueryRow(ctx, `UPDATE at_agent_sessions SET open_gen = open_gen + 1, state = 'opening' WHERE id = $1
		RETURNING open_gen`, id).Scan(&gen)
	return gen, MapErr(err)
}

// SetSessionState records the node-reported session state.
func SetSessionState(ctx context.Context, tx *Tx, id, state string) error {
	return Affected(tx.Exec(ctx, `UPDATE at_agent_sessions SET state = $2, last_active_at = now()
		WHERE id = $1 AND state <> 'closed'`, id, state))
}

// RecordOpened stores session.opened (SDK session id, init) and marks the session active.
func RecordOpened(ctx context.Context, tx *Tx, id string, sdkID, codexID *string, init json.RawMessage) error {
	if init == nil {
		init = json.RawMessage(`{}`)
	}
	return Affected(tx.Exec(ctx, `UPDATE at_agent_sessions SET state = 'active', init = $4, last_active_at = now(),
		sdk_session_id = COALESCE($2, sdk_session_id), codex_thread_id = COALESCE($3, codex_thread_id)
		WHERE id = $1 AND state <> 'closed'`, id, sdkID, codexID, init))
}

// CloseSession marks a session closed; its unacked commands are superseded.
func CloseSession(ctx context.Context, tx *Tx, id, reason string) error {
	if _, err := tx.Exec(ctx, `UPDATE at_agent_sessions SET state = 'closed', closed_at = now(), close_reason = $2
		WHERE id = $1 AND state <> 'closed'`, id, reason); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE at_node_commands SET status = 'superseded'
		WHERE session_id = $1 AND status IN ('pending','sent') AND type <> 'session.close'`, id)
	return err
}

// SupersedeOldGen marks unacked commands of older open generations superseded.
func SupersedeOldGen(ctx context.Context, tx *Tx, id string, gen int) error {
	_, err := tx.Exec(ctx, `UPDATE at_node_commands SET status = 'superseded'
		WHERE session_id = $1 AND open_gen < $2 AND status IN ('pending','sent')`, id, gen)
	return err
}

// TouchSessionEvent advances last_event_no; false when the event was already seen.
func TouchSessionEvent(ctx context.Context, tx *Tx, id string, eventNo int64) (bool, error) {
	tag, err := tx.Exec(ctx, `UPDATE at_agent_sessions SET last_event_no = $2, last_active_at = now()
		WHERE id = $1 AND last_event_no < $2`, id, eventNo)
	return tag.RowsAffected() > 0, err
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
	err = MapErr(q.QueryRow(ctx, `SELECT user_id FROM at_projects WHERE id = $1`, sc.Employee.ProjectID).Scan(&sc.OwnerID))
	return sc, err
}

// ThreadLiveSessions lists the live sessions of a thread's employees.
func ThreadLiveSessions(ctx context.Context, q DB, threadID string) ([]AgentSession, error) {
	rows, err := q.Query(ctx, `SELECT `+prefixed("a", sessionCols)+` FROM at_agent_sessions a
		JOIN at_employees e ON e.id = a.employee_id WHERE e.thread_id = $1 AND a.state <> 'closed'`, threadID)
	return collect(rows, err, scanSession)
}

// ProjectLiveSessions lists the live sessions of a project.
func ProjectLiveSessions(ctx context.Context, q DB, projectID string) ([]AgentSession, error) {
	rows, err := q.Query(ctx, `SELECT `+prefixed("a", sessionCols)+` FROM at_agent_sessions a
		JOIN at_employees e ON e.id = a.employee_id WHERE e.project_id = $1 AND a.state <> 'closed'`, projectID)
	return collect(rows, err, scanSession)
}

// UserLiveSessions lists the live sessions of all of a user's projects.
func UserLiveSessions(ctx context.Context, q DB, userID string) ([]AgentSession, error) {
	rows, err := q.Query(ctx, `SELECT `+prefixed("a", sessionCols)+` FROM at_agent_sessions a
		JOIN at_employees e ON e.id = a.employee_id JOIN at_projects p ON p.id = e.project_id
		WHERE p.user_id = $1 AND a.state <> 'closed'`, userID)
	return collect(rows, err, scanSession)
}
