package kv

import (
	"context"
	"os"
	"testing"
	"time"
)

func exercise(t *testing.T, store KV) {
	t.Helper()
	ctx := context.Background()
	key := "test:" + time.Now().Format("150405.000000000")
	if _, ok, _ := store.Get(ctx, key); ok {
		t.Fatal("unexpected key")
	}
	if err := store.Set(ctx, key, "v", time.Minute); err != nil {
		t.Fatal(err)
	}
	if v, ok, err := store.Get(ctx, key); !ok || v != "v" || err != nil {
		t.Fatalf("get = %q %v %v", v, ok, err)
	}
	if err := store.Del(ctx, key); err != nil {
		t.Fatal(err)
	}
	for want := int64(1); want <= 3; want++ {
		if n, err := store.Incr(ctx, key+":n", time.Minute); n != want || err != nil {
			t.Fatalf("incr = %d %v", n, err)
		}
	}
	if err := store.Publish(ctx, "chan", "hi"); err != nil {
		t.Fatal(err)
	}
	if err := store.Ping(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestMemory(t *testing.T) {
	m := NewMemory()
	exercise(t, m)
	clock := time.Now()
	m.now = func() time.Time { return clock }
	_ = m.Set(context.Background(), "e", "x", time.Second)
	clock = clock.Add(2 * time.Second)
	if _, ok, _ := m.Get(context.Background(), "e"); ok {
		t.Fatal("expired entry returned")
	}
}

func TestValkey(t *testing.T) {
	url := os.Getenv("TEST_VALKEY_URL")
	if url == "" {
		t.Skip("TEST_VALKEY_URL not set")
	}
	v, err := Dial(url)
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	exercise(t, v)
}

func TestLimiter(t *testing.T) {
	l := NewLimiter(NewMemory())
	clock := time.Date(2026, 11, 14, 10, 0, 5, 0, time.UTC)
	l.now = func() time.Time { return clock }
	for i := 0; i < 3; i++ {
		if ok, _ := l.Allow(context.Background(), "ip", 3, time.Minute); !ok {
			t.Fatalf("hit %d refused", i)
		}
	}
	ok, wait := l.Allow(context.Background(), "ip", 3, time.Minute)
	if ok || wait != 55*time.Second {
		t.Fatalf("4th hit ok=%v wait=%v", ok, wait)
	}
	clock = clock.Add(time.Minute)
	if ok, _ := l.Allow(context.Background(), "ip", 3, time.Minute); !ok {
		t.Fatal("new window refused")
	}
}
