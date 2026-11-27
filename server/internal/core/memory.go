package core

import (
	"context"

	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// MemoryFiles keeps auto memory in the owners' shared folders (memfiles.Syncer); NoMemoryFiles
// when the console has none (the rows are then all there is).
type MemoryFiles interface {
	// Delete removes a topic file: from the folder first, then its row and index line (committed
	// on its own, not in the caller's transaction: the file cannot be put back on a rollback). A
	// MemoryError when the folder is out of reach and the file is there.
	Delete(ctx context.Context, s store.MemoryScope, name, by string) error
	// Changed has rows just written stored in the folder soon (call after the commit).
	Changed(s store.MemoryScope)
	// Sync reads the folder into the rows and writes pending rows out, now.
	Sync(ctx context.Context, s store.MemoryScope) error
}

// NoMemoryFiles is MemoryFiles without shared folders: the rows in DB are all there is.
type NoMemoryFiles struct{ DB store.DB }

// Delete implements MemoryFiles.
func (n NoMemoryFiles) Delete(ctx context.Context, s store.MemoryScope, name, by string) error {
	return store.DeleteMemoryFile(ctx, n.DB, s, name, by)
}

// Changed implements MemoryFiles.
func (NoMemoryFiles) Changed(store.MemoryScope) {}

// Sync implements MemoryFiles.
func (NoMemoryFiles) Sync(context.Context, store.MemoryScope) error { return nil }

// EmitMemory announces a memory file written or deleted (op): on the project's stream, or the
// user's for their own memory.
func (tx *Tx) EmitMemory(ctx context.Context, s store.MemoryScope, actor, op, name string) error {
	payload := map[string]any{"op": op, "name": name}
	if s.Kind == store.MemoryUser {
		_, err := tx.EmitUser(ctx, s.ID, EvMemory, payload)
		return err
	}
	_, err := tx.Emit(ctx, s.ID, nil, EvMemory, actor, payload)
	return err
}
