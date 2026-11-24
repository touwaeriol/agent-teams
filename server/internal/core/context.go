package core

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// SessionContextDetail is the latest context breakdown a session reported (session.context).
type SessionContextDetail struct {
	Detail json.RawMessage // ContextDetail JSON; nil = never reported
	At     *time.Time
}

// SetSessionContextDetail stores a session's latest context breakdown and its totals (the totals
// also come with turn_ended; a refresh brings newer ones), unless the session already holds one
// sampled later (a slow full refresh overtaken by a turn's summary): stored=false, nothing changes.
// at is when the breakdown was sampled. Taken for a closed session too.
func SetSessionContextDetail(ctx context.Context, q DB, sessionID string, detail json.RawMessage, used, max int, at time.Time) (bool, error) {
	res := q.Gorm(ctx).Table("at_agent_sessions").Where("id = ? AND (context_detail_at IS NULL OR context_detail_at <= ?)", sessionID, at).
		Updates(map[string]any{"context_detail": detail, "context_detail_at": at, "context_used": used, "context_max": max})
	return res.RowsAffected > 0, res.Error
}

// ContextDetailOf loads a session's latest context breakdown.
func ContextDetailOf(ctx context.Context, q DB, sessionID string) (SessionContextDetail, error) {
	var rows []struct {
		ContextDetail   []byte
		ContextDetailAt *time.Time
	}
	err := q.Gorm(ctx).Table("at_agent_sessions").Select("context_detail, context_detail_at").Where("id = ?", sessionID).Scan(&rows).Error
	if err != nil {
		return SessionContextDetail{}, err
	}
	if len(rows) == 0 {
		return SessionContextDetail{}, ErrNotFound
	}
	return SessionContextDetail{Detail: rows[0].ContextDetail, At: rows[0].ContextDetailAt}, nil
}

// SessionPlace is where a session's events go: its project and thread (nil thread: coordinator).
type SessionPlace struct {
	ProjectID string
	ThreadID  *string
	ThreadNo  *int
	Solo      bool
}

// SessionPlaceOf finds the project and thread of a session.
func SessionPlaceOf(ctx context.Context, q DB, sessionID string) (SessionPlace, error) {
	var rows []struct {
		ProjectID string
		ThreadID  *string
	}
	err := q.Gorm(ctx).Table("at_agent_sessions s").Select("e.project_id, s.thread_id").
		Joins("JOIN at_employees e ON e.id = s.employee_id").Where("s.id = ?", sessionID).Scan(&rows).Error
	if err != nil {
		return SessionPlace{}, err
	}
	if len(rows) == 0 {
		return SessionPlace{}, ErrNotFound
	}
	p := SessionPlace{ProjectID: rows[0].ProjectID, ThreadID: rows[0].ThreadID}
	if p.ThreadID == nil {
		return p, nil
	}
	t, err := ThreadByID(ctx, q, *p.ThreadID)
	if errors.Is(err, ErrNotFound) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	p.ThreadNo, p.Solo = &t.No, t.Solo()
	return p, nil
}
