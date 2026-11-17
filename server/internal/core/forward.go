package core

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// UserMessagesByID loads the user's own messages of a conversation among ids, oldest first.
// Ids that are not the user's messages of that conversation are simply missing.
func UserMessagesByID(ctx context.Context, q DB, conversationID string, ids []int64) ([]Message, error) {
	if len(ids) == 0 {
		return []Message{}, nil
	}
	return find[Message](q.Gorm(ctx).Where("conversation_id = ? AND role = 'user' AND id IN ?", conversationID, ids).Order("id"))
}

// LatestUserMessages is the user's n most recent messages of a conversation, oldest first.
func LatestUserMessages(ctx context.Context, q DB, conversationID string, n int) ([]Message, error) {
	out, err := find[Message](q.Gorm(ctx).Where("conversation_id = ? AND role = 'user'", conversationID).Order("id DESC").Limit(n))
	slices.Reverse(out)
	return out, err
}

// LockKey serialises transactions on one key until the transaction ends (advisory lock).
func LockKey(ctx context.Context, tx *Tx, key string) error {
	_, err := store.Exec(ctx, tx, `SELECT pg_advisory_xact_lock(hashtext($1))`, key)
	return err
}

// RecentNote finds a note of the conversation written within the last seconds whose meta
// object field has key = value (the double-click guard of forwards).
func RecentNote(ctx context.Context, q DB, conversationID, field, key string, seconds int) (Message, error) {
	return take[Message](q.Gorm(ctx).Where("conversation_id = ? AND meta->?->>'key' = ? AND created_at > now() - (? * interval '1 second')",
		conversationID, field, key, seconds).Order("id DESC"))
}

// MessagesWithMeta lists a conversation's messages that carry a meta field, oldest first.
func MessagesWithMeta(ctx context.Context, q DB, conversationID, field string) ([]Message, error) {
	return find[Message](q.Gorm(ctx).Where("conversation_id = ? AND jsonb_exists(meta, ?)", conversationID, field).Order("id"))
}

// NextMessageID reserves the id the next message will be stored with (NewMessage.ID).
func NextMessageID(ctx context.Context, tx *Tx) (int64, error) {
	var id int64
	err := store.QueryRow(ctx, tx, `SELECT nextval(pg_get_serial_sequence('at_messages', 'id'))`).Scan(&id)
	return id, err
}

// AttachmentsOf decodes meta.attachments of a stored message.
func AttachmentsOf(m Message) []AttachmentRef {
	return decodeMeta[[]AttachmentRef](m.Meta["attachments"])
}

// RefChipsOf decodes meta.refs (the @ reference chips) of a stored message.
func RefChipsOf(m Message) []map[string]any {
	return decodeMeta[[]map[string]any](m.Meta["refs"])
}

func decodeMeta[T any](v any) T {
	var out T
	if v == nil {
		return out
	}
	raw, err := json.Marshal(v)
	if err == nil {
		_ = json.Unmarshal(raw, &out)
	}
	return out
}
