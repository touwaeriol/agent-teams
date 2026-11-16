package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	protocol "github.com/touwaeriol/agent-teams/protocol"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/orchestrator"
	"github.com/touwaeriol/agent-teams/server/internal/status"
	"github.com/touwaeriol/agent-teams/server/internal/store"
	"github.com/touwaeriol/agent-teams/server/internal/timers"
)

// router dispatches daemon messages to the engines (nodegw.Handler).
type router struct{ e *Engine }

// Handle implements nodegw.Handler.
func (r *router) Handle(ctx context.Context, node store.Node, typ string, body any) error {
	e := r.e
	switch m := body.(type) {
	case *protocol.SessionOpened:
		return e.Orch.HandleOpened(ctx, m)
	case *protocol.SessionState:
		return r.sessionState(ctx, m)
	case *protocol.SessionDelivered:
		return r.delivered(ctx, m)
	case *protocol.SessionClosed:
		return e.Orch.HandleClosed(ctx, m)
	case *protocol.SessionEvent:
		return r.sessionEvent(ctx, m)
	case *protocol.SessionsReport:
		return r.report(ctx, node, m)
	case *protocol.ApprovalRequest:
		return e.Approvals.Request(ctx, m)
	case *protocol.ApprovalCancel:
		return e.Approvals.Cancel(ctx, m)
	case *protocol.ToolCall:
		return e.Tools.HandleCall(ctx, m)
	case *protocol.ToolCancel:
		return e.Tools.HandleCancel(ctx, m)
	case *protocol.FileChunk:
		return e.Tools.HandleChunk(ctx, m)
	case *protocol.GitResult:
		return e.Delivery.JobResult(ctx, node, m)
	case *protocol.CredRequest:
		return e.Delivery.Credential(ctx, node, m)
	case *protocol.StreamDelta:
		e.Events.Delta(string(m.SessionID), m.Text)
		return nil
	}
	return r.other(ctx, node, typ, body)
}

func (r *router) other(ctx context.Context, node store.Node, typ string, body any) error {
	switch m := body.(type) {
	case *protocol.ApprovalAck:
		if m.Stale {
			r.e.log.Info("approval resolve was stale on the node", "approval_id", m.ApprovalID)
		}
	case *protocol.SessionInterrupted:
		r.e.log.Debug("session interrupted", "session_id", m.SessionID)
	case *protocol.NodeUpgradeResult:
		r.e.log.Warn("node upgrade result", "node_id", node.ID, "ok", m.Ok, "error", m.Error)
	default:
		r.e.log.Debug("unhandled node message", "type", typ)
	}
	return nil
}

// NodeOnline implements nodegw.Handler.
func (r *router) NodeOnline(ctx context.Context, node store.Node) { r.nodeChanged(ctx, node, true) }

// NodeOffline implements nodegw.Handler.
func (r *router) NodeOffline(ctx context.Context, node store.Node) { r.nodeChanged(ctx, node, false) }

// nodeChanged recomputes employee states and emits "node" on the user stream and on the
// streams of the projects whose employees use the node.
func (r *router) nodeChanged(ctx context.Context, node store.Node, online bool) {
	r.e.Orch.NodeChanged(ctx, node.ID)
	brief := map[string]any{"node": map[string]any{"id": node.ID, "name": node.Name, "online": online}}
	r.e.Events.User(ctx, node.UserID, core.EvNode, brief)
	rows, err := r.e.Core.Pool.Query(ctx, `SELECT DISTINCT project_id FROM at_employees WHERE node_id = $1`, node.ID)
	if err != nil {
		return
	}
	var projects []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			projects = append(projects, id)
		}
	}
	rows.Close()
	for _, id := range projects {
		r.e.Events.Project(ctx, id, core.EvNode, brief)
	}
}

// sessionState records the state; crashed/parked/closed sessions lose their pending cards (§3, §5.3).
func (r *router) sessionState(ctx context.Context, m *protocol.SessionState) error {
	st := string(m.State)
	if st == "parked" {
		// A session parks with pending requests only when the daemon shuts down: its cards
		// are detached before the state change resumes the interrupted turn.
		if err := r.e.Core.InTx(ctx, func(tx *core.Tx) error {
			return r.e.Approvals.SessionRestarted(ctx, tx, string(m.SessionID), nil)
		}); err != nil {
			return err
		}
	}
	if err := r.e.Orch.HandleState(ctx, m); err != nil {
		return err
	}
	if st != "crashed" && st != "closed" {
		return nil
	}
	return r.e.Core.InTx(ctx, func(tx *core.Tx) error {
		if err := r.e.Approvals.SessionGone(ctx, tx, string(m.SessionID), nil); err != nil {
			return err
		}
		if st != "crashed" {
			return nil
		}
		sc, err := core.SessionContextOf(ctx, tx, string(m.SessionID))
		if err != nil {
			return ignoreMissing(err)
		}
		return r.e.Sched.SessionLost(ctx, tx, sc.Employee)
	})
}

func ignoreMissing(err error) error {
	if err == core.ErrNotFound {
		return nil
	}
	return err
}

// delivered stamps the message and starts the task it carried.
func (r *router) delivered(ctx context.Context, m *protocol.SessionDelivered) error {
	return r.e.Core.InTx(ctx, func(tx *core.Tx) error {
		msg, fresh, err := core.MarkDelivered(ctx, tx, string(m.MsgID))
		if err != nil || !fresh {
			return err
		}
		return r.e.Sched.TaskDelivered(ctx, tx, msg)
	})
}

// report reconciles sessions, cards and calls with sessions.report: cards the daemon no
// longer has are detached (the resumed employee re-issues them), its lost calls are
// cancelled, then sessions are reconciled and interrupted turns resumed (protocol.md §3).
func (r *router) report(ctx context.Context, node store.Node, m *protocol.SessionsReport) error {
	items := map[string]protocol.SessionReportItem{}
	for _, it := range m.Sessions {
		items[string(it.SessionID)] = it
	}
	err := r.e.Core.InTx(ctx, func(tx *core.Tx) error {
		live, err := core.NodeLiveSessions(ctx, tx, node.ID)
		if err != nil {
			return err
		}
		for _, s := range live {
			if err := r.reconcileItem(ctx, tx, s.ID, items[s.ID]); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return r.e.Orch.Reconcile(ctx, node, m)
}

func (r *router) reconcileItem(ctx context.Context, tx *core.Tx, sessionID string, it protocol.SessionReportItem) error {
	approvals := make([]string, len(it.PendingApprovals))
	for i, a := range it.PendingApprovals {
		approvals[i] = string(a)
	}
	calls := make([]string, len(it.PendingToolCalls))
	for i, c := range it.PendingToolCalls {
		calls[i] = string(c)
	}
	if err := r.e.Approvals.SessionRestarted(ctx, tx, sessionID, approvals); err != nil {
		return err
	}
	return r.e.Tools.Reconcile(ctx, tx, sessionID, calls)
}

// sessionEvent turns a session fact into status, activity, replies, usage and nudges (§11.2).
func (r *router) sessionEvent(ctx context.Context, ev *protocol.SessionEvent) error {
	var obs []status.Observation
	var empID string
	err := r.e.Core.InTx(ctx, func(tx *core.Tx) error {
		fresh, err := core.TouchSessionEvent(ctx, tx, string(ev.SessionID), int64(ev.EventNo))
		if err != nil || !fresh {
			return err
		}
		sc, err := core.SessionContextOf(ctx, tx, string(ev.SessionID))
		if err != nil {
			return ignoreMissing(err)
		}
		empID = sc.Employee.ID
		obs, err = r.applyEvent(ctx, tx, sc, ev)
		return err
	})
	for _, o := range obs {
		r.e.Status.Observe(ctx, empID, o)
	}
	return err
}

func (r *router) applyEvent(ctx context.Context, tx *core.Tx, sc core.SessionContext, ev *protocol.SessionEvent) ([]status.Observation, error) {
	cause := string(ev.Kind)
	switch ev.Kind {
	case "turn_started":
		return []status.Observation{{Kind: status.TurnStarted, Cause: cause}}, r.checkTools(ctx, tx, sc, ev.Tools)
	case "tool_started":
		return r.toolStarted(ctx, tx, sc, ev)
	case "tool_ended":
		return []status.Observation{{Kind: status.ToolEnded, Cause: cause}}, nil
	case "turn_ended":
		return []status.Observation{{Kind: status.TurnEnded, Cause: cause}}, r.turnEnded(ctx, tx, sc, ev)
	case "text":
		return nil, r.text(ctx, tx, sc, ev)
	case "rate_limited":
		return nil, r.rateLimited(ctx, tx, sc, ev)
	case "error":
		return []status.Observation{{Kind: status.Touch, Cause: "error"}}, r.sessionError(ctx, tx, sc, ev)
	case "todos":
		return []status.Observation{{Kind: status.Touch, Cause: cause}}, r.e.Orch.RecordTodos(ctx, tx, sc.Employee, ev.Todos)
	case "plan":
		return []status.Observation{{Kind: status.Touch, Cause: cause}}, r.e.Orch.RecordPlan(ctx, tx, sc.Employee, val(ev.Plan))
	}
	return []status.Observation{{Kind: status.Touch, Cause: cause}}, nil
}

func val(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// checkTools re-checks the per-turn tool list against the role (§10.2).
func (r *router) checkTools(ctx context.Context, tx *core.Tx, sc core.SessionContext, tools []string) error {
	if len(tools) == 0 {
		return nil
	}
	if ok, bad := orchestrator.ToolsAllowed(sc.Employee, tools); !ok {
		r.e.log.Warn("turn tools violate the role policy", "session_id", sc.Session.ID, "tool", bad)
		if err := r.e.Orch.CloseSession(ctx, tx, sc.Session, "policy_violation"); err != nil {
			return err
		}
		return r.e.Orch.PostError(ctx, tx, sc.Employee, fmt.Sprintf("%s 的会话出现了不允许的工具 %s，已关闭", sc.Employee.Name, bad))
	}
	return nil
}

func (r *router) toolStarted(ctx context.Context, tx *core.Tx, sc core.SessionContext, ev *protocol.SessionEvent) ([]status.Observation, error) {
	tool := val(ev.Tool)
	cat := val(ev.Category)
	if cat == "" {
		cat = status.Category(tool)
	}
	o := status.Observation{Kind: status.ToolStarted, Cause: "tool_started:" + tool, Category: cat, Sub: val(ev.Subcategory),
		Summary: val(ev.Summary)}
	if cat == "platform" || o.Summary == "" {
		return []status.Observation{o}, nil
	}
	text := fmt.Sprintf("%s %s", tool, o.Summary)
	return []status.Observation{o}, tx.EmitActivity(ctx, sc.Employee.ProjectID, sc.Employee.ThreadID, sc.Employee.Name, text)
}

// turnEnded records usage and nudges a member whose task is still running.
// An employee waiting for its usage limit to reset is not nudged: its thread is paused.
func (r *router) turnEnded(ctx context.Context, tx *core.Tx, sc core.SessionContext, ev *protocol.SessionEvent) error {
	if err := recordUsage(ctx, tx, sc, ev); err != nil {
		return err
	}
	if err := r.e.Approvals.ExpireDetached(ctx, tx, sc.Employee); err != nil {
		return err
	}
	waiting, err := timers.Waiting(ctx, tx, sc.Employee.ID)
	if err != nil || waiting {
		return err
	}
	return r.e.Sched.TurnEnded(ctx, tx, sc.Employee)
}

// rateLimited logs the limit as activity; a usage limit (reset minutes or hours away)
// also pauses the employee's thread until then (data-model.md §2.10).
func (r *router) rateLimited(ctx context.Context, tx *core.Tx, sc core.SessionContext, ev *protocol.SessionEvent) error {
	e := sc.Employee
	if err := tx.EmitActivity(ctx, e.ProjectID, e.ThreadID, e.Name, "限速中："+val(ev.Detail)); err != nil {
		return err
	}
	if !timers.IsUsageLimit(ev.RetryAt, time.Now()) {
		return nil
	}
	return timers.RecordLimit(ctx, tx, r.e.Orch, e, *ev.RetryAt, val(ev.Detail))
}

func recordUsage(ctx context.Context, tx *core.Tx, sc core.SessionContext, ev *protocol.SessionEvent) error {
	turn := fmt.Sprintf("e%d", ev.EventNo)
	if ev.TurnID != nil {
		turn = string(*ev.TurnID)
	}
	var usage map[string]any
	if ev.Usage != nil {
		_ = json.Unmarshal(*ev.Usage, &usage)
	}
	_, err := tx.Exec(ctx, `INSERT INTO at_usage (session_id, project_id, thread_id, turn_id, cost_usd, input_tokens,
		output_tokens, cache_read_tokens, cache_write_tokens, duration_ms) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (session_id, turn_id) DO NOTHING`, sc.Session.ID, sc.Employee.ProjectID, sc.Employee.ThreadID, turn,
		ev.CostUSD, num(usage, "input_tokens"), num(usage, "output_tokens"), num(usage, "cache_read_input_tokens"),
		num(usage, "cache_creation_input_tokens"), ev.DurationMS)
	return err
}

func num(m map[string]any, key string) *int64 {
	f, ok := m[key].(float64)
	if !ok {
		return nil
	}
	n := int64(f)
	return &n
}

// text: final replies of the coordinator and leads become conversation messages; members' are activity.
func (r *router) text(ctx context.Context, tx *core.Tx, sc core.SessionContext, ev *protocol.SessionEvent) error {
	if ev.Final == nil || !*ev.Final || ev.Text == nil {
		return nil
	}
	e := sc.Employee
	if e.Tier == "coordinator" || e.Tier == "lead" {
		return r.e.Orch.RecordReply(ctx, tx, e, *ev.Text)
	}
	return tx.EmitActivity(ctx, e.ProjectID, e.ThreadID, e.Name, clip(*ev.Text, 300))
}

func (r *router) sessionError(ctx context.Context, tx *core.Tx, sc core.SessionContext, ev *protocol.SessionEvent) error {
	if ev.Fatal == nil || !*ev.Fatal {
		return nil
	}
	code := ""
	if ev.Code != nil {
		code = string(*ev.Code)
	}
	return r.e.Orch.PostError(ctx, tx, sc.Employee, fmt.Sprintf("%s 的会话出错（%s）：%s", sc.Employee.Name, code, val(ev.Message)))
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
