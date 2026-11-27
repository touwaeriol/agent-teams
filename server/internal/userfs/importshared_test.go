package userfs

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/touwaeriol/agent-teams/server/internal/blobstore"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

type importBlobs struct {
	blobstore.Store
	objects map[string]string
}

func (b importBlobs) Open(_ context.Context, key string) (io.ReadCloser, error) {
	v, ok := b.objects[key]
	if !ok {
		return nil, blobstore.ErrNotFound
	}
	return io.NopCloser(strings.NewReader(v)), nil
}

// TestImportSharedFiles: the old shared files land in the owner's volume (switched on for it),
// from the object store or the data column; a second run skips them.
func TestImportSharedFiles(t *testing.T) {
	s, fs, st := newTest(t)
	ctx := context.Background()
	if _, err := store.Exec(ctx, st.Pool, `INSERT INTO at_projects (id, user_id, name) VALUES ('p1', 'u1', 'A');
INSERT INTO at_shared_files (project_id, path, content_type, size, sha256, data, uploaded_by, storage_key, object_id, state)
VALUES ('p1', 'data/a.csv', 'text/csv', 3, '', NULL, 'user', 'projects/p1/shared/o1', 'o1', 'ready'),
       ('p1', 'notes.md', 'text/markdown', 5, '', 'hello', 'dev-1', NULL, NULL, 'ready')`); err != nil {
		t.Fatal(err)
	}
	blobs := importBlobs{objects: map[string]string{"projects/p1/shared/o1": "1,2"}}
	if rep, err := s.ImportSharedFiles(ctx, blobs, true, nil); err != nil || rep.Copied != 2 || rep.Users != 1 {
		t.Fatalf("dry run: %+v %v", rep, err)
	}
	if _, err := Get(ctx, st.Pool, "u1"); !IsNotFound(err) {
		t.Fatal("dry run switched the folder on")
	}
	rep, err := s.ImportSharedFiles(ctx, blobs, false, nil)
	if err != nil || rep.Copied != 2 || rep.Failed != 0 {
		t.Fatalf("import: %+v %v", rep, err)
	}
	f := DirFiles{Root: fs.userPath("u1")}
	r, _, err := f.Open(ctx, "projects/p1/data/a.csv")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(r)
	r.Close()
	if string(b) != "1,2" {
		t.Fatalf("content %q", b)
	}
	if rep, _ := s.ImportSharedFiles(ctx, blobs, false, nil); rep.Skipped != 2 || rep.Copied != 0 {
		t.Fatalf("second run: %+v", rep)
	}
}

// TestImportSharedFilesVolumeFails: a user whose volume cannot be switched on is one failure, not
// one per file.
func TestImportSharedFilesVolumeFails(t *testing.T) {
	s, fs, st := newTest(t)
	ctx := context.Background()
	if _, err := store.Exec(ctx, st.Pool, `INSERT INTO at_projects (id, user_id, name) VALUES ('p1', 'u1', 'A');
INSERT INTO at_shared_files (project_id, path, content_type, size, sha256, data, uploaded_by, state)
VALUES ('p1', 'a.md', 'text/markdown', 1, '', 'a', 'user', 'ready'), ('p1', 'b.md', 'text/markdown', 1, '', 'b', 'user', 'ready')`); err != nil {
		t.Fatal(err)
	}
	fs.fail = errors.New("connection refused")
	rep, err := s.ImportSharedFiles(ctx, importBlobs{}, false, nil)
	if err != nil || rep.Users != 1 || rep.Failed != 1 || rep.Copied != 0 || len(rep.Errors) != 1 {
		t.Fatalf("%+v %v", rep, err)
	}
}
