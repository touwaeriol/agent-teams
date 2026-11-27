package engine_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw/nodetest"
	"github.com/touwaeriol/agent-teams/server/internal/store"
	"github.com/touwaeriol/agent-teams/server/internal/userfs"
)

type fsFakes struct {
	formatted int
	destroyed atomic.Int32
	root      string
}

func (f *fsFakes) Ensure(context.Context, string, string) error { return nil }
func (f *fsFakes) Drop(context.Context, string) error           { return nil }
func (f *fsFakes) Exists(context.Context, string) (bool, error) { return true, nil }

type fsIAM struct{}

func (fsIAM) Ensure(context.Context, userfs.Names, string, []byte) error { return nil }
func (fsIAM) Remove(context.Context, userfs.Names) error                 { return nil }

func (f *fsFakes) Format(context.Context, userfs.FormatSpec) error { f.formatted++; return nil }
func (f *fsFakes) Destroy(context.Context, string, string) error   { f.destroyed.Add(1); return nil }
func (f *fsFakes) Files(_ context.Context, volume, _, _ string) (userfs.Files, error) {
	return userfs.DirFiles{Root: filepath.Join(f.root, volume)}, os.MkdirAll(filepath.Join(f.root, volume), 0o755)
}
func (f *fsFakes) Close(string) {}

// useFolder gives the world shared folders whose volumes are local directories.
func useFolder(t *testing.T, w *world) *fsFakes {
	t.Helper()
	fakes := &fsFakes{root: t.TempDir()}
	cfg := userfs.Config{AdminURL: "postgres://admin@db/at", MetaHost: "db.example:5432", MetaSSLMode: "verify-full", S3Endpoint: "https://s3.example",
		Bucket: "jfs", AdminAccessKey: "ak", AdminSecretKey: "sk", CapacityGB: 20, ClientVersion: userfs.DefaultClientVersion,
		ClientBase: userfs.DefaultClientBase, Assets: userfs.DefaultAssets}
	s := userfs.New(cfg, w.db.Pool, testVault(t), fakes, fsIAM{}, fakes, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.UnmountPoll = 20 * time.Millisecond
	w.e.UseSharedFolder(s)
	t.Cleanup(s.Wait)
	return fakes
}

// TestSharedFolderReachesNodesAndSessions: the folder is on by default: the sweep provisions it
// and sends the user's mounting node fs.mount on the live link only (never the outbox), the node's
// fs.status shows on the node page, sessions opened there carry the folders, and deleting the
// account sends fs.unmount and destroys the volume only once the node reports it unmounted.
func TestSharedFolderReachesNodesAndSessions(t *testing.T) {
	w := newWorldWith(t, nil, nil)
	fakes := useFolder(t, w)

	tm := &team{w: w, b: w.signUp("fs@example.com")}
	var node nodeCreated
	tm.b.must(201, "POST", "/api/nodes", map[string]string{"name": "fake"}, &node)
	var project struct{ ID string }
	tm.b.must(201, "POST", "/api/projects", map[string]any{"name": "p"}, &project)
	tm.n = w.connect(node.Token, nodetest.Options{Features: []string{"sessions", "git_worktree", "cred_helper", "file_download", "fs_mount", "memory_files"}})

	var view struct {
		Configured bool
		Volume     struct{ Volume, State string }
	}
	w.e.SweepSharedFolders(w.ctx)
	tm.b.must(200, "GET", "/api/storage/fs", nil, &view)
	if !view.Configured || view.Volume.State != userfs.StateReady || fakes.formatted != 1 {
		t.Fatalf("enable: %+v formatted %d", view, fakes.formatted)
	}
	ctx, cancel := w.waitCtx()
	defer cancel()
	f, err := tm.n.Wait(ctx, protocol.TypeFsMount, nil)
	if err != nil {
		t.Fatal(err)
	}
	var mount protocol.FsMount
	_ = json.Unmarshal(f.Body, &mount)
	if mount.Volume != view.Volume.Volume || mount.MetaPassword == "" || strings.Contains(mount.MetaURL, mount.MetaPassword) {
		t.Fatalf("fs.mount %+v", mount)
	}
	var queued int
	if err := store.QueryRow(w.ctx, w.db.Pool, `SELECT count(*) FROM at_node_commands WHERE type LIKE 'fs.%'`).Scan(&queued); err != nil || queued != 0 {
		t.Fatalf("fs commands in the outbox: %d %v", queued, err)
	}

	vol, gen := mount.Volume, mount.Generation
	if err := tm.n.SendUnreliable(ctx, protocol.TypeFsStatus, protocol.FsStatus{State: protocol.FsStatusStateMounted, Mode: protocol.FsStatusModeDaemon, Volume: &vol, Generation: &gen}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "node page shows the mount", func() bool {
		var nodes []struct {
			SharedFolder *struct{ Mode, State string } `json:"shared_folder"`
		}
		tm.b.must(200, "GET", "/api/nodes", nil, &nodes)
		return len(nodes) == 1 && nodes[0].SharedFolder != nil && nodes[0].SharedFolder.State == "mounted"
	})

	tm.b.must(201, "POST", "/api/projects/"+project.ID+"/threads", map[string]any{"title": "Files", "brief": "use the folder"}, nil)
	lead, open := expectOpen(t, w, tm.n, "lead-1")
	if open.Fs == nil || string(open.Fs.Project) != "projects/"+project.ID || string(open.Fs.Shared) != "shared" {
		t.Fatalf("session.open fs %+v", open.Fs)
	}
	if open.Claude == nil || open.Claude.MemoryFiles == nil || !*open.Claude.MemoryFiles || slices.Contains(open.Claude.DisallowedTools, "Edit") {
		t.Fatalf("the lead cannot edit the memory files: %+v", open.Claude)
	}
	sharedFolderMemory(t, w, lead, fakes.root+"/"+vol, project.ID)

	tm.projectID = project.ID
	if err := w.e.Hooks().UserDeleting(w.ctx, tm.ownerID(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := tm.n.Wait(ctx, protocol.TypeFsUnmount, nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if fakes.destroyed.Load() != 0 {
		t.Fatal("volume destroyed while the node still mounts it")
	}
	if err := tm.n.SendUnreliable(ctx, protocol.TypeFsStatus, protocol.FsStatus{State: protocol.FsStatusStateUnmounted, Mode: protocol.FsStatusModeDaemon}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "volume destroyed after the unmount", func() bool { return fakes.destroyed.Load() == 1 })
}

// TestSharedFolderStatusMustMatch: a node's fs_status counts only for the user's current volume
// and generation, and starts over when the node reconnects.
func TestSharedFolderStatusMustMatch(t *testing.T) {
	w := newWorldWith(t, nil, nil)
	useFolder(t, w)
	tm := &team{w: w, b: w.signUp("fs2@example.com")}
	var node nodeCreated
	tm.b.must(201, "POST", "/api/nodes", map[string]string{"name": "fake"}, &node)
	features := nodetest.Options{Features: []string{"sessions", "git_worktree", "cred_helper", "file_download", "fs_mount"}}
	tm.n = w.connect(node.Token, features)
	var view struct{ Volume struct{ Volume string } }
	tm.b.must(200, "POST", "/api/storage/fs", nil, &view)
	var userID string
	if err := store.QueryRow(w.ctx, w.db.Pool, `SELECT user_id FROM at_nodes WHERE id = $1`, node.Node.ID).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	vol, err := w.e.FS.Get(w.ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	mounted := func(volume string, gen int) {
		st := protocol.FsStatus{State: protocol.FsStatusStateMounted, Mode: protocol.FsStatusModeDaemon, Volume: &volume, Generation: &gen}
		ctx, cancel := w.waitCtx()
		defer cancel()
		if err := tm.n.SendUnreliable(ctx, protocol.TypeFsStatus, st); err != nil {
			t.Fatal(err)
		}
	}
	open := func() bool {
		fs, err := w.e.SharedFolderFor(w.ctx, vol.UserID, node.Node.ID, "p1")
		if err != nil {
			t.Fatal(err)
		}
		return fs != nil
	}
	mounted(vol.Volume, vol.Generation+1)
	eventually(t, "other generation stored", func() bool { return fsStatusOf(t, w, node.Node.ID) != "{}" })
	if open() {
		t.Fatal("a status of another generation counts")
	}
	mounted(vol.Volume, vol.Generation)
	eventually(t, "current generation counts", open)
	tm.n.Close()
	eventually(t, "status reset on disconnect", func() bool { return fsStatusOf(t, w, node.Node.ID) == "{}" })
	if open() {
		t.Fatal("status survives the node's disconnect")
	}
}

func fsStatusOf(t *testing.T, w *world, nodeID string) string {
	var raw string
	if err := store.QueryRow(w.ctx, w.db.Pool, `SELECT fs_status::text FROM at_nodes WHERE id = $1`, nodeID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

// sharedFolderMemory: the lead edits a memory file on the node (its tool call names it): the sweep
// reads it back credited to the lead, and the next session's prompt carries it.
func sharedFolderMemory(t *testing.T, w *world, lead *fakeAgent, volRoot, projectID string) {
	t.Helper()
	lead.event("tool_started", func(e *protocol.SessionEvent) {
		tool, cat, sum, use := "Write", "edit", "/work/lead/.project-files/.memory/testing.md", "toolu_m1"
		e.Tool, e.Category, e.Summary, e.ToolUseID = &tool, &cat, &sum, &use
	})
	scope := store.ProjectMemory(projectID)
	eventually(t, "the touch is recorded", func() bool {
		touches, err := store.MemoryTouches(w.ctx, w.db.Pool, scope)
		return err == nil && touches["testing.md"] == "lead-1"
	})
	dir := filepath.Join(volRoot, "projects", projectID, ".memory")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "testing.md"), []byte("---\nname: testing\ndescription: 怎么跑测试\n---\n\nmake test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	w.e.SweepSharedFolders(w.ctx)
	f, err := store.GetMemoryFile(w.ctx, w.db.Pool, scope, "testing.md")
	if err != nil || f.Content != "make test" || f.UpdatedBy != "lead-1" {
		t.Fatalf("memory file not read back: %+v %v", f, err)
	}
	idx, err := os.ReadFile(filepath.Join(dir, "MEMORY.md"))
	if err != nil || !strings.Contains(string(idx), "- [testing.md](testing.md) — 怎么跑测试") {
		t.Fatalf("MEMORY.md in the folder %q %v", idx, err)
	}
}
