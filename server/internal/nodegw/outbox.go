package nodegw

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// ErrOffline means the node has no live link (unreliable messages cannot wait).
var ErrOffline = errors.New("node offline")

// Command is one reliable console → node message for the outbox.
type Command struct {
	Type      string
	Body      any
	SessionID string         // session commands: superseded when the session closes / gen advances
	OpenGen   int            // the session's open_gen the command belongs to
	Meta      map[string]any // what to do when the result arrives (stored, never sent)
}

// Queued is a command row as stored.
type Queued struct {
	Seq    int64
	EnvID  string
	Status string
	Fresh  bool // false when an earlier command with the same idempotency key was reused
}

// expiry is how long a command may wait to be (re)sent (protocol.md §2.1).
func expiry(typ string) time.Duration {
	switch {
	case typ == protocol.TypeSessionSend || typ == protocol.TypeSessionInterrupt || typ == protocol.TypeSessionSetMode:
		return 10 * time.Minute
	case typ == protocol.TypeApprovalResolve || typ == protocol.TypeToolResult:
		return 24 * time.Hour
	case typ == protocol.TypeSessionOpen || strings.HasPrefix(typ, "git."):
		return 30 * time.Minute
	}
	return time.Hour
}

// idemKey builds "<type>:<key fields>" like the daemon's commandKey.
func idemKey(typ, envID string, body []byte) (string, error) {
	meta, ok := protocol.Messages[typ]
	if !ok || !meta.Reliable || meta.Direction != protocol.ConsoleToNode {
		return "", fmt.Errorf("%s is not a reliable console command", typ)
	}
	if len(meta.Key) == 0 {
		return typ + ":" + envID, nil
	}
	var fields map[string]any
	if err := json.Unmarshal(body, &fields); err != nil {
		return "", err
	}
	parts := make([]string, len(meta.Key))
	for i, k := range meta.Key {
		if k == "id" {
			parts[i] = envID
			continue
		}
		parts[i] = fmt.Sprint(fields[k])
	}
	return typ + ":" + strings.Join(parts, ":"), nil
}

// Enqueue stores a command in the node's outbox inside tx; the link sends it
// after the commit. A command whose idempotency key is already queued is not
// duplicated.
func (g *Gateway) Enqueue(ctx context.Context, tx *core.Tx, nodeID string, cmd Command) (Queued, error) {
	body, err := json.Marshal(cmd.Body)
	if err != nil {
		return Queued{}, err
	}
	envID := core.NewID("c")
	key, err := idemKey(cmd.Type, envID, body)
	if err != nil {
		return Queued{}, err
	}
	q, found, err := existing(ctx, tx, nodeID, key)
	if err != nil {
		return q, err
	}
	if found && !retired(q.Status) {
		return q, nil
	}
	if found {
		// The earlier command expired or was superseded but the engine wants it sent again.
		if err := commandByKey(ctx, tx, nodeID, key).Delete(&nodeCommand{}).Error; err != nil {
			return Queued{}, err
		}
	}
	var seq int64
	if err := store.QueryRow(ctx, tx, `UPDATE at_nodes SET cmd_seq = cmd_seq + 1 WHERE id = $1 RETURNING cmd_seq`, nodeID).Scan(&seq); err != nil {
		return Queued{}, core.MapErr(err)
	}
	meta, _ := json.Marshal(nonNil(cmd.Meta))
	err = tx.Gorm(ctx).Create(&nodeCommand{NodeID: nodeID, Seq: seq, Type: cmd.Type, IdemKey: key, Body: body,
		SessionID: nullable(cmd.SessionID), OpenGen: nullableInt(cmd.OpenGen), ExpiresAt: time.Now().Add(expiry(cmd.Type)),
		EnvID: envID, Meta: meta}).Error
	if err != nil {
		return Queued{}, err
	}
	tx.AfterCommit(func(ctx context.Context) { g.hub.Publish(ctx, nodeChannel(nodeID), "1") })
	return Queued{Seq: seq, EnvID: envID, Status: "pending", Fresh: true}, nil
}

func retired(status string) bool {
	return status == "expired" || status == "superseded" || status == "failed"
}

func existing(ctx context.Context, tx *core.Tx, nodeID, key string) (Queued, bool, error) {
	var c nodeCommand
	err := core.MapErr(commandByKey(ctx, tx, nodeID, key).Select("seq, env_id, status").Take(&c).Error)
	if err == core.ErrNotFound {
		return Queued{}, false, nil
	}
	return Queued{Seq: c.Seq, EnvID: c.EnvID, Status: c.Status}, err == nil, err
}

func nonNil(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func nullableInt(n int) *int {
	if n == 0 {
		return nil
	}
	return &n
}

// nodeCommand is a row of at_node_commands (the reliable outbox).
type nodeCommand struct {
	NodeID    string
	Seq       int64
	Type      string
	IdemKey   string
	EnvID     string
	Status    string `gorm:"default:pending"`
	Body      json.RawMessage
	SessionID *string
	OpenGen   *int
	ExpiresAt time.Time
	Meta      json.RawMessage
}

func (nodeCommand) TableName() string { return "at_node_commands" }

func commands(ctx context.Context, q core.DB, nodeID string) *gorm.DB {
	return q.Gorm(ctx).Model(&nodeCommand{}).Where("node_id = ?", nodeID)
}

func commandByKey(ctx context.Context, q core.DB, nodeID, key string) *gorm.DB {
	return commands(ctx, q, nodeID).Where("idem_key = ?", key)
}

// unacked selects a node's commands still waiting for an ack.
const unacked = "status IN ('pending','sent')"

// flush sends the link's pending commands in seq order.
func (g *Gateway) flush(ctx context.Context, l *link) error {
	if err := commands(ctx, g.core.Pool, l.node.ID).Where(unacked).Where("expires_at <= now()").
		Update("status", "expired").Error; err != nil {
		return err
	}
	for {
		batch, err := g.pendingAfter(ctx, l.node.ID, l.lastSent())
		if err != nil || len(batch) == 0 {
			return err
		}
		if err := g.sendBatch(ctx, l, batch); err != nil {
			return err
		}
	}
}

func (l *link) lastSent() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.sentSeq
}

func (g *Gateway) pendingAfter(ctx context.Context, nodeID string, after int64) ([]nodeCommand, error) {
	var out []nodeCommand
	err := commands(ctx, g.core.Pool, nodeID).Select("seq, type, env_id, body").
		Where("seq > ? AND expires_at > now()", after).Where(unacked).Order("seq").Limit(200).Find(&out).Error
	return out, err
}

func (g *Gateway) sendBatch(ctx context.Context, l *link, batch []nodeCommand) error {
	for _, p := range batch {
		seq := int(p.Seq)
		ts := time.Now().UTC()
		env := &protocol.Envelope{V: 2, Type: p.Type, ID: p.EnvID, Seq: &seq, Ts: ts, Body: p.Body}
		if err := l.write(ctx, env); err != nil {
			return err
		}
		if err := commands(ctx, g.core.Pool, l.node.ID).Where("seq = ? AND status = 'pending'", p.Seq).
			Update("status", "sent").Error; err != nil {
			return err
		}
		l.mu.Lock()
		l.sentSeq = p.Seq
		l.mu.Unlock()
	}
	return nil
}

// markAckedUpTo marks commands with seq <= upTo acked.
func (g *Gateway) markAckedUpTo(ctx context.Context, nodeID string, upTo int64) error {
	if upTo <= 0 {
		return nil
	}
	return commands(ctx, g.core.Pool, nodeID).Where("seq <= ?", upTo).Where(unacked).
		Updates(map[string]any{"status": "acked", "acked_at": gorm.Expr("now()")}).Error
}

// SendNow writes an unreliable message (cred.response) on the node's live link.
func (g *Gateway) SendNow(ctx context.Context, nodeID, typ string, body any) error {
	l := g.link(nodeID)
	if l == nil {
		return ErrOffline
	}
	return l.sendUnreliable(ctx, typ, body)
}

// CommandMeta returns the meta and result of the command with an idempotency key.
func (g *Gateway) CommandMeta(ctx context.Context, q core.DB, nodeID, key string) (map[string]any, error) {
	var c nodeCommand
	if err := commandByKey(ctx, q, nodeID, key).Select("meta").Take(&c).Error; err != nil {
		return nil, core.MapErr(err)
	}
	var meta map[string]any
	return meta, json.Unmarshal(c.Meta, &meta)
}

// FinishCommand records the result of a command (git.result for a job) and marks it done.
func FinishCommand(ctx context.Context, tx *core.Tx, nodeID, key string, ok bool, result any) error {
	raw, _ := json.Marshal(result)
	status := "done"
	if !ok {
		status = "failed"
	}
	return commandByKey(ctx, tx, nodeID, key).Updates(map[string]any{"status": status, "result": raw,
		"acked_at": gorm.Expr("COALESCE(acked_at, now())")}).Error
}
