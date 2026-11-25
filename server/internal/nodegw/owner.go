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

// afterClose messages are still taken for a session closed meanwhile (same open_gen): the live
// digest of its last turn arrives after a close the turn itself caused (a usage limit, a move).
var afterClose = map[string]bool{protocol.TypeSessionDigest: true, protocol.TypeSessionContext: true}

// terminal messages about a session that is already closed are dropped quietly.
var terminal = map[string]bool{
	protocol.TypeSessionClosed: true, protocol.TypeSessionState: true, protocol.TypeApprovalCancel: true,
	protocol.TypeToolCancel: true, protocol.TypeApprovalAck: true, protocol.TypeSessionInterrupted: true,
	protocol.TypeSessionDelivered: true, protocol.TypeSessionEvent: true, protocol.TypeStreamDelta: true,
	protocol.TypeSessionsReport: true, protocol.TypeSessionModeSet: true, protocol.TypeSessionContext: true,
}

type refs struct {
	SessionID  *string `json:"session_id"`
	JobID      *string `json:"job_id"`
	OpenGen    *int    `json:"open_gen"`
	ExportID   *string `json:"export_id"`
	ApprovalID *string `json:"approval_id"`
	MsgID      *string `json:"msg_id"`
	CompactID  *string `json:"compact_id"`
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
	if typ == protocol.TypeSessionExported {
		// About a session the console already closed (it is being moved): the export must be this node's.
		return g.checkExport(ctx, node, deref(r.ExportID), deref(r.SessionID))
	}
	if typ == protocol.TypeApprovalCancel || typ == protocol.TypeApprovalAck {
		// session_id is optional here: the card's own session decides (#55 N3).
		return g.checkApproval(ctx, node, typ, deref(r.ApprovalID), r.SessionID, r.OpenGen)
	}
	if r.SessionID != nil {
		v, code, s, err := g.checkSessionRow(ctx, node, typ, *r.SessionID, r.OpenGen)
		if err != nil || v != allowed {
			return v, code, err
		}
		// Secondary ids must belong to that session too, not just exist somewhere (#55 N3).
		switch typ {
		case protocol.TypeSessionDelivered:
			return g.checkDelivered(ctx, node, s, deref(r.MsgID))
		case protocol.TypeSessionCompacted:
			return g.checkCompacted(ctx, s, deref(r.CompactID))
		}
		return allowed, "", nil
	}
	if r.JobID != nil {
		return g.checkJob(ctx, node, *r.JobID)
	}
	return allowed, "", nil
}

// sessionInfo is what the secondary-id checks need of an authorized session.
type sessionInfo struct {
	ID        string
	ProjectID string
	Employee  string // the employee's name (tasks are claimed by name)
}

func (g *Gateway) checkSession(ctx context.Context, node store.Node, typ, sessionID string, gen *int) (verdict, string, error) {
	v, code, _, err := g.checkSessionRow(ctx, node, typ, sessionID, gen)
	return v, code, err
}

func (g *Gateway) checkSessionRow(ctx context.Context, node store.Node, typ, sessionID string, gen *int) (verdict, string, sessionInfo, error) {
	var s struct {
		NodeID    *string
		State     string
		OpenGen   int
		UserID    string
		ProjectID string
		Name      string
	}
	err := core.MapErr(g.core.Pool.Gorm(ctx).Table("at_agent_sessions a").
		Select("a.node_id, a.state, a.open_gen, p.user_id, p.id AS project_id, e.name").
		Joins("JOIN at_employees e ON e.id = a.employee_id").Joins("JOIN at_projects p ON p.id = e.project_id").
		Where("a.id = ?", sessionID).Take(&s).Error)
	info := sessionInfo{ID: sessionID, ProjectID: s.ProjectID, Employee: s.Name}
	nodeID, state, openGen, owner := s.NodeID, s.State, s.OpenGen, s.UserID
	if err == core.ErrNotFound {
		return rejected, "unknown_session", info, nil
	}
	if err != nil {
		return rejected, "", info, err
	}
	if nodeID == nil || *nodeID != node.ID || owner != node.UserID {
		return rejected, "unknown_session", info, nil
	}
	if state == "closed" && afterClose[typ] && (gen == nil || *gen == openGen) {
		return allowed, "", info, nil
	}
	if state == "closed" || (gen != nil && *gen < openGen) {
		if terminal[typ] {
			return dropped, "", info, nil
		}
		return rejected, "unknown_session", info, nil
	}
	if gen != nil && *gen > openGen {
		return rejected, "unknown_session", info, nil
	}
	return allowed, "", info, nil
}

// checkApproval: approval.cancel / approval.ack act on a card found by approval_id, so the card's
// session must be one of this node's (and the one the message names, if it names one). An
// approval the console does not know is dropped (the handler would ignore it anyway).
func (g *Gateway) checkApproval(ctx context.Context, node store.Node, typ, approvalID string, sessionID *string, gen *int) (verdict, string, error) {
	var q struct{ SessionID *string }
	err := core.MapErr(g.core.Pool.Gorm(ctx).Table("at_questions").Select("session_id").
		Where("approval_id = ?", approvalID).Take(&q).Error)
	if err == core.ErrNotFound {
		if sessionID != nil {
			if v, code, err := g.checkSession(ctx, node, typ, *sessionID, gen); err != nil || v == rejected {
				return v, code, err
			}
		}
		return dropped, "", nil
	}
	if err != nil {
		return rejected, "", err
	}
	if q.SessionID == nil || (sessionID != nil && *sessionID != *q.SessionID) {
		return rejected, "unknown_session", nil
	}
	return g.checkSession(ctx, node, typ, *q.SessionID, gen)
}

// checkDelivered: the delivered message must be in a conversation of the session's project (or,
// for a conversation without a project, of the node's owner). An unknown msg_id is dropped.
func (g *Gateway) checkDelivered(ctx context.Context, node store.Node, s sessionInfo, msgID string) (verdict, string, error) {
	var c struct {
		ProjectID *string
		UserID    *string
	}
	err := core.MapErr(g.core.Pool.Gorm(ctx).Table("at_messages m").Select("c.project_id, c.user_id").
		Joins("JOIN at_conversations c ON c.id = m.conversation_id").
		Where("m.meta->>'msg_id' = ?", msgID).Take(&c).Error)
	if err == core.ErrNotFound {
		return dropped, "", nil
	}
	if err != nil {
		return rejected, "", err
	}
	if c.ProjectID != nil && *c.ProjectID == s.ProjectID {
		return allowed, "", nil
	}
	if c.ProjectID == nil && c.UserID != nil && *c.UserID == node.UserID {
		return allowed, "", nil
	}
	return rejected, "unknown_session", nil
}

// checkCompacted: the task waiting for compact_id must be claimed by the session's employee in
// its project. No such task is fine (the context is still recorded; the handler skips the task).
func (g *Gateway) checkCompacted(ctx context.Context, s sessionInfo, compactID string) (verdict, string, error) {
	var t struct {
		ClaimedBy string
		ProjectID *string
	}
	err := core.MapErr(g.core.Pool.Gorm(ctx).Table("at_tasks t").Select("t.claimed_by, c.project_id").
		Joins("JOIN at_conversations c ON c.id = t.conversation_id").
		Where("t.compact_id = ? AND t.status = 'claimed'", compactID).Take(&t).Error)
	if err == core.ErrNotFound {
		return allowed, "", nil
	}
	if err != nil {
		return rejected, "", err
	}
	if t.ClaimedBy != s.Employee || t.ProjectID == nil || *t.ProjectID != s.ProjectID {
		return rejected, "unknown_session", nil
	}
	return allowed, "", nil
}

// checkExport: session.exported answers a session.export the console sent this node for that session.
func (g *Gateway) checkExport(ctx context.Context, node store.Node, exportID, sessionID string) (verdict, string, error) {
	var n int64
	err := g.core.Pool.Gorm(ctx).Table("at_session_moves").
		Where("id = ? AND from_node_id = ? AND from_session_id = ?", exportID, node.ID, sessionID).Count(&n).Error
	if err != nil {
		return rejected, "", err
	}
	if n == 0 {
		return rejected, "unknown_session", nil
	}
	return allowed, "", nil
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// JobCredentialWindow is how long after it was queued a git job may still ask for credentials.
// Git steps time out after at most 30 minutes each (node/src/git/jobs.ts); this leaves room for
// a lock wait and a few steps.
const JobCredentialWindow = 2 * time.Hour

// JobLive reports whether nodeID's git job jobID still runs: queued within JobCredentialWindow,
// no git.result yet and not expired or superseded.
func JobLive(ctx context.Context, q core.DB, nodeID, jobID string) (bool, error) {
	var n int64
	err := commands(ctx, q, nodeID).Where("type LIKE 'git.%' AND body->>'job_id' = ? AND status IN ('pending','sent','acked') AND created_at > ?",
		jobID, time.Now().Add(-JobCredentialWindow)).Count(&n).Error
	return n > 0, err
}

// checkJob: the job is one the console sent this node. Whether it still runs matters only for
// credentials (delivery.jobScope answers cred.request with unknown_job, see JobLive); a replayed
// git.result of a finished job is still this node's.
func (g *Gateway) checkJob(ctx context.Context, node store.Node, jobID string) (verdict, string, error) {
	var n int64
	err := commands(ctx, g.core.Pool, node.ID).Where("type LIKE 'git.%' AND body->>'job_id' = ?", jobID).Count(&n).Error
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
