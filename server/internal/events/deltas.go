package events

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// UnwatchDelay is how long a session keeps streaming after its last viewer left (api.md §1.3).
const UnwatchDelay = 30 * time.Second

// deltas fans stream.delta text out to the viewers of a session (in this process only).
type deltas struct {
	mu      sync.Mutex
	viewers map[string]map[chan string]struct{}
	timers  map[string]*time.Timer
}

func newDeltas() *deltas {
	return &deltas{viewers: map[string]map[chan string]struct{}{}, timers: map[string]*time.Timer{}}
}

// Delta delivers live output of a session to its viewers (drops when a viewer is slow).
func (h *Hub) Delta(sessionID, text string) {
	h.delta.mu.Lock()
	defer h.delta.mu.Unlock()
	for ch := range h.delta.viewers[sessionID] {
		select {
		case ch <- text:
		default:
		}
	}
}

// Watching reports whether a session has viewers.
func (h *Hub) Watching(sessionID string) bool {
	h.delta.mu.Lock()
	defer h.delta.mu.Unlock()
	return len(h.delta.viewers[sessionID]) > 0
}

// ServeDeltas streams a session's live output. watch(true) runs for the first viewer,
// watch(false) UnwatchDelay after the last one left.
func (h *Hub) ServeDeltas(ctx context.Context, w Writer, sessionID string, watch func(on bool)) error {
	ch, first := h.delta.join(sessionID)
	if first {
		watch(true)
	}
	defer func() {
		if h.delta.leave(sessionID, ch) {
			h.delta.later(sessionID, func() { watch(false) })
		}
	}()
	if _, err := fmt.Fprint(w.W, ": watching\n\n"); err != nil {
		return err
	}
	w.Flush()
	return h.pumpDeltas(ctx, w, ch)
}

func (h *Hub) pumpDeltas(ctx context.Context, w Writer, ch chan string) error {
	ping := time.NewTicker(h.Ping)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case text := <-ch:
			raw, _ := json.Marshal(map[string]string{"text": text})
			if _, err := fmt.Fprintf(w.W, "event: delta\ndata: %s\n\n", raw); err != nil {
				return err
			}
		case <-ping.C:
			if _, err := fmt.Fprint(w.W, ": ping\n\n"); err != nil {
				return err
			}
		}
		w.Flush()
	}
}

func (d *deltas) join(sessionID string) (chan string, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if t, ok := d.timers[sessionID]; ok {
		t.Stop()
		delete(d.timers, sessionID)
	}
	if d.viewers[sessionID] == nil {
		d.viewers[sessionID] = map[chan string]struct{}{}
	}
	ch := make(chan string, 256)
	first := len(d.viewers[sessionID]) == 0
	d.viewers[sessionID][ch] = struct{}{}
	return ch, first
}

// leave removes a viewer; true when it was the last one.
func (d *deltas) leave(sessionID string, ch chan string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.viewers[sessionID], ch)
	if len(d.viewers[sessionID]) > 0 {
		return false
	}
	delete(d.viewers, sessionID)
	return true
}

func (d *deltas) later(sessionID string, fn func()) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.timers[sessionID] = time.AfterFunc(UnwatchDelay, func() {
		d.mu.Lock()
		delete(d.timers, sessionID)
		empty := len(d.viewers[sessionID]) == 0
		d.mu.Unlock()
		if empty {
			fn()
		}
	})
}
