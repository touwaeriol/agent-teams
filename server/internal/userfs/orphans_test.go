package userfs

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// TestSweepOrphans: a user's folder and key whose row is gone (the removal failed) are removed at
// the second sweep that finds them ownerless; owned folders and keys, other prefixes and keys not
// named like a user's are left alone.
func TestSweepOrphans(t *testing.T) {
	s, fs, st := newTest(t)
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
	// Prefixes and keys not named like a user's are no one's business here.
	for _, d := range []string{"attachments", "users/someone"} {
		if err := os.MkdirAll(filepath.Join(fs.root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	fs.keys["platform"] = fakeKey{name: "agent-teams"}

	if n, err := s.SweepOrphans(ctx); err != nil || n != 0 {
		t.Fatalf("first sweep = %d %v", n, err)
	}
	if !exists(fs.userPath("u2")) {
		t.Fatal("removed at the first sweep")
	}
	if n, err := s.SweepOrphans(ctx); err != nil || n != 2 {
		t.Fatalf("second sweep = %d %v", n, err)
	}
	if n, err := s.SweepOrphans(ctx); err != nil || n != 0 {
		t.Fatalf("u2 key revoked at the same sweep: %d %v", n, err)
	}
	if exists(fs.userPath("u2")) || fs.keyCount() != 2 {
		t.Fatalf("ownerless folder or its key left (%d keys)", fs.keyCount())
	}
	u1, _ := s.Get(ctx, "u1")
	if _, ok := fs.key(u1.AccessKey); !ok || !exists(fs.userPath("u1", "projects")) || !exists(filepath.Join(fs.root, "attachments")) ||
		!exists(filepath.Join(fs.root, "users/someone")) {
		t.Fatal("owned or foreign folder or key touched")
	}
	if n, err := s.SweepOrphans(ctx); err != nil || n != 0 {
		t.Fatalf("third sweep = %d %v", n, err)
	}
}
