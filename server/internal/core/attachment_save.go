package core

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"github.com/touwaeriol/agent-teams/server/internal/blobgc"
	"github.com/touwaeriol/agent-teams/server/internal/blobstore"
	"github.com/touwaeriol/agent-teams/server/internal/sharedfiles"
)

// SaveAttachmentShared keeps a project attachment as the shared file at path (save_attachment and
// POST /api/attachments/{id}/save-shared, #58). The object is copied inside the store first, with
// no transaction open (no network work while the project row is locked); then sharedfiles.Adopt
// records it under the project lock, checking the path, an existing file (sharedfiles.ErrExists
// unless overwrite) and the quota again. On any failure the copy is deleted. Rows from before
// #58 are written from their data column. owner is the project owner (reads of the data column).
func (s *Store) SaveAttachmentShared(ctx context.Context, owner string, a Attachment, path string, by sharedfiles.Uploader,
	overwrite bool) (sharedfiles.SharedFile, bool, error) {
	bs := blobstore.Or(s.Blobs)
	if !bs.Configured() {
		return sharedfiles.SharedFile{}, false, blobstore.ErrNotConfigured
	}
	pid := a.ProjectID
	obj := sharedfiles.Object{ObjectID: sharedfiles.NewObjectID(), Size: int64(a.Size), SHA256: a.SHA256}
	obj.Key = blobstore.SharedKey(pid, obj.ObjectID)
	// If the process dies between the copy and the commit, blobgc removes the copy.
	if err := blobgc.Reserve(ctx, s.Pool, obj.Key); err != nil {
		return sharedfiles.SharedFile{}, false, err
	}
	if err := s.copyAttachment(ctx, bs, owner, a, obj.Key); err != nil {
		return sharedfiles.SharedFile{}, false, err
	}
	var f sharedfiles.SharedFile
	var overwritten bool
	err := s.InTx(ctx, func(tx *Tx) error {
		var err error
		if f, overwritten, err = sharedfiles.Adopt(ctx, tx, pid, path, obj, by, overwrite); err != nil {
			return err
		}
		return tx.EmitSharedFile(ctx, pid, f.Label(), "write", f.Path)
	})
	if err != nil {
		_ = bs.Delete(context.WithoutCancel(ctx), obj.Key)
		return f, false, err
	}
	return f, overwritten, nil
}

// copyAttachment puts the attachment's bytes at key: a copy inside the store, or (rows from
// before #58, at most 5 MB) a write from the data column.
func (s *Store) copyAttachment(ctx context.Context, bs blobstore.Store, owner string, a Attachment, key string) error {
	if a.StorageKey != nil {
		return bs.Copy(ctx, *a.StorageKey, key)
	}
	a2, rc, err := OpenAttachment(ctx, s.Pool, bs, owner, a.ID)
	if err != nil {
		return err
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, int64(a2.Size)+1))
	if err != nil {
		return err
	}
	if len(data) != a2.Size {
		return fmt.Errorf("attachment %s has %d bytes, want %d", a.ID, len(data), a2.Size)
	}
	return bs.Put(ctx, key, bytes.NewReader(data), int64(len(data)), a.ContentType)
}
