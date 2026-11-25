package engine_test

import (
	"sync"
	"testing"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/nodegw/nodetest"
)

// fakeClock drives Gateway.Now (displacement window and cooldown).
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func useClock(t *testing.T, w *world) *fakeClock {
	c := &fakeClock{t: time.Now()}
	w.e.Gateway.Now = c.now
	t.Cleanup(func() { w.e.Gateway.Now = nil })
	return c
}

// takeoverCounts: node_token_takeover audit rows and node_takeover notices for the node.
func takeoverCounts(t *testing.T, w *world, nodeID string) (audits, notices int64) {
	t.Helper()
	g := w.e.Core.Pool.Gorm(w.ctx)
	if err := g.Table("at_audit_log").Where("action = 'node_token_takeover' AND target_id = ?", nodeID).Count(&audits).Error; err != nil {
		t.Fatal(err)
	}
	if err := g.Table("at_user_events").Where("type = 'notice' AND payload->>'kind' = 'node_takeover' AND payload->>'node_id' = ?", nodeID).
		Count(&notices).Error; err != nil {
		t.Fatal(err)
	}
	return audits, notices
}

// TestTakeoverQuietOnNormalReconnects (#55 C2): reconnects of the same daemon (same epoch, the
// old link possibly still half-open) and one reinstall (a new epoch once) raise no alert.
func TestTakeoverQuietOnNormalReconnects(t *testing.T) {
	w := newWorld(t, nil)
	b := w.signUp("ops@example.com")
	var node nodeCreated
	b.must(201, "POST", "/api/nodes", map[string]string{"name": "n1"}, &node)
	w.connect(node.Token, nodetest.Options{Epoch: "e-home"})
	for i := 0; i < 3; i++ { // the old link is still open each time: a reconnect before the dead-link timeout
		w.connect(node.Token, nodetest.Options{Epoch: "e-home"})
	}
	clock := useClock(t, w)
	w.connect(node.Token, nodetest.Options{Epoch: "e-reinstalled"})
	w.connect(node.Token, nodetest.Options{Epoch: "e-reinstalled"})
	// Two instance changes, but further apart than the window (a reinstall, later moved back).
	clock.add(11 * time.Minute)
	w.connect(node.Token, nodetest.Options{Epoch: "e-home"})
	if a, n := takeoverCounts(t, w, node.Node.ID); a != 0 || n != 0 {
		t.Fatalf("alerts on normal reconnects: audit %d notice %d", a, n)
	}
}

// TestTakeoverAlertDeduplicated (#55 C2): two instances (different epochs) displacing each other
// within the window raise one audit row and one notice to the owner; more ping-pong within the
// cooldown adds nothing. The links themselves are not cut by the alert.
func TestTakeoverAlertDeduplicated(t *testing.T) {
	w := newWorld(t, nil)
	b := w.signUp("ops@example.com")
	var node nodeCreated
	b.must(201, "POST", "/api/nodes", map[string]string{"name": "n1"}, &node)
	clock := useClock(t, w)
	w.connect(node.Token, nodetest.Options{Epoch: "e-real"})
	w.connect(node.Token, nodetest.Options{Epoch: "e-thief"}) // one displacement: could be a reinstall
	if a, n := takeoverCounts(t, w, node.Node.ID); a != 0 || n != 0 {
		t.Fatalf("alert after one displacement: audit %d notice %d", a, n)
	}
	w.connect(node.Token, nodetest.Options{Epoch: "e-real"}) // the real daemon comes back: second one
	if a, n := takeoverCounts(t, w, node.Node.ID); a != 1 || n != 1 {
		t.Fatalf("after two displacements: audit %d notice %d", a, n)
	}
	w.connect(node.Token, nodetest.Options{Epoch: "e-thief"})
	last := w.connect(node.Token, nodetest.Options{Epoch: "e-real"})
	if a, n := takeoverCounts(t, w, node.Node.ID); a != 1 || n != 1 {
		t.Fatalf("not deduplicated: audit %d notice %d", a, n)
	}
	if !w.e.Gateway.Online(node.Node.ID) {
		t.Fatal("the alert took the node offline")
	}
	var text string
	if err := w.e.Core.Pool.Gorm(w.ctx).Table("at_user_events").Select("payload->>'text'").
		Where("type = 'notice' AND payload->>'kind' = 'node_takeover'").Take(&text).Error; err != nil || text == "" {
		t.Fatalf("notice text %q %v", text, err)
	}
	_ = last
	// After the cooldown the same ping-pong alerts again.
	clock.add(61 * time.Minute)
	w.connect(node.Token, nodetest.Options{Epoch: "e-thief"})
	w.connect(node.Token, nodetest.Options{Epoch: "e-real"})
	if a, n := takeoverCounts(t, w, node.Node.ID); a != 2 || n != 2 {
		t.Fatalf("after the cooldown: audit %d notice %d", a, n)
	}
}
