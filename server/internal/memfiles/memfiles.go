// Package memfiles keeps auto memory in the owners' shared folders: a project's files in
// projects/<pid>/.memory/, the user's own in shared/.memory/ (juicefs-shared-storage design). The
// files are the source, so employees read and edit them with their own file tools; the memory rows
// are the cache that sessions, the web pages and a folder out of reach read. A write through the
// platform (memory tools, thread summaries, the web page) lands in the rows as pending and is
// written out next; a change made on a node is read back by the sweep and credited to the
// employee whose tool call touched the file.
package memfiles

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/store"
	"github.com/touwaeriol/agent-teams/server/internal/userfs"
)

// DirName is the memory folder inside a project's folder and inside the user's shared folder.
const DirName = ".memory"

// ByNode credits a change seen in the folder that no tool call touched (edited by hand on a node).
const ByNode = "节点上直接修改"

// maxFile is the most read of one file (the rows keep store.MemoryContentMax characters).
const maxFile = 1 << 20

// Dir is a scope's memory folder in the owner's volume.
func Dir(s store.MemoryScope) string {
	if s.Kind == store.MemoryUser {
		return userfs.SharedDir + "/" + DirName
	}
	return userfs.ProjectDir(s.ID) + "/" + DirName
}

// Folders opens a user's ready volume (userfs.Service).
type Folders interface {
	Files(ctx context.Context, userID string) (userfs.Files, error)
}

// Syncer keeps the memory rows and the folders in step (store.MemoryFiles).
type Syncer struct {
	DB      store.DB
	Folders Folders
	Log     *slog.Logger
	// OnChange reports a file changed or deleted in the folder (op write | delete).
	OnChange func(ctx context.Context, s store.MemoryScope, name, op, by string)
	mu       sync.Mutex
	base     context.Context
	locks    map[store.MemoryScope]*sync.Mutex
	kicked   map[store.MemoryScope]bool
}

var _ store.MemoryFiles = (*Syncer)(nil)

func (m *Syncer) lock(s store.MemoryScope) func() {
	m.mu.Lock()
	if m.locks == nil {
		m.locks = map[store.MemoryScope]*sync.Mutex{}
	}
	l := m.locks[s]
	if l == nil {
		l = &sync.Mutex{}
		m.locks[s] = l
	}
	m.mu.Unlock()
	l.Lock()
	return l.Unlock
}

// open is the owner's volume; nil when the owner has no ready shared folder.
func (m *Syncer) open(ctx context.Context, s store.MemoryScope) (userfs.Files, error) {
	owner, err := store.MemoryOwner(ctx, m.DB, s)
	if err != nil {
		return nil, err
	}
	files, err := m.Folders.Files(ctx, owner)
	if userfs.Unavailable(err) {
		return nil, nil
	}
	return files, err
}

// Start sets the context of the syncs Kick starts (the engine's lifetime).
func (m *Syncer) Start(ctx context.Context) {
	m.mu.Lock()
	m.base = ctx
	m.mu.Unlock()
}

// Kick syncs a scope after a while (a write was just committed, an employee is editing a file);
// one pending kick per scope.
func (m *Syncer) Kick(s store.MemoryScope, after time.Duration) {
	m.mu.Lock()
	if m.kicked == nil {
		m.kicked = map[store.MemoryScope]bool{}
	}
	if m.kicked[s] {
		m.mu.Unlock()
		return
	}
	m.kicked[s] = true
	m.mu.Unlock()
	time.AfterFunc(after, func() {
		m.mu.Lock()
		delete(m.kicked, s)
		ctx := m.base
		m.mu.Unlock()
		if ctx == nil {
			ctx = context.Background()
		}
		if ctx.Err() != nil {
			return
		}
		if err := m.Sync(ctx, s); err != nil {
			m.Log.Warn("memory folder sync failed", "scope", s.Kind, "id", s.ID, "err", err)
		}
	})
}

// Sweep syncs the scopes with something to do (all: every scope of a ready folder).
func (m *Syncer) Sweep(ctx context.Context, all bool) {
	if err := store.PruneMemoryTouches(ctx, m.DB); err != nil {
		m.Log.Warn("memory touches not pruned", "err", err)
	}
	scopes, err := store.MemoryScopesToSync(ctx, m.DB, all)
	if err != nil {
		m.Log.Warn("memory scopes not listed", "err", err)
		return
	}
	for _, s := range scopes {
		if ctx.Err() != nil {
			return
		}
		if err := m.Sync(ctx, s); err != nil {
			m.Log.Warn("memory folder sync failed", "scope", s.Kind, "id", s.ID, "err", err)
		}
	}
}

// Removing deletes a file from the folder before its row goes. With the folder out of reach a
// file already written there is refused (it would come back); one never written out is fine.
func (m *Syncer) Removing(ctx context.Context, s store.MemoryScope, name string) error {
	name, err := store.MemoryName(name)
	if err != nil {
		return err
	}
	files, err := m.open(ctx, s)
	if err != nil {
		return err
	}
	if files == nil {
		rows, err := store.MemoryStates(ctx, m.DB, s)
		if err != nil {
			return err
		}
		if rows[name].FileMtime != nil {
			return store.NewMemoryError("共享文件夹暂时不可用，稍后再删这个记忆文件")
		}
		return nil
	}
	if err := files.Remove(ctx, Dir(s)+"/"+name); err != nil && !errors.Is(err, userfs.ErrNoFile) {
		return err
	}
	return nil
}

// Sync reads the scope's folder into the rows (changed and deleted files), then writes the
// pending rows out. Nothing when the owner has no ready shared folder.
func (m *Syncer) Sync(ctx context.Context, s store.MemoryScope) error {
	defer m.lock(s)()
	files, err := m.open(ctx, s)
	if err != nil || files == nil {
		return err
	}
	disk, err := list(ctx, files, Dir(s))
	if err != nil {
		return err
	}
	rows, err := store.MemoryStates(ctx, m.DB, s)
	if err != nil {
		return err
	}
	touches, err := store.MemoryTouches(ctx, m.DB, s)
	if err != nil {
		return err
	}
	for name, e := range disk {
		if err := m.pull(ctx, s, files, name, e, rows, touches); err != nil {
			return err
		}
	}
	for name, r := range rows {
		if _, on := disk[name]; !on {
			if err := m.gone(ctx, s, name, r, touches); err != nil {
				return err
			}
		}
	}
	return m.push(ctx, s, files)
}

// list is the memory files of a folder by name (none when it does not exist yet).
func list(ctx context.Context, files userfs.Files, dir string) (map[string]userfs.Entry, error) {
	entries, err := files.List(ctx, dir)
	if errors.Is(err, userfs.ErrNoFile) {
		return map[string]userfs.Entry{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := make(map[string]userfs.Entry, len(entries))
	for _, e := range entries {
		if n, err := store.MemoryName(e.Name); err == nil && n == e.Name && !e.Dir {
			out[e.Name] = e
		}
	}
	return out, nil
}

// changed: the file is not what the row last saw (mtime to the second, as object listings give it).
func changed(e userfs.Entry, r store.MemoryState) bool {
	return r.FileMtime == nil || r.FileSize != e.Size || !r.FileMtime.Truncate(time.Second).Equal(e.ModTime.Truncate(time.Second))
}

func credit(touches map[string]string, name string) string {
	if by := touches[name]; by != "" {
		return by
	}
	return ByNode
}

// pull reads a file changed in the folder into its row. A row written here and not yet out
// (pending) wins: it is written over the file next.
func (m *Syncer) pull(ctx context.Context, s store.MemoryScope, files userfs.Files, name string, e userfs.Entry,
	rows map[string]store.MemoryState, touches map[string]string) error {
	r, known := rows[name]
	if known && (r.Pending || !changed(e, r)) {
		return nil
	}
	data, err := read(ctx, files, Dir(s)+"/"+name)
	if errors.Is(err, userfs.ErrNoFile) {
		return nil
	}
	if err != nil {
		return err
	}
	p := Parse(name, data)
	by := credit(touches, name)
	ok, err := store.ImportMemoryFile(ctx, m.DB, s, store.MemoryImport{Name: name, Description: p.Description, Type: p.Type,
		Content: p.Content, By: by, ModTime: e.ModTime, Size: e.Size})
	if err != nil {
		return err
	}
	if !ok && !known {
		m.Log.Warn("memory file in the folder not taken (too many files)", "scope", s.Kind, "id", s.ID, "name", name)
	}
	if ok {
		m.changed(ctx, s, name, "write", by)
	}
	return nil
}

func read(ctx context.Context, files userfs.Files, name string) ([]byte, error) {
	rc, _, err := files.Open(ctx, name)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(io.LimitReader(rc, maxFile))
}

// gone handles a row whose file is not in the folder: never written out or pending → written
// next; MEMORY.md → written again; a topic file deleted on a node → forgotten.
func (m *Syncer) gone(ctx context.Context, s store.MemoryScope, name string, r store.MemoryState, touches map[string]string) error {
	if r.Pending || r.FileMtime == nil {
		return nil
	}
	if name == store.MemoryIndex {
		return store.MarkMemoryPending(ctx, m.DB, s, name)
	}
	dropped, err := store.DropMemoryFile(ctx, m.DB, s, name)
	if dropped {
		m.changed(ctx, s, name, "delete", credit(touches, name))
	}
	return err
}

// push writes the pending rows out and records the files as the folder reports them.
func (m *Syncer) push(ctx context.Context, s store.MemoryScope, files userfs.Files) error {
	pending, err := store.PendingMemory(ctx, m.DB, s)
	if err != nil || len(pending) == 0 {
		return err
	}
	dir := Dir(s)
	for _, f := range pending {
		data := Render(f)
		if err := files.Put(ctx, dir+"/"+f.Name, bytesReader(data), int64(len(data))); err != nil {
			return err
		}
	}
	disk, err := list(ctx, files, dir)
	if err != nil {
		return err
	}
	for _, f := range pending {
		e, ok := disk[f.Name]
		if !ok {
			continue
		}
		if err := store.MarkMemoryFlushed(ctx, m.DB, s, f.Name, f.UpdatedAt, e.ModTime, e.Size); err != nil {
			return err
		}
	}
	return nil
}

func (m *Syncer) changed(ctx context.Context, s store.MemoryScope, name, op, by string) {
	if m.OnChange != nil {
		m.OnChange(ctx, s, name, op, by)
	}
}
