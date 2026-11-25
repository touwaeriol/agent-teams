// Package nodetest is a fake node daemon speaking protocol 2 over a real
// WebSocket, for gateway and end-to-end tests: hello/welcome, reliable
// messages with seq, cumulative acks of console commands, and helpers to wait
// for commands.
package nodetest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/oklog/ulid/v2"

	protocol "github.com/touwaeriol/agent-teams/protocol"
)

// Frame is a received console message.
type Frame struct {
	Type string
	ID   string
	Seq  int
	Body json.RawMessage
}

// Decode unmarshals the body.
func (f Frame) Decode(v any) error { return json.Unmarshal(f.Body, v) }

// Node is one fake daemon connection.
type Node struct {
	conn      *websocket.Conn
	validator *protocol.Validator
	Welcome   protocol.Welcome

	mu       sync.Mutex
	seq      int
	frames   []Frame
	acks     []int
	errs     []protocol.ProtocolError
	consumed map[int]bool
	notify   chan struct{}
	closed   chan struct{}
	NoAck    bool // do not acknowledge commands (replay tests)
	ackedCmd int
	readErr  error
}

// Options shape the hello.
type Options struct {
	Epoch       string
	LastSeq     int
	AckedCmdSeq int
	Isolation   string
	MaxActive   int
	NoAck       bool
	SkipWelcome bool     // return right after hello (rejection tests)
	Features    []string // hello.features (nil = sessions, git_worktree, cred_helper, file_download)
	Protocols   []int
	Build       string             // hello.daemon.build ("" = none)
	Runtimes    *protocol.Runtimes // hello.runtimes (nil = none reported)
	BeforeHello func()             // runs after the upgrade, before the hello is sent
}

// Dial connects to ws(s)://…/nodes/connect with the token and completes the handshake.
func Dial(ctx context.Context, url, token string, o Options) (*Node, error) {
	conn, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + token}}})
	if err != nil {
		return nil, err
	}
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	conn.SetReadLimit(4 << 20)
	if o.BeforeHello != nil {
		o.BeforeHello()
	}
	v, err := protocol.NewValidator()
	if err != nil {
		return nil, err
	}
	n := &Node{conn: conn, validator: v, seq: o.LastSeq, consumed: map[int]bool{}, notify: make(chan struct{}, 1),
		closed: make(chan struct{}), NoAck: o.NoAck, ackedCmd: o.AckedCmdSeq}
	if err := n.hello(ctx, o); err != nil {
		return nil, err
	}
	go n.read()
	if o.SkipWelcome {
		return n, nil
	}
	return n, n.waitWelcome(ctx)
}

func (n *Node) hello(ctx context.Context, o Options) error {
	iso := protocol.HelloIsolation(orDefault(o.Isolation, "sandbox"))
	maxActive := o.MaxActive
	if maxActive == 0 {
		maxActive = 4
	}
	protocols := o.Protocols
	if protocols == nil {
		protocols = []int{2}
	}
	body := protocol.Hello{Protocols: protocols, Daemon: protocol.DaemonInfo{Version: "2.0.0"}, Epoch: orDefault(o.Epoch, "e1"),
		LastSeq: o.LastSeq, AckedCmdSeq: o.AckedCmdSeq, Host: "fake", Root: "/tmp/fake", Isolation: &iso,
		Capacity: &protocol.Capacity{MaxActiveSessions: &maxActive}, Features: o.Features}
	if body.Features == nil {
		body.Features = []string{"sessions", "git_worktree", "cred_helper", "file_download"}
	}
	if o.Build != "" {
		body.Daemon.Build = &o.Build
	}
	if o.Runtimes != nil {
		body.Runtimes = o.Runtimes
	}
	env, err := protocol.NewEnvelope(protocol.TypeHello, "n_"+ulid.Make().String(), 0, body)
	if err != nil {
		return err
	}
	return n.write(ctx, env)
}

func orDefault(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

func (n *Node) waitWelcome(ctx context.Context) error {
	f, err := n.Wait(ctx, protocol.TypeWelcome, nil)
	if err != nil {
		return err
	}
	return f.Decode(&n.Welcome)
}

func (n *Node) read() {
	defer close(n.closed)
	for {
		_, raw, err := n.conn.Read(context.Background())
		if err != nil {
			n.mu.Lock()
			n.readErr = err
			n.mu.Unlock()
			return
		}
		n.receive(raw)
	}
}

func (n *Node) receive(raw []byte) {
	if err := n.validator.ValidateFrame(raw); err != nil {
		n.mu.Lock()
		msg := "console sent an invalid frame: " + err.Error()
		n.errs = append(n.errs, protocol.ProtocolError{Code: "bad_schema", Message: &msg})
		n.mu.Unlock()
	}
	var env protocol.Envelope
	if json.Unmarshal(raw, &env) != nil {
		return
	}
	f := Frame{Type: env.Type, ID: env.ID, Body: env.Body}
	if env.Seq != nil {
		f.Seq = *env.Seq
	}
	n.record(f)
	if f.Seq > 0 && !n.NoAck {
		n.ackCommand(f.Seq)
	}
}

func (n *Node) record(f Frame) {
	n.mu.Lock()
	defer n.mu.Unlock()
	switch f.Type {
	case protocol.TypeAck:
		var a protocol.Ack
		_ = f.Decode(&a)
		n.acks = append(n.acks, a.Seq)
	case protocol.TypeError:
		var e protocol.ProtocolError
		_ = f.Decode(&e)
		n.errs = append(n.errs, e)
	}
	n.frames = append(n.frames, f)
	select {
	case n.notify <- struct{}{}:
	default:
	}
}

func (n *Node) ackCommand(seq int) {
	n.mu.Lock()
	if seq > n.ackedCmd {
		n.ackedCmd = seq
	}
	n.mu.Unlock()
	_ = n.sendUnreliable(context.Background(), protocol.TypeAck, protocol.Ack{Seq: seq})
}

// AckedCmd is the highest console seq this node acknowledged (next hello's acked_cmd_seq).
func (n *Node) AckedCmd() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.ackedCmd
}

// LastSeq is the highest reliable seq this node sent.
func (n *Node) LastSeq() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.seq
}

// Send writes a reliable message with the next seq and returns the seq.
func (n *Node) Send(ctx context.Context, typ string, body any) (int, error) {
	n.mu.Lock()
	n.seq++
	seq := n.seq
	n.mu.Unlock()
	return seq, n.SendSeq(ctx, typ, seq, body)
}

// SendSeq writes a reliable message with an explicit seq (replays).
func (n *Node) SendSeq(ctx context.Context, typ string, seq int, body any) error {
	env, err := protocol.NewEnvelope(typ, "n_"+ulid.Make().String(), seq, body)
	if err != nil {
		return err
	}
	return n.write(ctx, env)
}

func (n *Node) sendUnreliable(ctx context.Context, typ string, body any) error {
	env, err := protocol.NewEnvelope(typ, "n_"+ulid.Make().String(), 0, body)
	if err != nil {
		return err
	}
	return n.write(ctx, env)
}

// SendUnreliable writes an unreliable message (cred.request, node.stats, log).
func (n *Node) SendUnreliable(ctx context.Context, typ string, body any) error {
	return n.sendUnreliable(ctx, typ, body)
}

func (n *Node) write(ctx context.Context, env *protocol.Envelope) error {
	raw, err := json.Marshal(env)
	if err != nil {
		return err
	}
	return n.conn.Write(ctx, websocket.MessageText, raw)
}

// Wait returns the first not yet consumed frame of type typ matching pred (nil = any).
func (n *Node) Wait(ctx context.Context, typ string, pred func(Frame) bool) (Frame, error) {
	for {
		if f, ok := n.take(typ, pred); ok {
			return f, nil
		}
		select {
		case <-ctx.Done():
			return Frame{}, fmt.Errorf("waiting for %s: %w (seen: %s)", typ, ctx.Err(), n.seen())
		case <-n.closed:
			if f, ok := n.take(typ, pred); ok {
				return f, nil
			}
			return Frame{}, fmt.Errorf("link closed while waiting for %s (seen: %s)", typ, n.seen())
		case <-n.notify:
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (n *Node) take(typ string, pred func(Frame) bool) (Frame, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for i, f := range n.frames {
		if n.consumed[i] || f.Type != typ || (pred != nil && !pred(f)) {
			continue
		}
		n.consumed[i] = true
		return f, true
	}
	return Frame{}, false
}

func (n *Node) seen() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	types := make([]string, 0, len(n.frames))
	for _, f := range n.frames {
		types = append(types, f.Type)
	}
	return strings.Join(types, ",")
}

// Acked reports whether the console acknowledged seq.
func (n *Node) Acked(seq int) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, a := range n.acks {
		if a >= seq {
			return true
		}
	}
	return false
}

// WaitAck waits until the console acknowledged seq.
func (n *Node) WaitAck(ctx context.Context, seq int) error {
	for !n.Acked(seq) {
		select {
		case <-ctx.Done():
			return fmt.Errorf("ack %d: %w", seq, ctx.Err())
		case <-n.closed:
			return fmt.Errorf("link closed before ack %d", seq)
		case <-time.After(20 * time.Millisecond):
		}
	}
	return nil
}

// Errors returns protocol errors received from the console.
func (n *Node) Errors() []protocol.ProtocolError {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]protocol.ProtocolError(nil), n.errs...)
}

// Close closes the link.
func (n *Node) Close() { _ = n.conn.Close(websocket.StatusNormalClosure, "bye") }

// Closed is closed when the console ended the link.
func (n *Node) Closed() <-chan struct{} { return n.closed }

// CloseCode is the console's close code once Closed fired (-1 when unknown).
func (n *Node) CloseCode() websocket.StatusCode {
	n.mu.Lock()
	defer n.mu.Unlock()
	return websocket.CloseStatus(n.readErr)
}
