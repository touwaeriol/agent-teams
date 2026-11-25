package sharedfiles

import (
	"context"
	"errors"
	"fmt"

	"github.com/touwaeriol/agent-teams/server/internal/blobgc"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// ErrExists is a path that already has a file when the caller did not ask to overwrite it.
var ErrExists = errors.New("shared file exists")

// Object is an object already written to the store that becomes a shared file (Adopt): its key
// and object id (SharedKey(projectID, ObjectID)), size and hex sha256 ("" = the blob_hash job
// computes it; readers check the size only until then).
type Object struct {
	Key      string
	ObjectID string
	Size     int64
	SHA256   string
}

// CheckSpace tells whether size bytes at path would fit the project's limits now, and whether the
// path has a file (ErrExists unless overwrite). It is the early check before an object is copied;
// Adopt checks again under the project lock.
func CheckSpace(ctx context.Context, q store.DB, projectID, p string, size int64, overwrite bool) error {
	u, err := ProjectUsage(ctx, q, projectID)
	if err != nil {
		return err
	}
	old := int64(-1)
	if prev, err := Stat(ctx, q, projectID, p); err == nil {
		if !overwrite {
			return fmt.Errorf("%w：%s", ErrExists, p)
		}
		old = prev.Size
	} else if !errors.Is(err, ErrNotFound) {
		return err
	}
	return Check(u, size, old)
}

// Adopt records an object as the shared file at path (inserted, or with overwrite replacing the
// file there; ErrExists otherwise) after checking the path and the limits under the project row
// lock. The replaced file's object is queued for deletion. The caller deletes obj when Adopt
// fails. It returns the stored file and whether it replaced one.
func Adopt(ctx context.Context, q store.DB, projectID, rawPath string, obj Object, by Uploader, overwrite bool) (SharedFile, bool, error) {
	p, err := CleanPath(rawPath)
	if err != nil {
		return SharedFile{}, false, err
	}
	if err := lockProject(ctx, q, projectID); err != nil {
		return SharedFile{}, false, err
	}
	old, oldKey := int64(-1), ""
	if prev, err := Stat(ctx, q, projectID, p); err == nil {
		if !overwrite {
			return SharedFile{}, false, fmt.Errorf("%w：%s", ErrExists, p)
		}
		old = prev.Size
		if prev.StorageKey != nil {
			oldKey = *prev.StorageKey
		}
	} else if !errors.Is(err, ErrNotFound) {
		return SharedFile{}, false, err
	}
	u, err := ProjectUsage(ctx, q, projectID)
	if err != nil {
		return SharedFile{}, false, err
	}
	if err := Check(u, obj.Size, old); err != nil {
		return SharedFile{}, false, err
	}
	var empID *string
	if by.EmployeeID != "" {
		empID = &by.EmployeeID
	}
	var sum *string
	if obj.SHA256 != "" {
		sum = &obj.SHA256
	}
	err = q.Gorm(ctx).Exec(`INSERT INTO at_shared_files (project_id, path, content_type, size, sha256, data, uploaded_by,
		uploader_id, uploader_thread_id, storage_key, object_id, declared_sha256, state) VALUES (?, ?, ?, ?, ?, NULL, ?, ?, ?, ?, ?, ?, 'ready')
		ON CONFLICT (project_id, path) DO UPDATE SET content_type = EXCLUDED.content_type, size = EXCLUDED.size,
		sha256 = EXCLUDED.sha256, data = NULL, uploaded_by = EXCLUDED.uploaded_by, uploader_id = EXCLUDED.uploader_id,
		uploader_thread_id = EXCLUDED.uploader_thread_id, storage_key = EXCLUDED.storage_key, object_id = EXCLUDED.object_id,
		declared_sha256 = EXCLUDED.declared_sha256, state = 'ready', updated_at = now()`,
		projectID, p, ContentType(p), obj.Size, sum, by.Name, empID, by.ThreadID, obj.Key, obj.ObjectID, sum).Error
	if err != nil {
		return SharedFile{}, false, err
	}
	if oldKey != "" && oldKey != obj.Key {
		if err := blobgc.Enqueue(ctx, q, oldKey); err != nil {
			return SharedFile{}, false, err
		}
	}
	f, err := Stat(ctx, q, projectID, p)
	return f, old >= 0, err
}
