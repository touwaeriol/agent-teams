// Package kv wraps Valkey (valkey-go) behind a small interface: cache entries
// with TTL, counters for rate limits and pub/sub publishing. Memory is an
// in-process implementation for tests and single-binary development.
package kv

import (
	"context"
	"sync"
	"time"
)

// KV is the subset of Valkey the control plane uses.
type KV interface {
	Get(ctx context.Context, key string) (string, bool, error)
	Set(ctx context.Context, key, value string, ttl time.Duration) error
	Del(ctx context.Context, keys ...string) error
	// Incr increments key and sets ttl when the key is new; returns the new value.
	Incr(ctx context.Context, key string, ttl time.Duration) (int64, error)
	Publish(ctx context.Context, channel, message string) error
	Ping(ctx context.Context) error
	Close()
}

// Memory is a process-local KV.
type Memory struct {
	mu     sync.Mutex
	items  map[string]entry
	now    func() time.Time
	Events []Message // published messages, for tests
}

type entry struct {
	value   string
	expires time.Time
}

// Message is one Publish call recorded by Memory.
type Message struct{ Channel, Body string }

// NewMemory returns an empty in-process store.
func NewMemory() *Memory { return &Memory{items: map[string]entry{}, now: time.Now} }

func (m *Memory) live(key string) (entry, bool) {
	e, ok := m.items[key]
	if ok && !e.expires.IsZero() && !m.now().Before(e.expires) {
		delete(m.items, key)
		return entry{}, false
	}
	return e, ok
}

// Get implements KV.
func (m *Memory) Get(_ context.Context, key string) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.live(key)
	return e.value, ok, nil
}

// Set implements KV.
func (m *Memory) Set(_ context.Context, key, value string, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items[key] = entry{value: value, expires: m.expiry(ttl)}
	return nil
}

func (m *Memory) expiry(ttl time.Duration) time.Time {
	if ttl <= 0 {
		return time.Time{}
	}
	return m.now().Add(ttl)
}

// Del implements KV.
func (m *Memory) Del(_ context.Context, keys ...string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, k := range keys {
		delete(m.items, k)
	}
	return nil
}

// Incr implements KV.
func (m *Memory) Incr(_ context.Context, key string, ttl time.Duration) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.live(key)
	if !ok {
		e = entry{value: "0", expires: m.expiry(ttl)}
	}
	n := parseInt(e.value) + 1
	e.value = formatInt(n)
	m.items[key] = e
	return n, nil
}

// Publish implements KV.
func (m *Memory) Publish(_ context.Context, channel, message string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Events = append(m.Events, Message{channel, message})
	return nil
}

// Ping implements KV.
func (m *Memory) Ping(context.Context) error { return nil }

// Close implements KV.
func (m *Memory) Close() {}
