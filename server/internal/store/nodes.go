package store

import (
	"context"
	"encoding/json"
	"time"

	"gorm.io/gorm"
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
	Labels            []string `gorm:"serializer:jsonb"`
	LastHello         json.RawMessage
	LastSeenAt        *time.Time
}

// TableName maps Node to at_nodes.
func (Node) TableName() string { return "at_nodes" }

const nodeCols = `id, user_id, name, created_at, max_active_sessions, park_after_s, isolated, isolation, labels,
	last_hello, last_seen_at`

func (s *Store) nodes(ctx context.Context) *gorm.DB { return s.g(ctx).Model(&Node{}).Select(nodeCols) }

func (s *Store) node(g *gorm.DB) (Node, error) {
	var n Node
	err := g.Take(&n).Error
	return n, mapErr(err)
}

// CreateNode registers a node; ErrConflict if the user already has that name.
func (s *Store) CreateNode(ctx context.Context, userID, name, tokenHash string) (Node, error) {
	id := NewHexID()
	err := s.g(ctx).Table("at_nodes").Create(map[string]any{"id": id, "user_id": userID, "name": name,
		"token_hash": tokenHash}).Error
	if err != nil {
		return Node{}, mapErr(err)
	}
	return s.NodeOf(ctx, userID, id)
}

// ListNodes returns the user's nodes in creation order.
func (s *Store) ListNodes(ctx context.Context, userID string) ([]Node, error) {
	out := []Node{}
	err := s.nodes(ctx).Where("user_id = ?", userID).Order("created_at").Find(&out).Error
	return out, err
}

// CountNodes counts the user's nodes (quota).
func (s *Store) CountNodes(ctx context.Context, userID string) (int, error) {
	var n int64
	err := s.g(ctx).Model(&Node{}).Where("user_id = ?", userID).Count(&n).Error
	return int(n), err
}

// NodeOf loads one of the user's nodes.
func (s *Store) NodeOf(ctx context.Context, userID, id string) (Node, error) {
	return s.node(s.nodes(ctx).Where("id = ? AND user_id = ?", id, userID))
}

// NodeByToken identifies a daemon by sha256(token) (node gateway).
func (s *Store) NodeByToken(ctx context.Context, tokenHash string) (Node, error) {
	return s.node(s.nodes(ctx).Where("token_hash = ?", tokenHash))
}

// SetNodeToken rotates the token hash.
func (s *Store) SetNodeToken(ctx context.Context, userID, id, tokenHash string) error {
	return Changed(s.g(ctx).Table("at_nodes").Where("id = ? AND user_id = ?", id, userID).Update("token_hash", tokenHash))
}

// UpdateNode applies settings changes (max_active_sessions, park_after_s, isolated, labels).
func (s *Store) UpdateNode(ctx context.Context, userID, id string, changes []Change) (Node, error) {
	if len(changes) > 0 {
		res := s.g(ctx).Table("at_nodes").Where("id = ? AND user_id = ?", id, userID).Updates(changeMap(changes))
		if err := Changed(res); err != nil {
			return Node{}, err
		}
	}
	return s.NodeOf(ctx, userID, id)
}

// DeleteNode removes one of the user's nodes.
func (s *Store) DeleteNode(ctx context.Context, userID, id string) error {
	return Changed(s.g(ctx).Where("id = ? AND user_id = ?", id, userID).Delete(&Node{}))
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
	out := []NodeSession{}
	err := s.g(ctx).Table("at_agent_sessions a").
		Select("a.id AS session_id, e.name AS employee, p.name AS project, t.no AS thread_no, a.state, a.last_active_at").
		Joins("JOIN at_employees e ON e.id = a.employee_id").Joins("JOIN at_projects p ON p.id = e.project_id").
		Joins("LEFT JOIN at_threads t ON t.id = COALESCE(e.thread_id, a.thread_id)").
		Where("a.node_id = ? AND a.state <> 'closed'", nodeID).Order("a.opened_at").Find(&out).Error
	return out, err
}

// NodeLog is one warn/error line the daemon sent back.
type NodeLog struct {
	ID        int64           `json:"id"`
	Level     string          `json:"level"`
	Msg       string          `json:"msg"`
	Fields    json.RawMessage `json:"fields"`
	CreatedAt time.Time       `json:"created_at"`
}

// TableName maps NodeLog to at_node_logs.
func (NodeLog) TableName() string { return "at_node_logs" }

// NodeLogs pages the node's logs newest first; before = id cursor (0 = newest).
func (s *Store) NodeLogs(ctx context.Context, nodeID, level string, before int64, limit int) ([]NodeLog, error) {
	g := s.g(ctx).Where("node_id = ?", nodeID)
	if level != "" {
		g = g.Where("level = ?", level)
	}
	if before != 0 {
		g = g.Where("id < ?", before)
	}
	out := []NodeLog{}
	err := g.Order("id DESC").Limit(limit).Find(&out).Error
	return out, err
}
