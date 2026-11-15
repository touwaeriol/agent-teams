package approvals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/orchestrator"
)

// Lead review (architecture.md §9 ⑤): a card no policy settled goes first to the thread's
// lead as "[Permission request qN …]"; the lead answers with review_permission. ask_user,
// the lead's silence (lead_review_timeout_s), a request from the lead itself or the project
// setting approval_routing = user turn it into the user's card.

// Errors the lead's review_permission call is refused with.
var (
	ErrNotReview   = errors.New("没有这个待你判断的权限请求")
	ErrReviewTaken = errors.New("这个权限请求已经处理过了")
	ErrBadDecision = errors.New("decision 只能是 allow、deny 或 ask_user")
)

// DecidedLeadPrefix marks decisions of a lead in at_questions.decided_by ("lead:<name>").
const DecidedLeadPrefix = "lead:"

// wireLeadReview is decided_by on approval.resolve for a lead decision: older daemons
// only accept the protocol's user:/policy: values.
const wireLeadReview = "policy:rule:lead-review"

const inputPreviewLimit = 2000

// cardFields are the text fields of a card that the lead sees too.
var cardFields = []struct{ key, label string }{{"title", "请求"}, {"description", "说明"}, {"decision_reason", "原因"}}

// route shows the card to the thread lead first, or to the user.
func (e *Engine) route(ctx context.Context, tx *core.Tx, sc core.SessionContext, q core.Question) error {
	lead, ok, err := reviewerOf(ctx, tx, sc.Employee)
	if err != nil {
		return err
	}
	if !ok {
		return e.showCard(ctx, tx, sc, q)
	}
	return e.toLead(ctx, tx, sc, lead, q)
}

// reviewerOf is the lead that reviews an employee's requests (ok=false: ask the user directly).
func reviewerOf(ctx context.Context, tx *core.Tx, emp core.Employee) (core.Employee, bool, error) {
	if emp.ThreadID == nil || emp.Tier != "member" {
		return core.Employee{}, false, nil
	}
	d, err := core.DeliveryOf(ctx, tx, emp.ProjectID)
	if err != nil || d.ApprovalRouting == core.RouteUser {
		return core.Employee{}, false, err
	}
	lead, err := core.Lead(ctx, tx, *emp.ThreadID)
	if errors.Is(err, core.ErrNotFound) {
		return core.Employee{}, false, nil
	}
	return lead, err == nil, err
}

// toLead addresses the card to the lead and sends it the request.
func (e *Engine) toLead(ctx context.Context, tx *core.Tx, sc core.SessionContext, lead core.Employee, q core.Question) error {
	q, err := setAudience(ctx, tx, q, "lead", map[string]any{"reviewer": lead.Name, "reviewer_id": lead.ID})
	if err != nil {
		return err
	}
	if err := tx.EmitQuestion(ctx, q); err != nil {
		return err
	}
	e.orch.Touch(tx, sc.Employee.ID, "approval.open:"+deref(q.ApprovalID))
	if err := tx.EmitActivity(ctx, sc.Employee.ProjectID, sc.Employee.ThreadID, q.Asker,
		fmt.Sprintf("请求 %s，等组长 %s 判断", Summary(q), lead.Name)); err != nil {
		return err
	}
	_, err = e.orch.Deliver(ctx, tx, lead, orchestrator.Delivery{Text: LeadMessage(q), Kind: "mail", Priority: "next"})
	return err
}

// LeadMessage is what the lead sees: only what the user's card shows (tool, command or
// input, title, description), the input clipped.
func LeadMessage(q core.Question) string {
	var b strings.Builder
	task := ""
	if q.TaskID != nil {
		task = " · " + *q.TaskID
	}
	fmt.Fprintf(&b, "[Permission request %s from %s%s]\n", q.ID, q.Asker, task)
	fmt.Fprintf(&b, "工具：%v\n", q.Detail["tool_name"])
	for _, f := range cardFields {
		if v, ok := q.Detail[f.key].(string); ok && v != "" {
			fmt.Fprintf(&b, "%s：%s\n", f.label, v)
		}
	}
	fmt.Fprintf(&b, "内容：%s\n", clip(inputText(q), inputPreviewLimit))
	b.WriteString(`(decide with review_permission(request_id="` + q.ID + `", decision="allow"|"deny"|"ask_user", reason="…"))`)
	return b.String()
}

// inputText is the command / path the card shows, else the input as JSON.
func inputText(q core.Question) string {
	input, _ := q.Detail["input"].(map[string]any)
	for _, key := range []string{"command", "cmd", "file_path", "path"} {
		if v, ok := input[key].(string); ok && v != "" {
			return v
		}
	}
	raw, _ := json.Marshal(input)
	return string(raw)
}

// Summary is the request in one short line for the activity feed: tool and command.
func Summary(q core.Question) string {
	return fmt.Sprintf("%v %s", q.Detail["tool_name"], clip(inputText(q), 80))
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// setAudience readdresses an open card and merges detail keys into it.
func setAudience(ctx context.Context, tx *core.Tx, q core.Question, audience string, detail map[string]any) (core.Question, error) {
	raw, _ := json.Marshal(detail)
	if _, err := tx.Exec(ctx, `UPDATE at_questions SET audience = $3, detail = detail || $4::jsonb
		WHERE conversation_id = $1 AND id = $2`, q.ConversationID, q.ID, audience, raw); err != nil {
		return q, err
	}
	return core.QuestionByID(ctx, tx, q.ConversationID, q.ID)
}

// LeadReview is the lead's review_permission: allow / deny settle the card, ask_user hands it
// to the user. The returned text is the tool result.
func (e *Engine) LeadReview(ctx context.Context, tx *core.Tx, lead core.Employee, threadID, requestID, decision, reason string) (string, error) {
	q, err := core.LockQuestion(ctx, tx, threadID, requestID)
	if err != nil || !reviewerIs(q, lead, threadID) {
		return "", ErrNotReview
	}
	if q.Status != "open" || q.Audience != "lead" {
		return "", ErrReviewTaken
	}
	review := map[string]any{"by": DecidedLeadPrefix + lead.Name, "decision": decision, "reason": reason}
	switch decision {
	case "allow", "deny":
		return e.leadDecides(ctx, tx, lead, q, decision, review)
	case "ask_user":
		return "已转给用户决定 " + q.ID, e.escalate(ctx, tx, q, review, lead.Name, "拿不准，转给你决定："+Summary(q)+"（"+reason+"）")
	}
	return "", ErrBadDecision
}

// reviewerIs: only the lead the card was addressed to (by employee id, in its own thread)
// may review it, never the asker.
func reviewerIs(q core.Question, lead core.Employee, threadID string) bool {
	return q.Kind == "approval" && lead.Tier == "lead" && lead.ThreadID != nil && *lead.ThreadID == threadID &&
		q.Detail["reviewer_id"] == lead.ID && q.Asker != lead.Name
}

func (e *Engine) leadDecides(ctx context.Context, tx *core.Tx, lead core.Employee, q core.Question, decision string, review map[string]any) (string, error) {
	q, err := setAudience(ctx, tx, q, "lead", map[string]any{"review": review})
	if err != nil {
		return "", err
	}
	reason, _ := review["reason"].(string)
	message, verb := "", "放行了"
	if decision == "deny" {
		message, verb = "组长拒绝了这个操作："+reason, "拒绝了"
	}
	if err := e.resolve(ctx, tx, q, decision, DecidedLeadPrefix+lead.Name, message); err != nil {
		return "", err
	}
	line := fmt.Sprintf("%s %s 的请求 %s（%s）", verb, q.Asker, Summary(q), reason)
	return verb + " " + q.ID, tx.EmitActivity(ctx, lead.ProjectID, lead.ThreadID, lead.Name, line)
}

// escalate turns a lead card into the user's card (ask_user or timeout) and logs why.
func (e *Engine) escalate(ctx context.Context, tx *core.Tx, q core.Question, review map[string]any, who, line string) error {
	q, err := setAudience(ctx, tx, q, "user", map[string]any{"review": review, "escalated_at": time.Now().UTC().Format(time.RFC3339Nano)})
	if err != nil {
		return err
	}
	sc, err := core.SessionContextOf(ctx, tx, deref(q.SessionID))
	if err != nil {
		return err
	}
	if err := tx.EmitActivity(ctx, deref(q.ProjectID), q.ThreadID, who, line); err != nil {
		return err
	}
	return e.showCard(ctx, tx, sc, q)
}

// SweepLeadReviews hands cards the lead left unanswered past lead_review_timeout_s to the user.
func (e *Engine) SweepLeadReviews(ctx context.Context) {
	rows, err := e.core.Pool.Query(ctx, `SELECT conversation_id, id, project_id, extract(epoch FROM now() - created_at)::int
		FROM at_questions WHERE kind = 'approval' AND status = 'open' AND audience = 'lead'`)
	if err != nil {
		e.log.Warn("lead review sweep", "err", err)
		return
	}
	type due struct{ conv, id, project string }
	var list []due
	for rows.Next() {
		var d due
		var age int
		if rows.Scan(&d.conv, &d.id, &d.project, &age) == nil && age >= e.leadTimeout(ctx, d.project) {
			list = append(list, d)
		}
	}
	rows.Close()
	for _, d := range list {
		e.leadTimedOut(ctx, d.conv, d.id)
	}
}

func (e *Engine) leadTimeout(ctx context.Context, projectID string) int {
	d, err := core.DeliveryOf(ctx, e.core.Pool, projectID)
	if err != nil || d.LeadReviewTimeoutS <= 0 {
		return core.DefaultLeadReviewSecs
	}
	return d.LeadReviewTimeoutS
}

func (e *Engine) leadTimedOut(ctx context.Context, conv, id string) {
	err := e.core.InTx(ctx, func(tx *core.Tx) error {
		q, err := core.LockQuestion(ctx, tx, conv, id)
		if err != nil || q.Status != "open" || q.Audience != "lead" {
			return ignoreMissing(err)
		}
		review := map[string]any{"by": "policy:lead_timeout", "decision": "ask_user", "reason": "组长没有及时回应"}
		return e.escalate(ctx, tx, q, review, "平台", fmt.Sprintf("组长 %v 没有及时判断，%s 的请求 %s 转给你决定", q.Detail["reviewer"], q.Asker, Summary(q)))
	})
	if err != nil {
		e.log.Warn("lead review timeout", "question", id, "err", err)
	}
}

// wireDecidedBy maps a stored decided_by onto the values approval.resolve accepts.
func wireDecidedBy(decidedBy string) string {
	if strings.HasPrefix(decidedBy, DecidedLeadPrefix) {
		return wireLeadReview
	}
	return decidedBy
}
