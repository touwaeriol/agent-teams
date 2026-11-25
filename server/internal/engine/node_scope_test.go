package engine_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw/nodetest"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// TestGatewayRevokedBeforeHello (#55 N2): a link that passed the token check at the upgrade but
// sends its hello only after the token was rotated, or the node deleted, is closed with 4401 and
// never becomes the node's link; the new token still connects.
func TestGatewayRevokedBeforeHello(t *testing.T) {
	w := newWorld(t, nil)
	b := w.signUp("ops@example.com")
	var node nodeCreated
	b.must(201, "POST", "/api/nodes", map[string]string{"name": "n1"}, &node)

	var rotated nodeCreated
	stale := w.dialRaw(node.Token, nodetest.Options{SkipWelcome: true, BeforeHello: func() {
		b.must(200, "POST", "/api/nodes/"+node.Node.ID+"/token", nil, &rotated)
	}})
	waitClosed(t, stale)
	if stale.CloseCode() != nodegw.CloseUnauthorized {
		t.Fatalf("rotated before hello: close %d", stale.CloseCode())
	}
	if w.e.Gateway.Online(node.Node.ID) {
		t.Fatal("the stale link became the node's link")
	}
	if rotated.Token == "" {
		t.Fatalf("rotate answer %+v", rotated)
	}
	fresh := w.connect(rotated.Token, nodetest.Options{})
	if !w.e.Gateway.Online(node.Node.ID) {
		t.Fatal("new token does not connect")
	}
	fresh.Close()

	var gone nodeCreated
	b.must(201, "POST", "/api/nodes", map[string]string{"name": "n2"}, &gone)
	deleted := w.dialRaw(gone.Token, nodetest.Options{SkipWelcome: true, BeforeHello: func() {
		b.must(204, "DELETE", "/api/nodes/"+gone.Node.ID, nil, nil)
	}})
	waitClosed(t, deleted)
	if deleted.CloseCode() != nodegw.CloseUnauthorized {
		t.Fatalf("deleted before hello: close %d", deleted.CloseCode())
	}
}

// TestNodeSecondaryIDsScoped (#55 N3): a node of another user, through a session of its own,
// cannot cancel a card, mark a message delivered or move a compaction on by naming the other
// user's approval_id / msg_id / compact_id: each is rejected (error + audit) and nothing changes.
// The owner's own daemon still cancels its card without a session_id.
func TestNodeSecondaryIDsScoped(t *testing.T) {
	tm := contextTeam(t, contextFeatures, 8, nil)
	dev, lead, th := firstTask(t, tm, 70000)
	lead.mustCall("assign_task", map[string]any{"assignee": "dev", "instruction": "add rate limits", "main": "做"})
	c := expectCompact(t, tm, dev)
	if _, err := store.Exec(tm.w.ctx, tm.w.e.Core.Pool, `UPDATE at_employees SET spec = jsonb_set(spec, '{permission_mode}', '"ask"')
		WHERE project_id = $1 AND name = 'dev-1'`, tm.projectID); err != nil {
		t.Fatal(err)
	}
	gen := protocol.OpenGen(dev.gen)
	dev.send(protocol.TypeApprovalRequest, protocol.ApprovalRequest{ApprovalID: "ap_victim", SessionID: protocol.SessionID(dev.session),
		OpenGen: &gen, Source: "claude_can_use_tool", ToolName: "Bash", Input: json.RawMessage(`{"command":"npm test"}`),
		Available: []protocol.ApprovalDecision{"allow", "deny"}})
	w, ctx, pool := tm.w, tm.w.ctx, tm.w.e.Core.Pool
	eventually(t, "victim card", func() bool { _, err := core.QuestionByApproval(ctx, pool, "ap_victim"); return err == nil })
	// A message of the victim's not delivered yet: the brief of t2 waits for the compaction, so
	// mint one by hand in the victim's thread.
	victimMsg := "m_victim_" + time.Now().Format("150405.000")
	if _, err := store.Exec(ctx, pool, `INSERT INTO at_messages (conversation_id, role, content, author, kind, meta)
		VALUES ($1, 'system', 'x', 'platform', 'note', jsonb_build_object('msg_id', $2::text))`, th.ID, victimMsg); err != nil {
		t.Fatal(err)
	}

	// The attacker: another user with a node and a lead session of its own.
	ab := w.signUp("attacker@example.com")
	var an nodeCreated
	ab.must(201, "POST", "/api/nodes", map[string]string{"name": "evil"}, &an)
	ab.must(200, "GET", "/api/roles", nil, nil)
	var ap struct{ ID string }
	ab.must(201, "POST", "/api/projects", map[string]any{"name": "evil"}, &ap)
	evil := w.connect(an.Token, nodetest.Options{Features: contextFeatures})
	ab.must(201, "POST", "/api/projects/"+ap.ID+"/threads", map[string]any{"title": "x", "brief": "x"}, nil)
	own, _ := expectOpen(t, w, evil, "lead-1")
	ownSID := protocol.SessionID(own.session)
	ownGen := protocol.OpenGen(own.gen)

	rejected := func(what, code string, typ string, body any) {
		t.Helper()
		before := len(evil.Errors())
		wctx, cancel := w.waitCtx()
		defer cancel()
		if _, err := evil.Send(wctx, typ, body); err != nil {
			t.Fatal(err)
		}
		eventually(t, what+" rejected", func() bool { return len(evil.Errors()) > before })
		if e := evil.Errors()[before]; string(e.Code) != code {
			t.Fatalf("%s: error %+v, want %s", what, e, code)
		}
	}
	rejected("approval.cancel without session_id", "unknown_session", protocol.TypeApprovalCancel,
		protocol.ApprovalCancel{ApprovalID: "ap_victim", Reason: "interrupted"})
	rejected("approval.cancel with its own session_id", "unknown_session", protocol.TypeApprovalCancel,
		protocol.ApprovalCancel{ApprovalID: "ap_victim", SessionID: &ownSID, Reason: "interrupted"})
	rejected("approval.ack with its own session_id", "unknown_session", protocol.TypeApprovalAck,
		protocol.ApprovalAck{ApprovalID: "ap_victim", SessionID: &ownSID})
	rejected("session.delivered of a foreign message", "unknown_session", protocol.TypeSessionDelivered,
		protocol.SessionDelivered{SessionID: ownSID, MsgID: protocol.MsgID(victimMsg)})
	rejected("session.compacted of a foreign task", "unknown_session", protocol.TypeSessionCompacted,
		protocol.SessionCompacted{SessionID: ownSID, OpenGen: ownGen, CompactID: c.CompactID, Ok: true})

	if q, _ := core.QuestionByApproval(ctx, pool, "ap_victim"); q.Status != "open" {
		t.Fatalf("victim card %s", q.Status)
	}
	if m, err := core.MessageByMsgID(ctx, pool, victimMsg); err != nil || m.DeliveredAt != nil {
		t.Fatalf("victim message delivered %v %v", m.DeliveredAt, err)
	}
	if task := tm.taskRow(t, th.ID, "t2"); task.Status != "claimed" || task.CompactID != c.CompactID {
		t.Fatalf("victim t2 moved on: %s %q", task.Status, task.CompactID)
	}
	var audits int64
	if err := pool.Gorm(ctx).Table("at_audit_log").Where("actor = ? AND action = 'node_message_rejected'", "node:"+an.Node.ID).
		Count(&audits).Error; err != nil || audits < 5 {
		t.Fatalf("audit rows %d %v", audits, err)
	}

	// The victim's own daemon: approval.cancel without session_id still cancels its card, and
	// its compaction goes on.
	if _, err := tm.n.Send(context.Background(), protocol.TypeApprovalCancel, protocol.ApprovalCancel{ApprovalID: "ap_victim", Reason: "interrupted"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "own card cancelled", func() bool {
		q, _ := core.QuestionByApproval(ctx, pool, "ap_victim")
		return q.Status == "cancelled"
	})
	dev.send(protocol.TypeSessionCompacted, protocol.SessionCompacted{SessionID: c.SessionID, OpenGen: c.OpenGen, CompactID: c.CompactID, Ok: true})
	dev.expectSend("[Task t2")
	for _, e := range tm.n.Errors() {
		if strings.HasPrefix(string(e.Code), "unknown_") {
			t.Fatalf("owner's daemon got %+v", e)
		}
	}
}

// TestGatewayRevokedWhileReplacing (#55 N2, reviewer-2): a live link O is displaced by a new link
// L of the same token, and the rotation (new hash + Disconnect) lands right after L is registered:
// Disconnect closes L, L's second check fails and O is put back, but O's token is checked again
// there, so O is closed with 4401 too and the node is offline, not left on a revoked token.
func TestGatewayRevokedWhileReplacing(t *testing.T) {
	w := newWorld(t, nil)
	b := w.signUp("ops@example.com")
	var node nodeCreated
	b.must(201, "POST", "/api/nodes", map[string]string{"name": "n1"}, &node)
	old := w.connect(node.Token, nodetest.Options{})

	var once sync.Once
	w.e.Gateway.AfterRegister = func(id string) {
		once.Do(func() { // what POST /api/nodes/{id}/token does
			if _, err := store.Exec(w.ctx, w.e.Core.Pool, `UPDATE at_nodes SET token_hash = 'rotated' WHERE id = $1`, id); err != nil {
				t.Error(err)
			}
			w.e.Gateway.Disconnect(w.ctx, id, "token_rotated")
		})
	}
	t.Cleanup(func() { w.e.Gateway.AfterRegister = nil })
	replacing := w.dialRaw(node.Token, nodetest.Options{SkipWelcome: true})
	waitClosed(t, replacing)
	if replacing.CloseCode() != nodegw.CloseUnauthorized {
		t.Fatalf("replacing link close %d", replacing.CloseCode())
	}
	waitClosed(t, old)
	if old.CloseCode() != nodegw.CloseUnauthorized {
		t.Fatalf("displaced link close %d (put back on a revoked token)", old.CloseCode())
	}
	eventually(t, "node offline", func() bool { return !w.e.Gateway.Online(node.Node.ID) })
}

// TestGatewayRotatedWhileSecondLinkHandshakes (#55 N2, review): with the node's link O online, a
// second link of the same token rotates it between upgrade and hello: the second one is refused
// at its hello and O, still the registered link, is cut off by the rotation: both 4401, offline.
func TestGatewayRotatedWhileSecondLinkHandshakes(t *testing.T) {
	w := newWorld(t, nil)
	b := w.signUp("ops@example.com")
	var node nodeCreated
	b.must(201, "POST", "/api/nodes", map[string]string{"name": "n1"}, &node)
	old := w.connect(node.Token, nodetest.Options{})
	second := w.dialRaw(node.Token, nodetest.Options{SkipWelcome: true, BeforeHello: func() {
		b.must(200, "POST", "/api/nodes/"+node.Node.ID+"/token", nil, nil)
	}})
	waitClosed(t, second)
	waitClosed(t, old)
	if second.CloseCode() != nodegw.CloseUnauthorized || old.CloseCode() != nodegw.CloseUnauthorized {
		t.Fatalf("close codes: second %d, old %d", second.CloseCode(), old.CloseCode())
	}
	eventually(t, "node offline", func() bool { return !w.e.Gateway.Online(node.Node.ID) })
}
