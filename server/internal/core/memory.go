package core

import (
	"context"

	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// MemoryFiles keeps auto memory in the owners' shared folders (memfiles.Syncer); NoMemoryFiles
// when the console has none (the rows are then all there is).
type MemoryFiles interface {
	// Delete removes a topic file: from the folder first, then its row and index line through q
	// (the caller's transaction). A MemoryError when the folder is out of reach and the file is there.
	Delete(ctx context.Context, q store.DB, s store.MemoryScope, name, by string) error
	// Changed has rows just written stored in the folder soon (call after the commit).
	Changed(s store.MemoryScope)
	// Sync reads the folder into the rows and writes pending rows out, now.
	Sync(ctx context.Context, s store.MemoryScope) error
}

// NoMemoryFiles is MemoryFiles without shared folders.
type NoMemoryFiles struct{}

// Delete implements MemoryFiles.
func (NoMemoryFiles) Delete(ctx context.Context, q store.DB, s store.MemoryScope, name, by string) error {
	return store.DeleteMemoryFile(ctx, q, s, name, by)
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
