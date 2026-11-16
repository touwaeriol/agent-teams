package nodegw

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

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
		if _, err := store.Exec(ctx, tx, `DELETE FROM at_node_commands WHERE node_id = $1 AND idem_key = $2`, nodeID, key); err != nil {
			return Queued{}, err
		}
	}
	var seq int64
	if err := store.QueryRow(ctx, tx, `UPDATE at_nodes SET cmd_seq = cmd_seq + 1 WHERE id = $1 RETURNING cmd_seq`, nodeID).Scan(&seq); err != nil {
		return Queued{}, core.MapErr(err)
	}
	meta, _ := json.Marshal(nonNil(cmd.Meta))
	_, err = store.Exec(ctx, tx, `INSERT INTO at_node_commands (node_id, seq, type, idem_key, body, session_id, open_gen,
		expires_at, env_id, meta) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		nodeID, seq, cmd.Type, key, body, nullable(cmd.SessionID), nullableInt(cmd.OpenGen),
		time.Now().Add(expiry(cmd.Type)), envID, meta)
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
	var q Queued
	err := core.MapErr(store.QueryRow(ctx, tx, `SELECT seq, env_id, status FROM at_node_commands WHERE node_id = $1 AND idem_key = $2`,
		nodeID, key).Scan(&q.Seq, &q.EnvID, &q.Status))
	if err == core.ErrNotFound {
		return q, false, nil
	}
	return q, err == nil, err
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

type pending struct {
	seq   int64
	typ   string
	envID string
	body  json.RawMessage
}

// flush sends the link's pending commands in seq order.
func (g *Gateway) flush(ctx context.Context, l *link) error {
	if _, err := store.Exec(ctx, g.core.Pool, `UPDATE at_node_commands SET status = 'expired'
		WHERE node_id = $1 AND status IN ('pending','sent') AND expires_at <= now()`, l.node.ID); err != nil {
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

func (g *Gateway) pendingAfter(ctx context.Context, nodeID string, after int64) ([]pending, error) {
	rows, err := store.Query(ctx, g.core.Pool, `SELECT seq, type, env_id, body FROM at_node_commands
		WHERE node_id = $1 AND seq > $2 AND status IN ('pending','sent') AND expires_at > now() ORDER BY seq LIMIT 200`,
		nodeID, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.seq, &p.typ, &p.envID, &p.body); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (g *Gateway) sendBatch(ctx context.Context, l *link, batch []pending) error {
	for _, p := range batch {
		seq := int(p.seq)
		ts := time.Now().UTC()
		env := &protocol.Envelope{V: 2, Type: p.typ, ID: p.envID, Seq: &seq, Ts: ts, Body: p.body}
		if err := l.write(ctx, env); err != nil {
			return err
		}
		if _, err := store.Exec(ctx, g.core.Pool, `UPDATE at_node_commands SET status = 'sent'
			WHERE node_id = $1 AND seq = $2 AND status = 'pending'`, l.node.ID, p.seq); err != nil {
			return err
		}
		l.mu.Lock()
		l.sentSeq = p.seq
		l.mu.Unlock()
	}
	return nil
}

// markAckedUpTo marks commands with seq <= upTo acked.
func (g *Gateway) markAckedUpTo(ctx context.Context, nodeID string, upTo int64) error {
	if upTo <= 0 {
		return nil
	}
	_, err := store.Exec(ctx, g.core.Pool, `UPDATE at_node_commands SET status = 'acked', acked_at = now()
		WHERE node_id = $1 AND seq <= $2 AND status IN ('pending','sent')`, nodeID, upTo)
	return err
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
	var meta map[string]any
	err := store.QueryRow(ctx, q, `SELECT meta FROM at_node_commands WHERE node_id = $1 AND idem_key = $2`, nodeID, key).Scan(&meta)
	return meta, core.MapErr(err)
}

// FinishCommand records the result of a command (git.result for a job) and marks it done.
func FinishCommand(ctx context.Context, tx *core.Tx, nodeID, key string, ok bool, result any) error {
	raw, _ := json.Marshal(result)
	status := "done"
	if !ok {
		status = "failed"
	}
	_, err := store.Exec(ctx, tx, `UPDATE at_node_commands SET status = $3, result = $4, acked_at = COALESCE(acked_at, now())
		WHERE node_id = $1 AND idem_key = $2`, nodeID, key, status, raw)
	return err
}
