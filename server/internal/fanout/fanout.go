// Package fanout delivers realtime hints ("project p has new events", "node n
// has commands to send", "thread t needs scheduling") to subscribers in this
// process and, through Valkey pub/sub, in other replicas. Hints carry no data
// that matters: receivers re-read PostgreSQL, so a lost or duplicated hint is
// harmless (architecture.md §11.4).
package fanout

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/valkey-io/valkey-go"

	"github.com/touwaeriol/agent-teams/server/internal/kv"
)

// Patterns are the channel families relayed through Valkey.
var Patterns = []string{"project:*", "user:*", "node:*", "sched:*", "merge:*"}

// Hub is the process-local subscriber registry plus the Valkey relay.
type Hub struct {
	mu     sync.Mutex
	subs   map[string]map[chan string]struct{}
	valkey valkey.Client
	log    *slog.Logger
}

// New returns a hub; with a *kv.Valkey store hints also cross replicas.
func New(store kv.KV, log *slog.Logger) *Hub {
	h := &Hub{subs: map[string]map[chan string]struct{}{}, log: log}
	if v, ok := store.(*kv.Valkey); ok {
		h.valkey = v.Client
	}
	return h
}

// Publish delivers msg to local subscribers of channel and to Valkey.
func (h *Hub) Publish(ctx context.Context, channel, msg string) {
	h.deliver(channel, msg)
	if h.valkey == nil {
		return
	}
	cmd := h.valkey.B().Publish().Channel(channel).Message(msg).Build()
	if err := h.valkey.Do(ctx, cmd).Error(); err != nil {
		h.log.Warn("valkey publish failed", "channel", channel, "err", err)
	}
}

// Subscribe returns a channel that receives hints for one channel name; call cancel to stop.
// Hints coalesce: a slow reader sees at least one wake-up after the latest publish.
func (h *Hub) Subscribe(channel string) (<-chan string, func()) {
	ch := make(chan string, 1)
	h.mu.Lock()
	if h.subs[channel] == nil {
		h.subs[channel] = map[chan string]struct{}{}
	}
	h.subs[channel][ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() { h.unsubscribe(channel, ch) }
}

func (h *Hub) unsubscribe(channel string, ch chan string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.subs[channel], ch)
	if len(h.subs[channel]) == 0 {
		delete(h.subs, channel)
	}
}

// Subscribers counts local subscribers of a channel.
func (h *Hub) Subscribers(channel string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs[channel])
}

func (h *Hub) deliver(channel, msg string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs[channel] {
		select {
		case ch <- msg:
		default:
		}
	}
}

// Run relays Valkey messages from other replicas until ctx ends (no-op without Valkey).
func (h *Hub) Run(ctx context.Context) {
	if h.valkey == nil {
		return
	}
	for ctx.Err() == nil {
		cmd := h.valkey.B().Psubscribe().Pattern(Patterns...).Build()
		err := h.valkey.Receive(ctx, cmd, func(m valkey.PubSubMessage) { h.deliver(m.Channel, m.Message) })
		if ctx.Err() != nil {
			return
		}
		h.log.Warn("valkey subscription ended; retrying", "err", err)
		sleep(ctx, 2*time.Second)
	}
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
