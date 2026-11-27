package userfs

import (
	"context"
	"strings"
	"testing"

	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// TestSweepOrphans: a volume whose user row is gone (teardown failed) is destroyed at the second
// sweep that finds it ownerless, with a reset metadata password; its account and role go too,
// and the volumes that still have an owner are left alone.
func TestSweepOrphans(t *testing.T) {
	s, m, iam, vols, st := newTest(t)
	ctx := context.Background()
	if _, err := store.Exec(ctx, st.Pool, `INSERT INTO at_users (id, email, password_hash) VALUES ('u2', 'b@example.com', 'x')`); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"u1", "u2"} {
		if _, err := s.Enable(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	gone := NamesFor("u2")
	oldPassword := m.roles[gone.Role]
	if _, err := store.Exec(ctx, st.Pool, `DELETE FROM at_users WHERE id = 'u2'`); err != nil {
		t.Fatal(err)
	}

	if n, err := s.SweepOrphans(ctx); err != nil || n != 0 {
		t.Fatalf("first sweep = %d %v", n, err)
	}
	if _, ok := m.roles[gone.Role]; !ok || vols.destroys() != 0 {
		t.Fatal("destroyed at the first sweep")
	}
	if n, err := s.SweepOrphans(ctx); err != nil || n != 1 {
		t.Fatalf("second sweep = %d %v", n, err)
	}
	if _, ok := m.roles[gone.Role]; ok {
		t.Fatal("role left")
	}
	if _, ok := iam.users[gone.AccessKey]; ok {
		t.Fatal("object-store account left")
	}
	if vols.destroys() != 1 || !strings.Contains(vols.destroyAt[0], gone.Role+"@") {
		t.Fatalf("destroy calls %v", vols.destroyAt)
	}
	if m.roles[NamesFor("u1").Role] == "" || oldPassword == "" {
		t.Fatal("owned volume touched")
	}
	if n, err := s.SweepOrphans(ctx); err != nil || n != 0 {
		t.Fatalf("third sweep = %d %v", n, err)
	}
}
