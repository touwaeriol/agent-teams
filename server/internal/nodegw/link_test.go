package nodegw

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// failingHandler fails its first calls with err, then succeeds.
type failingHandler struct {
	fails int
	err   error
	calls int
}

func (h *failingHandler) Handle(context.Context, store.Node, string, any) error {
	h.calls++
	if h.calls <= h.fails {
		return h.err
	}
	return nil
}

func (h *failingHandler) NodeOnline(context.Context, store.Node)  {}
func (h *failingHandler) NodeOffline(context.Context, store.Node) {}

func testLink(h Handler) *link {
	g := &Gateway{handler: h, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	return &link{g: g}
}

// TestHandleRetriesDeadlock: a message whose transaction PostgreSQL rolled back as a deadlock
// victim (or a serialization failure) is run again in place instead of closing the link; other
// errors, and a deadlock that keeps coming back, still close it (the daemon replays).
func TestHandleRetriesDeadlock(t *testing.T) {
	ctx := context.Background()
	deadlock := fmt.Errorf("session event: %w", &pgconn.PgError{Code: "40P01", Message: "deadlock detected"})
	serial := &pgconn.PgError{Code: "40001", Message: "could not serialize access"}

	h := &failingHandler{fails: 2, err: deadlock}
	if err := testLink(h).handle(ctx, "session.event", nil); err != nil || h.calls != 3 {
		t.Fatalf("deadlock twice: err %v after %d calls", err, h.calls)
	}
	h = &failingHandler{fails: 1, err: serial}
	if err := testLink(h).handle(ctx, "session.event", nil); err != nil || h.calls != 2 {
		t.Fatalf("serialization failure: err %v after %d calls", err, h.calls)
	}
	h = &failingHandler{fails: 100, err: deadlock}
	if err := testLink(h).handle(ctx, "session.event", nil); !store.IsRetryable(err) || h.calls != handleAttempts {
		t.Fatalf("endless deadlock: err %v after %d calls", err, h.calls)
	}
	other := errors.New("boom")
	h = &failingHandler{fails: 1, err: other}
	if err := testLink(h).handle(ctx, "session.event", nil); !errors.Is(err, other) || h.calls != 1 {
		t.Fatalf("other error: err %v after %d calls", err, h.calls)
	}
}
