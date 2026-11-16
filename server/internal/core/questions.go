package core

import (
	"context"
	"encoding/json"
)

// QuestionByID loads a question of a conversation.
func QuestionByID(ctx context.Context, q DB, conversationID, id string) (Question, error) {
	return scanQuestion(q.QueryRow(ctx, `SELECT `+questionCols+` FROM at_questions WHERE conversation_id = $1 AND id = $2`,
		conversationID, id))
}

// LockQuestion loads a question FOR UPDATE.
func LockQuestion(ctx context.Context, tx *Tx, conversationID, id string) (Question, error) {
	return scanQuestion(tx.QueryRow(ctx, `SELECT `+questionCols+` FROM at_questions WHERE conversation_id = $1 AND id = $2
		FOR UPDATE`, conversationID, id))
}

// QuestionByApproval finds the card of an approval request.
func QuestionByApproval(ctx context.Context, q DB, approvalID string) (Question, error) {
	return scanQuestion(q.QueryRow(ctx, `SELECT `+questionCols+` FROM at_questions WHERE approval_id = $1`, approvalID))
}

// QuestionByCall finds the blocking question created by a tool call.
func QuestionByCall(ctx context.Context, q DB, sessionID, callID string) (Question, error) {
	return scanQuestion(q.QueryRow(ctx, `SELECT `+questionCols+` FROM at_questions WHERE session_id = $1 AND call_id = $2`,
		sessionID, callID))
}

// ConversationQuestions lists a conversation's questions (status "" or "all" = every status).
func ConversationQuestions(ctx context.Context, q DB, conversationID, status string) ([]Question, error) {
	rows, err := q.Query(ctx, `SELECT `+questionCols+` FROM at_questions WHERE conversation_id = $1
		AND ($2 IN ('', 'all') OR status = $2) ORDER BY created_at, id`, conversationID, status)
	return collect(rows, err, scanQuestion)
}

// OpenUserQuestions lists the project's open questions and cards addressed to the user.
func OpenUserQuestions(ctx context.Context, q DB, projectID string) ([]Question, error) {
	rows, err := q.Query(ctx, `SELECT `+questionCols+` FROM at_questions WHERE project_id = $1 AND status = 'open'
		AND audience = 'user' ORDER BY created_at`, projectID)
	return collect(rows, err, scanQuestion)
}

// OpenApprovals lists the open approval cards of a session (or of all sessions when sessionID = "").
func OpenApprovals(ctx context.Context, q DB, sessionID string) ([]Question, error) {
	rows, err := q.Query(ctx, `SELECT `+questionCols+` FROM at_questions WHERE kind = 'approval' AND status = 'open'
		AND session_id = $1`, sessionID)
	return collect(rows, err, scanQuestion)
}

// ThreadOpenApprovals lists the open approval cards of a thread.
func ThreadOpenApprovals(ctx context.Context, q DB, threadID string) ([]Question, error) {
	rows, err := q.Query(ctx, `SELECT `+questionCols+` FROM at_questions WHERE kind = 'approval' AND status = 'open'
		AND thread_id = $1`, threadID)
	return collect(rows, err, scanQuestion)
}

// RaisedHands counts an employee's open approvals and blocking questions.
func RaisedHands(ctx context.Context, q DB, projectID string, threadID *string, employee string) (int, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT count(*) FROM at_questions WHERE project_id = $1
		AND COALESCE(thread_id, '') = COALESCE($2, '') AND asker = $3 AND status = 'open'
		AND (kind = 'approval' OR blocking)`, projectID, threadID, employee).Scan(&n)
	return n, err
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
	_, err = tx.Exec(ctx, `INSERT INTO at_questions (conversation_id, id, asker, task_id, audience, kind, text, options,
		detail, status, thread_id, project_id, session_id, approval_id, call_id, blocking)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,'open',$10,$11,$12,$13,$14,$15)`,
		n.ConversationID, id, n.Asker, n.TaskID, n.Audience, n.Kind, n.Text, opts, detail, n.ThreadID, n.ProjectID,
		n.SessionID, n.ApprovalID, n.CallID, n.Blocking)
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
	_, err = tx.Exec(ctx, `UPDATE at_questions SET detail = detail || $3::jsonb WHERE conversation_id = $1 AND id = $2`,
		q.ConversationID, q.ID, b)
	return err
}

// CloseQuestion sets answer/status/decided_by on an open question; ErrConflict when it is not open.
func CloseQuestion(ctx context.Context, tx *Tx, q Question, status string, answer *string, decidedBy string) (Question, error) {
	tag, err := tx.Exec(ctx, `UPDATE at_questions SET status = $3, answer = $4, decided_by = $5, answered_at = now()
		WHERE conversation_id = $1 AND id = $2 AND status = 'open'`, q.ConversationID, q.ID, status, answer, decidedBy)
	if err != nil {
		return q, err
	}
	if tag.RowsAffected() == 0 {
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
	var no int
	if err := q.QueryRow(ctx, `SELECT no FROM at_threads WHERE id = $1`, *threadID).Scan(&no); err != nil {
		return nil, MapErr(err)
	}
	return &no, nil
}

// ThreadNoOf exposes threadNoOf.
func ThreadNoOf(ctx context.Context, q DB, threadID *string) (*int, error) {
	return threadNoOf(ctx, q, threadID)
}
