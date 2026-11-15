package store

import (
	"context"
	"encoding/json"
	"net/netip"
	"time"
)

// AuditEntry is a row of at_audit_log.
type AuditEntry struct {
	ID         int64           `json:"id"`
	UserID     *string         `json:"user_id"`
	Actor      string          `json:"actor"`
	Action     string          `json:"action"`
	TargetType string          `json:"target_type"`
	TargetID   string          `json:"target_id"`
	Detail     json.RawMessage `json:"detail"`
	IP         *string         `json:"ip"`
	CreatedAt  time.Time       `json:"created_at"`
}

// Audit records an action. ip may be "" when unknown.
func (s *Store) Audit(ctx context.Context, e AuditEntry) error {
	detail := e.Detail
	if len(detail) == 0 {
		detail = json.RawMessage(`{}`)
	}
	var ip *netip.Addr
	if e.IP != nil {
		if addr, err := netip.ParseAddr(*e.IP); err == nil {
			ip = &addr
		}
	}
	_, err := s.db.Exec(ctx, `INSERT INTO at_audit_log (user_id, actor, action, target_type, target_id, detail, ip)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`, e.UserID, e.Actor, e.Action, e.TargetType, e.TargetID, detail, ip)
	return err
}

// AuditFilter narrows the admin audit listing.
type AuditFilter struct {
	UserID string
	Action string
	Before int64 // id cursor, 0 = newest
	Limit  int
}

// ListAudit pages the audit log newest first.
func (s *Store) ListAudit(ctx context.Context, f AuditFilter) ([]AuditEntry, error) {
	rows, err := s.db.Query(ctx, `SELECT id, user_id, actor, action, target_type, target_id, detail, host(ip), created_at
		FROM at_audit_log WHERE ($1 = '' OR user_id = $1) AND ($2 = '' OR action = $2) AND ($3 = 0 OR id < $3)
		ORDER BY id DESC LIMIT $4`, f.UserID, f.Action, f.Before, f.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditEntry{}
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.ID, &e.UserID, &e.Actor, &e.Action, &e.TargetType, &e.TargetID, &e.Detail, &e.IP, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
