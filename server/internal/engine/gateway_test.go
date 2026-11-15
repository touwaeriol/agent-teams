package engine_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	protocol "github.com/touwaeriol/agent-teams/protocol"

	"github.com/touwaeriol/agent-teams/server/internal/nodegw"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw/nodetest"
)

func waitClosed(t *testing.T, n *nodetest.Node) {
	t.Helper()
	select {
	case <-n.Closed():
	case <-time.After(10 * time.Second):
		t.Fatal("link not closed")
	}
}

func TestGatewayHandshakeRules(t *testing.T) {
	w := newWorld(t, nil)
	b := w.signUp("ops@example.com")
	var node nodeCreated
	b.must(201, "POST", "/api/nodes", map[string]string{"name": "n1"}, &node)

	bad := w.dialRaw("not-the-token", nodetest.Options{SkipWelcome: true})
	waitClosed(t, bad)
	if bad.CloseCode() != nodegw.CloseUnauthorized {
		t.Fatalf("bad token close %d", bad.CloseCode())
	}
	old := w.dialRaw(node.Token, nodetest.Options{SkipWelcome: true, Protocols: []int{1}})
	waitClosed(t, old)
	if old.CloseCode() != nodegw.CloseUpgrade {
		t.Fatalf("protocol 1 close %d", old.CloseCode())
	}

	first := w.connect(node.Token, nodetest.Options{})
	var nodes []struct {
		ID      string `json:"id"`
		Online  bool   `json:"online"`
		Version string `json:"version"`
	}
	b.must(200, "GET", "/api/nodes", nil, &nodes)
	if len(nodes) != 1 || !nodes[0].Online || nodes[0].Version != "2.0.0" {
		t.Fatalf("nodes %+v", nodes)
	}
	second := w.connect(node.Token, nodetest.Options{})
	waitClosed(t, first)
	if first.CloseCode() != nodegw.CloseReplaced {
		t.Fatalf("replaced close %d", first.CloseCode())
	}
	_ = second
}

func (w *world) dialRaw(token string, o nodetest.Options) *nodetest.Node {
	w.t.Helper()
	ctx, cancel := context.WithTimeout(w.ctx, 10*time.Second)
	defer cancel()
	n, err := nodetest.Dial(ctx, "ws"+strings.TrimPrefix(w.srv.URL, "http")+"/nodes/connect", token, o)
	if err != nil {
		w.t.Fatalf("dial: %v", err)
	}
	w.t.Cleanup(n.Close)
	return n
}

// TestGatewayOutboxReplay: unacknowledged commands are resent after a reconnect with the
// same seq and envelope id; acknowledged ones are not; an epoch change expires them.
func TestGatewayOutboxReplay(t *testing.T) {
	w := newWorld(t, nil)
	b := w.signUp("ops@example.com")
	var node nodeCreated
	b.must(201, "POST", "/api/nodes", map[string]string{"name": "n1"}, &node)
	n := w.connect(node.Token, nodetest.Options{NoAck: true})
	b.must(200, "PATCH", "/api/nodes/"+node.Node.ID, map[string]any{"max_active_sessions": 2}, nil)
	ctx, cancel := w.waitCtx()
	defer cancel()
	first, err := n.Wait(ctx, protocol.TypeNodeConfig, nil)
	if err != nil {
		t.Fatal(err)
	}
	n.Close()
	waitClosed(t, n)

	again := w.connect(node.Token, nodetest.Options{AckedCmdSeq: 0})
	replayed, err := again.Wait(ctx, protocol.TypeNodeConfig, nil)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Seq != first.Seq || replayed.ID != first.ID {
		t.Fatalf("replay changed identity: %+v vs %+v", replayed, first)
	}
	eventually(t, "command acked", func() bool { return commandStatus(t, w, node.Node.ID, first.Seq) == "acked" })
	again.Close()
	waitClosed(t, again)

	third := w.connect(node.Token, nodetest.Options{AckedCmdSeq: first.Seq})
	short, cancelShort := context.WithTimeout(w.ctx, 700*time.Millisecond)
	defer cancelShort()
	if _, err := third.Wait(short, protocol.TypeNodeConfig, nil); err == nil {
		t.Fatal("an acknowledged command was resent")
	}
	third.Close()
	waitClosed(t, third)

	noack := w.connect(node.Token, nodetest.Options{NoAck: true, AckedCmdSeq: first.Seq})
	b.must(200, "PATCH", "/api/nodes/"+node.Node.ID, map[string]any{"max_active_sessions": 3}, nil)
	pending, err := noack.Wait(ctx, protocol.TypeNodeConfig, nil)
	if err != nil {
		t.Fatal(err)
	}
	noack.Close()
	waitClosed(t, noack)
	fresh := w.connect(node.Token, nodetest.Options{Epoch: "e2"})
	if fresh.Welcome.AckSeq != 0 {
		t.Fatalf("epoch change keeps ack_seq %d", fresh.Welcome.AckSeq)
	}
	short2, cancel2 := context.WithTimeout(w.ctx, 700*time.Millisecond)
	defer cancel2()
	if _, err := fresh.Wait(short2, protocol.TypeNodeConfig, nil); err == nil {
		t.Fatal("commands of the old epoch were resent")
	}
	if st := commandStatus(t, w, node.Node.ID, pending.Seq); st != "expired" {
		t.Fatalf("old epoch command status %s", st)
	}
}

func commandStatus(t *testing.T, w *world, nodeID string, seq int) string {
	var st string
	if err := w.e.Core.Pool.QueryRow(w.ctx, `SELECT status FROM at_node_commands WHERE node_id = $1 AND seq = $2`, nodeID, seq).Scan(&st); err != nil {
		t.Fatal(err)
	}
	return st
}

// TestGatewayInboundDedupAndOwnership: a replayed daemon seq is acknowledged but not handled
// twice; a message about a session the node does not own is rejected and audited.
func TestGatewayInboundDedupAndOwnership(t *testing.T) {
	w := newWorld(t, nil)
	b := w.signUp("ops@example.com")
	var node nodeCreated
	b.must(201, "POST", "/api/nodes", map[string]string{"name": "n1"}, &node)
	n := w.connect(node.Token, nodetest.Options{})
	ctx, cancel := w.waitCtx()
	defer cancel()
	call := protocol.ToolCall{CallID: "tc_x", SessionID: "s_nobody", Tool: "set_status", Args: json.RawMessage(`{"text":"hi"}`)}
	seq, err := n.Send(ctx, protocol.TypeToolCall, call)
	if err != nil {
		t.Fatal(err)
	}
	if err := n.WaitAck(ctx, seq); err != nil {
		t.Fatal(err)
	}
	eventually(t, "unknown_session error", func() bool {
		for _, e := range n.Errors() {
			if e.Code == "unknown_session" {
				return true
			}
		}
		return false
	})
	var audits int
	_ = w.e.Core.Pool.QueryRow(ctx, `SELECT count(*) FROM at_audit_log WHERE action = 'node_message_rejected'`).Scan(&audits)
	if audits != 1 {
		t.Fatalf("audits %d", audits)
	}
	if err := n.SendSeq(ctx, protocol.TypeToolCall, seq, call); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	_ = w.e.Core.Pool.QueryRow(ctx, `SELECT count(*) FROM at_audit_log WHERE action = 'node_message_rejected'`).Scan(&audits)
	if audits != 1 {
		t.Fatalf("replayed seq was handled again (%d audits)", audits)
	}
	var ack int64
	_ = w.e.Core.Pool.QueryRow(ctx, `SELECT ack_seq FROM at_nodes WHERE id = $1`, node.Node.ID).Scan(&ack)
	if ack != int64(seq) {
		t.Fatalf("ack_seq %d", ack)
	}
}
