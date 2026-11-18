package engine

import (
	"context"

	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// Route hands a node message to the engine as if it had just passed the gateway's session
// check (a message of a session that closes while it is being handled).
func (e *Engine) Route(ctx context.Context, node store.Node, typ string, body any) error {
	return (&router{e: e}).Handle(ctx, node, typ, body)
}
