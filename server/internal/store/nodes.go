package store

import (
	"context"
	"encoding/json"
	"time"
)

// Node is a row of at_nodes.
type Node struct {
	ID                string
	UserID            string
	Name              string
	CreatedAt         time.Time
	MaxActiveSessions *int
	ParkAfterS        *int
	Isolated          bool
	Isolation         string
	Labels            []string
	LastHello         json.RawMessage
	LastSeenAt        *time.Time
}

const nodeCols = `n.id, n.user_id, n.name, n.created_at, n.max_active_sessions, n.park_after_s, n.isolated,
	n.isolation, n.labels, n.last_hello, n.last_seen_at`

func scanNode(row interface{ Scan(...any) error }) (Node, error) {
	var n Node
	err := row.Scan(&n.ID, &n.UserID, &n.Name, &n.CreatedAt, &n.MaxActiveSessions, &n.ParkAfterS,
		&n.Isolated, &n.Isolation, &n.Labels, &n.LastHello, &n.LastSeenAt)
	return n, mapErr(err)
}

func collectNodes(rows interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close()
}) ([]Node, error) {
	defer rows.Close()
	out := []Node{}
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// CreateNode registers a node; ErrConflict if the user already has that name.
func (s *Store) CreateNode(ctx context.Context, userID, name, tokenHash string) (Node, error) {
	row := s.db.QueryRow(ctx, `INSERT INTO at_nodes AS n (id, user_id, name, token_hash) VALUES ($1, $2, $3, $4)
		RETURNING `+nodeCols, NewHexID(), userID, name, tokenHash)
	return scanNode(row)
}

// ListNodes returns the user's nodes in creation order.
func (s *Store) ListNodes(ctx context.Context, userID string) ([]Node, error) {
	rows, err := s.db.Query(ctx, `SELECT `+nodeCols+` FROM at_nodes n WHERE n.user_id = $1 ORDER BY n.created_at`, userID)
	if err != nil {
		return nil, err
	}
	return collectNodes(rows)
}

// CountNodes counts the user's nodes (quota).
func (s *Store) CountNodes(ctx context.Context, userID string) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `SELECT count(*) FROM at_nodes WHERE user_id = $1`, userID).Scan(&n)
	return n, err
}

// NodeOf loads one of the user's nodes.
func (s *Store) NodeOf(ctx context.Context, userID, id string) (Node, error) {
	return scanNode(s.db.QueryRow(ctx, `SELECT `+nodeCols+` FROM at_nodes n WHERE n.id = $1 AND n.user_id = $2`, id, userID))
}

// NodeByToken identifies a daemon by sha256(token) (node gateway).
func (s *Store) NodeByToken(ctx context.Context, tokenHash string) (Node, error) {
	return scanNode(s.db.QueryRow(ctx, `SELECT `+nodeCols+` FROM at_nodes n WHERE n.token_hash = $1`, tokenHash))
}

// SetNodeToken rotates the token hash.
func (s *Store) SetNodeToken(ctx context.Context, userID, id, tokenHash string) error {
	return affected(s.db.Exec(ctx, `UPDATE at_nodes SET token_hash = $3 WHERE id = $1 AND user_id = $2`, id, userID, tokenHash))
}

// UpdateNode applies settings changes (max_active_sessions, park_after_s, isolated, labels).
func (s *Store) UpdateNode(ctx context.Context, userID, id string, changes []Change) (Node, error) {
	if len(changes) == 0 {
		return s.NodeOf(ctx, userID, id)
	}
	set, args := setClause(changes, 3)
	row := s.db.QueryRow(ctx, `UPDATE at_nodes AS n SET `+set+` WHERE n.id = $1 AND n.user_id = $2 RETURNING `+nodeCols,
		append([]any{id, userID}, args...)...)
	return scanNode(row)
}

// DeleteNode removes one of the user's nodes.
func (s *Store) DeleteNode(ctx context.Context, userID, id string) error {
	return affected(s.db.Exec(ctx, `DELETE FROM at_nodes WHERE id = $1 AND user_id = $2`, id, userID))
}

// NodeSession is one live agent session on a node (Nodes page).
type NodeSession struct {
	SessionID    string     `json:"session_id"`
	Employee     string     `json:"employee"`
	Project      string     `json:"project"`
	ThreadNo     *int       `json:"thread_no"`
	State        string     `json:"state"`
	LastActiveAt *time.Time `json:"last_active_at"`
}

// NodeSessions lists the node's sessions that are not closed.
func (s *Store) NodeSessions(ctx context.Context, nodeID string) ([]NodeSession, error) {
	rows, err := s.db.Query(ctx, `SELECT a.id, e.name, p.name, t.no, a.state, a.last_active_at
		FROM at_agent_sessions a JOIN at_employees e ON e.id = a.employee_id
		JOIN at_projects p ON p.id = e.project_id LEFT JOIN at_threads t ON t.id = e.thread_id
		WHERE a.node_id = $1 AND a.state <> 'closed' ORDER BY a.opened_at`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NodeSession{}
	for rows.Next() {
		var x NodeSession
		if err := rows.Scan(&x.SessionID, &x.Employee, &x.Project, &x.ThreadNo, &x.State, &x.LastActiveAt); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// NodeLog is one warn/error line the daemon sent back.
type NodeLog struct {
	ID        int64           `json:"id"`
	Level     string          `json:"level"`
	Msg       string          `json:"msg"`
	Fields    json.RawMessage `json:"fields"`
	CreatedAt time.Time       `json:"created_at"`
}

// NodeLogs pages the node's logs newest first; before = id cursor (0 = newest).
func (s *Store) NodeLogs(ctx context.Context, nodeID, level string, before int64, limit int) ([]NodeLog, error) {
	rows, err := s.db.Query(ctx, `SELECT id, level, msg, fields, created_at FROM at_node_logs
		WHERE node_id = $1 AND ($2 = '' OR level = $2) AND ($3 = 0 OR id < $3)
		ORDER BY id DESC LIMIT $4`, nodeID, level, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NodeLog{}
	for rows.Next() {
		var x NodeLog
		if err := rows.Scan(&x.ID, &x.Level, &x.Msg, &x.Fields, &x.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}
