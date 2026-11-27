package engine

import (
	"context"
	"regexp"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/memfiles"
	"github.com/touwaeriol/agent-teams/server/internal/store"
	"github.com/touwaeriol/agent-teams/server/internal/userfs"
)

// Auto memory in the owners' shared folders (memfiles): the folder sweep reads the files back and
// writes pending rows out; a tool call of an employee that names a memory file records who is
// editing it, so the change is credited to them and read back within seconds.

// memoryFullEvery is how often the sweep reads every ready folder's memory, not only the scopes
// with pending rows or fresh touches (catches edits no tool call announced).
const memoryFullEvery = time.Hour

// touchKick is how long after an employee's tool call names a memory file its folder is read
// (the call reports when it starts, before the file is written).
const touchKick = 5 * time.Second

// useMemoryFiles wires memory to the shared folders (s enabled) or to the rows alone.
func (e *Engine) useMemoryFiles(s *userfs.Service) {
	if s == nil || !s.Enabled() {
		e.memory, e.Core.Memory = nil, store.NoMemoryFiles{}
		return
	}
	e.memory = &memfiles.Syncer{DB: e.Core.Pool, Folders: s, Log: e.log, OnChange: e.memoryChanged}
	e.Core.Memory = e.memory
}

// sweepMemory syncs the scopes with something to do, and every ready folder once an hour.
func (e *Engine) sweepMemory(ctx context.Context) {
	if e.memory == nil {
		return
	}
	full := time.Since(e.memoryFullAt) >= memoryFullEvery
	if full {
		e.memoryFullAt = time.Now()
	}
	e.memory.Sweep(ctx, full)
}

// memoryChanged refreshes the pages showing a memory file changed in the folder.
func (e *Engine) memoryChanged(ctx context.Context, s store.MemoryScope, name, op, by string) {
	payload := map[string]any{"op": op, "name": name}
	err := e.Core.InTx(ctx, func(tx *core.Tx) error {
		if s.Kind == store.MemoryUser {
			_, err := tx.EmitUser(ctx, s.ID, core.EvMemory, payload)
			return err
		}
		_, err := tx.Emit(ctx, s.ID, nil, core.EvMemory, by, payload)
		return err
	})
	if err != nil {
		e.log.Warn("memory change not announced", "scope", s.Kind, "id", s.ID, "err", err)
	}
}

// memoryPath finds memory files in a tool call's summary: the links in the working directory
// (.project-files / .user-files) or the volume's own folders (projects/<pid>, shared).
var memoryPath = regexp.MustCompile(`(\.project-files|\.user-files|projects/[A-Za-z0-9_-]+|shared)/\.memory/([^/\s,'"` + "`" + `]+\.md)`)

// touchMemory records who is about to change memory files named in a tool call and has their
// folder read soon after.
func (e *Engine) touchMemory(ctx context.Context, tx *core.Tx, emp core.Employee, summary string) error {
	if e.memory == nil {
		return nil
	}
	for _, m := range memoryPath.FindAllStringSubmatch(summary, -1) {
		s, err := e.memoryScopeOf(ctx, tx, emp, m[1])
		if err != nil || s.ID == "" {
			return err
		}
		name, err := store.MemoryName(m[2])
		if err != nil {
			continue
		}
		if err := store.TouchMemory(ctx, tx, s, name, emp.Name); err != nil {
			return err
		}
		tx.AfterCommit(func(context.Context) { e.memory.Kick(s, touchKick) })
	}
	return nil
}

// memoryScopeOf is the scope a path's folder names: the employee's project, the user's own
// memory, or none (another project's folder).
func (e *Engine) memoryScopeOf(ctx context.Context, tx *core.Tx, emp core.Employee, dir string) (store.MemoryScope, error) {
	switch dir {
	case ".project-files", userfs.ProjectDir(emp.ProjectID):
		return store.ProjectMemory(emp.ProjectID), nil
	case ".user-files", userfs.SharedDir:
		userID, err := store.MemoryOwner(ctx, tx, store.ProjectMemory(emp.ProjectID))
		return store.UserMemory(userID), err
	}
	return store.MemoryScope{}, nil
}
