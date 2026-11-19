package sharedfiles_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/touwaeriol/agent-teams/server/internal/artifacts"
	"github.com/touwaeriol/agent-teams/server/internal/sharedfiles"
	"github.com/touwaeriol/agent-teams/server/internal/store"
	"github.com/touwaeriol/agent-teams/server/internal/testdb"
)

// TestCleanPath: relative clean paths pass (a leading ./ and surrounding spaces are dropped);
// absolute paths, traversal, hidden and empty segments, backslashes, control characters and
// paths over 8 levels or 255 bytes are refused.
func TestCleanPath(t *testing.T) {
	ok := map[string]string{
		"notes.md": "notes.md", "data/sample.csv": "data/sample.csv", "./data/a b.txt": "data/a b.txt",
		"报告/2026-11.xlsx": "报告/2026-11.xlsx", "a b/c d.txt": "a b/c d.txt", strings.Repeat("中", 85): strings.Repeat("中", 85), "a/b/c/d/e/f/g/h": "a/b/c/d/e/f/g/h", "no-ext": "no-ext",
		"a..b/c": "a..b/c", strings.Repeat("x", 255): strings.Repeat("x", 255),
	}
	for in, want := range ok {
		got, err := sharedfiles.CleanPath(in)
		if err != nil || got != want {
			t.Errorf("CleanPath(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	bad := []string{
		"", " ", "/etc/passwd", "../secret", "a/../../b", "a/..", "..", ".", "./", "a/./b", "a//b", "a/", "/",
		".env", "a/.git/config", ".shared/x", `a\b`, `..\..\windows`, "a\x00b", "a\nb", "a\tb", "a\x7fb",
		"a/b/c/d/e/f/g/h/i", strings.Repeat("x", 256), "\xff\xfe", " a", "a ", "a/ b", "a /b", " 报告/x.csv",
		"a\u0085b", "a\u009fb", "a\u2028b", "a\u2029b", strings.Repeat("中", 86), // 86 characters but 258 bytes
		".shared/x/y",
	}
	for _, in := range bad {
		if got, err := sharedfiles.CleanPath(in); !errors.Is(err, sharedfiles.ErrInvalidPath) {
			t.Errorf("CleanPath(%q) = %q, %v; want ErrInvalidPath", in, got, err)
		}
	}
	if p, err := sharedfiles.Join("data/", "a.csv"); err != nil || p != "data/a.csv" {
		t.Errorf("join %q %v", p, err)
	}
	if p, err := sharedfiles.Join("", "a.csv"); err != nil || p != "a.csv" {
		t.Errorf("join without dir %q %v", p, err)
	}
	for _, c := range [][2]string{{"..", "a"}, {"data", "../a"}, {"/etc", "a"}, {"a/b/c/d/e/f/g", "h/i"}} {
		if _, err := sharedfiles.Join(c[0], c[1]); !errors.Is(err, sharedfiles.ErrInvalidPath) {
			t.Errorf("Join(%q, %q) accepted", c[0], c[1])
		}
	}
}

func TestContentType(t *testing.T) {
	for p, want := range map[string]string{"a.csv": "text/csv; charset=utf-8", "b.PNG": "image/png", "c.zip": "application/zip",
		"d.bin": "application/octet-stream", "Makefile": "application/octet-stream", "e.html": "text/html; charset=utf-8"} {
		if got := sharedfiles.ContentType(p); got != want {
			t.Errorf("ContentType(%s) = %s, want %s", p, got, want)
		}
	}
}

// TestCheck: the per-file limit, and the project's byte and file limits with overwrites counted by
// the difference.
func TestCheck(t *testing.T) {
	const mb = 1 << 20
	full := sharedfiles.Usage{Files: sharedfiles.MaxProjectFiles, Bytes: 10 * mb}
	cases := []struct {
		name      string
		u         sharedfiles.Usage
		size, old int64
		want      error
	}{
		{"fits", sharedfiles.Usage{Files: 3, Bytes: 10 * mb}, 5 * mb, -1, nil},
		{"exactly 20 MB", sharedfiles.Usage{}, sharedfiles.MaxFileBytes, -1, nil},
		{"one byte over 20 MB", sharedfiles.Usage{}, sharedfiles.MaxFileBytes + 1, -1, sharedfiles.ErrFileTooLarge},
		{"project bytes", sharedfiles.Usage{Files: 30, Bytes: sharedfiles.MaxProjectBytes - mb}, 2 * mb, -1, sharedfiles.ErrQuota},
		{"overwrite counts the difference", sharedfiles.Usage{Files: 30, Bytes: sharedfiles.MaxProjectBytes - mb}, 2 * mb, 2 * mb, nil},
		{"overwrite that grows too much", sharedfiles.Usage{Files: 30, Bytes: sharedfiles.MaxProjectBytes - mb}, 5 * mb, 2 * mb, sharedfiles.ErrQuota},
		{"file count", full, 1, -1, sharedfiles.ErrQuota},
		{"overwrite at the file count", full, 1, 1, nil},
	}
	for _, c := range cases {
		err := sharedfiles.Check(c.u, c.size, c.old)
		if (c.want == nil && err != nil) || (c.want != nil && !errors.Is(err, c.want)) {
			t.Errorf("%s: %v, want %v", c.name, err, c.want)
		}
	}
	if artifacts.MaxPending < 2*sharedfiles.MaxFileBytes || artifacts.MaxUploadBytes < sharedfiles.MaxFileBytes {
		t.Fatal("unclaimed chunk limit must hold two full shared-file uploads")
	}
}

// TestPutOverwriteDelete runs against PostgreSQL: insert, overwrite in place (new uploader, same
// created_at, overwritten=true), list with prefix, quotas measured in the database, delete.
func TestPutOverwriteDelete(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, testdb.Migrated(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	u, err := s.CreateUser(ctx, "sf@example.com", "h")
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.CreateProject(ctx, u.ID, store.NewProject{Name: "sf", WorkMode: "worktree"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.CreateProject(ctx, u.ID, store.NewProject{Name: "other", WorkMode: "worktree"})
	if err != nil {
		t.Fatal(err)
	}
	q := s.Pool
	f, over, err := sharedfiles.Put(ctx, q, p.ID, "data/a.csv", []byte("x,y\n1,2\n"), sharedfiles.Uploader{Name: sharedfiles.UserName})
	if err != nil || over || f.Size != 8 || f.UploadedBy != "user" || f.ContentType != "text/csv; charset=utf-8" ||
		f.SHA256 != sharedfiles.Sum([]byte("x,y\n1,2\n")) {
		t.Fatalf("put %+v %v %v", f, over, err)
	}
	g, over, err := sharedfiles.Put(ctx, q, p.ID, "./data/a.csv", []byte("x\n"), sharedfiles.Uploader{Name: "dev-1"})
	if err != nil || !over || g.Size != 2 || g.UploadedBy != "dev-1" || !g.CreatedAt.Equal(f.CreatedAt) || g.UpdatedAt.Before(f.UpdatedAt) {
		t.Fatalf("overwrite %+v %v %v", g, over, err)
	}
	if _, _, err := sharedfiles.Put(ctx, q, p.ID, "../a.csv", []byte("x"), sharedfiles.Uploader{Name: "user"}); !errors.Is(err, sharedfiles.ErrInvalidPath) {
		t.Fatalf("traversal stored: %v", err)
	}
	if _, _, err := sharedfiles.Put(ctx, q, p.ID, "big.bin", make([]byte, sharedfiles.MaxFileBytes+1), sharedfiles.Uploader{Name: "user"}); !errors.Is(err, sharedfiles.ErrFileTooLarge) {
		t.Fatalf("21 MB stored: %v", err)
	}
	if _, _, err := sharedfiles.Put(ctx, q, p.ID, "notes.md", []byte("# n"), sharedfiles.Uploader{Name: "user"}); err != nil {
		t.Fatal(err)
	}
	usage, _ := sharedfiles.ProjectUsage(ctx, q, p.ID)
	if usage.Files != 2 || usage.Bytes != 5 {
		t.Fatalf("usage %+v", usage)
	}
	list, _ := sharedfiles.List(ctx, q, p.ID, "data/")
	if len(list) != 1 || list[0].Path != "data/a.csv" {
		t.Fatalf("prefix list %+v", list)
	}
	if list, _ := sharedfiles.List(ctx, q, other.ID, ""); len(list) != 0 {
		t.Fatalf("other project sees %+v", list)
	}
	if _, _, err := sharedfiles.Load(ctx, q, other.ID, "notes.md"); !errors.Is(err, sharedfiles.ErrNotFound) {
		t.Fatalf("other project loads: %v", err)
	}
	_, data, err := sharedfiles.Load(ctx, q, p.ID, "data/a.csv")
	if err != nil || string(data) != "x\n" {
		t.Fatalf("load %q %v", data, err)
	}
	// The project quota is measured in the database: pretend 499.99 MB are taken.
	if err := q.Gorm(ctx).Exec(`UPDATE at_shared_files SET size = ? WHERE project_id = ? AND path = 'notes.md'`,
		sharedfiles.MaxProjectBytes-10, p.ID).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := sharedfiles.Put(ctx, q, p.ID, "more.txt", []byte("0123456789"), sharedfiles.Uploader{Name: "user"}); !errors.Is(err, sharedfiles.ErrQuota) {
		t.Fatalf("over the project total: %v", err)
	}
	if _, _, err := sharedfiles.Put(ctx, q, p.ID, "data/a.csv", []byte("0123456789"), sharedfiles.Uploader{Name: "user"}); err != nil {
		t.Fatalf("overwrite within the difference: %v", err)
	}
	if ok, err := sharedfiles.Delete(ctx, q, p.ID, "data/a.csv"); !ok || err != nil {
		t.Fatalf("delete %v %v", ok, err)
	}
	if ok, _ := sharedfiles.Delete(ctx, q, p.ID, "data/a.csv"); ok {
		t.Fatal("deleted twice")
	}
}
