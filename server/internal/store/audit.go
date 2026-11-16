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

// TableName maps AuditEntry to at_audit_log.
func (AuditEntry) TableName() string { return "at_audit_log" }

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
	return s.g(ctx).Table("at_audit_log").Create(map[string]any{"user_id": e.UserID, "actor": e.Actor,
		"action": e.Action, "target_type": e.TargetType, "target_id": e.TargetID, "detail": []byte(detail), "ip": ip}).Error
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
	g := s.g(ctx).Select("id, user_id, actor, action, target_type, target_id, detail, host(ip) AS ip, created_at")
	if f.UserID != "" {
		g = g.Where("user_id = ?", f.UserID)
	}
	if f.Action != "" {
		g = g.Where("action = ?", f.Action)
	}
	if f.Before != 0 {
		g = g.Where("id < ?", f.Before)
	}
	out := []AuditEntry{}
	err := g.Order("id DESC").Limit(f.Limit).Find(&out).Error
	return out, err
}
