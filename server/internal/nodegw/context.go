package nodegw

import (
	"context"
	"errors"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/platform"
)

// FeatureContextDetail: the daemon sends session.context after each Claude turn and answers
// session.context_refresh (protocol.md §4.10 上下文细分).
const FeatureContextDetail = "context_detail"

// ErrContextDetailUnsupported: the session's node runs a daemon without context_detail.
var ErrContextDetailUnsupported = errors.New("节点的守护进程版本太旧，不支持读取上下文细分：请先更新守护进程")

// ContextDetailSupported reports whether the node is online with a daemon that reports context breakdowns.
func (g *Gateway) ContextDetailSupported(nodeID string) bool {
	l := g.link(nodeID)
	return l != nil && hasFeature(l.hello.Features, FeatureContextDetail)
}

// RefreshContext sends session.context_refresh; the answer comes back as session.context (reason
// refresh) through the handler. Unreliable: nothing is queued for an offline node.
func (g *Gateway) RefreshContext(ctx context.Context, nodeID string, body protocol.SessionContextRefresh) error {
	l := g.link(nodeID)
	if l == nil {
		return platform.ErrNodeOffline
	}
	if !hasFeature(l.hello.Features, FeatureContextDetail) {
		return ErrContextDetailUnsupported
	}
	return l.sendUnreliable(ctx, protocol.TypeSessionContextRefresh, body)
}
