// Package events serves the realtime streams (api.md §1): the project stream
// (at_events ∪ at_status_events by seq, Last-Event-ID replay from PostgreSQL,
// topic/thread filters, skip frames, reset), the user stream (at_user_events)
// and the live output of one session (stream.delta, not stored). Valkey/local
// hints only wake the streams; what is sent is always re-read from the database.
package events

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/fanout"
	"github.com/touwaeriol/agent-teams/server/internal/platform"
)

// Limits of the replay window (api.md §1.1, §1.2).
const (
	ProjectReplayWindow = 24 * time.Hour
	UserReplayWindow    = time.Hour
	ReplayMax           = 5000
)

// Hub serves SSE streams and publishes events for the REST layer.
type Hub struct {
	core  *core.Store
	fan   *fanout.Hub
	log   *slog.Logger
	Ping  time.Duration
	delta *deltas
}

// New builds the hub.
func New(cs *core.Store, fan *fanout.Hub, log *slog.Logger) *Hub {
	return &Hub{core: cs, fan: fan, log: log, Ping: 15 * time.Second, delta: newDeltas()}
}

var _ platform.Events = (*Hub)(nil)

// Project implements platform.Events: the event is stored with the project seq, then pushed.
func (h *Hub) Project(ctx context.Context, projectID, typ string, payload any) {
	err := h.core.InTx(ctx, func(tx *core.Tx) error {
		_, err := tx.Emit(ctx, projectID, nil, typ, "", payload)
		return err
	})
	if err != nil {
		h.log.Warn("project event", "project_id", projectID, "type", typ, "err", err)
	}
}

// User implements platform.Events.
func (h *Hub) User(ctx context.Context, userID, typ string, payload any) {
	err := h.core.InTx(ctx, func(tx *core.Tx) error {
		_, err := tx.EmitUser(ctx, userID, typ, payload)
		return err
	})
	if err != nil {
		h.log.Warn("user event", "user_id", userID, "type", typ, "err", err)
	}
}

// Source is one stream: where hints arrive and how to read events after a seq.
type Source struct {
	Channel string
	Window  time.Duration
	After   func(ctx context.Context, after int64, since time.Time, limit int) ([]core.Event, error)
	Current func(ctx context.Context) (int64, error)
	Keep    func(e core.Event) bool // filter; nil keeps everything
}

// ProjectSource is a project's stream with optional topic and thread filters.
func (h *Hub) ProjectSource(projectID string, topics []string, threadNo *int) Source {
	return Source{Channel: core.ProjectChannel(projectID), Window: ProjectReplayWindow,
		After: func(ctx context.Context, after int64, since time.Time, limit int) ([]core.Event, error) {
			return core.EventsAfter(ctx, h.core.Pool, projectID, after, since, limit)
		},
		Current: func(ctx context.Context) (int64, error) { return core.ProjectSeq(ctx, h.core.Pool, projectID) },
		Keep:    filter(topics, threadNo)}
}

// UserSource is a user's stream.
func (h *Hub) UserSource(userID string) Source {
	return Source{Channel: core.UserChannel(userID), Window: UserReplayWindow,
		After: func(ctx context.Context, after int64, since time.Time, limit int) ([]core.Event, error) {
			return core.UserEventsAfter(ctx, h.core.Pool, userID, after, since, limit)
		},
		Current: func(ctx context.Context) (int64, error) { return core.UserSeq(ctx, h.core.Pool, userID) }}
}

func filter(topics []string, threadNo *int) func(core.Event) bool {
	set := map[string]bool{}
	for _, t := range topics {
		if t = strings.TrimSpace(t); t != "" {
			set[t] = true
		}
	}
	return func(e core.Event) bool {
		if len(set) > 0 && !set[e.Type] {
			return false
		}
		return threadNo == nil || (e.ThreadNo != nil && *e.ThreadNo == *threadNo)
	}
}

// Writer is the response a stream writes to.
type Writer struct {
	W     io.Writer
	Flush func()
}

// Serve streams src to w until ctx ends. last < 0 = no Last-Event-ID (start at the current seq).
func (h *Hub) Serve(ctx context.Context, w Writer, src Source, last int64) error {
	hints, cancel := h.fan.Subscribe(src.Channel)
	defer cancel()
	s := &session{h: h, w: w, src: src}
	if err := s.start(ctx, last); err != nil {
		return err
	}
	ping := time.NewTicker(h.Ping)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-hints:
			if err := s.pump(ctx); err != nil {
				return err
			}
		case <-ping.C:
			if err := s.ping(); err != nil {
				return err
			}
		}
	}
}

// session is the state of one SSE connection.
type session struct {
	h      *Hub
	w      Writer
	src    Source
	cursor int64 // highest seq processed (sent or skipped)
	sent   int64 // highest seq whose id the browser has seen
}

func (s *session) start(ctx context.Context, last int64) error {
	if last < 0 {
		cur, err := s.src.Current(ctx)
		s.cursor, s.sent = cur, cur
		if err != nil {
			return err
		}
		return s.comment("connected")
	}
	s.cursor, s.sent = last, last
	return s.replay(ctx)
}

// replay sends what was missed since Last-Event-ID, or "reset" when the window is not enough.
func (s *session) replay(ctx context.Context) error {
	since := time.Now().Add(-s.src.Window)
	evs, err := s.src.After(ctx, s.cursor, since, ReplayMax+1)
	if err != nil {
		return err
	}
	if len(evs) > ReplayMax || (len(evs) > 0 && evs[0].Seq > s.cursor+1 && s.cursor > 0) {
		return s.reset(ctx)
	}
	return s.send(evs)
}

func (s *session) reset(ctx context.Context) error {
	cur, err := s.src.Current(ctx)
	if err != nil {
		return err
	}
	s.cursor, s.sent = cur, cur
	return s.frame(cur, "reset", json.RawMessage(`{}`))
}

func (s *session) pump(ctx context.Context) error {
	for {
		evs, err := s.src.After(ctx, s.cursor, time.Now().Add(-s.src.Window), 500)
		if err != nil || len(evs) == 0 {
			return err
		}
		if err := s.send(evs); err != nil {
			return err
		}
		if len(evs) < 500 {
			return nil
		}
	}
}

func (s *session) send(evs []core.Event) error {
	for _, e := range evs {
		s.cursor = e.Seq
		if s.src.Keep != nil && !s.src.Keep(e) {
			continue
		}
		if err := s.frame(e.Seq, e.Type, e.Data); err != nil {
			return err
		}
	}
	if s.cursor > s.sent {
		return s.skip()
	}
	return nil
}

func (s *session) frame(id int64, typ string, data json.RawMessage) error {
	if _, err := fmt.Fprintf(s.w.W, "id: %d\nevent: %s\ndata: %s\n\n", id, typ, compact(data)); err != nil {
		return err
	}
	s.sent = id
	s.w.Flush()
	return nil
}

// skip moves the browser's Last-Event-ID past filtered-out events.
func (s *session) skip() error {
	if _, err := fmt.Fprintf(s.w.W, "id: %d\n: skip\n\n", s.cursor); err != nil {
		return err
	}
	s.sent = s.cursor
	s.w.Flush()
	return nil
}

func (s *session) ping() error {
	if s.cursor > s.sent {
		return s.skip()
	}
	return s.comment("ping")
}

func (s *session) comment(text string) error {
	if _, err := fmt.Fprintf(s.w.W, ": %s\n\n", text); err != nil {
		return err
	}
	s.w.Flush()
	return nil
}

// compact removes the whitespace PostgreSQL puts into jsonb text (one SSE data line).
func compact(raw json.RawMessage) string {
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		return strings.NewReplacer("\n", "", "\r", "").Replace(string(raw))
	}
	return b.String()
}
