package orchestrator

import (
	"context"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// FeatureSessionCompact is the hello feature of daemons that run session.compact (shared-members.md
// §5.3). Their context_usage needs no check: a daemon without it reports no context, so only the
// task count clears.
const FeatureSessionCompact = "session_compact"

// CanCompact reports whether the session's node runs session.compact.
func (o *Orchestrator) CanCompact(ctx context.Context, q core.DB, s core.AgentSession) (bool, error) {
	if s.NodeID == nil {
		return false, nil
	}
	return core.NodeHasFeature(ctx, q, *s.NodeID, FeatureSessionCompact)
}

// Compact asks the session's node to compact it now; session.compacted answers with compactID.
func (o *Orchestrator) Compact(ctx context.Context, tx *core.Tx, s core.AgentSession, compactID, instructions string) error {
	if s.NodeID == nil {
		return ErrNoNode
	}
	body := protocol.SessionCompact{SessionID: protocol.SessionID(s.ID), OpenGen: protocol.OpenGen(s.OpenGen), CompactID: compactID}
	if instructions != "" {
		body.Instructions = &instructions
	}
	_, err := o.gw.Enqueue(ctx, tx, *s.NodeID, nodegw.Command{Type: protocol.TypeSessionCompact, Body: body,
		SessionID: s.ID, OpenGen: s.OpenGen})
	return err
}

// ClearSession closes a member's session so its next task starts a fresh one with a digest of
// the thread (context_clear, continuation).
func (o *Orchestrator) ClearSession(ctx context.Context, tx *core.Tx, s core.AgentSession) error {
	return o.closeSession(ctx, tx, s, CloseContextClear)
}

// wireCloseReason is the session.close reason the node is sent: daemons older than
// context_clear (no session_compact) get switch_thread, which they treat the same way.
func wireCloseReason(ctx context.Context, q core.DB, nodeID, reason string) string {
	if reason != CloseContextClear {
		return reason
	}
	if ok, err := core.NodeHasFeature(ctx, q, nodeID, FeatureSessionCompact); err != nil || !ok {
		return "switch_thread"
	}
	return reason
}

// sessionContext is session.open's context: the role's limit, in-task percentage and focus, and
// the window the employee's last session of the runtime reported (window_tokens: Codex caps the
// limit at it, and without a limit both runtimes take the percentage of it).
func sessionContext(r store.Role, window *int) *protocol.SessionOpenContext {
	if r.ContextLimit == nil && r.CompactInTaskPct == nil && r.CompactFocus == "" {
		return nil
	}
	c := &protocol.SessionOpenContext{LimitTokens: r.ContextLimit, InTaskPct: r.CompactInTaskPct, WindowTokens: window}
	if r.CompactFocus != "" {
		c.Focus = &r.CompactFocus
	}
	return c
}
