package core

import (
	"context"
	"encoding/json"
	"time"
)

// Conversation is the part of at_conversations the engines use.
type Conversation struct {
	ID        string
	UserID    string
	ProjectID *string
	Kind      string
	Title     string
	Plan      json.RawMessage
	CreatedAt time.Time
}

// ConversationOfUser loads one of the user's conversations.
func ConversationOfUser(ctx context.Context, q DB, userID, id string) (Conversation, error) {
	var c Conversation
	err := q.QueryRow(ctx, `SELECT id, COALESCE(user_id, ''), project_id, kind, title, plan, created_at FROM at_conversations
		WHERE id = $1 AND user_id = $2`, id, userID).Scan(&c.ID, &c.UserID, &c.ProjectID, &c.Kind, &c.Title, &c.Plan, &c.CreatedAt)
	return c, MapErr(err)
}

// LegacyConversations lists the user's v1 chat conversations (optionally of one project).
func LegacyConversations(ctx context.Context, q DB, userID, projectID, kind string) ([]Conversation, error) {
	rows, err := q.Query(ctx, `SELECT id, COALESCE(user_id, ''), project_id, kind, title, plan, created_at FROM at_conversations
		WHERE user_id = $1 AND kind = $2 AND ($3 = '' OR project_id = $3) ORDER BY created_at DESC`, userID, kind, projectID)
	return collect(rows, err, func(r Scanner) (Conversation, error) {
		var c Conversation
		err := r.Scan(&c.ID, &c.UserID, &c.ProjectID, &c.Kind, &c.Title, &c.Plan, &c.CreatedAt)
		return c, err
	})
}

// EnsureCoordinatorConversation returns the project's coordinator conversation, creating it.
func EnsureCoordinatorConversation(ctx context.Context, tx *Tx, userID, projectID string) (string, error) {
	var id string
	err := MapErr(tx.QueryRow(ctx, `SELECT id FROM at_conversations WHERE project_id = $1 AND kind = 'coordinator'`,
		projectID).Scan(&id))
	if err != ErrNotFound {
		return id, err
	}
	id = NewHex(6)
	_, err = tx.Exec(ctx, `INSERT INTO at_conversations (id, user_id, title, project_id, kind) VALUES ($1, $2, '项目统筹', $3, 'coordinator')
		ON CONFLICT (project_id) WHERE kind = 'coordinator' DO NOTHING`, id, userID, projectID)
	if err != nil {
		return "", err
	}
	err = tx.QueryRow(ctx, `SELECT id FROM at_conversations WHERE project_id = $1 AND kind = 'coordinator'`, projectID).Scan(&id)
	return id, err
}

// NewMessage is what InsertMessage stores.
type NewMessage struct {
	ConversationID string
	Role           string
	Author         string
	Kind           string
	Content        string
	Meta           map[string]any
}

// InsertMessage stores a conversation message.
func InsertMessage(ctx context.Context, tx *Tx, n NewMessage) (Message, error) {
	if n.Kind == "" {
		n.Kind = "chat"
	}
	if n.Meta == nil {
		n.Meta = map[string]any{}
	}
	meta, _ := json.Marshal(n.Meta)
	row := tx.QueryRow(ctx, `INSERT INTO at_messages (conversation_id, role, content, author, kind, meta)
		VALUES ($1,$2,$3,$4,$5,$6) RETURNING `+messageCols, n.ConversationID, n.Role, n.Content, n.Author, n.Kind, meta)
	m, err := scanMessage(row)
	if err != nil {
		return m, err
	}
	_, err = tx.Exec(ctx, `UPDATE at_conversations SET updated_at = now() WHERE id = $1`, n.ConversationID)
	return m, err
}

// MessageByMsgID finds the message delivered with a session.send msg_id.
func MessageByMsgID(ctx context.Context, q DB, msgID string) (Message, error) {
	return scanMessage(q.QueryRow(ctx, `SELECT `+messageCols+` FROM at_messages WHERE meta->>'msg_id' = $1`, msgID))
}

// MarkDelivered stamps delivered_at; false when it was already set or unknown.
func MarkDelivered(ctx context.Context, tx *Tx, msgID string) (Message, bool, error) {
	m, err := scanMessage(tx.QueryRow(ctx, `UPDATE at_messages SET delivered_at = now()
		WHERE meta->>'msg_id' = $1 AND delivered_at IS NULL RETURNING `+messageCols, msgID))
	if err == ErrNotFound {
		return m, false, nil
	}
	return m, err == nil, err
}

// Messages pages a conversation's messages: newest limit before the id cursor (0 = newest), returned oldest first.
func Messages(ctx context.Context, q DB, conversationID string, before int64, limit int) ([]Message, error) {
	rows, err := q.Query(ctx, `SELECT `+messageCols+` FROM (SELECT `+messageCols+` FROM at_messages
		WHERE conversation_id = $1 AND ($2 = 0 OR id < $2) ORDER BY id DESC LIMIT $3) x ORDER BY id`,
		conversationID, before, limit)
	return collect(rows, err, scanMessage)
}

// EmitMessage emits a message event.
func (tx *Tx) EmitMessage(ctx context.Context, projectID string, threadID *string, m Message) error {
	no, err := threadNoOf(ctx, tx, threadID)
	if err != nil {
		return err
	}
	_, err = tx.Emit(ctx, projectID, threadID, EvMessage, m.Author,
		map[string]any{"conversation_id": m.ConversationID, "thread_no": no, "message": MessageViewOf(m)})
	return err
}

// InsertMail stores an employee-to-employee message.
func InsertMail(ctx context.Context, tx *Tx, threadID, sender, recipient, body string) (Mail, error) {
	return scanMail(tx.QueryRow(ctx, `INSERT INTO at_mail (conversation_id, sender, recipient, body) VALUES ($1,$2,$3,$4)
		RETURNING `+mailCols, threadID, sender, recipient, body))
}

// UnreadMail returns mail for the employee (direct or broadcast) not yet read, and marks it read.
func UnreadMail(ctx context.Context, tx *Tx, threadID, name string) ([]Mail, error) {
	rows, err := tx.Query(ctx, `UPDATE at_mail SET read_by = read_by || to_jsonb($2::text)
		WHERE conversation_id = $1 AND (recipient = $2 OR (recipient = '*' AND sender <> $2))
		AND NOT read_by ? $2 RETURNING `+mailCols, threadID, name)
	return collect(rows, err, scanMail)
}

// PeekUnread returns unread mail without marking it.
func PeekUnread(ctx context.Context, q DB, threadID, name string) ([]Mail, error) {
	rows, err := q.Query(ctx, `SELECT `+mailCols+` FROM at_mail WHERE conversation_id = $1
		AND (recipient = $2 OR (recipient = '*' AND sender <> $2)) AND NOT read_by ? $2 ORDER BY id`, threadID, name)
	return collect(rows, err, scanMail)
}

// MailPage pages a thread's mail newest first before the id cursor.
func MailPage(ctx context.Context, q DB, threadID string, before int64, limit int) ([]Mail, error) {
	rows, err := q.Query(ctx, `SELECT `+mailCols+` FROM at_mail WHERE conversation_id = $1 AND ($2 = 0 OR id < $2)
		ORDER BY id DESC LIMIT $3`, threadID, before, limit)
	return collect(rows, err, scanMail)
}

// EmitMail emits a mail event.
func (tx *Tx) EmitMail(ctx context.Context, t Thread, m Mail) error {
	_, err := tx.Emit(ctx, t.ProjectID, &t.ID, EvMail, m.Sender, map[string]any{"thread_no": t.No, "mail": MailViewOf(m)})
	return err
}

// EmitActivity records an employee activity line (persisted, api.md §1.1).
func (tx *Tx) EmitActivity(ctx context.Context, projectID string, threadID *string, employee, text string) error {
	no, err := threadNoOf(ctx, tx, threadID)
	if err != nil {
		return err
	}
	_, err = tx.Emit(ctx, projectID, threadID, EvActivity, employee,
		map[string]any{"thread_no": no, "employee": employee, "text": text, "at": time.Now().UTC()})
	return err
}

// Activity is one activity line of a thread.
type Activity struct {
	ID       int64     `json:"id"`
	Employee string    `json:"employee"`
	Text     string    `json:"text"`
	At       time.Time `json:"at"`
}

// ThreadActivity pages a thread's activity newest first before the seq cursor.
func ThreadActivity(ctx context.Context, q DB, threadID string, before int64, limit int) ([]Activity, error) {
	rows, err := q.Query(ctx, `SELECT seq, COALESCE(payload->>'employee', actor), COALESCE(payload->>'text', ''), created_at
		FROM at_events WHERE thread_id = $1 AND type = 'activity' AND ($2 = 0 OR seq < $2) ORDER BY seq DESC LIMIT $3`,
		threadID, before, limit)
	return collect(rows, err, func(r Scanner) (Activity, error) {
		var a Activity
		err := r.Scan(&a.ID, &a.Employee, &a.Text, &a.At)
		return a, err
	})
}

// LatestCard is the newest chat card of a kind an employee left in a conversation.
func LatestCard(ctx context.Context, q DB, conversationID, kind, author string) (Message, error) {
	return scanMessage(q.QueryRow(ctx, `SELECT `+messageCols+` FROM at_messages WHERE conversation_id = $1 AND kind = $2
		AND author = $3 ORDER BY id DESC LIMIT 1`, conversationID, kind, author))
}

// UpdateCard rewrites a chat card in place (content and meta; its time and position stay).
func UpdateCard(ctx context.Context, tx *Tx, id int64, content string, meta map[string]any) (Message, error) {
	b, err := json.Marshal(meta)
	if err != nil {
		return Message{}, err
	}
	return scanMessage(tx.QueryRow(ctx, `UPDATE at_messages SET content = $2, meta = $3 WHERE id = $1 RETURNING `+messageCols,
		id, content, b))
}
