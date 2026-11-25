package core

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/blobstore"
	"github.com/touwaeriol/agent-teams/server/internal/blobstore/blobtest"
	"github.com/touwaeriol/agent-teams/server/internal/store"
	"github.com/touwaeriol/agent-teams/server/internal/testdb"
)

// stallingStore answers ReadRange for "fast" keys and blocks every other one until ctx ends.
type stallingStore struct{ blobstore.None }

func (stallingStore) Configured() bool { return true }
func (stallingStore) ReadRange(ctx context.Context, key string, _, _ int64) ([]byte, error) {
	if strings.HasSuffix(key, "fast") {
		return []byte("fast"), nil
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

// TestReadTextsBounded: the object reads run concurrently and give up together after
// textReadTimeout; a stalled or failing read is left out, the others are kept (#62 M3).
func TestReadTextsBounded(t *testing.T) {
	old := textReadTimeout
	textReadTimeout = 200 * time.Millisecond
	t.Cleanup(func() { textReadTimeout = old })
	keys := map[string]string{"a": "k/fast"}
	for _, id := range []string{"b", "c", "d", "e", "f"} {
		keys[id] = "k/" + id + "/slow"
	}
	start := time.Now()
	got := readTexts(context.Background(), stallingStore{}, keys)
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("reads took %v: not concurrent or not bounded", d)
	}
	if len(got) != 1 || string(got["a"]) != "fast" {
		t.Fatalf("got %q, want only the fast one", got)
	}
}

// TestUploadsInFlightLimit: a user's attachments still uploading and shared-file uploads of their
// projects count together against MaxUploadsInFlight / MaxUploadBytesInFlight; another user's do not.
func TestUploadsInFlightLimit(t *testing.T) {
	ctx := context.Background()
	pool, err := store.OpenPool(ctx, testdb.Migrated(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := store.Exec(ctx, pool, `INSERT INTO at_users (id, email, password_hash) VALUES ('u1', 'a@example.com', 'x'), ('u2', 'b@example.com', 'x');
INSERT INTO at_projects (id, user_id, name) VALUES ('p1', 'u1', 'A'), ('p2', 'u2', 'B')`); err != nil {
		t.Fatal(err)
	}
	bs := blobtest.New(t)
	// Shared-file uploads in flight: 1 GiB in p1 (u1), lots in p2 (u2, not counted for u1).
	if _, err := store.Exec(ctx, pool, `INSERT INTO at_shared_uploads (id, project_id, path, object_id, storage_key, size, content_type, uploaded_by)
	VALUES ('up_1', 'p1', 'a.bin', 'o1', 'projects/p1/shared/o1', $1, 'application/octet-stream', 'user'),
	       ('up_2', 'p2', 'b.bin', 'o2', 'projects/p2/shared/o2', $2, 'application/octet-stream', 'user')`,
		int64(1<<30), int64(MaxUploadBytesInFlight)); err != nil {
		t.Fatal(err)
	}
	if err := CheckUploadsInFlight(ctx, pool, "u1", MaxUploadBytesInFlight-(1<<30)+1); !errors.Is(err, ErrTooManyUploads) {
		t.Fatalf("bytes over the limit: %v", err)
	}
	if err := CheckUploadsInFlight(ctx, pool, "u2", 1); !errors.Is(err, ErrTooManyUploads) {
		t.Fatalf("u2 is at the byte limit: %v", err)
	}
	// Attachments fill the count: 1 shared + MaxUploadsInFlight-1 attachments, then the next is refused.
	for i := 0; i < MaxUploadsInFlight-1; i++ {
		if _, err := BeginAttachmentUpload(ctx, pool, bs, 0, "p1", "u1", "f.bin", 10); err != nil {
			t.Fatalf("upload %d: %v", i+1, err)
		}
	}
	_, err = BeginAttachmentUpload(ctx, pool, bs, 0, "", "u1", "g.bin", 10)
	if !errors.Is(err, ErrTooManyUploads) || !strings.Contains(err.Error(), "同时进行中的上传太多：最多") {
		t.Fatalf("upload over the count: %v", err)
	}
	// Finishing (here: aborting) one frees a slot.
	var id string
	_ = store.QueryRow(ctx, pool, `SELECT id FROM at_attachments WHERE user_id = 'u1' LIMIT 1`).Scan(&id)
	if err := AbortAttachmentUpload(ctx, pool, bs, OwnerOfProject("p1"), id); err != nil {
		t.Fatal(err)
	}
	if _, err := BeginAttachmentUpload(ctx, pool, bs, 0, "", "u1", "g.bin", 10); err != nil {
		t.Fatalf("after one ended: %v", err)
	}
}
