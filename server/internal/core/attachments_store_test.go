package core

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/blobstore"
	"github.com/touwaeriol/agent-teams/server/internal/blobstore/blobtest"
	"github.com/touwaeriol/agent-teams/server/internal/store"
	"github.com/touwaeriol/agent-teams/server/internal/testdb"
)

// TestAttachmentStorage: new attachments go to the object store (data stays NULL) and are read
// back from it; rows from before #58 are still read from data; without a store new uploads fail
// with ErrNotConfigured; rows still uploading are not served.
func TestAttachmentStorage(t *testing.T) {
	ctx := context.Background()
	pool, err := store.OpenPool(ctx, testdb.Migrated(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := store.Exec(ctx, pool, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO at_users (id, email, password_hash) VALUES ('u1', 'a@example.com', 'x');
INSERT INTO at_projects (id, user_id, name) VALUES ('p1', 'u1', 'A');
INSERT INTO at_attachments (id, project_id, user_id, name, content_type, size, sha256, data)
	VALUES ('img_old', 'p1', 'u1', 'old.txt', 'text/plain', 3, 'x', '\x6f6c64');
INSERT INTO at_attachments (id, project_id, user_id, name, content_type, size, storage_key, state, upload_id)
	VALUES ('img_up', 'p1', 'u1', 'up.zip', 'application/zip', 9, 'projects/p1/attachments/img_up', 'uploading', 'u')`)
	bs := blobtest.New(t)
	body := []byte("new text")
	a, err := NewAttachment("p1", "u1", "n.md", body)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := InsertAttachment(ctx, pool, blobstore.None{}, 0, a, body); !errors.Is(err, blobstore.ErrNotConfigured) {
		t.Fatalf("insert without a store: %v", err)
	}
	a, err = InsertAttachment(ctx, pool, bs, 0, a, body)
	if err != nil || a.StorageKey == nil || *a.StorageKey != blobstore.AttachmentKey("p1", a.ID) {
		t.Fatalf("insert %+v %v", a, err)
	}
	var nulls int
	_ = store.QueryRow(ctx, pool, `SELECT count(*) FROM at_attachments WHERE id = $1 AND data IS NULL AND state = 'ready'`, a.ID).Scan(&nulls)
	if nulls != 1 {
		t.Fatal("new attachment kept its bytes in the database")
	}
	for id, want := range map[string]string{a.ID: "new text", "img_old": "old"} {
		got, data, err := AttachmentData(ctx, pool, bs, "u1", id)
		if err != nil || string(data) != want || got.ID != id {
			t.Fatalf("read %s: %q %v", id, data, err)
		}
	}
	if _, _, err := AttachmentData(ctx, pool, bs, "u2", a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another user's attachment: %v", err)
	}
	if _, rc, err := OpenAttachment(ctx, pool, bs, "u1", "img_up"); err == nil {
		rc.Close()
		t.Fatal("an attachment still uploading was served")
	}
	texts, err := AttachmentTexts(ctx, pool, bs, "p1", []string{a.ID, "img_old", "img_up"})
	if err != nil || string(texts[a.ID]) != "new text" || string(texts["img_old"]) != "old" || len(texts) != 2 {
		t.Fatalf("texts %q %v", texts, err)
	}
	// Retention: a use pushes expires_at to now+TTL (never earlier); expired ones are refused.
	exec(`UPDATE at_attachments SET expires_at = now() + interval '1 hour' WHERE id = $1`, a.ID)
	if _, err := UseAttachments(ctx, pool, "p1", []string{a.ID}, 48*time.Hour); err != nil {
		t.Fatal(err)
	}
	var hours float64
	_ = store.QueryRow(ctx, pool, `SELECT extract(epoch FROM expires_at - now()) / 3600 FROM at_attachments WHERE id = $1`, a.ID).Scan(&hours)
	if hours < 47 || hours > 49 {
		t.Fatalf("expires in %.1f h after a use with a 48 h TTL", hours)
	}
	if err := TouchAttachments(ctx, pool, "p1", []string{a.ID}, time.Hour); err != nil {
		t.Fatal(err)
	}
	_ = store.QueryRow(ctx, pool, `SELECT extract(epoch FROM expires_at - now()) / 3600 FROM at_attachments WHERE id = $1`, a.ID).Scan(&hours)
	if hours < 47 {
		t.Fatalf("a shorter touch moved the expiry earlier: %.1f h", hours)
	}
	exec(`UPDATE at_attachments SET state = 'expired', data = NULL WHERE id = 'img_old'`)
	if _, err := UseAttachments(ctx, pool, "p1", []string{a.ID, "img_old"}, 0); !errors.Is(err, ErrAttachmentExpired) {
		t.Fatalf("use of an expired attachment: %v", err)
	}
	if _, _, err := OpenAttachment(ctx, pool, bs, "u1", "img_old"); !errors.Is(err, ErrAttachmentExpired) {
		t.Fatalf("open expired: %v", err)
	}
	if name, err := ExpiredAttachment(ctx, pool, "p1", []string{a.ID, "img_old"}); err != nil || name != "old.txt" {
		t.Fatalf("expired name %q %v", name, err)
	}
	if _, err := UseAttachments(ctx, pool, "p1", []string{"img_up"}, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("use of an upload in progress: %v", err)
	}
	_, rc, _ := OpenAttachment(ctx, pool, bs, "u1", a.ID)
	b, _ := io.ReadAll(rc)
	rc.Close()
	if string(b) != "new text" {
		t.Fatalf("stream %q", b)
	}
}
