package delivery

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// TestCredExpiry: an installation token reports its own expiry; a PAT (no expiry) patTTL from now.
func TestCredExpiry(t *testing.T) {
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.FixedZone("x", 3600))
	real := now.Add(17 * time.Minute)
	if got := credExpiry(real, now); !got.Equal(real) || got.Location() != time.UTC {
		t.Fatalf("installation token expiry %v", got)
	}
	if got := credExpiry(time.Time{}, now); !got.Equal(now.Add(patTTL)) {
		t.Fatalf("PAT expiry %v", got)
	}
}

// TestCredentialFullSlotsDoNotBlock: with every slot taken, Credential returns at once (the read
// loop never waits) and drops the request; the daemon retries it after 10 s.
func TestCredentialFullSlotsDoNotBlock(t *testing.T) {
	for i := 0; i < credInflight; i++ {
		credSlots <- struct{}{}
	}
	defer func() {
		for i := 0; i < credInflight; i++ {
			<-credSlots
		}
	}()
	e := &Engine{log: slog.New(slog.NewTextHandler(io.Discard, nil)), now: time.Now}
	done := make(chan error, 1)
	go func() {
		done <- e.Credential(context.Background(), store.Node{ID: "n1"}, &protocol.CredRequest{ReqID: "r1", Host: "github.com", Path: "o/r.git", Purpose: "read"})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Credential with full slots: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Credential blocked the read loop while every slot was taken")
	}
	if len(credSlots) != credInflight {
		t.Fatalf("a dropped request took a slot: %d", len(credSlots))
	}
}
