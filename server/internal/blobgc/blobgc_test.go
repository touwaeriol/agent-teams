package blobgc_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/blobgc"
	"github.com/touwaeriol/agent-teams/server/internal/blobstore"
	"github.com/touwaeriol/agent-teams/server/internal/blobstore/blobtest"
	"github.com/touwaeriol/agent-teams/server/internal/store"
	"github.com/touwaeriol/agent-teams/server/internal/testdb"
)

func hexsum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// flaky fails Delete while broken is set.
type flaky struct {
	blobstore.Store
	broken bool
}

func (f *flaky) Delete(ctx context.Context, keys ...string) error {
	if f.broken {
		return errors.New("store down")
	}
	return f.Store.Delete(ctx, keys...)
}

func TestWorker(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, testdb.Migrated(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	db := s.Pool
	u, _ := s.CreateUser(ctx, "gc@example.com", "h")
	p, err := s.CreateProject(ctx, u.ID, store.NewProject{Name: "gc", WorkMode: "worktree"})
	if err != nil {
		t.Fatal(err)
	}
	bs := &flaky{Store: blobtest.New(t)}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := store.Exec(ctx, db, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	count := func(q string, args ...any) int {
		t.Helper()
		var n int
		if err := store.QueryRow(ctx, db, q, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	put := func(key string, body []byte) {
		t.Helper()
		if err := bs.Put(ctx, key, bytes.NewReader(body), int64(len(body)), ""); err != nil {
			t.Fatal(err)
		}
	}
	exists := func(key string) bool {
		_, err := bs.Head(ctx, key)
		return err == nil
	}
	later := time.Now().Add(blobgc.Stale + time.Hour)
	w := &blobgc.Worker{DB: db, Blobs: bs}

	// Hashes: an attachment and two shared files uploaded without a sha256 (one does not match what
	// its uploader declared).
	att, good, bad := []byte("attachment body"), []byte("good shared"), []byte("tampered")
	kAtt, kGood, kBad := blobstore.AttachmentKey(p.ID, "img_h"), blobstore.SharedKey(p.ID, "sf_good"), blobstore.SharedKey(p.ID, "sf_bad")
	put(kAtt, att)
	put(kGood, good)
	put(kBad, bad)
	exec(`INSERT INTO at_attachments (id, project_id, user_id, name, content_type, size, storage_key, expires_at)
		VALUES ('img_h', $1, $2, 'a', 'text/plain', $3, $4, now() + interval '3 days')`, p.ID, u.ID, len(att), kAtt)
	exec(`INSERT INTO at_shared_files (project_id, path, content_type, size, uploaded_by, storage_key, object_id, declared_sha256)
		VALUES ($1, 'good.txt', 'text/plain', $2, 'user', $3, 'sf_good', $4), ($1, 'bad.txt', 'text/plain', $5, 'user', $6, 'sf_bad', $7)`,
		p.ID, len(good), kGood, hexsum(good), len(bad), kBad, hexsum([]byte("what was declared")))

	// Deletions: one queued key, one project prefix.
	kGone, kPrefix := blobstore.AttachmentKey("p-gone", "img_x"), blobstore.ProjectPrefix("p-del")
	put(kGone, []byte("x"))
	put(kPrefix+"shared/sf_1", []byte("y"))
	if err := blobgc.Enqueue(ctx, db, kGone, ""); err != nil {
		t.Fatal(err)
	}
	if err := blobgc.EnqueuePrefix(ctx, db, kPrefix); err != nil {
		t.Fatal(err)
	}

	// A failing store (Delete only; DeletePrefix goes through): the key stays queued with a backoff.
	bs.broken = true
	st, err := w.RunOnce(ctx)
	if err != nil || st.Deleted != 1 || st.DeleteFailed != 1 || st.Hashed != 2 || st.HashFailed != 1 {
		t.Fatalf("first pass %+v %v", st, err)
	}
	if count(`SELECT count(*) FROM at_blob_deletions WHERE attempts = 1 AND next_at > now() AND last_error = 'store down'`) != 1 {
		t.Fatal("failed deletion not backed off")
	}
	var sum string
	_ = store.QueryRow(ctx, db, `SELECT sha256 FROM at_attachments WHERE id = 'img_h'`).Scan(&sum)
	if sum != hexsum(att) {
		t.Fatalf("attachment hash %q", sum)
	}
	if count(`SELECT count(*) FROM at_shared_files WHERE path = 'good.txt' AND sha256 = $1 AND state = 'ready'`, hexsum(good)) != 1 ||
		count(`SELECT count(*) FROM at_shared_files WHERE path = 'bad.txt' AND sha256 IS NULL AND state = 'failed'`) != 1 {
		t.Fatal("shared hashes")
	}

	// Store back, time moved on: deletions go through.
	bs.broken = false
	w.Now = func() time.Time { return later }
	if st, err := w.RunOnce(ctx); err != nil || st.Deleted != 1 {
		t.Fatalf("second pass %+v %v", st, err)
	}
	if exists(kGone) || exists(kPrefix+"shared/sf_1") || count(`SELECT count(*) FROM at_blob_deletions`) != 0 {
		t.Fatal("objects not deleted")
	}

	// Cleanup: an attachment still uploading, an expired one, a staged shared upload, a stray
	// multipart upload nobody knows, all older than a day; an attachment used recently stays.
	upAtt, err := bs.CreateMultipart(ctx, blobstore.AttachmentKey(p.ID, "img_up"), "")
	if err != nil {
		t.Fatal(err)
	}
	upShared, _ := bs.CreateMultipart(ctx, blobstore.SharedKey(p.ID, "sf_up"), "")
	stray, _ := bs.CreateMultipart(ctx, blobstore.AttachmentKey(p.ID, "img_stray"), "")
	put(blobstore.AttachmentKey(p.ID, "img_un"), []byte("u"))
	exec(`INSERT INTO at_attachments (id, project_id, user_id, name, content_type, size, storage_key, state, upload_id, created_at)
		VALUES ('img_up', $1, $2, 'up', 'application/zip', 10, $3, 'uploading', $4, now() - interval '2 days')`,
		p.ID, u.ID, blobstore.AttachmentKey(p.ID, "img_up"), upAtt)
	exec(`INSERT INTO at_attachments (id, project_id, user_id, name, content_type, size, sha256, storage_key, created_at, expires_at)
		VALUES ('img_un', $1, $2, 'un', 'text/plain', 1, 'x', $3, now() - interval '2 days', now() - interval '1 day'),
		('img_legacy', $1, $2, 'old', 'text/plain', 1, 'x', NULL, now() - interval '2 days', now() - interval '1 day')`,
		p.ID, u.ID, blobstore.AttachmentKey(p.ID, "img_un"))
	exec(`UPDATE at_attachments SET data = '\x41' WHERE id = 'img_legacy'`)
	exec(`UPDATE at_attachments SET created_at = now() - interval '2 days', expires_at = now() + interval '30 hours' WHERE id = 'img_h'`)
	exec(`INSERT INTO at_shared_uploads (id, project_id, path, object_id, storage_key, s3_upload_id, size, content_type, uploaded_by, created_at)
		VALUES ('up_1', $1, 'big.zip', 'sf_up', $2, $3, 10, 'application/zip', 'user', now() - interval '2 days')`,
		p.ID, blobstore.SharedKey(p.ID, "sf_up"), upShared)
	w.Now = func() time.Time { return time.Now().Add(blobgc.Stale + time.Hour) }
	st, err = w.RunOnce(ctx)
	if err != nil || st.Aborted != 3 || st.Expired != 2 {
		t.Fatalf("cleanup pass %+v %v", st, err)
	}
	if count(`SELECT count(*) FROM at_attachments WHERE id = 'img_up'`) != 0 || count(`SELECT count(*) FROM at_shared_uploads`) != 0 ||
		count(`SELECT count(*) FROM at_attachments WHERE id IN ('img_un', 'img_legacy') AND state = 'expired' AND data IS NULL AND storage_key IS NULL`) != 2 ||
		count(`SELECT count(*) FROM at_attachments WHERE id = 'img_h' AND state = 'ready'`) != 1 {
		t.Fatal("cleanup rows")
	}
	if pending, _ := bs.ListMultipart(ctx, "projects/", time.Now().Add(time.Hour)); len(pending) != 0 {
		t.Fatalf("multipart uploads left: %+v (stray %s)", pending, stray)
	}
	if _, err := w.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if exists(blobstore.AttachmentKey(p.ID, "img_un")) {
		t.Fatal("expired attachment object not deleted")
	}

	// No store configured: nothing happens.
	if st, err := (&blobgc.Worker{DB: db}).RunOnce(ctx); err != nil || st != (blobgc.Stats{}) {
		t.Fatalf("unconfigured %+v %v", st, err)
	}
}

// TestReserveAndReferences: an object written for a transaction that rolls back is removed once
// its reservation is due; one whose transaction committed is kept (the queue row is dropped); a
// key queued while a row points at it again is kept; a passing hash failure is retried later
// without blocking the rows behind it.
func TestReserveAndReferences(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, testdb.Migrated(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	db := s.Pool
	u, _ := s.CreateUser(ctx, "res@example.com", "h")
	p, err := s.CreateProject(ctx, u.ID, store.NewProject{Name: "r", WorkMode: "worktree"})
	if err != nil {
		t.Fatal(err)
	}
	bs := blobtest.New(t)
	put := func(key string) {
		t.Helper()
		if err := bs.Put(ctx, key, bytes.NewReader([]byte("x")), 1, ""); err != nil {
			t.Fatal(err)
		}
	}
	exists := func(key string) bool { _, err := bs.Head(ctx, key); return err == nil }
	kept, lost, back := blobstore.AttachmentKey(p.ID, "img_kept"), blobstore.AttachmentKey(p.ID, "img_lost"), blobstore.SharedKey(p.ID, "sf_back")
	for _, k := range []string{kept, lost, back} {
		put(k)
		if err := blobgc.Reserve(ctx, db, k); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Exec(ctx, db, `INSERT INTO at_attachments (id, project_id, user_id, name, content_type, size, sha256, storage_key)
		VALUES ('img_kept', $1, $2, 'k', 'text/plain', 1, 'x', $3)`, p.ID, u.ID, kept); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Exec(ctx, db, `INSERT INTO at_shared_files (project_id, path, content_type, size, sha256, uploaded_by, storage_key, object_id)
		VALUES ($1, 'b.txt', 'text/plain', 1, 'x', 'user', $2, 'sf_back')`, p.ID, back); err != nil {
		t.Fatal(err)
	}
	if err := blobgc.Enqueue(ctx, db, back); err != nil { // queued for real while still referenced
		t.Fatal(err)
	}
	w := &blobgc.Worker{DB: db, Blobs: bs}
	st, err := w.RunOnce(ctx) // reservations not due yet; back is due but referenced
	if err != nil || st.Kept != 1 || st.Deleted != 0 || !exists(back) {
		t.Fatalf("first pass %+v %v", st, err)
	}
	w.Now = func() time.Time { return time.Now().Add(blobgc.ReserveDelay + time.Minute) }
	st, err = w.RunOnce(ctx)
	if err != nil || st.Kept != 1 || st.Deleted != 1 || !exists(kept) || exists(lost) {
		t.Fatalf("due pass %+v %v kept=%v lost=%v", st, err, exists(kept), exists(lost))
	}
	if ref, err := blobgc.Referenced(ctx, db, kept); err != nil || !ref {
		t.Fatalf("referenced %v %v", ref, err)
	}

	// EnqueueUnreferenced skips a key a row points at.
	if err := blobgc.EnqueueUnreferenced(ctx, db, kept); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = store.QueryRow(ctx, db, `SELECT count(*) FROM at_blob_deletions WHERE key = $1`, kept).Scan(&n)
	if n != 0 {
		t.Fatal("referenced key queued")
	}

	// A store that cannot be read: the first attachment is put aside, the next one is hashed.
	if _, err := store.Exec(ctx, db, `INSERT INTO at_attachments (id, project_id, user_id, name, content_type, size, storage_key, created_at)
		VALUES ('img_a', $1, $2, 'a', 'text/plain', 1, 'projects/x/a', now() - interval '1 hour'),
		('img_b', $1, $2, 'b', 'text/plain', 1, $3, now())`, p.ID, u.ID, blobstore.AttachmentKey(p.ID, "img_b")); err != nil {
		t.Fatal(err)
	}
	put(blobstore.AttachmentKey(p.ID, "img_b"))
	hw := &blobgc.Worker{DB: db, Blobs: &unreadable{Store: bs, key: "projects/x/a"}, Batch: 1}
	for i := 0; i < 2; i++ {
		if _, err := hw.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var hashed, state string
	_ = store.QueryRow(ctx, db, `SELECT COALESCE(sha256, '') FROM at_attachments WHERE id = 'img_b'`).Scan(&hashed)
	_ = store.QueryRow(ctx, db, `SELECT state FROM at_attachments WHERE id = 'img_a'`).Scan(&state)
	if hashed != hexsum([]byte("x")) || state != "ready" {
		t.Fatalf("img_b hash %q, img_a state %s", hashed, state)
	}
}

// unreadable fails Open of one key with a passing error.
type unreadable struct {
	blobstore.Store
	key string
}

func (u *unreadable) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	if key == u.key {
		return nil, errors.New("connection reset")
	}
	return u.Store.Open(ctx, key)
}
