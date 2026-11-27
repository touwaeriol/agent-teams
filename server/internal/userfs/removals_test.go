package userfs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// TestRemoveRetried: a deleted project's folder that cannot be removed (volume unreachable) is
// recorded and removed by RetryRemovals once the volume answers; no volume is nothing to do.
func TestRemoveRetried(t *testing.T) {
	s, _, _, vols, st := newTest(t)
	ctx := context.Background()
	count := func() (n, attempts int) {
		_ = store.QueryRow(ctx, st.Pool, `SELECT count(*), COALESCE(max(attempts), 0) FROM at_fs_removals`).Scan(&n, &attempts)
		return
	}
	if err := s.RemoveProject(ctx, "u1", "p0"); err != nil {
		t.Fatalf("no volume: %v", err)
	}
	if _, err := s.Enable(ctx, "u1"); err != nil {
		t.Fatal(err)
	}
	dir := vols.userPath("u1", "projects", "p1")
	if err := os.MkdirAll(filepath.Join(dir, ".memory"), 0o755); err != nil {
		t.Fatal(err)
	}

	vols.filesErr = errors.New("gateway down")
	if err := s.RemoveProject(ctx, "u1", "p1"); err == nil {
		t.Fatal("failed removal not reported")
	}
	if n, _ := count(); n != 1 {
		t.Fatalf("recorded removals = %d", n)
	}
	if done, err := s.RetryRemovals(ctx); err != nil || done != 0 {
		t.Fatalf("retry while down = %d %v", done, err)
	}
	if n, attempts := count(); n != 1 || attempts != 1 {
		t.Fatalf("after a failed retry: %d rows, %d attempts", n, attempts)
	}

	vols.filesErr = nil
	if done, err := s.RetryRemovals(ctx); err != nil || done != 1 {
		t.Fatalf("retry = %d %v", done, err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("project folder left: %v", err)
	}
	if n, _ := count(); n != 0 {
		t.Fatalf("rows left = %d", n)
	}
}
