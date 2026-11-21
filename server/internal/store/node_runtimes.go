package store

import (
	"context"
	"time"

	"gorm.io/gorm/clause"
)

// NodeRuntime is one at_node_runtimes row: the install and login state of a runtime CLI on a
// node as the daemon last reported it (node.runtimes, protocol.md §9.2). Never holds credentials.
type NodeRuntime struct {
	NodeID        string     `json:"-" gorm:"primaryKey"`
	Runtime       string     `json:"runtime" gorm:"primaryKey" doc:"Runtime id (claude, codex, …)"`
	Installed     bool       `json:"installed" doc:"The CLI was found and runs"`
	Version       string     `json:"version,omitempty"`
	Path          string     `json:"path,omitempty" doc:"Executable the daemon found"`
	Auth          string     `json:"auth" enum:"subscription,chatgpt,api_key,none,unknown" doc:"How the runtime is logged in"`
	AuthExpiresAt *time.Time `json:"auth_expires_at,omitempty" doc:"When the login expires, when the daemon can tell"`
	Expired       bool       `json:"expired" doc:"The login is known to be expired"`
	Error         string     `json:"error,omitempty" doc:"Why the check failed (no credentials)"`
	CheckedAt     time.Time  `json:"checked_at" doc:"When the daemon checked"`
	UpdatedAt     time.Time  `json:"updated_at" doc:"When the console last stored a change"`
	// ReadyNow is Ready at the time of the API call (not stored).
	ReadyNow bool `json:"ready" gorm:"-" doc:"Installed, logged in and not expired: sessions of this runtime may be scheduled on the node"`
}

// TableName maps NodeRuntime to at_node_runtimes.
func (NodeRuntime) TableName() string { return "at_node_runtimes" }

// Ready: sessions of the runtime may be placed on the node (installed, logged in, not expired).
func (r NodeRuntime) Ready(now time.Time) bool {
	if !r.Installed || r.Expired || r.Auth == "none" || r.Auth == "unknown" || r.Auth == "" {
		return false
	}
	return r.AuthExpiresAt == nil || r.AuthExpiresAt.After(now)
}

// Same reports whether two reports of a runtime say the same thing (checked_at aside).
func (r NodeRuntime) Same(o NodeRuntime) bool {
	return r.Runtime == o.Runtime && r.Installed == o.Installed && r.Version == o.Version && r.Path == o.Path &&
		r.Auth == o.Auth && r.Expired == o.Expired && r.Error == o.Error && sameTime(r.AuthExpiresAt, o.AuthExpiresAt)
}

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

// NodeRuntimes loads the stored runtime states of the nodes, by node id, each list ordered by runtime.
func (s *Store) NodeRuntimes(ctx context.Context, nodeIDs []string) (map[string][]NodeRuntime, error) {
	out := map[string][]NodeRuntime{}
	if len(nodeIDs) == 0 {
		return out, nil
	}
	var rows []NodeRuntime
	if err := s.g(ctx).Where("node_id IN ?", nodeIDs).Order("node_id, runtime").Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.NodeID] = append(out[r.NodeID], r)
	}
	return out, nil
}

// ReplaceNodeRuntimes makes rows the node's runtime states (a report carries every runtime the
// daemon knows): rows whose content did not change only get the new checked_at. It returns the
// states before the report. Run it in a transaction (the node's rows are locked first).
func (s *Store) ReplaceNodeRuntimes(ctx context.Context, nodeID string, rows []NodeRuntime) ([]NodeRuntime, error) {
	var before []NodeRuntime
	if err := s.g(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("node_id = ?", nodeID).Order("runtime").
		Find(&before).Error; err != nil {
		return nil, err
	}
	keep := make([]string, 0, len(rows))
	now := time.Now().UTC()
	for _, r := range rows {
		keep = append(keep, r.Runtime)
		r.NodeID, r.UpdatedAt = nodeID, now
		if old, ok := findRuntime(before, r.Runtime); ok && old.Same(r) {
			r.UpdatedAt = old.UpdatedAt
		}
		if err := s.g(ctx).Clauses(clause.OnConflict{UpdateAll: true}).Create(&r).Error; err != nil {
			return nil, err
		}
	}
	del := s.g(ctx).Where("node_id = ?", nodeID)
	if len(keep) > 0 {
		del = del.Where("runtime NOT IN ?", keep)
	}
	return before, del.Delete(&NodeRuntime{}).Error
}

func findRuntime(rows []NodeRuntime, runtime string) (NodeRuntime, bool) {
	for _, r := range rows {
		if r.Runtime == runtime {
			return r, true
		}
	}
	return NodeRuntime{}, false
}
