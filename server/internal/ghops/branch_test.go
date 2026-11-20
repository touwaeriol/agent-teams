package ghops

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// age moves a cached default branch back in time.
func age(key string, d time.Duration) {
	defaultBranches.Lock()
	c := defaultBranches.items[key]
	c.at = c.at.Add(-d)
	defaultBranches.items[key] = c
	defaultBranches.Unlock()
}

// TestDefaultBranchStaleWhileRevalidate: a stale default branch is served at once (a session open
// holds the project row and must not wait for GitHub) while one background request renews it.
func TestDefaultBranchStaleWhileRevalidate(t *testing.T) {
	ctx := context.Background()
	key := "conn-swr|o/swr"
	var calls atomic.Int32
	release := make(chan struct{})
	slow := func(ctx context.Context) (string, error) {
		if calls.Add(1) > 1 {
			<-release // the refresh: GitHub is slow
			return "develop", nil
		}
		return "main", nil
	}
	if b, err := cachedBranchOf(ctx, key, slow); b != "main" || err != nil {
		t.Fatalf("first lookup %q %v", b, err)
	}
	if b, _ := cachedBranchOf(ctx, key, slow); b != "main" || calls.Load() != 1 {
		t.Fatalf("fresh entry: %q after %d calls", b, calls.Load())
	}
	age(key, branchTTL)
	start := time.Now()
	for range 3 {
		if b, _ := cachedBranchOf(ctx, key, slow); b != "main" {
			t.Fatalf("stale entry served %q", b)
		}
	}
	if took := time.Since(start); took > 100*time.Millisecond {
		t.Fatalf("stale lookups waited %v for GitHub", took)
	}
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for {
		b, _ := cachedBranchOf(ctx, key, slow)
		if b == "develop" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("never refreshed: %q", b)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("GitHub asked %d times, want 2 (one refresh for several stale reads)", n)
	}
}

// TestDefaultBranchRefreshFailureKeepsName: a failed background refresh keeps the last good name.
func TestDefaultBranchRefreshFailureKeepsName(t *testing.T) {
	ctx := context.Background()
	key := "conn-keep|o/keep"
	var calls atomic.Int32
	fetch := func(context.Context) (string, error) {
		if calls.Add(1) > 1 {
			return "", errors.New("github down")
		}
		return "trunk", nil
	}
	_, _ = cachedBranchOf(ctx, key, fetch)
	age(key, branchTTL)
	_, _ = cachedBranchOf(ctx, key, fetch)
	deadline := time.Now().Add(5 * time.Second)
	for calls.Load() < 2 || refreshing(key) {
		if time.Now().After(deadline) {
			t.Fatal("refresh did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if b, err := cachedBranchOf(ctx, key, fetch); b != "trunk" || err != nil || calls.Load() != 2 {
		t.Fatalf("after failed refresh %q %v (%d calls)", b, err, calls.Load())
	}
}

func refreshing(key string) bool {
	defaultBranches.Lock()
	defer defaultBranches.Unlock()
	return defaultBranches.items[key].refreshing
}

// TestDefaultBranchFailureRemembered: a failed lookup is not repeated for branchFailTTL (an outage
// does not cost a GitHub round trip on every session open), then is asked again.
func TestDefaultBranchFailureRemembered(t *testing.T) {
	ctx := context.Background()
	key := "conn-fail|o/fail"
	var calls atomic.Int32
	fetch := func(context.Context) (string, error) {
		if calls.Add(1) == 1 {
			return "", errors.New("github down")
		}
		return "main", nil
	}
	for range 3 {
		if _, err := cachedBranchOf(ctx, key, fetch); err == nil {
			t.Fatal("failure not reported")
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("failed lookup repeated %d times", calls.Load())
	}
	age(key, branchFailTTL)
	if b, err := cachedBranchOf(ctx, key, fetch); b != "main" || err != nil {
		t.Fatalf("after the failure expired %q %v", b, err)
	}
}

// TestDefaultBranchCallerGaveUp: a lookup whose caller's context ended is not remembered as a failure.
func TestDefaultBranchCallerGaveUp(t *testing.T) {
	key := "conn-gone|o/gone"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = cachedBranchOf(ctx, key, func(ctx context.Context) (string, error) { return "", ctx.Err() })
	if b, err := cachedBranchOf(context.Background(), key, func(context.Context) (string, error) { return "main", nil }); b != "main" || err != nil {
		t.Fatalf("cancelled lookup was cached: %q %v", b, err)
	}
}

// The default branch cache is per git connection, like the file lists.
func TestBranchKeyIsPerConnection(t *testing.T) {
	a := Repo{Slug: "acme/app"}
	a.conn.ID = "conn-a"
	b := Repo{Slug: "ACME/app"}
	b.conn.ID = "conn-b"
	if branchKey(a) == branchKey(b) {
		t.Fatal("two connections share a cache entry")
	}
}
