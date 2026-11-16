package approvals

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/core"
)

// A daemon restart (or a session process restart) kills the Promise behind every pending
// approval. The card is not cancelled: it is detached (detail.detached_at) and stays open,
// the interrupted employee is resumed with a note asking it to re-issue the request, and the
// re-issued request (new approval_id, same tool and input) is attached to the same card. A
// decision taken while the card was detached is applied to the re-issued request at once.
// Cards the employee does not re-issue are cancelled when its next turn ends.

// detachedKey marks a card whose daemon-side request died.
const detachedKey = "detached_at"

// MatchHash identifies a request across a restart: the tool and its input without the
// free-text "description" (the model rewords it when it re-issues a Bash call).
func MatchHash(tool string, input json.RawMessage) string {
	var v any
	_ = json.Unmarshal(input, &v)
	if m, ok := v.(map[string]any); ok {
		delete(m, "description")
	}
	canon, _ := json.Marshal(v)
	sum := sha256.Sum256(append([]byte(tool+"\n"), canon...))
	return hex.EncodeToString(sum[:])
}

// detach keeps an open card for the re-issued request; false when there was nothing to do.
func (e *Engine) detach(ctx context.Context, tx *core.Tx, q core.Question) (bool, error) {
	if q.Status != "open" {
		return false, nil
	}
	if _, ok := q.Detail[detachedKey]; ok {
		return false, nil
	}
	stamp := map[string]any{detachedKey: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := core.MergeQuestionDetail(ctx, tx, q, stamp); err != nil {
		return false, err
	}
	q, err := core.QuestionByID(ctx, tx, q.ConversationID, q.ID)
	if err != nil {
		return false, err
	}
	return true, tx.EmitQuestion(ctx, q)
}

// SessionRestarted detaches the session's open cards that are not in keep: the daemon lost
// their requests with its process (sessions.report, parked by a shutdown). The employee was
// mid-turn then, so it is resumed (once, whatever else noticed the restart).
func (e *Engine) SessionRestarted(ctx context.Context, tx *core.Tx, sessionID string, keep []string) error {
	open, err := core.OpenApprovals(ctx, tx, sessionID)
	if err != nil {
		return err
	}
	lost := false
	for _, q := range open {
		if contains(keep, deref(q.ApprovalID)) {
			continue
		}
		fresh, err := e.detach(ctx, tx, q)
		if err != nil {
			return err
		}
		lost = lost || fresh
	}
	if !lost {
		return nil
	}
	s, err := core.SessionByID(ctx, tx, sessionID)
	if err != nil || s.State == "closed" {
		return ignoreMissing(err)
	}
	return e.orch.ResumeCutTurn(ctx, tx, s)
}

// reattach moves a detached card of the same employee, tool and input onto the re-issued
// request. ok=false: there is none, the request gets a card of its own.
func (e *Engine) reattach(ctx context.Context, tx *core.Tx, sc core.SessionContext, m *protocol.ApprovalRequest) (bool, error) {
	emp := sc.Employee
	var card core.Question
	err := core.MapErr(detachedCards(ctx, tx, emp).Select("conversation_id, id").Clauses(core.ForUpdate).
		Where("detail->>'tool_name' = ? AND (detail->>'match_hash' = ? OR detail->>'input_hash' = ?)", m.ToolName,
			MatchHash(m.ToolName, m.Input), InputHash(m.ToolName, m.Input)).Order("created_at").Take(&card).Error)
	conv, id := card.ConversationID, card.ID
	if errors.Is(err, core.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var input any
	_ = json.Unmarshal(m.Input, &input)
	raw, _ := json.Marshal(map[string]any{"input": input, "input_hash": InputHash(m.ToolName, m.Input),
		"match_hash": MatchHash(m.ToolName, m.Input), "suggestions": m.Suggestions,
		"reattached_at": time.Now().UTC().Format(time.RFC3339Nano)})
	err = tx.Gorm(ctx).Table("at_questions").Where("conversation_id = ? AND id = ?", conv, id).Updates(map[string]any{
		"approval_id": string(m.ApprovalID), "session_id": sc.Session.ID,
		"detail": gorm.Expr("(detail - 'detached_at') || ?::jsonb", raw)}).Error
	if err != nil {
		return false, err
	}
	q, err := core.QuestionByID(ctx, tx, conv, id)
	if err != nil {
		return false, err
	}
	if err := tx.EmitQuestion(ctx, q); err != nil {
		return false, err
	}
	e.orch.Touch(tx, emp.ID, "approval.reattached:"+deref(q.ApprovalID))
	if q.Status == "answered" && q.Answer != nil {
		return true, e.sendResolve(ctx, tx, q, *q.Answer, q.DecidedBy, decidedMessage(*q.Answer, q.DecidedBy))
	}
	return true, nil
}

// decidedMessage is the deny message for a decision taken while the card was detached.
func decidedMessage(answer, decidedBy string) string {
	if answer != "deny" {
		return ""
	}
	switch {
	case len(decidedBy) > len(DecidedLeadPrefix) && decidedBy[:len(DecidedLeadPrefix)] == DecidedLeadPrefix:
		return "组长拒绝了这个操作"
	case decidedBy == "policy:timeout":
		return "审批等待超时，按拒绝处理"
	}
	return "用户拒绝了这个操作"
}

// ExpireDetached runs when the employee's turn ends: requests it did not re-issue are gone
// for good, so their cards are cancelled (answered ones just lose the mark).
func (e *Engine) ExpireDetached(ctx context.Context, tx *core.Tx, emp core.Employee) error {
	var keys []core.Question
	if err := detachedCards(ctx, tx, emp).Select("conversation_id, id").Find(&keys).Error; err != nil {
		return err
	}
	for _, k := range keys {
		q, err := core.LockQuestion(ctx, tx, k.ConversationID, k.ID)
		if err != nil {
			return ignoreMissing(err)
		}
		err = tx.Gorm(ctx).Table("at_questions").Where("conversation_id = ? AND id = ?", q.ConversationID, q.ID).
			Update("detail", gorm.Expr("detail - 'detached_at'")).Error
		if err != nil {
			return err
		}
		if q.Status != "open" {
			continue
		}
		if err := e.cancelCard(ctx, tx, q, "cancelled:process_restarted"); err != nil {
			return err
		}
	}
	return nil
}

// DetachedSummary lists the employee's detached cards for the resume note ("Bash make test").
func DetachedSummary(ctx context.Context, q core.DB, emp core.Employee) ([]string, error) {
	var cards []core.Question
	if err := detachedCards(ctx, q, emp).Select("id, status, detail").Order("created_at").Find(&cards).Error; err != nil {
		return nil, err
	}
	var out []string
	for _, c := range cards {
		line := fmt.Sprintf("%s：%s", c.ID, Summary(core.Question{Detail: c.Detail}))
		if c.Status == "answered" {
			line += "（已有答复，重新发起后立即生效）"
		}
		out = append(out, line)
	}
	return out, nil
}

// detachedCards selects the employee's approval cards whose request was lost with the process.
func detachedCards(ctx context.Context, q core.DB, emp core.Employee) *gorm.DB {
	return q.Gorm(ctx).Model(&core.Question{}).Where(`kind = 'approval' AND status IN ('open','answered')
		AND jsonb_exists(detail, 'detached_at') AND project_id = ? AND COALESCE(thread_id, '') = COALESCE(?, '')
		AND asker = ?`, emp.ProjectID, emp.ThreadID, emp.Name)
}
