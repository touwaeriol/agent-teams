package core

import (
	"context"
	"encoding/json"

	"gorm.io/gorm"
)

func questions(ctx context.Context, q DB) *gorm.DB { return q.Gorm(ctx).Model(&Question{}) }

// QuestionByID loads a question of a conversation.
func QuestionByID(ctx context.Context, q DB, conversationID, id string) (Question, error) {
	return take[Question](questions(ctx, q).Where("conversation_id = ? AND id = ?", conversationID, id))
}

// LockQuestion loads a question FOR UPDATE.
func LockQuestion(ctx context.Context, tx *Tx, conversationID, id string) (Question, error) {
	return take[Question](questions(ctx, tx).Clauses(ForUpdate).Where("conversation_id = ? AND id = ?", conversationID, id))
}

// QuestionByApproval finds the card of an approval request.
func QuestionByApproval(ctx context.Context, q DB, approvalID string) (Question, error) {
	return take[Question](questions(ctx, q).Where("approval_id = ?", approvalID))
}

// QuestionByCall finds the blocking question created by a tool call.
func QuestionByCall(ctx context.Context, q DB, sessionID, callID string) (Question, error) {
	return take[Question](questions(ctx, q).Where("session_id = ? AND call_id = ?", sessionID, callID))
}

// ConversationQuestions lists a conversation's questions (status "" or "all" = every status).
func ConversationQuestions(ctx context.Context, q DB, conversationID, status string) ([]Question, error) {
	g := questions(ctx, q).Where("conversation_id = ?", conversationID)
	if status != "" && status != "all" {
		g = g.Where("status = ?", status)
	}
	return find[Question](g.Order("created_at, id"))
}

// OpenUserQuestions lists the project's open questions and cards addressed to the user.
func OpenUserQuestions(ctx context.Context, q DB, projectID string) ([]Question, error) {
	return find[Question](questions(ctx, q).Where("project_id = ? AND status = 'open' AND audience = 'user'", projectID).
		Order("created_at"))
}

// openApprovals selects open approval cards.
func openApprovals(ctx context.Context, q DB) *gorm.DB {
	return questions(ctx, q).Where("kind = 'approval' AND status = 'open'")
}

// OpenApprovals lists the open approval cards of a session (or of all sessions when sessionID = "").
func OpenApprovals(ctx context.Context, q DB, sessionID string) ([]Question, error) {
	return find[Question](openApprovals(ctx, q).Where("session_id = ?", sessionID))
}

// ThreadOpenApprovals lists the open approval cards of a thread.
func ThreadOpenApprovals(ctx context.Context, q DB, threadID string) ([]Question, error) {
	return find[Question](openApprovals(ctx, q).Where("thread_id = ?", threadID))
}

// RaisedHands counts an employee's open approvals and blocking questions.
func RaisedHands(ctx context.Context, q DB, projectID string, threadID *string, employee string) (int, error) {
	return count(questions(ctx, q).Where(`project_id = ? AND COALESCE(thread_id, '') = COALESCE(?, '') AND asker = ?
		AND status = 'open' AND (kind = 'approval' OR blocking)`, projectID, threadID, employee))
}

// NewQuestion is what InsertQuestion stores.
type NewQuestion struct {
	ConversationID string
	ProjectID      string
	ThreadID       *string
	Asker          string
	TaskID         *string
	Audience       string
	Kind           string
	Text           string
	Options        []string
	Detail         map[string]any
	SessionID      *string
	ApprovalID     *string
	CallID         *string
	Blocking       bool
}

// InsertQuestion allocates q<n> and stores the question (open).
func InsertQuestion(ctx context.Context, tx *Tx, n NewQuestion) (Question, error) {
	id, err := nextLocalID(ctx, tx, "at_questions", "q", n.ConversationID)
	if err != nil {
		return Question{}, err
	}
	opts, _ := json.Marshal(nonNilStrings(n.Options))
	if n.Detail == nil {
		n.Detail = map[string]any{}
	}
	detail, _ := json.Marshal(n.Detail)
	err = tx.Gorm(ctx).Table("at_questions").Create(map[string]any{"conversation_id": n.ConversationID, "id": id,
		"asker": n.Asker, "task_id": n.TaskID, "audience": n.Audience, "kind": n.Kind, "text": n.Text, "options": opts,
		"detail": detail, "status": "open", "thread_id": n.ThreadID, "project_id": n.ProjectID, "session_id": n.SessionID,
		"approval_id": n.ApprovalID, "call_id": n.CallID, "blocking": n.Blocking}).Error
	if err != nil {
		return Question{}, err
	}
	return QuestionByID(ctx, tx, n.ConversationID, id)
}

// MergeQuestionDetail adds keys to an open question's detail.
func MergeQuestionDetail(ctx context.Context, tx *Tx, q Question, patch map[string]any) error {
	b, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	return questionRow(ctx, tx, q).Update("detail", gorm.Expr("detail || ?::jsonb", b)).Error
}

// questionRow selects one question for a write.
func questionRow(ctx context.Context, tx *Tx, q Question) *gorm.DB {
	return tx.Gorm(ctx).Table("at_questions").Where("conversation_id = ? AND id = ?", q.ConversationID, q.ID)
}

// CloseQuestion sets answer/status/decided_by on an open question; ErrConflict when it is not open.
func CloseQuestion(ctx context.Context, tx *Tx, q Question, status string, answer *string, decidedBy string) (Question, error) {
	res := questionRow(ctx, tx, q).Where("status = 'open'").Updates(map[string]any{"status": status, "answer": answer,
		"decided_by": decidedBy, "answered_at": gorm.Expr("now()")})
	if res.Error != nil {
		return q, res.Error
	}
	if res.RowsAffected == 0 {
		return q, ErrConflict
	}
	return QuestionByID(ctx, tx, q.ConversationID, q.ID)
}

// EmitQuestion emits a question event.
func (tx *Tx) EmitQuestion(ctx context.Context, q Question) error {
	no, err := threadNoOf(ctx, tx, q.ThreadID)
	if err != nil {
		return err
	}
	pid := ""
	if q.ProjectID != nil {
		pid = *q.ProjectID
	}
	_, err = tx.Emit(ctx, pid, q.ThreadID, EvQuestion, q.Asker, map[string]any{"thread_no": no, "question": QuestionViewOf(q, no)})
	return err
}

// threadNoOf returns the thread number for an optional thread id.
func threadNoOf(ctx context.Context, q DB, threadID *string) (*int, error) {
	if threadID == nil {
		return nil, nil
	}
	t, err := take[struct{ No int }](threads(ctx, q).Select("no").Where("id = ?", *threadID))
	if err != nil {
		return nil, err
	}
	return &t.No, nil
}

// ThreadNoOf exposes threadNoOf.
func ThreadNoOf(ctx context.Context, q DB, threadID *string) (*int, error) {
	return threadNoOf(ctx, q, threadID)
}
