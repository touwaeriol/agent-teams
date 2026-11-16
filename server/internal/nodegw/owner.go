package nodegw

import (
	"context"
	"encoding/json"
	"time"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// maxRejectsPerMinute closes a link that keeps naming foreign sessions (protocol.md §2.3).
const maxRejectsPerMinute = 20

// terminal messages about a session that is already closed are dropped quietly.
var terminal = map[string]bool{
	protocol.TypeSessionClosed: true, protocol.TypeSessionState: true, protocol.TypeApprovalCancel: true,
	protocol.TypeToolCancel: true, protocol.TypeApprovalAck: true, protocol.TypeSessionInterrupted: true,
	protocol.TypeSessionDelivered: true, protocol.TypeSessionEvent: true, protocol.TypeStreamDelta: true,
	protocol.TypeSessionsReport: true,
}

type refs struct {
	SessionID *string `json:"session_id"`
	JobID     *string `json:"job_id"`
	OpenGen   *int    `json:"open_gen"`
}

type verdict int

const (
	allowed  verdict = iota
	dropped          // harmless message about a closed session: ack, do not handle
	rejected         // foreign session/job: error + audit
)

// authorize applies the ownership checks; ok=false means "do not hand to the handler".
func (g *Gateway) authorize(ctx context.Context, l *link, env *protocol.Envelope, body any) (any, bool, error) {
	if report, isReport := body.(*protocol.SessionsReport); isReport {
		return g.filterReport(ctx, l, report)
	}
	var r refs
	_ = json.Unmarshal(env.Body, &r)
	v, code, err := g.check(ctx, l.node, env.Type, r)
	if err != nil {
		return nil, false, err
	}
	switch v {
	case dropped:
		return nil, false, nil
	case rejected:
		g.reject(ctx, l, env, code)
		return nil, false, nil
	}
	return body, true, nil
}

func (g *Gateway) check(ctx context.Context, node store.Node, typ string, r refs) (verdict, string, error) {
	if r.SessionID != nil {
		return g.checkSession(ctx, node, typ, *r.SessionID, r.OpenGen)
	}
	if r.JobID != nil {
		return g.checkJob(ctx, node, *r.JobID)
	}
	return allowed, "", nil
}

func (g *Gateway) checkSession(ctx context.Context, node store.Node, typ, sessionID string, gen *int) (verdict, string, error) {
	var nodeID *string
	var state, owner string
	var openGen int
	err := core.MapErr(store.QueryRow(ctx, g.core.Pool, `SELECT a.node_id, a.state, a.open_gen, p.user_id FROM at_agent_sessions a
		JOIN at_employees e ON e.id = a.employee_id JOIN at_projects p ON p.id = e.project_id WHERE a.id = $1`,
		sessionID).Scan(&nodeID, &state, &openGen, &owner))
	if err == core.ErrNotFound {
		return rejected, "unknown_session", nil
	}
	if err != nil {
		return rejected, "", err
	}
	if nodeID == nil || *nodeID != node.ID || owner != node.UserID {
		return rejected, "unknown_session", nil
	}
	if state == "closed" || (gen != nil && *gen < openGen) {
		if terminal[typ] {
			return dropped, "", nil
		}
		return rejected, "unknown_session", nil
	}
	if gen != nil && *gen > openGen {
		return rejected, "unknown_session", nil
	}
	return allowed, "", nil
}

func (g *Gateway) checkJob(ctx context.Context, node store.Node, jobID string) (verdict, string, error) {
	var n int
	err := store.QueryRow(ctx, g.core.Pool, `SELECT count(*) FROM at_node_commands WHERE node_id = $1 AND type LIKE 'git.%'
		AND body->>'job_id' = $2`, node.ID, jobID).Scan(&n)
	if err != nil {
		return rejected, "", err
	}
	if n == 0 {
		return rejected, "unknown_job", nil
	}
	return allowed, "", nil
}

// filterReport keeps only the node's own sessions of a sessions.report.
func (g *Gateway) filterReport(ctx context.Context, l *link, report *protocol.SessionsReport) (any, bool, error) {
	kept := make([]protocol.SessionReportItem, 0, len(report.Sessions))
	for _, item := range report.Sessions {
		gen := int(item.OpenGen)
		v, _, err := g.checkSession(ctx, l.node, protocol.TypeSessionsReport, string(item.SessionID), &gen)
		if err != nil {
			return nil, false, err
		}
		if v == rejected {
			g.log.Warn("sessions.report names a foreign session", "node_id", l.node.ID, "session_id", item.SessionID)
			continue
		}
		kept = append(kept, item)
	}
	return &protocol.SessionsReport{Sessions: kept}, true, nil
}

func (g *Gateway) reject(ctx context.Context, l *link, env *protocol.Envelope, code string) {
	l.sendError(env.ID, code, "message refers to a session or job this node does not own")
	detail, _ := json.Marshal(map[string]string{"type": env.Type, "code": code, "id": env.ID})
	uid := l.node.UserID
	if err := g.store.Audit(ctx, store.AuditEntry{UserID: &uid, Actor: "node:" + l.node.ID, Action: "node_message_rejected",
		TargetType: "node", TargetID: l.node.ID, Detail: detail}); err != nil {
		g.log.Warn("audit node rejection", "err", err)
	}
	if l.noteReject() > maxRejectsPerMinute {
		l.close(CloseUnauthorized, "too many rejected messages")
	}
}

// noteReject counts rejections in the last minute.
func (l *link) noteReject() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	kept := l.rejects[:0]
	for _, t := range l.rejects {
		if now.Sub(t) < time.Minute {
			kept = append(kept, t)
		}
	}
	l.rejects = append(kept, now)
	return len(l.rejects)
}
