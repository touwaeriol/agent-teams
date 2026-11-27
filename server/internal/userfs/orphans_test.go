package userfs

import (
	"context"
	"os"
	"testing"

	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// TestSweepOrphans: a user folder whose row is gone (the removal failed) is removed at the second
// sweep that finds it ownerless; the folders that still have an owner are left alone.
func TestSweepOrphans(t *testing.T) {
	s, _, _, vols, st := newTest(t)
	ctx := context.Background()
	if n, err := s.SweepOrphans(ctx); err != nil || n != 0 {
		t.Fatalf("empty volume = %d %v", n, err)
	}
	if _, err := store.Exec(ctx, st.Pool, `INSERT INTO at_users (id, email, password_hash) VALUES ('u2', 'b@example.com', 'x')`); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"u1", "u2"} {
		if _, err := s.Enable(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Exec(ctx, st.Pool, `DELETE FROM at_users WHERE id = 'u2'`); err != nil {
		t.Fatal(err)
	}
	// A file at users/ is no folder of anyone's.
	if err := os.WriteFile(vols.userPath("u1", "..", "stray.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if n, err := s.SweepOrphans(ctx); err != nil || n != 0 {
		t.Fatalf("first sweep = %d %v", n, err)
	}
	if !exists(vols.userPath("u2")) {
		t.Fatal("removed at the first sweep")
	}
	if n, err := s.SweepOrphans(ctx); err != nil || n != 1 {
		t.Fatalf("second sweep = %d %v", n, err)
	}
	if exists(vols.userPath("u2")) {
		t.Fatal("ownerless folder left")
	}
	if !exists(vols.userPath("u1", "projects")) || !exists(vols.userPath("u1", "..", "stray.txt")) {
		t.Fatal("owned folder or a file touched")
	}
	if n, err := s.SweepOrphans(ctx); err != nil || n != 0 {
		t.Fatalf("third sweep = %d %v", n, err)
	}
}
