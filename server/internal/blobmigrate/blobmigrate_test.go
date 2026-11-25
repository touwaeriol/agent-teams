package blobmigrate_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/blobgc"
	"github.com/touwaeriol/agent-teams/server/internal/blobmigrate"
	"github.com/touwaeriol/agent-teams/server/internal/blobstore"
	"github.com/touwaeriol/agent-teams/server/internal/blobstore/blobtest"
	"github.com/touwaeriol/agent-teams/server/internal/store"
	"github.com/touwaeriol/agent-teams/server/internal/testdb"
)

func hexsum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

type fixture struct {
	ctx context.Context
	db  *store.Pool
	bs  *blobstore.S3
	r   *blobmigrate.Runner
	pid string
}

func setup(t *testing.T) fixture {
	ctx := context.Background()
	s, err := store.Open(ctx, testdb.Migrated(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	u, err := s.CreateUser(ctx, "mig@example.com", "h")
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.CreateProject(ctx, u.ID, store.NewProject{Name: "m", WorkMode: "worktree"})
	if err != nil {
		t.Fatal(err)
	}
	bs := blobtest.New(t)
	f := fixture{ctx: ctx, db: s.Pool, bs: bs, r: &blobmigrate.Runner{DB: s.Pool, Blobs: bs}, pid: p.ID}
	// Rows from before #58: three attachments (one with a wrong sha256), two shared files.
	for i, body := range []string{"aaa", "bbbb", "ccccc"} {
		sum := hexsum([]byte(body))
		if i == 2 {
			sum = strings.Repeat("0", 64)
		}
		f.exec(t, `INSERT INTO at_attachments (id, project_id, user_id, name, content_type, size, sha256, data)
			VALUES ($1, $2, $3, 'f', 'text/plain', $4, $5, $6)`, []string{"img_a", "img_b", "img_c"}[i], p.ID, u.ID, len(body), sum, []byte(body))
	}
	for _, path := range []string{"docs/a.md", "z.txt"} {
		body := []byte("shared " + path)
		f.exec(t, `INSERT INTO at_shared_files (project_id, path, content_type, size, sha256, data, uploaded_by)
			VALUES ($1, $2, 'text/plain', $3, $4, $5, 'user')`, p.ID, path, len(body), hexsum(body), body)
	}
	return f
}

func (f fixture) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := store.Exec(f.ctx, f.db, q, args...); err != nil {
		t.Fatal(err)
	}
}

func (f fixture) count(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := store.QueryRow(f.ctx, f.db, q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// reports(t)(runner.Step(...)) indexes a run's reports by table.
func reports(t *testing.T) func([]blobmigrate.Report, error) map[string]blobmigrate.Report {
	return func(rs []blobmigrate.Report, err error) map[string]blobmigrate.Report {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]blobmigrate.Report{}
		for _, r := range rs {
			out[r.Table] = r
		}
		return out
	}
}

// TestMigrateVerifyPurgeRestore walks the whole lifecycle: dry run, an interrupted migrate (Limit),
// resume, idempotent re-run, sha mismatch skipped, verify, purge (data cleared only after the
// object matched), restore back into data.
func TestMigrateVerifyPurgeRestore(t *testing.T) {
	f := setup(t)
	o := blobmigrate.Options{Batch: 2}

	dry := reports(t)(f.r.Migrate(f.ctx, blobmigrate.Options{DryRun: true}))
	if dry["attachments"].Done != 2 || dry["attachments"].Failed != 1 || dry["shared"].Done != 2 ||
		f.count(t, `SELECT count(*) FROM at_attachments WHERE storage_key IS NOT NULL`) != 0 {
		t.Fatalf("dry run %+v", dry)
	}

	// Interrupted after one row per table, then resumed.
	first := reports(t)(f.r.Migrate(f.ctx, blobmigrate.Options{Batch: 2, Limit: 1}))
	if first["attachments"].Done != 1 || first["shared"].Done != 1 {
		t.Fatalf("limited run %+v", first)
	}
	rest := reports(t)(f.r.Migrate(f.ctx, o))
	if rest["attachments"].Done != 1 || rest["attachments"].Failed != 1 || rest["shared"].Done != 1 ||
		!strings.Contains(strings.Join(rest["attachments"].Errors, "\n"), "img_c") {
		t.Fatalf("resumed run %+v", rest)
	}
	again := reports(t)(f.r.Migrate(f.ctx, o))
	if again["attachments"].Done != 0 || again["shared"].Done != 0 {
		t.Fatalf("re-run must find nothing left: %+v", again)
	}
	if n := f.count(t, `SELECT count(*) FROM at_attachments WHERE data IS NOT NULL`); n != 3 {
		t.Fatalf("migrate must keep data, %d rows have it", n)
	}
	var key string
	_ = store.QueryRow(f.ctx, f.db, `SELECT storage_key FROM at_shared_files WHERE path = 'z.txt'`).Scan(&key)
	if key != blobstore.SharedKey(f.pid, blobmigrate.LegacyObjectID(f.pid, "z.txt")) {
		t.Fatalf("shared key %q", key)
	}

	// Verify catches a corrupted object; purge leaves that row's data alone.
	if err := f.bs.Put(f.ctx, blobstore.AttachmentKey(f.pid, "img_b"), bytes.NewReader([]byte("XXXX")), 4, ""); err != nil {
		t.Fatal(err)
	}
	v := reports(t)(f.r.Verify(f.ctx, o))
	if v["attachments"].Done != 1 || v["attachments"].Failed != 1 || v["shared"].Done != 2 {
		t.Fatalf("verify %+v", v)
	}
	p := reports(t)(f.r.PurgeData(f.ctx, o))
	if p["attachments"].Done != 1 || p["attachments"].Failed != 1 || p["shared"].Done != 2 {
		t.Fatalf("purge %+v", p)
	}
	if f.count(t, `SELECT count(*) FROM at_attachments WHERE id = 'img_b' AND data IS NOT NULL`) != 1 ||
		f.count(t, `SELECT count(*) FROM at_attachments WHERE id = 'img_a' AND data IS NULL`) != 1 {
		t.Fatal("purge cleared the wrong rows")
	}

	// Restore puts the bytes back for rows without data (and skips big ones).
	f.exec(t, `INSERT INTO at_attachments (id, project_id, user_id, name, content_type, size, storage_key)
		SELECT 'img_big', project_id, user_id, 'big', 'application/zip', 209715200, 'projects/x/big' FROM at_attachments WHERE id = 'img_a'`)
	r := reports(t)(f.r.Restore(f.ctx, o))
	if r["attachments"].Done != 1 || r["attachments"].Failed != 1 || r["shared"].Done != 2 {
		t.Fatalf("restore %+v", r)
	}
	var data []byte
	_ = store.QueryRow(f.ctx, f.db, `SELECT data FROM at_attachments WHERE id = 'img_a'`).Scan(&data)
	if string(data) != "aaa" {
		t.Fatalf("restored %q", data)
	}
	_ = store.QueryRow(f.ctx, f.db, `SELECT data FROM at_shared_files WHERE path = 'docs/a.md'`).Scan(&data)
	if string(data) != "shared docs/a.md" {
		t.Fatalf("restored shared %q", data)
	}
	if rs := reports(t)(f.r.Restore(f.ctx, o)); rs["attachments"].Done != 0 || rs["shared"].Done != 0 {
		t.Fatalf("restore twice %+v", rs)
	}
}

// TestMigrateRaceOverwrite: a shared file overwritten after its row was read is not given the old
// bytes' object; the stray copy is queued for deletion.
func TestMigrateRaceOverwrite(t *testing.T) {
	f := setup(t)
	// The guarded update compares the row's sha256 with the bytes it uploaded; a row whose sha256
	// no longer matches (here: blanked, as if overwritten meanwhile) is skipped.
	f.exec(t, `UPDATE at_shared_files SET sha256 = '' WHERE path = 'z.txt'`)
	rep := reports(t)(f.r.Migrate(f.ctx, blobmigrate.Options{Table: "shared"}))
	if rep["shared"].Done != 1 || rep["shared"].Skipped != 1 {
		t.Fatalf("race %+v", rep)
	}
	if f.count(t, `SELECT count(*) FROM at_blob_deletions WHERE key LIKE '%sf_legacy_%'`) != 1 {
		t.Fatal("stray copy not queued")
	}
}

func TestNotConfigured(t *testing.T) {
	r := blobmigrate.Runner{Blobs: blobstore.None{}}
	if _, err := r.Migrate(context.Background(), blobmigrate.Options{}); err == nil {
		t.Fatal("migrate without a store")
	}
	if _, err := (&blobmigrate.Runner{Blobs: blobtest.New(t)}).Migrate(context.Background(), blobmigrate.Options{Table: "x"}); err == nil {
		t.Fatal("unknown table accepted")
	}
}

// TestConcurrentMigrate: two migrate runs at once write the same deterministic keys; the run that
// loses a row's update must not queue the object the winner recorded, so after blobgc has run
// every migrated row's object is still there.
func TestConcurrentMigrate(t *testing.T) {
	f := setup(t)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = (&blobmigrate.Runner{DB: f.db, Blobs: f.bs}).Migrate(f.ctx, blobmigrate.Options{Batch: 1})
		}()
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		t.Fatal(err)
	}
	// The loser's rows (if any raced) are simulated too: a stale copy of a migrated row.
	if err := blobgc.EnqueueUnreferenced(f.ctx, f.db, blobstore.AttachmentKey(f.pid, "img_a")); err != nil {
		t.Fatal(err)
	}
	w := &blobgc.Worker{DB: f.db, Blobs: f.bs, Now: func() time.Time { return time.Now().Add(2 * time.Hour) }}
	if _, err := w.RunOnce(f.ctx); err != nil {
		t.Fatal(err)
	}
	v := reports(t)(f.r.Verify(f.ctx, blobmigrate.Options{}))
	if v["attachments"].Done != 2 || v["attachments"].Failed != 0 || v["shared"].Done != 2 || v["shared"].Failed != 0 {
		t.Fatalf("after concurrent migrate %+v", v)
	}
}
