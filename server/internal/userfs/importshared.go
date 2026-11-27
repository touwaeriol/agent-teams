package userfs

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/touwaeriol/agent-teams/server/internal/blobstore"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// ImportReport is what ImportSharedFiles did.
type ImportReport struct {
	Users, Copied, Skipped, Failed int
	Errors                         []string
}

func (r ImportReport) String() string {
	return fmt.Sprintf("users %d, copied %d, skipped %d (already there), failed %d", r.Users, r.Copied, r.Skipped, r.Failed)
}

// legacyFile is one old shared file without its bytes (body loads them one file at a time).
type legacyFile struct {
	ProjectID, UserID, Path string
	StorageKey              *string
	Size                    int64
}

// ImportSharedFiles copies the project shared files of the old store (at_shared_files: bytes in
// the object store, or the data column for files from before #58) into projects/<pid>/<path> of
// each owner's volume, switching the volume on when needed. A file already there with the same
// size is skipped, so a run can be repeated; nothing is deleted. dryRun only counts.
func (s *Service) ImportSharedFiles(ctx context.Context, blobs blobstore.Store, dryRun bool, log *slog.Logger) (ImportReport, error) {
	var rep ImportReport
	var files []legacyFile
	if err := store.Raw(ctx, s.db, `SELECT f.project_id, p.user_id, f.path, f.storage_key, f.size
		FROM at_shared_files f JOIN at_projects p ON p.id = f.project_id
		WHERE f.state = 'ready' ORDER BY p.user_id, f.project_id, f.path`).Scan(&files).Error; err != nil {
		return rep, err
	}
	var vol Files
	user := ""
	for _, f := range files {
		if f.UserID != user {
			user = f.UserID
			vol = s.openUser(ctx, user, dryRun, &rep)
		}
		switch {
		case dryRun:
			rep.Copied++
		case vol != nil:
			s.importOne(ctx, blobs, vol, f, &rep, log)
		}
	}
	return rep, nil
}

// openUser counts the next user and opens their volume (nil on a dry run or a failure, which is
// counted once for the user, not again for each of their files).
func (s *Service) openUser(ctx context.Context, userID string, dryRun bool, rep *ImportReport) Files {
	rep.Users++
	if dryRun {
		return nil
	}
	vol, err := s.openForImport(ctx, userID)
	if err != nil {
		rep.fail("user %s: %v", userID, err)
		return nil
	}
	return vol
}

func (r *ImportReport) fail(format string, a ...any) {
	r.Failed++
	r.Errors = append(r.Errors, fmt.Sprintf(format, a...))
}

// openForImport switches the user's volume on when it is not ready yet, then opens it.
func (s *Service) openForImport(ctx context.Context, userID string) (Files, error) {
	vol, err := Get(ctx, s.db, userID)
	if IsNotFound(err) || (err == nil && vol.State == StateError) {
		vol, err = s.Enable(ctx, userID)
	}
	if err != nil {
		return nil, err
	}
	if vol.State != StateReady {
		return nil, fmt.Errorf("bucket %s is %s: %s", vol.Bucket, vol.State, vol.Error)
	}
	return s.Files(ctx, userID)
}

func (s *Service) importOne(ctx context.Context, blobs blobstore.Store, vol Files, f legacyFile, rep *ImportReport, log *slog.Logger) {
	target, err := Join(ProjectDir(f.ProjectID), f.Path)
	if err != nil {
		rep.fail("%s:%s: %v", f.ProjectID, f.Path, err)
		return
	}
	if r, e, err := vol.Open(ctx, target); err == nil {
		r.Close()
		if e.Size == f.Size {
			rep.Skipped++
			return
		}
	}
	body, err := s.legacyBody(ctx, blobs, f)
	if err != nil {
		rep.fail("%s:%s: open: %v", f.ProjectID, f.Path, err)
		return
	}
	defer body.Close()
	if err := vol.Put(ctx, target, body, f.Size); err != nil {
		rep.fail("%s:%s: write: %v", f.ProjectID, f.Path, err)
		return
	}
	rep.Copied++
	if log != nil {
		log.Info("imported shared file", "project", f.ProjectID, "path", f.Path, "size", f.Size)
	}
}

// legacyBody opens one old file: from the object store, or its data column (files from before #58).
func (s *Service) legacyBody(ctx context.Context, blobs blobstore.Store, f legacyFile) (io.ReadCloser, error) {
	if f.StorageKey != nil {
		return blobs.Open(ctx, *f.StorageKey)
	}
	var data []byte
	if err := store.QueryRow(ctx, s.db, `SELECT data FROM at_shared_files WHERE project_id = $1 AND path = $2`, f.ProjectID, f.Path).Scan(&data); err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}
