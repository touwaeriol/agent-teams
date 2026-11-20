package nodegw

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"gorm.io/gorm"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// link is one authenticated daemon connection.
type link struct {
	g      *Gateway
	conn   *websocket.Conn
	node   store.Node
	hello  *protocol.Hello
	since  time.Time
	ackSeq int64 // highest daemon seq durably handled
	ctx    context.Context
	cancel context.CancelFunc

	mu      sync.Mutex
	sentSeq int64 // highest command seq written on this link
	stats   protocol.NodeStats
	rejects []time.Time
	sendMu  sync.Mutex
}

func newLink(g *Gateway, conn *websocket.Conn, node store.Node, hello *protocol.Hello, ackSeq int64) *link {
	ctx, cancel := context.WithCancel(context.Background())
	return &link{g: g, conn: conn, node: node, hello: hello, since: time.Now().UTC(), ackSeq: ackSeq, ctx: ctx, cancel: cancel}
}

func (l *link) welcome(ctx context.Context) error {
	cfg := &protocol.WelcomeConfig{ParkAfterS: ptr(l.g.cfg.ParkAfterS)}
	if l.node.ParkAfterS != nil {
		cfg.ParkAfterS = l.node.ParkAfterS
	}
	if l.node.MaxActiveSessions != nil {
		cfg.MaxActiveSessions = l.node.MaxActiveSessions
	}
	body := protocol.Welcome{Protocol: 2, NodeID: l.node.ID, AckSeq: int(l.ackSeq), ServerTime: time.Now().UTC(),
		Config: cfg, MinDaemon: ptr(l.g.cfg.MinDaemon), Features: l.g.welcomeFeatures(l.hello)}
	return l.sendUnreliable(ctx, protocol.TypeWelcome, body)
}

func ptr[T any](v T) *T { return &v }

func (l *link) serve(ctx context.Context) {
	notify, cancel := l.g.hub.Subscribe(nodeChannel(l.node.ID))
	defer cancel()
	go l.writer(notify)
	go l.pinger()
	if l.g.handler != nil {
		l.g.handler.NodeOnline(ctx, l.node)
	}
	l.reader(ctx)
}

func nodeChannel(nodeID string) string { return "node:" + nodeID + ":cmd" }

func (l *link) stop() { l.cancel() }

func (l *link) close(code websocket.StatusCode, reason string) {
	l.cancel()
	_ = l.conn.Close(code, reason)
}

func (l *link) pinger() {
	t := time.NewTicker(l.g.cfg.PingInterval)
	defer t.Stop()
	for {
		select {
		case <-l.ctx.Done():
			return
		case <-t.C:
		}
		pctx, cancel := context.WithTimeout(l.ctx, 45*time.Second)
		err := l.conn.Ping(pctx)
		cancel()
		if err != nil && l.ctx.Err() == nil {
			l.close(websocket.StatusGoingAway, "ping timeout")
			return
		}
	}
}

// writer sends pending commands now and whenever the outbox is poked.
func (l *link) writer(notify <-chan string) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		if err := l.g.flush(l.ctx, l); err != nil && l.ctx.Err() == nil {
			l.g.log.Warn("send pending commands", "node_id", l.node.ID, "err", err)
		}
		select {
		case <-l.ctx.Done():
			return
		case <-notify:
		case <-t.C:
		}
	}
}

func (l *link) reader(ctx context.Context) {
	for {
		_, frame, err := l.conn.Read(l.ctx)
		if err != nil {
			return
		}
		if err := l.handleFrame(ctx, frame); err != nil {
			l.g.log.Warn("node message not processed; closing link for replay", "node_id", l.node.ID, "err", err)
			l.close(websocket.StatusInternalError, "processing failed")
			return
		}
	}
}

func (l *link) handleFrame(ctx context.Context, frame []byte) error {
	if l.g.cfg.Validate {
		if err := l.g.validator.ValidateFrame(frame); err != nil {
			return l.rejectFrame(ctx, frame, err)
		}
	}
	env, body, err := protocol.Decode(frame)
	if err != nil {
		l.sendError(idOf(frame), unsupportedOrSchema(err), truncate(err.Error(), 300))
		return nil
	}
	if !protocol.Messages[env.Type].Reliable {
		return l.handleUnreliable(ctx, env, body)
	}
	return l.handleReliable(ctx, env, body)
}

// rejectFrame answers an invalid frame with error bad_schema; a reliable one is still
// acknowledged so the daemon does not replay it forever.
func (l *link) rejectFrame(ctx context.Context, frame []byte, err error) error {
	l.g.log.Warn("invalid node frame", "node_id", l.node.ID, "err", truncate(err.Error(), 300))
	l.sendError(idOf(frame), "bad_schema", truncate(err.Error(), 300))
	var head struct {
		Seq int64 `json:"seq"`
	}
	if json.Unmarshal(frame, &head) != nil || head.Seq <= l.ackSeq {
		return nil
	}
	return l.advance(ctx, head.Seq)
}

func unsupportedOrSchema(err error) string {
	if strings.Contains(err.Error(), "unsupported message type") {
		return "unsupported"
	}
	return "bad_schema"
}

func (l *link) handleReliable(ctx context.Context, env *protocol.Envelope, body any) error {
	seq := int64(0)
	if env.Seq != nil {
		seq = int64(*env.Seq)
	}
	if seq <= l.ackSeq {
		return l.ack(ctx, l.ackSeq)
	}
	if protocol.Messages[env.Type].Direction == protocol.ConsoleToNode {
		l.sendError(env.ID, "unsupported", env.Type+" is a console command")
		return l.advance(ctx, seq)
	}
	body, ok, err := l.g.authorize(ctx, l, env, body)
	if err != nil {
		return err
	}
	if ok && l.g.handler != nil {
		if err := l.handle(ctx, env.Type, body); err != nil {
			return err
		}
	}
	return l.advance(ctx, seq)
}

// handleAttempts bounds how often a reliable message is run again here after PostgreSQL rolled
// its transaction back as a deadlock victim or a serialization failure.
const handleAttempts = 4

// handle runs the handler, again (after a short pause) when the database rolled it back as a
// deadlock or serialization failure. Running a reliable message again is what the daemon's
// replay after a closed link would do (handlers are idempotent per seq); doing it here spares the
// reconnect and the replay of everything unacknowledged behind it.
func (l *link) handle(ctx context.Context, typ string, body any) error {
	var err error
	for attempt := 1; ; attempt++ {
		err = l.g.handler.Handle(ctx, l.node, typ, body)
		if err == nil || !store.IsRetryable(err) || attempt == handleAttempts || ctx.Err() != nil {
			return err
		}
		l.g.log.Info("node message rolled back, running it again", "node_id", l.node.ID, "type", typ,
			"attempt", attempt, "err", err)
		select {
		case <-ctx.Done():
			return err
		case <-time.After(time.Duration(attempt*20) * time.Millisecond):
		}
	}
}

// advance persists the daemon seq as handled and acknowledges it.
func (l *link) advance(ctx context.Context, seq int64) error {
	err := nodeRow(ctx, l.g.core.Pool, l.node.ID).Updates(map[string]any{"ack_seq": gorm.Expr("GREATEST(ack_seq, ?)", seq),
		"last_seen_at": gorm.Expr("now()")}).Error
	if err != nil {
		return err
	}
	l.ackSeq = seq
	return l.ack(ctx, seq)
}

func (l *link) ack(ctx context.Context, seq int64) error {
	return l.sendUnreliable(ctx, protocol.TypeAck, protocol.Ack{Seq: int(seq)})
}

func (l *link) handleUnreliable(ctx context.Context, env *protocol.Envelope, body any) error {
	switch b := body.(type) {
	case *protocol.Ack:
		return l.g.markAckedUpTo(ctx, l.node.ID, int64(b.Seq))
	case *protocol.NodeStats:
		l.mu.Lock()
		l.stats = *b
		l.mu.Unlock()
		return nil
	case *protocol.NodeUsage:
		l.g.recordUsage(l.node.ID, b, l.g.now())
		return nil
	case *protocol.Log:
		return l.g.storeLog(ctx, l.node.ID, b)
	case *protocol.ProtocolError:
		l.g.log.Warn("node reported a protocol error", "node_id", l.node.ID, "code", b.Code, "ref", b.RefID, "message", b.Message)
		return nil
	case *protocol.Hello:
		return nil
	}
	return l.handleUnreliableOwned(ctx, env, body)
}

func (l *link) handleUnreliableOwned(ctx context.Context, env *protocol.Envelope, body any) error {
	body, ok, err := l.g.authorize(ctx, l, env, body)
	if err != nil || !ok || l.g.handler == nil {
		return err
	}
	if err := l.g.handler.Handle(ctx, l.node, env.Type, body); err != nil {
		l.g.log.Warn("unreliable node message failed", "node_id", l.node.ID, "type", env.Type, "err", err)
	}
	return nil
}

func (l *link) sendUnreliable(ctx context.Context, typ string, body any) error {
	env, err := protocol.NewEnvelope(typ, core.NewID("c"), 0, body)
	if err != nil {
		return err
	}
	return l.write(ctx, env)
}

func (l *link) write(ctx context.Context, env *protocol.Envelope) error {
	raw, err := json.Marshal(env)
	if err != nil {
		return err
	}
	l.sendMu.Lock()
	defer l.sendMu.Unlock()
	wctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return l.conn.Write(wctx, websocket.MessageText, raw)
}

func (l *link) sendError(refID, code, message string) {
	body := protocol.ProtocolError{Code: protocol.ProtocolErrorCode(code), Message: &message}
	if refID != "" {
		body.RefID = &refID
	}
	if err := l.sendUnreliable(l.ctx, protocol.TypeError, body); err != nil {
		l.g.log.Debug("send error frame", "err", err)
	}
}

func idOf(frame []byte) string {
	var head struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(frame, &head)
	return head.ID
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func (g *Gateway) storeLog(ctx context.Context, nodeID string, b *protocol.Log) error {
	fields := json.RawMessage(`{}`)
	if b.Fields != nil {
		fields = *b.Fields
	}
	return g.core.Pool.Gorm(ctx).Table("at_node_logs").Create(map[string]any{"node_id": nodeID, "level": string(b.Level),
		"msg": truncate(b.Msg, 4000), "fields": fields}).Error
}
