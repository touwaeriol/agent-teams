package orchapi

import (
	"context"
	"testing"
	"time"
)

func acquireAsync(u *moveUploads, ctx context.Context, node string) <-chan func() {
	ch := make(chan func(), 1)
	go func() {
		if rel, ok := u.acquire(ctx, node); ok {
			ch <- rel
		} else {
			ch <- nil
		}
	}()
	return ch
}

func got(t *testing.T, ch <-chan func(), want bool, what string) func() {
	t.Helper()
	select {
	case rel := <-ch:
		if !want {
			t.Fatalf("%s: got a slot, want to wait", what)
		}
		return rel
	case <-time.After(100 * time.Millisecond):
		if want {
			t.Fatalf("%s: still waiting", what)
		}
		return nil
	}
}

// TestMoveUploadSlots: one slot per node, maxMoveUploads in total; waiters run when a slot frees,
// a cancelled waiter gives up without leaking its slot (audit #55 N5).
func TestMoveUploadSlots(t *testing.T) {
	if maxMoveUploads != 2 {
		t.Skip("test written for 2 slots")
	}
	u := newMoveUploads()
	ctx := context.Background()
	relA := got(t, acquireAsync(u, ctx, "a"), true, "a")
	a2 := acquireAsync(u, ctx, "a")
	got(t, a2, false, "a again")
	relB := got(t, acquireAsync(u, ctx, "b"), true, "b")
	cctx, cancel := context.WithCancel(ctx)
	c := acquireAsync(u, cctx, "c")
	got(t, c, false, "c with both slots taken")
	cancel()
	if rel := <-c; rel != nil {
		t.Fatal("cancelled waiter got a slot")
	}
	relB()
	got(t, a2, false, "a again while a's first runs")
	d := got(t, acquireAsync(u, ctx, "d"), true, "d after b released")
	relA()
	relA2 := got(t, a2, true, "a again after a's first")
	relA2()
	d()
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.nodes) != 0 || len(u.slots) != 0 {
		t.Fatalf("leaked: nodes %v, slots %d", u.nodes, len(u.slots))
	}
}
