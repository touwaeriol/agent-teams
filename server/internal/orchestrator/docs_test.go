package orchestrator

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// TestDocsCacheStaleWhileRevalidate: a stale CLAUDE.md is served at once while one background
// read renews it; only an absent entry waits for GitHub.
func TestDocsCacheStaleWhileRevalidate(t *testing.T) {
	c := &docsCache{items: map[string]docEntry{}}
	ctx := context.Background()
	var calls atomic.Int32
	release := make(chan struct{})
	read := func(context.Context) string {
		if calls.Add(1) > 1 {
			<-release
			return "new"
		}
		return "old"
	}
	if got := c.get(ctx, "p@o/r", read); got != "old" {
		t.Fatalf("first read %q", got)
	}
	c.mu.Lock()
	e := c.items["p@o/r"]
	e.at = e.at.Add(-docTTL)
	c.items["p@o/r"] = e
	c.mu.Unlock()
	start := time.Now()
	for range 3 {
		if got := c.get(ctx, "p@o/r", read); got != "old" {
			t.Fatalf("stale entry served %q", got)
		}
	}
	if took := time.Since(start); took > 100*time.Millisecond {
		t.Fatalf("stale reads waited %v", took)
	}
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for c.get(ctx, "p@o/r", read) != "new" {
		if time.Now().After(deadline) {
			t.Fatal("never refreshed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("read %d times, want 2", n)
	}
}
