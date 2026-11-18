package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// HandleOpened records session.opened: tool check → session.release, policy violation → close,
// resume_failed → handoff (protocol.md §4.2, architecture.md §10.2).
func (o *Orchestrator) HandleOpened(ctx context.Context, m *protocol.SessionOpened) error {
	return o.core.InTx(ctx, func(tx *core.Tx) error {
		s, err := core.LockSession(ctx, tx, string(m.SessionID))
		if err != nil || s.State == "closed" || int(m.OpenGen) != s.OpenGen {
			return ignoreMissing(err)
		}
		e, err := core.EmployeeByID(ctx, tx, s.EmployeeID)
		if err != nil {
			return err
		}
		if m.ResumeFailed != nil && *m.ResumeFailed {
			return o.handoff(ctx, tx, e, s, nil)
		}
		return o.checkOpened(ctx, tx, e, s, m)
	})
}

func (o *Orchestrator) checkOpened(ctx context.Context, tx *core.Tx, e core.Employee, s core.AgentSession, m *protocol.SessionOpened) error {
	init, _ := json.Marshal(m.Init)
	if err := core.RecordOpened(ctx, tx, s.ID, m.SDKSessionID, m.CodexThreadID, init); err != nil {
		return err
	}
	var tools []string
	if m.Init != nil {
		tools = m.Init.Tools
	}
	if ok, bad := ToolsAllowed(e, tools); !ok {
		return o.policyViolation(ctx, tx, e, s, bad)
	}
	o.touch(tx, e.ID, "session.opened")
	_, err := o.gw.Enqueue(ctx, tx, *s.NodeID, nodegw.Command{Type: protocol.TypeSessionRelease,
		Body:      protocol.SessionRelease{SessionID: protocol.SessionID(s.ID), OpenGen: protocol.OpenGen(s.OpenGen)},
		SessionID: s.ID, OpenGen: s.OpenGen})
	return err
}

// policyViolation closes a session whose tools break its role and raises an error message.
func (o *Orchestrator) policyViolation(ctx context.Context, tx *core.Tx, e core.Employee, s core.AgentSession, tool string) error {
	o.log.Warn("session tools violate the role policy", "session_id", s.ID, "employee", e.Name, "tool", tool)
	if err := o.closeSession(ctx, tx, s, "policy_violation"); err != nil {
		return err
	}
	if _, err := store.Exec(ctx, tx, `INSERT INTO at_audit_log (user_id, actor, action, target_type, target_id, detail)
		SELECT p.user_id, 'system', 'session_policy_violation', 'session', $2, jsonb_build_object('tool', $3::text, 'employee', $4::text)
		FROM at_projects p WHERE p.id = $1`, e.ProjectID, s.ID, tool, e.Name); err != nil {
		return err
	}
	return o.postError(ctx, tx, e, fmt.Sprintf("%s 的会话出现了角色不允许的工具 %s，平台已关闭该会话", e.Name, tool))
}

// postError records an error message in the employee's conversation.
func (o *Orchestrator) postError(ctx context.Context, tx *core.Tx, e core.Employee, text string) error {
	conv, err := o.conversationOf(ctx, tx, e)
	if err != nil {
		return err
	}
	m, err := core.InsertMessage(ctx, tx, core.NewMessage{ConversationID: conv, Role: "error", Author: "platform", Kind: "note",
		Content: text})
	if err != nil {
		return err
	}
	return tx.EmitMessage(ctx, e.ProjectID, e.WorkThreadID(), m)
}

// PostError exposes postError.
func (o *Orchestrator) PostError(ctx context.Context, tx *core.Tx, e core.Employee, text string) error {
	return o.postError(ctx, tx, e, text)
}

func ignoreMissing(err error) error {
	if errors.Is(err, core.ErrNotFound) {
		return nil
	}
	return err
}

// HandleState records session.state.
func (o *Orchestrator) HandleState(ctx context.Context, m *protocol.SessionState) error {
	return o.core.InTx(ctx, func(tx *core.Tx) error {
		s, err := core.LockSession(ctx, tx, string(m.SessionID))
		if err != nil || s.State == "closed" {
			return ignoreMissing(err)
		}
		state := string(m.State)
		if state == "closed" {
			return core.CloseSession(ctx, tx, s.ID, "node_closed")
		}
		if state == "starting" {
			state = "opening"
		}
		if err := core.SetSessionState(ctx, tx, s.ID, state); err != nil {
			return err
		}
		o.touch(tx, s.EmployeeID, "session.state:"+state)
		if state == "crashed" {
			return o.crashed(ctx, tx, s, m)
		}
		if cutTurn(s.State, state) && (m.OpenGen == nil || int(*m.OpenGen) == s.OpenGen) {
			return o.resumeCutTurn(ctx, tx, s)
		}
		return nil
	})
}

func (o *Orchestrator) crashed(ctx context.Context, tx *core.Tx, s core.AgentSession, m *protocol.SessionState) error {
	e, err := core.EmployeeByID(ctx, tx, s.EmployeeID)
	if err != nil {
		return err
	}
	detail := ""
	if m.Detail != nil {
		detail = "：" + *m.Detail
	}
	return o.postError(ctx, tx, e, fmt.Sprintf("%s 的会话崩溃了%s。下一条消息到来时平台会尝试恢复。", e.Name, detail))
}

// HandleDelivered stamps the message as delivered.
func (o *Orchestrator) HandleDelivered(ctx context.Context, m *protocol.SessionDelivered) error {
	return o.core.InTx(ctx, func(tx *core.Tx) error {
		_, _, err := core.MarkDelivered(ctx, tx, string(m.MsgID))
		return err
	})
}

// HandleClosed confirms a close.
func (o *Orchestrator) HandleClosed(ctx context.Context, m *protocol.SessionClosed) error {
	return o.core.InTx(ctx, func(tx *core.Tx) error {
		s, err := core.SessionByID(ctx, tx, string(m.SessionID))
		if err != nil {
			return ignoreMissing(err)
		}
		reason := "node_closed"
		if m.Reason != nil {
			reason = *m.Reason
		}
		return core.CloseSession(ctx, tx, s.ID, reason)
	})
}

// Reconcile compares sessions.report with the database (protocol.md §3):
// sessions that should be open but are missing are reopened with resume;
// sessions the database closed (or older generations) are closed on the node.
func (o *Orchestrator) Reconcile(ctx context.Context, node store.Node, r *protocol.SessionsReport) error {
	reported := map[string]protocol.SessionReportItem{}
	for _, it := range r.Sessions {
		reported[string(it.SessionID)] = it
	}
	return o.core.InTx(ctx, func(tx *core.Tx) error {
		if err := o.closeStale(ctx, tx, node, r.Sessions); err != nil {
			return err
		}
		live, err := core.NodeLiveSessions(ctx, tx, node.ID)
		if err != nil {
			return err
		}
		for _, s := range live {
			if err := o.reconcileOne(ctx, tx, s, reported); err != nil {
				return err
			}
		}
		if err := o.closeEnded(ctx, tx, node.ID); err != nil {
			return err
		}
		return o.wakeStalledThreads(ctx, tx, node)
	})
}

func (o *Orchestrator) closeStale(ctx context.Context, tx *core.Tx, node store.Node, items []protocol.SessionReportItem) error {
	for _, it := range items {
		s, err := core.SessionByID(ctx, tx, string(it.SessionID))
		if err != nil {
			continue
		}
		stale := s.State == "closed" || int(it.OpenGen) < s.OpenGen
		if !stale || it.State == "closed" {
			continue
		}
		gen := it.OpenGen
		r := protocol.SessionCloseReason("employee_removed")
		if _, err := o.gw.Enqueue(ctx, tx, node.ID, nodegw.Command{Type: protocol.TypeSessionClose,
			Body: protocol.SessionClose{SessionID: it.SessionID, OpenGen: &gen, Reason: &r}, SessionID: s.ID, OpenGen: int(gen)}); err != nil {
			return err
		}
	}
	return nil
}

// closeEnded closes the node's sessions that still belong to finished or archived threads
// (left parked by a daemon restart, or from before archive closed them): they are not
// resumed, their node slots are freed and the employees stay idle until unarchived.
func (o *Orchestrator) closeEnded(ctx context.Context, tx *core.Tx, nodeID string) error {
	var stale []core.AgentSession
	err := tx.Gorm(ctx).Table("at_agent_sessions a").Select("a.*").Joins("JOIN at_employees e ON e.id = a.employee_id").
		Joins("JOIN at_threads t ON t.id = COALESCE(e.thread_id, a.thread_id)").
		Where("a.node_id = ? AND a.state <> 'closed' AND t.status IN ('done','failed','archived')", nodeID).Find(&stale).Error
	if err != nil {
		return err
	}
	return o.closeAll(ctx, tx, stale, "thread_done")
}

// reconcileOne brings one session in line with the report; a turn that the daemon's
// restart cut off is resumed. Sessions of finished threads are left to closeEnded.
func (o *Orchestrator) reconcileOne(ctx context.Context, tx *core.Tx, s core.AgentSession, reported map[string]protocol.SessionReportItem) error {
	e, err := core.EmployeeByID(ctx, tx, s.EmployeeID)
	if err != nil {
		return err
	}
	if ended, err := o.threadEnded(ctx, tx, e); err != nil || ended {
		return err
	}
	it, ok := reported[s.ID]
	if ok && int(it.OpenGen) == s.OpenGen && it.State != "closed" {
		state := string(it.State)
		if state != "turn_running" && state != "parked" && state != "active" {
			return nil
		}
		if err := core.SetSessionState(ctx, tx, s.ID, state); err != nil {
			return err
		}
		if cutTurn(s.State, state) {
			return o.resumeCutTurn(ctx, tx, s)
		}
		return nil
	}
	if s.State == "opening" && hasPendingOpen(ctx, tx, s) {
		return nil // the replayed session.open is on its way
	}
	if err := o.reopen(ctx, tx, e, s, nil); err != nil {
		return err
	}
	if s.State == "turn_running" {
		return o.resumeCutTurn(ctx, tx, s)
	}
	return nil
}

func hasPendingOpen(ctx context.Context, tx *core.Tx, s core.AgentSession) bool {
	var n int64
	_ = tx.Gorm(ctx).Table("at_node_commands").Where(`session_id = ? AND open_gen = ? AND type = 'session.open'
		AND status IN ('pending','sent')`, s.ID, s.OpenGen).Count(&n).Error
	return n > 0
}

// NodeChanged recomputes the status of employees whose sessions run on the node.
func (o *Orchestrator) NodeChanged(ctx context.Context, nodeID string) {
	live, err := core.NodeLiveSessions(ctx, o.core.Pool, nodeID)
	if err != nil {
		return
	}
	for _, s := range live {
		o.status.Observe(ctx, s.EmployeeID, statusTouch("node"))
	}
}
