package core

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// Conversation is the part of at_conversations the engines use.
type Conversation struct {
	ID        string
	UserID    string
	ProjectID *string
	Kind      string
	Title     string
	CreatedAt time.Time
}

// TableName maps Conversation to at_conversations.
func (Conversation) TableName() string { return "at_conversations" }

func conversations(ctx context.Context, q DB) *gorm.DB {
	return q.Gorm(ctx).Model(&Conversation{}).
		Select("id, COALESCE(user_id, '') AS user_id, project_id, kind, title, created_at")
}

// ConversationOfUser loads one of the user's conversations.
func ConversationOfUser(ctx context.Context, q DB, userID, id string) (Conversation, error) {
	return take[Conversation](conversations(ctx, q).Where("id = ? AND user_id = ?", id, userID))
}

// coordinatorConversation finds the project's coordinator conversation id.
func coordinatorConversation(ctx context.Context, q DB, projectID string) (string, error) {
	c, err := take[struct{ ID string }](q.Gorm(ctx).Table("at_conversations").Select("id").
		Where("project_id = ? AND kind = 'coordinator'", projectID))
	return c.ID, err
}

// EnsureCoordinatorConversation returns the project's coordinator conversation, creating it.
func EnsureCoordinatorConversation(ctx context.Context, tx *Tx, userID, projectID string) (string, error) {
	id, err := coordinatorConversation(ctx, tx, projectID)
	if err != ErrNotFound {
		return id, err
	}
	_, err = store.Exec(ctx, tx, `INSERT INTO at_conversations (id, user_id, title, project_id, kind) VALUES ($1, $2, '项目统筹', $3, 'coordinator')
		ON CONFLICT (project_id) WHERE kind = 'coordinator' DO NOTHING`, NewHex(6), userID, projectID)
	if err != nil {
		return "", err
	}
	return coordinatorConversation(ctx, tx, projectID)
}

// NewMessage is what InsertMessage stores.
type NewMessage struct {
	// ID is a reserved id (NextMessageID); 0 takes the next one.
	ID             int64
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
	m := Message{ID: n.ID, ConversationID: n.ConversationID, Role: n.Role, Content: n.Content, Author: n.Author, Kind: n.Kind,
		Meta: n.Meta}
	if err := tx.Gorm(ctx).Clauses(clause.Returning{}).Create(&m).Error; err != nil {
		return m, err
	}
	err := tx.Gorm(ctx).Table("at_conversations").Where("id = ?", n.ConversationID).
		Update("updated_at", gorm.Expr("now()")).Error
	return m, err
}

// MessageByMsgID finds the message delivered with a session.send msg_id.
func MessageByMsgID(ctx context.Context, q DB, msgID string) (Message, error) {
	return take[Message](q.Gorm(ctx).Where("meta->>'msg_id' = ?", msgID))
}

// MarkDelivered stamps delivered_at; false when it was already set or unknown.
func MarkDelivered(ctx context.Context, tx *Tx, msgID string) (Message, bool, error) {
	m, err := rawOne[Message](ctx, tx, `UPDATE at_messages SET delivered_at = now()
		WHERE meta->>'msg_id' = $1 AND delivered_at IS NULL RETURNING `+messageCols, msgID)
	if err == ErrNotFound {
		return m, false, nil
	}
	return m, err == nil, err
}

// Messages pages a conversation's messages: newest limit before the id cursor (0 = newest), returned oldest first.
func Messages(ctx context.Context, q DB, conversationID string, before int64, limit int) ([]Message, error) {
	g := q.Gorm(ctx).Where("conversation_id = ?", conversationID)
	if before != 0 {
		g = g.Where("id < ?", before)
	}
	out, err := find[Message](g.Order("id DESC").Limit(limit))
	slices.Reverse(out)
	return out, err
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
	return rawOne[Mail](ctx, tx, `INSERT INTO at_mail (conversation_id, sender, recipient, body) VALUES ($1,$2,$3,$4)
		RETURNING `+mailCols, threadID, sender, recipient, body)
}

// UnreadMail returns mail for the employee (direct or broadcast) not yet read, and marks it read.
func UnreadMail(ctx context.Context, tx *Tx, threadID, name string) ([]Mail, error) {
	return rawAll[Mail](ctx, tx, `UPDATE at_mail SET read_by = read_by || to_jsonb($2::text)
		WHERE conversation_id = $1 AND (recipient = $2 OR (recipient = '*' AND sender <> $2))
		AND NOT jsonb_exists(read_by, $2) RETURNING `+mailCols, threadID, name)
}

// PeekUnread returns unread mail without marking it.
func PeekUnread(ctx context.Context, q DB, threadID, name string) ([]Mail, error) {
	return find[Mail](q.Gorm(ctx).Where(`conversation_id = ? AND (recipient = ? OR (recipient = '*' AND sender <> ?))
		AND NOT jsonb_exists(read_by, ?)`, threadID, name, name, name).Order("id"))
}

// ThreadCount is a count per thread number.
type ThreadCount struct {
	No int
	N  int
}

// UnreadElsewhere counts the unread mail addressed to a project member in the project's other
// threads that are not over, by thread number (a thread whose legacy member holds the name is
// that member's).
func UnreadElsewhere(ctx context.Context, q DB, projectID, threadID, name string) ([]ThreadCount, error) {
	return rawAll[ThreadCount](ctx, q, `SELECT th.no, count(*) AS n FROM at_mail m JOIN at_threads th ON th.id = m.conversation_id
		WHERE th.project_id = $1 AND th.id <> $2 AND th.status NOT IN ('done','failed','archived')
		AND m.recipient = $3 AND NOT jsonb_exists(m.read_by, $3)
		AND NOT EXISTS (SELECT 1 FROM at_employees l WHERE l.thread_id = th.id AND l.name = $3)
		GROUP BY th.no ORDER BY th.no`, projectID, threadID, name)
}

// MailPage pages a thread's mail newest first before the id cursor.
func MailPage(ctx context.Context, q DB, threadID string, before int64, limit int) ([]Mail, error) {
	g := q.Gorm(ctx).Where("conversation_id = ?", threadID)
	if before != 0 {
		g = g.Where("id < ?", before)
	}
	return find[Mail](g.Order("id DESC").Limit(limit))
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
	g := q.Gorm(ctx).Table("at_events").Select(`seq AS id, COALESCE(payload->>'employee', actor) AS employee,
		COALESCE(payload->>'text', '') AS text, created_at AS at`).Where("thread_id = ? AND type = 'activity'", threadID)
	if before != 0 {
		g = g.Where("seq < ?", before)
	}
	return find[Activity](g.Order("seq DESC").Limit(limit))
}

// LatestCard is the newest chat card of a kind an employee left in a conversation.
func LatestCard(ctx context.Context, q DB, conversationID, kind, author string) (Message, error) {
	return take[Message](q.Gorm(ctx).Where("conversation_id = ? AND kind = ? AND author = ?", conversationID, kind, author).
		Order("id DESC"))
}

// UpdateCard rewrites a chat card in place (content and meta; its time and position stay).
func UpdateCard(ctx context.Context, tx *Tx, id int64, content string, meta map[string]any) (Message, error) {
	b, err := json.Marshal(meta)
	if err != nil {
		return Message{}, err
	}
	return rawOne[Message](ctx, tx, `UPDATE at_messages SET content = $2, meta = $3 WHERE id = $1 RETURNING `+messageCols,
		id, content, b)
}
