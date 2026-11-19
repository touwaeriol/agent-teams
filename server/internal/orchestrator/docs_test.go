package orchestrator

import (
	"context"
	"errors"
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
	read := func(context.Context) (string, error) {
		if calls.Add(1) > 1 {
			<-release
			return "new", nil
		}
		return "old", nil
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

// staleDoc moves a cached CLAUDE.md back past docTTL.
func staleDoc(c *docsCache, key string) {
	c.mu.Lock()
	e := c.items[key]
	e.at = e.at.Add(-docTTL)
	c.items[key] = e
	c.mu.Unlock()
}

// TestDocsCacheRefreshFailureKeepsText: a failed background refresh keeps the cached CLAUDE.md
// (a GitHub hiccup must not drop it from the next sessions' prompt) and is not retried at once.
func TestDocsCacheRefreshFailureKeepsText(t *testing.T) {
	c := &docsCache{items: map[string]docEntry{}}
	ctx := context.Background()
	var calls atomic.Int32
	read := func(context.Context) (string, error) {
		if calls.Add(1) > 1 {
			return "", errors.New("github down")
		}
		return "rules", nil
	}
	c.get(ctx, "p@o/r", read)
	staleDoc(c, "p@o/r")
	c.get(ctx, "p@o/r", read)
	deadline := time.Now().Add(5 * time.Second)
	for {
		c.mu.Lock()
		e := c.items["p@o/r"]
		c.mu.Unlock()
		if calls.Load() == 2 && !e.refreshing {
			if e.text != "rules" || time.Since(e.at) >= docTTL {
				t.Fatalf("after failed refresh %+v", e)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("refresh did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := c.get(ctx, "p@o/r", read); got != "rules" || calls.Load() != 2 {
		t.Fatalf("served %q after %d reads", got, calls.Load())
	}
}

// TestDocsCacheCallerGaveUp: a read that failed because the caller's context ended is not cached;
// a failed read of a live caller is cached as "".
func TestDocsCacheCallerGaveUp(t *testing.T) {
	c := &docsCache{items: map[string]docEntry{}}
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	c.get(gone, "p@o/r", func(ctx context.Context) (string, error) { return "", ctx.Err() })
	if _, ok := c.items["p@o/r"]; ok {
		t.Fatal("cancelled read was cached")
	}
	if got := c.get(context.Background(), "p@o/r", func(context.Context) (string, error) { return "rules", nil }); got != "rules" {
		t.Fatalf("read after cancel %q", got)
	}
	c.get(context.Background(), "p@o/x", func(context.Context) (string, error) { return "", errors.New("github down") })
	if e, ok := c.items["p@o/x"]; !ok || e.text != "" {
		t.Fatalf("failed read not cached: %+v %v", e, ok)
	}
}
