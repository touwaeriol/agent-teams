package engine

import (
	"context"
	"encoding/json"
	"time"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/core"
)

// ContextEvent is the project event "context" (api.md §1.1): a session's context breakdown was
// stored (reason turn or refresh) or a refresh could not read it (error). The web refetches
// GET /api/conversations/{cid}/context/detail. Sent for sessions (多仓库会话) only.
type ContextEvent struct {
	ThreadNo       int        `json:"thread_no"`
	ConversationID string     `json:"conversation_id"`
	SessionID      string     `json:"session_id"`
	Reason         string     `json:"reason"`
	RefreshID      string     `json:"refresh_id,omitempty"`
	Error          string     `json:"error,omitempty"`
	UpdatedAt      *time.Time `json:"updated_at,omitempty"`
}

// sessionContext handles session.context: the breakdown (typed, so unknown fields are dropped) is
// stored on the session; a session's thread hears about it.
func (r *router) sessionContext(ctx context.Context, m *protocol.SessionContext) error {
	e := r.e
	sid := string(m.SessionID)
	ev := ContextEvent{SessionID: sid, Reason: string(m.Reason), RefreshID: strOf(m.RefreshID), Error: strOf(m.Error)}
	if m.Context != nil {
		raw, err := json.Marshal(m.Context)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		if err := core.SetSessionContextDetail(ctx, e.Core.Pool, sid, raw, m.Context.TotalTokens, m.Context.MaxTokens, now); err != nil {
			return err
		}
		ev.UpdatedAt = &now
	}
	place, err := core.SessionPlaceOf(ctx, e.Core.Pool, sid)
	if err != nil || !place.Solo || place.ThreadID == nil || place.ThreadNo == nil {
		return err
	}
	ev.ThreadNo, ev.ConversationID = *place.ThreadNo, *place.ThreadID
	return e.Core.InTx(ctx, func(tx *core.Tx) error {
		_, err := tx.Emit(ctx, place.ProjectID, place.ThreadID, core.EvContext, "", ev)
		return err
	})
}

func strOf(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
