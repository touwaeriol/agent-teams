package memfiles_test

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/memfiles"
	"github.com/touwaeriol/agent-teams/server/internal/store"
	"github.com/touwaeriol/agent-teams/server/internal/testdb"
	"github.com/touwaeriol/agent-teams/server/internal/userfs"
)

// folders gives every user a local directory as volume; off makes them unavailable.
type folders struct {
	root string
	off  bool
}

func (f *folders) Files(_ context.Context, userID string) (userfs.Files, error) {
	if f.off {
		return nil, userfs.ErrNotFound
	}
	dir := filepath.Join(f.root, userID)
	return userfs.DirFiles{Root: dir}, os.MkdirAll(dir, 0o755)
}

type fixture struct {
	ctx   context.Context
	s     *store.Store
	sync  *memfiles.Syncer
	f     *folders
	user  string
	scope store.MemoryScope
	dir   string
	seen  []string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, testdb.Migrated(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	u, err := s.CreateUser(ctx, "mem@example.com", "h")
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.CreateProject(ctx, u.ID, store.NewProject{Name: "m", WorkMode: "worktree"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Exec(ctx, s.Pool, `INSERT INTO at_user_fs (user_id, volume, meta_role, s3_access_key, secrets, capacity_gb, state)
		VALUES ($1, 'at-x', 'jfs_x', 'jfs-x', '{}', 20, 'ready')`, u.ID); err != nil {
		t.Fatal(err)
	}
	fx := &fixture{ctx: ctx, s: s, f: &folders{root: t.TempDir()}, user: u.ID, scope: store.ProjectMemory(p.ID)}
	fx.dir = filepath.Join(fx.f.root, u.ID, "projects", p.ID, ".memory")
	fx.sync = &memfiles.Syncer{DB: s.Pool, Folders: fx.f, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		OnChange: func(_ context.Context, _ store.MemoryScope, name, op, by string) {
			fx.seen = append(fx.seen, op+" "+name+" "+by)
		}}
	return fx
}

func (fx *fixture) file(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(fx.dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func (fx *fixture) row(t *testing.T, name string) store.MemoryFile {
	t.Helper()
	f, err := store.GetMemoryFile(fx.ctx, fx.s.Pool, fx.scope, name)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// edit writes a file as a node would, a few seconds in the future so the change shows.
func (fx *fixture) edit(t *testing.T, name, content string, ahead time.Duration) {
	t.Helper()
	p := filepath.Join(fx.dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(ahead)
	if err := os.Chtimes(p, at, at); err != nil {
		t.Fatal(err)
	}
}

// TestSyncWritesOutAndReadsBack: rows written through the platform land in the folder with their
// frontmatter; a node's edit, a new file and a deleted file come back into the rows (index lines
// follow, credited to whoever touched the file) and are announced.
func TestSyncWritesOutAndReadsBack(t *testing.T) {
	fx := newFixture(t)
	ctx, q := fx.ctx, fx.s.Pool
	if _, _, err := store.WriteMemoryFile(ctx, q, fx.scope, store.MemoryWrite{Name: "build", Description: "怎么构建", Type: "project", Content: "make all", By: "lead-1"}); err != nil {
		t.Fatal(err)
	}
	if err := fx.sync.Sync(ctx, fx.scope); err != nil {
		t.Fatal(err)
	}
	if got := fx.file(t, "build.md"); got != "---\nname: build\ndescription: 怎么构建\ntype: project\n---\n\nmake all\n" {
		t.Fatalf("build.md %q", got)
	}
	if got := fx.file(t, "MEMORY.md"); !strings.Contains(got, "- [build.md](build.md) — 怎么构建") {
		t.Fatalf("MEMORY.md %q", got)
	}
	if r := fx.row(t, "build.md"); r.Pending || r.FileMtime == nil {
		t.Fatalf("not flushed %+v", r)
	}
	if err := fx.sync.Sync(ctx, fx.scope); err != nil || len(fx.seen) != 0 {
		t.Fatalf("a second sync changed something: %v %v", fx.seen, err)
	}

	if err := store.TouchMemory(ctx, q, fx.scope, "build.md", "dev-1"); err != nil {
		t.Fatal(err)
	}
	fx.edit(t, "build.md", "---\nname: build\ndescription: 构建和打包\ntype: reference\n---\n\nmake all && make dist\n", 2*time.Second)
	fx.edit(t, "deploy.md", "---\ndescription: \"怎么发布\"\n---\n滚动发布\n", 2*time.Second)
	if err := fx.sync.Sync(ctx, fx.scope); err != nil {
		t.Fatal(err)
	}
	if r := fx.row(t, "build.md"); r.Content != "make all && make dist" || r.Description != "构建和打包" || r.Type != "reference" || r.UpdatedBy != "dev-1" {
		t.Fatalf("edit not read back %+v", r)
	}
	if r := fx.row(t, "deploy.md"); r.Content != "滚动发布" || r.UpdatedBy != memfiles.ByNode {
		t.Fatalf("new file %+v", r)
	}
	idx := fx.file(t, "MEMORY.md")
	if !strings.Contains(idx, "- [build.md](build.md) — 构建和打包") || !strings.Contains(idx, "- [deploy.md](deploy.md) — 怎么发布") {
		t.Fatalf("index not rewritten in the folder %q", idx)
	}

	if err := os.Remove(filepath.Join(fx.dir, "deploy.md")); err != nil {
		t.Fatal(err)
	}
	if err := fx.sync.Sync(ctx, fx.scope); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetMemoryFile(ctx, q, fx.scope, "deploy.md"); err == nil {
		t.Fatal("a file deleted on the node stays")
	}
	if strings.Contains(fx.file(t, "MEMORY.md"), "deploy.md") {
		t.Fatal("index line of a deleted file stays")
	}
	slices.Sort(fx.seen[:2]) // the two edits are read in any order
	want := []string{"write build.md dev-1", "write deploy.md " + memfiles.ByNode, "delete deploy.md " + memfiles.ByNode}
	if !slices.Equal(fx.seen, want) {
		t.Fatalf("announced %v", fx.seen)
	}
}

// TestPendingWinsAndRemoving: a row written here and not yet out wins over the file; MEMORY.md
// deleted on a node comes back; Removing deletes the file, and with the folder out of reach it
// refuses a file that was written out but lets one that never was go.
func TestPendingWinsAndRemoving(t *testing.T) {
	fx := newFixture(t)
	ctx, q := fx.ctx, fx.s.Pool
	if _, _, err := store.WriteMemoryFile(ctx, q, fx.scope, store.MemoryWrite{Name: "a", Description: "A", Content: "v1"}); err != nil {
		t.Fatal(err)
	}
	if err := fx.sync.Sync(ctx, fx.scope); err != nil {
		t.Fatal(err)
	}
	fx.edit(t, "a.md", "---\ndescription: A\n---\nnode", 2*time.Second)
	if _, _, err := store.WriteMemoryFile(ctx, q, fx.scope, store.MemoryWrite{Name: "a", Description: "A", Content: "platform"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(fx.dir, "MEMORY.md")); err != nil {
		t.Fatal(err)
	}
	if err := fx.sync.Sync(ctx, fx.scope); err != nil {
		t.Fatal(err)
	}
	if fx.row(t, "a.md").Content != "platform" || !strings.HasSuffix(fx.file(t, "a.md"), "platform\n") {
		t.Fatal("the pending write lost")
	}
	if err := fx.sync.Sync(ctx, fx.scope); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fx.file(t, "MEMORY.md"), "a.md") {
		t.Fatal("MEMORY.md not written again")
	}

	if err := fx.sync.Removing(ctx, fx.scope, "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(fx.dir, "a.md")); !os.IsNotExist(err) {
		t.Fatal("file not removed")
	}
	if err := store.DeleteMemoryFile(ctx, q, fx.scope, "a", "user"); err != nil {
		t.Fatal(err)
	}

	if _, _, err := store.WriteMemoryFile(ctx, q, fx.scope, store.MemoryWrite{Name: "b", Description: "B", Content: "b"}); err != nil {
		t.Fatal(err)
	}
	if err := fx.sync.Sync(ctx, fx.scope); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.WriteMemoryFile(ctx, q, fx.scope, store.MemoryWrite{Name: "c", Description: "C", Content: "c"}); err != nil {
		t.Fatal(err)
	}
	fx.f.off = true
	if err := fx.sync.Sync(ctx, fx.scope); err != nil || !fx.row(t, "c.md").Pending {
		t.Fatalf("an unreachable folder: %v", err)
	}
	if err := fx.sync.Removing(ctx, fx.scope, "b"); !store.IsMemoryError(err) {
		t.Fatalf("removing a written-out file without the folder: %v", err)
	}
	if err := fx.sync.Removing(ctx, fx.scope, "c"); err != nil {
		t.Fatalf("removing a file never written out: %v", err)
	}
}

// TestSweepUserMemory: the sweep writes the user's own memory into shared/.memory/ and picks every
// ready scope on a full pass.
func TestSweepUserMemory(t *testing.T) {
	fx := newFixture(t)
	ctx, q := fx.ctx, fx.s.Pool
	us := store.UserMemory(fx.user)
	if err := store.AppendMemoryNote(ctx, q, us, "回复用中文", "coordinator"); err != nil {
		t.Fatal(err)
	}
	fx.sync.Sweep(ctx, false)
	b, err := os.ReadFile(filepath.Join(fx.f.root, fx.user, "shared", ".memory", "MEMORY.md"))
	if err != nil || string(b) != "# 用户记忆\n- 回复用中文\n" {
		t.Fatalf("user MEMORY.md %q %v", b, err)
	}
	scopes, err := store.MemoryScopesToSync(ctx, q, true)
	if err != nil || len(scopes) != 2 {
		t.Fatalf("full pass scopes %v %v", scopes, err)
	}
	if p, _ := store.MemoryPrompt(ctx, q, us); !strings.Contains(p, "回复用中文") {
		t.Fatalf("user memory prompt %q", p)
	}
}

// TestParseRender: frontmatter round trip, broken or missing frontmatter keeps the text.
func TestParseRender(t *testing.T) {
	f := store.MemoryFile{Name: "x.md", Description: "说明: 带冒号", Type: "feedback", Content: "a\n\nb"}
	p := memfiles.Parse("x.md", memfiles.Render(f))
	if p.Description != f.Description || p.Type != f.Type || p.Content != f.Content {
		t.Fatalf("round trip %+v", p)
	}
	if p := memfiles.Parse("y.md", []byte("---\nno end\nbody")); p.Content != "---\nno end\nbody" || p.Description != "" {
		t.Fatalf("broken frontmatter %+v", p)
	}
	if p := memfiles.Parse("MEMORY.md", []byte("---\n- a\n")); p.Content != "---\n- a" {
		t.Fatalf("MEMORY.md %+v", p)
	}
	if p := memfiles.Parse("z.md", []byte("\ufeff---\r\ndescription: d\r\n---\r\nbody\r\n")); p.Description != "d" || p.Content != "body" {
		t.Fatalf("CRLF %+v", p)
	}
}
