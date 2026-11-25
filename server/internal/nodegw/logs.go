package nodegw

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	protocol "github.com/touwaeriol/agent-teams/protocol"
)

// Node log frames (audit #55 N5): each one is a row in at_node_logs, so a link may store only a
// limited rate of them, each row's msg and fields are capped, and the table keeps a bounded tail per
// node. Log frames are unreliable, so the ones over the rate are simply dropped.
const (
	logMsgMax       = 4000     // bytes of msg kept
	logFieldsMax    = 16 << 10 // bytes of fields JSON kept; larger is replaced by a marker
	logRate         = 5.0      // sustained log frames per second per link
	logBurst        = 200.0    // frames a link may send at once
	logKeepPerNode  = 2000     // newest rows kept per node
	logMaxAge       = 30 * 24 * time.Hour
	logPruneEvery   = 200 // rows stored on a link between two prunes
	logDropWarnFreq = time.Minute
)

// logBucket is a link's token bucket for log frames (only touched by the link's reader).
type logBucket struct {
	tokens   float64
	last     time.Time
	dropped  int
	warnedAt time.Time
	stored   int
}

// allow takes a token if one is left at now.
func (b *logBucket) allow(now time.Time) bool {
	if b.last.IsZero() {
		b.tokens = logBurst
	} else if d := now.Sub(b.last).Seconds(); d > 0 {
		b.tokens = min(logBurst, b.tokens+d*logRate)
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// logFields returns the fields to store: {} when absent, a marker when too large.
func logFields(f *json.RawMessage) json.RawMessage {
	if f == nil || len(*f) == 0 {
		return json.RawMessage(`{}`)
	}
	if len(*f) > logFieldsMax {
		return json.RawMessage(`{"truncated":true,"bytes":` + strconv.Itoa(len(*f)) + `}`)
	}
	return *f
}

// truncateUTF8 cuts s to at most n bytes without splitting a character (PostgreSQL rejects
// invalid UTF-8 in TEXT).
func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "")
}

// storeLog stores one log frame of the link's node, subject to the link's rate.
func (l *link) storeLog(ctx context.Context, b *protocol.Log) error {
	now := time.Now()
	lb := &l.logs
	if !lb.allow(now) {
		lb.dropped++
		if now.Sub(lb.warnedAt) >= logDropWarnFreq {
			lb.warnedAt = now
			l.g.log.Warn("node log frames dropped (rate limit)", "node_id", l.node.ID, "dropped", lb.dropped)
		}
		return nil
	}
	err := l.g.core.Pool.Gorm(ctx).Table("at_node_logs").Create(map[string]any{"node_id": l.node.ID, "level": string(b.Level),
		"msg": truncateUTF8(b.Msg, logMsgMax), "fields": logFields(b.Fields)}).Error
	if err != nil {
		return err
	}
	if lb.stored%logPruneEvery == 0 {
		if err := l.g.pruneLogs(ctx, l.node.ID); err != nil {
			l.g.log.Warn("prune node logs", "node_id", l.node.ID, "err", err)
		}
	}
	lb.stored++
	return nil
}

// pruneLogs deletes the node's rows older than logMaxAge or beyond its newest logKeepPerNode.
func (g *Gateway) pruneLogs(ctx context.Context, nodeID string) error {
	return g.core.Pool.Gorm(ctx).Exec(`DELETE FROM at_node_logs WHERE node_id = ? AND (created_at < ?
		OR id <= (SELECT id FROM at_node_logs WHERE node_id = ? ORDER BY id DESC OFFSET ? LIMIT 1))`,
		nodeID, time.Now().Add(-logMaxAge), nodeID, logKeepPerNode).Error
}
