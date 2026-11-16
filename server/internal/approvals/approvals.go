// Package approvals turns the daemon's approval requests (canUseTool, Codex
// requestApproval) into decisions (architecture.md §9): the fixed deny list,
// bypass roles, the thread's "allow everything", remembered rules (exact tool +
// input hash only — Claude's own matcher handles rule strings), the read-only
// allow list, else a card in at_questions that survives disconnects: first for
// the thread lead (review.go), then for the user. Answers become
// approval.resolve commands in the node outbox.
package approvals

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"gorm.io/gorm"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw"
	"github.com/touwaeriol/agent-teams/server/internal/orchestrator"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// Errors for the REST layer.
var (
	ErrNotApproval = errors.New("这不是审批卡片")
	ErrAnswered    = errors.New("已经处理过了")
	ErrBadAnswer   = errors.New("审批只能回答 allow、allow_always 或 deny")
)

// decided_by values must match the protocol pattern (user:… | policy:thread_auto |
// policy:rule:… | policy:timeout), so the built-in policies are named as rules.
const (
	DecidedDenyList = "policy:rule:builtin-deny-list"
	DecidedRoleSkip = "policy:rule:builtin-skip-permissions" // bypass role (kept name for existing records)
)

// Engine decides approvals.
type Engine struct {
	core *core.Store
	orch *orchestrator.Orchestrator
	gw   *nodegw.Gateway
	log  *slog.Logger
}

// New builds the engine.
func New(cs *core.Store, orch *orchestrator.Orchestrator, gw *nodegw.Gateway, log *slog.Logger) *Engine {
	return &Engine{core: cs, orch: orch, gw: gw, log: log}
}

// InputHash is sha256 of the tool name and the canonical JSON of its input.
func InputHash(tool string, input json.RawMessage) string {
	var v any
	_ = json.Unmarshal(input, &v)
	canon, _ := json.Marshal(v)
	sum := sha256.Sum256(append([]byte(tool+"\n"), canon...))
	return hex.EncodeToString(sum[:])
}

// Request stores an approval card (idempotent by approval_id) and applies the policy.
func (e *Engine) Request(ctx context.Context, m *protocol.ApprovalRequest) error {
	return e.core.InTx(ctx, func(tx *core.Tx) error {
		existing, err := core.QuestionByApproval(ctx, tx, string(m.ApprovalID))
		if err == nil {
			return e.replay(ctx, tx, existing)
		}
		if !errors.Is(err, core.ErrNotFound) {
			return err
		}
		sc, err := core.SessionContextOf(ctx, tx, string(m.SessionID))
		if err != nil {
			return err
		}
		if ok, err := e.reattach(ctx, tx, sc, m); ok || err != nil {
			return err
		}
		q, err := e.insertCard(ctx, tx, sc, m)
		if err != nil {
			return err
		}
		return e.decide(ctx, tx, sc, q, m)
	})
}

// replay re-sends the decision of an already decided card (daemon reconnected).
func (e *Engine) replay(ctx context.Context, tx *core.Tx, q core.Question) error {
	if q.Status != "answered" || q.Answer == nil {
		return nil
	}
	return e.sendResolve(ctx, tx, q, *q.Answer, q.DecidedBy, "")
}

func (e *Engine) insertCard(ctx context.Context, tx *core.Tx, sc core.SessionContext, m *protocol.ApprovalRequest) (core.Question, error) {
	conv, err := e.orch.ConversationOf(ctx, tx, sc.Employee)
	if err != nil {
		return core.Question{}, err
	}
	var input any
	_ = json.Unmarshal(m.Input, &input)
	detail := map[string]any{"tool_name": m.ToolName, "input": input, "source": string(m.Source),
		"input_hash": InputHash(m.ToolName, m.Input), "match_hash": MatchHash(m.ToolName, m.Input), "suggestions": m.Suggestions}
	addOpt(detail, "title", m.Title)
	addOpt(detail, "description", m.Description)
	addOpt(detail, "decision_reason", m.DecisionReason)
	addOpt(detail, "blocked_path", m.BlockedPath)
	addOpt(detail, "diff", m.Diff)
	text := fmt.Sprintf("%s 请求使用 %s", sc.Employee.Name, m.ToolName)
	if m.Title != nil && *m.Title != "" {
		text = *m.Title
	}
	pid, aid, sid := sc.Employee.ProjectID, string(m.ApprovalID), sc.Session.ID
	var taskID *string
	if sc.Employee.ThreadID != nil {
		if t, err := core.CurrentTask(ctx, tx, *sc.Employee.ThreadID, sc.Employee.Name); err == nil {
			taskID = &t.ID
		}
	}
	return core.InsertQuestion(ctx, tx, core.NewQuestion{ConversationID: conv, ProjectID: pid, ThreadID: sc.Employee.ThreadID,
		Asker: sc.Employee.Name, TaskID: taskID, Audience: "user", Kind: "approval", Text: text,
		Options: availableOf(m), Detail: detail, SessionID: &sid, ApprovalID: &aid})
}

func availableOf(m *protocol.ApprovalRequest) []string {
	if len(m.Available) == 0 {
		return []string{"allow", "allow_always", "deny"}
	}
	out := make([]string, len(m.Available))
	for i, a := range m.Available {
		out[i] = string(a)
	}
	return out
}

func addOpt(m map[string]any, key string, v *string) {
	if v != nil && *v != "" {
		m[key] = *v
	}
}

// decide applies the policy order of approvals.Decide (§9).
func (e *Engine) decide(ctx context.Context, tx *core.Tx, sc core.SessionContext, q core.Question, m *protocol.ApprovalRequest) error {
	if reason := DenyListed(m.ToolName, m.Input); reason != "" {
		e.log.Warn("approval hit the fixed deny list", "employee", sc.Employee.Name, "tool", m.ToolName, "reason", reason)
		return e.resolve(ctx, tx, q, "deny", DecidedDenyList, "平台禁止这个操作："+reason)
	}
	if orchestrator.Bypass(sc.Employee) {
		// bypass sessions do not ask (the daemon allows in canUseTool); an old daemon might.
		e.log.Warn("approval from a bypass session", "employee", sc.Employee.Name)
		return e.resolve(ctx, tx, q, "allow", DecidedRoleSkip, "")
	}
	if auto, err := threadAuto(ctx, tx, sc.Employee); err != nil || auto {
		if err != nil {
			return err
		}
		return e.resolve(ctx, tx, q, "allow", "policy:thread_auto", "")
	}
	ruleID, err := matchRule(ctx, tx, sc.Employee, m.ToolName, InputHash(m.ToolName, m.Input))
	if err != nil {
		return err
	}
	if ruleID != "" {
		return e.resolve(ctx, tx, q, "allow", "policy:rule:"+ruleID, "")
	}
	if readOnlyRole(sc.Employee) && ReadOnlyCommand(m.ToolName, m.Input) {
		return e.resolve(ctx, tx, q, "allow", DecidedReadOnly, "")
	}
	return e.route(ctx, tx, sc, q)
}

func threadAuto(ctx context.Context, q core.DB, emp core.Employee) (bool, error) {
	if emp.ThreadID == nil {
		return false, nil
	}
	t, err := core.ThreadByID(ctx, q, *emp.ThreadID)
	return t.AutoApprove, err
}

func matchRule(ctx context.Context, q core.DB, emp core.Employee, tool, hash string) (string, error) {
	var rule struct{ ID string }
	err := core.MapErr(rulesFor(ctx, q, emp.ProjectID, emp.ThreadID, emp.Name).Select("id").
		Where("tool_name = ? AND input_hash = ?", tool, hash).Order("created_at").Take(&rule).Error)
	if errors.Is(err, core.ErrNotFound) {
		return "", nil
	}
	return rule.ID, err
}

// rulesFor selects the permission rules that apply to an employee.
func rulesFor(ctx context.Context, q core.DB, projectID string, threadID *string, employee string) *gorm.DB {
	return q.Gorm(ctx).Table("at_permission_rules").Where(
		"project_id = ? AND (thread_id IS NULL OR thread_id = ?) AND (employee = '' OR employee = ?)",
		projectID, threadID, employee)
}

// showCard leaves the card open: SSE question event, raised hand, thread waiting, user notice.
func (e *Engine) showCard(ctx context.Context, tx *core.Tx, sc core.SessionContext, q core.Question) error {
	if err := tx.EmitQuestion(ctx, q); err != nil {
		return err
	}
	if err := e.refreshThread(ctx, tx, q); err != nil {
		return err
	}
	e.orch.Touch(tx, sc.Employee.ID, "approval.open:"+deref(q.ApprovalID))
	d, err := core.DeliveryOf(ctx, tx, sc.Employee.ProjectID)
	if err != nil || !d.NotifyOnAsk {
		return err
	}
	_, err = tx.EmitUser(ctx, sc.OwnerID, "notice", map[string]any{"kind": "approval", "project_id": sc.Employee.ProjectID,
		"question": core.QuestionViewOf(q, nil), "text": q.Text})
	return err
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// refreshThread recomputes the thread's waiting state after a card opened or closed.
func (e *Engine) refreshThread(ctx context.Context, tx *core.Tx, q core.Question) error {
	if q.ThreadID == nil {
		return nil
	}
	next, err := core.RefreshWaiting(ctx, tx, *q.ThreadID)
	if err != nil {
		return err
	}
	if next == "" {
		return tx.EmitThread(ctx, *q.ThreadID)
	}
	t, err := core.ThreadByID(ctx, tx, *q.ThreadID)
	if err != nil {
		return err
	}
	return e.orch.SetStatus(ctx, tx, t, next, "")
}

// resolve closes the card with a decision and queues approval.resolve.
func (e *Engine) resolve(ctx context.Context, tx *core.Tx, q core.Question, decision, decidedBy, message string) error {
	answer := decision
	closed, err := core.CloseQuestion(ctx, tx, q, "answered", &answer, decidedBy)
	if err != nil {
		return err
	}
	if err := tx.EmitQuestion(ctx, closed); err != nil {
		return err
	}
	if err := e.refreshThread(ctx, tx, closed); err != nil {
		return err
	}
	if err := e.sendResolve(ctx, tx, closed, decision, decidedBy, message); err != nil {
		return err
	}
	if emp, err := askerOf(ctx, tx, closed); err == nil {
		e.orch.Touch(tx, emp.ID, "approval.closed:"+deref(closed.ApprovalID))
	}
	return nil
}

func askerOf(ctx context.Context, q core.DB, card core.Question) (core.Employee, error) {
	return core.EmployeeByName(ctx, q, deref(card.ProjectID), card.ThreadID, card.Asker)
}

func (e *Engine) sendResolve(ctx context.Context, tx *core.Tx, q core.Question, decision, decidedBy, message string) error {
	s, err := core.SessionByID(ctx, tx, deref(q.SessionID))
	if err != nil || s.NodeID == nil || s.State == "closed" {
		return ignoreMissing(err)
	}
	body := protocol.ApprovalResolve{ApprovalID: protocol.ApprovalID(deref(q.ApprovalID)),
		Decision: protocol.ApprovalDecision(decision), DecidedBy: wireDecidedBy(decidedBy)}
	if message != "" {
		body.Message = &message
	}
	_, err = e.gw.Enqueue(ctx, tx, *s.NodeID, nodegw.Command{Type: protocol.TypeApprovalResolve, Body: body,
		SessionID: s.ID, OpenGen: s.OpenGen})
	return err
}

func ignoreMissing(err error) error {
	if errors.Is(err, core.ErrNotFound) {
		return nil
	}
	return err
}

// Answer is the user's decision on a card: allow, allow_always (remembers a rule) or deny.
func (e *Engine) Answer(ctx context.Context, user store.User, convID, qid, answer, reason string) (core.Question, error) {
	var out core.Question
	err := e.core.InTx(ctx, func(tx *core.Tx) error {
		q, err := core.LockQuestion(ctx, tx, convID, qid)
		if err != nil {
			return err
		}
		if err := checkAnswer(q, answer); err != nil {
			return err
		}
		if err := e.answerTx(ctx, tx, user, q, answer, reason); err != nil {
			return err
		}
		out, err = core.QuestionByID(ctx, tx, convID, qid)
		return err
	})
	return out, err
}

func checkAnswer(q core.Question, answer string) error {
	switch {
	case q.Kind != "approval":
		return ErrNotApproval
	case q.Status != "open":
		return ErrAnswered
	case answer != "allow" && answer != "deny" && answer != "allow_always":
		return ErrBadAnswer
	}
	return nil
}

func (e *Engine) answerTx(ctx context.Context, tx *core.Tx, user store.User, q core.Question, answer, reason string) error {
	by := "user:" + user.ID
	detail, _ := json.Marshal(map[string]string{"answer": answer, "tool": fmt.Sprint(q.Detail["tool_name"])})
	err := tx.Gorm(ctx).Table("at_audit_log").Create(map[string]any{"user_id": user.ID, "actor": by,
		"action": "approval_answer", "target_type": "approval", "target_id": deref(q.ApprovalID), "detail": detail}).Error
	if err != nil {
		return err
	}
	message := ""
	if answer == "deny" {
		message = "用户拒绝了这个操作"
		if reason != "" {
			message += "：" + reason
		}
	}
	if err := e.resolve(ctx, tx, q, answer, by, message); err != nil {
		return err
	}
	if err := userActivity(ctx, tx, q, answer, reason); err != nil {
		return err
	}
	if answer != "allow_always" {
		return nil
	}
	ruleID, err := e.remember(ctx, tx, q, by)
	if err != nil {
		return err
	}
	return e.applyRule(ctx, tx, q, ruleID)
}

var answerVerbs = map[string]string{"allow": "放行了", "allow_always": "总是允许", "deny": "拒绝了"}

// userActivity logs the user's decision in the thread's activity feed.
func userActivity(ctx context.Context, tx *core.Tx, q core.Question, answer, reason string) error {
	if q.ThreadID == nil {
		return nil
	}
	line := fmt.Sprintf("%s %s 的请求 %s", answerVerbs[answer], q.Asker, Summary(q))
	if reason != "" {
		line += "（" + reason + "）"
	}
	return tx.EmitActivity(ctx, deref(q.ProjectID), q.ThreadID, "你", line)
}

// remember stores an "always allow" rule (sdk_update kept verbatim, destination → session).
func (e *Engine) remember(ctx context.Context, tx *core.Tx, q core.Question, by string) (string, error) {
	tool := fmt.Sprint(q.Detail["tool_name"])
	hash, _ := q.Detail["input_hash"].(string)
	updates := sessionScoped(q.Detail["suggestions"])
	raw, _ := json.Marshal(updates)
	id := core.NewID("pr")
	err := tx.Gorm(ctx).Table("at_permission_rules").Create(map[string]any{"id": id, "project_id": deref(q.ProjectID),
		"thread_id": q.ThreadID, "employee": q.Asker, "tool_name": tool, "rule": ruleText(updates), "sdk_update": raw,
		"input_hash": hash, "created_by": by}).Error
	return id, err
}

// applyRule auto-allows the asker's other open cards with the same tool and input.
func (e *Engine) applyRule(ctx context.Context, tx *core.Tx, q core.Question, ruleID string) error {
	var keys []core.Question
	err := tx.Gorm(ctx).Model(&core.Question{}).Select("conversation_id, id").Where(`kind = 'approval' AND status = 'open'
		AND project_id = ? AND asker = ? AND COALESCE(thread_id, '') = COALESCE(?, '')
		AND detail->>'tool_name' = ? AND detail->>'input_hash' = ?`,
		deref(q.ProjectID), q.Asker, q.ThreadID, fmt.Sprint(q.Detail["tool_name"]), fmt.Sprint(q.Detail["input_hash"])).
		Find(&keys).Error
	if err != nil {
		return err
	}
	for _, k := range keys {
		other, err := core.LockQuestion(ctx, tx, k.ConversationID, k.ID)
		if err != nil || other.Status != "open" {
			continue
		}
		if err := e.resolve(ctx, tx, other, "allow", "policy:rule:"+ruleID, ""); err != nil {
			return err
		}
	}
	return nil
}

// sessionScoped rewrites every PermissionUpdate's destination to "session" (§6.1).
func sessionScoped(v any) []map[string]any {
	list, _ := v.([]any)
	out := make([]map[string]any, 0, len(list))
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		m["destination"] = "session"
		out = append(out, m)
	}
	return out
}

// ruleText renders addRules updates as Claude rule strings, e.g. "Bash(pnpm add:*)".
func ruleText(updates []map[string]any) string {
	rules := ExpandRules(updates)
	if len(rules) == 0 {
		return ""
	}
	raw, _ := json.Marshal(rules)
	return string(raw)
}

// ExpandRules turns PermissionUpdate addRules/allow into allowed_tools entries.
func ExpandRules(updates []map[string]any) []string {
	var out []string
	for _, u := range updates {
		if u["type"] != "addRules" || (u["behavior"] != nil && u["behavior"] != "allow") {
			continue
		}
		list, _ := u["rules"].([]any)
		for _, r := range list {
			out = append(out, ruleString(r))
		}
	}
	return out
}

func ruleString(r any) string {
	m, _ := r.(map[string]any)
	name, _ := m["toolName"].(string)
	content, _ := m["ruleContent"].(string)
	if content == "" {
		return name
	}
	return name + "(" + content + ")"
}

// Rules returns the allowed_tools expansions of the rules that apply to an employee.
func (e *Engine) Rules(ctx context.Context, q core.DB, projectID string, threadID *string, employee string) ([]string, error) {
	var rows []struct{ SdkUpdate []byte }
	err := rulesFor(ctx, q, projectID, threadID, employee).Select("sdk_update").Where("sdk_update IS NOT NULL").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	var out []string
	for _, r := range rows {
		var updates []map[string]any
		if err := json.Unmarshal(r.SdkUpdate, &updates); err != nil {
			return nil, err
		}
		out = append(out, ExpandRules(updates)...)
	}
	return out, nil
}

// Cancel marks a card cancelled (approval.cancel from the daemon); a request lost with the
// process (process_restarted) only detaches its card, the resumed employee re-issues it.
func (e *Engine) Cancel(ctx context.Context, m *protocol.ApprovalCancel) error {
	return e.core.InTx(ctx, func(tx *core.Tx) error {
		q, err := core.QuestionByApproval(ctx, tx, string(m.ApprovalID))
		if err != nil {
			return ignoreMissing(err)
		}
		if m.Reason == protocol.ApprovalCancelReasonProcessRestarted {
			_, err := e.detach(ctx, tx, q)
			return err
		}
		return e.cancelCard(ctx, tx, q, "cancelled:"+string(m.Reason))
	})
}

func (e *Engine) cancelCard(ctx context.Context, tx *core.Tx, q core.Question, by string) error {
	if q.Status != "open" {
		return nil
	}
	closed, err := core.CloseQuestion(ctx, tx, q, "cancelled", nil, by)
	if err != nil {
		return err
	}
	if err := tx.EmitQuestion(ctx, closed); err != nil {
		return err
	}
	if emp, err := askerOf(ctx, tx, closed); err == nil {
		e.orch.Touch(tx, emp.ID, "approval.cancelled")
	}
	return e.refreshThread(ctx, tx, closed)
}

// SessionGone cancels the session's open cards that are not in keep (crash, close).
func (e *Engine) SessionGone(ctx context.Context, tx *core.Tx, sessionID string, keep []string) error {
	open, err := core.OpenApprovals(ctx, tx, sessionID)
	if err != nil {
		return err
	}
	for _, q := range open {
		if contains(keep, deref(q.ApprovalID)) {
			continue
		}
		if err := e.cancelCard(ctx, tx, q, "cancelled:process_restarted"); err != nil {
			return err
		}
	}
	return nil
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// SetThreadAutoApprove switches "allow everything in this thread": session modes and open cards.
func (e *Engine) SetThreadAutoApprove(ctx context.Context, tx *core.Tx, t core.Thread, on bool) error {
	if err := core.UpdateThread(ctx, tx, t.ID, map[string]any{"auto_approve": on}); err != nil {
		return err
	}
	if err := e.setModes(ctx, tx, t, on); err != nil {
		return err
	}
	if on {
		if err := e.allowOpen(ctx, tx, t); err != nil {
			return err
		}
	}
	return tx.EmitThread(ctx, t.ID)
}

func (e *Engine) setModes(ctx context.Context, tx *core.Tx, t core.Thread, on bool) error {
	sessions, err := core.ThreadLiveSessions(ctx, tx, t.ID)
	if err != nil {
		return err
	}
	for _, s := range sessions {
		emp, err := core.EmployeeByID(ctx, tx, s.EmployeeID)
		if err != nil || emp.Tier != "member" || readOnly(emp) {
			continue
		}
		if err := e.orch.SetMode(ctx, tx, s, threadMode(emp, on)); err != nil {
			return err
		}
	}
	return nil
}

// threadMode is a member's mode with "allow everything" on (acceptEdits) or off (its own).
func threadMode(emp core.Employee, on bool) string {
	mode := orchestrator.ClaudeMode(emp)
	if on && mode != "bypassPermissions" {
		return "acceptEdits"
	}
	return mode
}

func readOnly(e core.Employee) bool { return e.Spec.ReadOnly != nil && *e.Spec.ReadOnly }

func (e *Engine) allowOpen(ctx context.Context, tx *core.Tx, t core.Thread) error {
	open, err := core.ThreadOpenApprovals(ctx, tx, t.ID)
	if err != nil {
		return err
	}
	for _, q := range open {
		decision, by, msg := "allow", "policy:thread_auto", ""
		if reason := DenyListed(fmt.Sprint(q.Detail["tool_name"]), inputOf(q)); reason != "" {
			decision, by, msg = "deny", DecidedDenyList, "平台禁止这个操作："+reason
		}
		if err := e.resolve(ctx, tx, q, decision, by, msg); err != nil {
			return err
		}
	}
	return nil
}

func inputOf(q core.Question) json.RawMessage {
	raw, _ := json.Marshal(q.Detail["input"])
	return raw
}

// SweepTimeouts denies user cards shown longer than the project's approval_timeout_s (§9 超时;
// a card the lead handed over counts from then).
func (e *Engine) SweepTimeouts(ctx context.Context) {
	var keys []core.Question
	err := e.core.Pool.Gorm(ctx).Table("at_questions q").Select("q.conversation_id, q.id").
		Joins("JOIN at_projects p ON p.id = q.project_id").
		Where(`q.kind = 'approval' AND q.status = 'open' AND q.audience = 'user'
			AND (p.delivery->>'approval_timeout_s') IS NOT NULL
			AND COALESCE((q.detail->>'escalated_at')::timestamptz, q.created_at)
			< now() - make_interval(secs => (p.delivery->>'approval_timeout_s')::int)`).Find(&keys).Error
	if err != nil {
		e.log.Warn("approval timeout sweep", "err", err)
		return
	}
	for _, k := range keys {
		e.timeout(ctx, k.ConversationID, k.ID)
	}
}

func (e *Engine) timeout(ctx context.Context, conv, id string) {
	err := e.core.InTx(ctx, func(tx *core.Tx) error {
		q, err := core.LockQuestion(ctx, tx, conv, id)
		if err != nil || q.Status != "open" {
			return ignoreMissing(err)
		}
		return e.resolve(ctx, tx, q, "deny", "policy:timeout", "审批等待超时，按拒绝处理")
	})
	if err != nil {
		e.log.Warn("approval timeout", "question", id, "err", err)
	}
}

// RunSweeper runs SweepTimeouts every interval until ctx ends.
func (e *Engine) RunSweeper(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e.SweepLeadReviews(ctx)
			e.SweepTimeouts(ctx)
		}
	}
}
