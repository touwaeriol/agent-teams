// Package blobgc keeps the object store and the database in step (#58 design §3.2, §3.4, §8):
//
//   - deletions: rows are deleted in a transaction that also queues their object keys in
//     at_blob_deletions (Enqueue / EnqueuePrefix); the objects are removed after commit, with
//     retries, so a rolled-back transaction never loses bytes;
//   - hashes: uploads finished after #58 get their sha256 by streaming the object (the control
//     plane never trusts the client's hash and never holds the object in memory); a shared file
//     whose hash does not match what its uploader declared becomes state=failed;
//   - cleanup: multipart uploads left open for a day are aborted, attachments past their retention
//     (expires_at: 24 h after the last use) become state=expired and lose their bytes, and stray
//     multipart uploads the database does not know are aborted.
//
// One replica runs it (advisory lock, like the timers loop).
package blobgc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/blobstore"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// Stale is how long an unfinished upload is kept.
const Stale = 24 * time.Hour

// Enqueue queues object keys for deletion after the transaction commits ("" keys are skipped). A
// key still referenced by a row when its turn comes is not deleted (see deletions).
func Enqueue(ctx context.Context, q store.DB, keys ...string) error {
	for _, k := range keys {
		if k == "" {
			continue
		}
		if _, err := store.Exec(ctx, q, `INSERT INTO at_blob_deletions (key) VALUES ($1)
			ON CONFLICT (key) DO UPDATE SET next_at = LEAST(at_blob_deletions.next_at, now())`, k); err != nil {
			return err
		}
	}
	return nil
}

// ReserveDelay is how long a reserved key waits before blobgc looks at it: far longer than any
// transaction that writes an object.
const ReserveDelay = time.Hour

// Reserve queues key for deletion in ReserveDelay, outside q's transaction (store.Detached), before
// the object is written: if the transaction that records the key rolls back (or the process dies)
// the object is removed; if it commits, the key is referenced and blobgc drops the reservation.
func Reserve(ctx context.Context, q store.DB, key string) error {
	_, err := store.Exec(ctx, store.Detached(q), `INSERT INTO at_blob_deletions (key, next_at) VALUES ($1, now() + make_interval(secs => $2))
		ON CONFLICT (key) DO NOTHING`, key, ReserveDelay.Seconds())
	return err
}

// referencedSQL tells whether a row still points at an object key ($1; $2 = its project id).
const referencedSQL = `SELECT EXISTS (SELECT 1 FROM at_attachments WHERE project_id = $2 AND storage_key = $1)
	OR EXISTS (SELECT 1 FROM at_shared_files WHERE project_id = $2 AND storage_key = $1)
	OR EXISTS (SELECT 1 FROM at_shared_uploads WHERE project_id = $2 AND storage_key = $1)`

// userKeySQL tells whether an attachment row points at a users/<uid>/… key (uploaded without a
// project; the row may since have joined one).
const userKeySQL = `SELECT EXISTS (SELECT 1 FROM at_attachments WHERE storage_key = $1 AND storage_key LIKE 'users/%')`

// Referenced reports whether a row of at_attachments, at_shared_files or at_shared_uploads still
// points at key (keys are projects/<pid>/…, or users/<uid>/attachments/… for an attachment
// uploaded without a project).
func Referenced(ctx context.Context, q store.DB, key string) (bool, error) {
	if strings.HasPrefix(key, "users/") {
		var ref bool
		err := store.QueryRow(ctx, q, userKeySQL, key).Scan(&ref)
		return ref, err
	}
	var pid string
	if rest, ok := strings.CutPrefix(key, "projects/"); ok {
		pid, _, _ = strings.Cut(rest, "/")
	}
	var ref bool
	err := store.QueryRow(ctx, q, referencedSQL, key, pid).Scan(&ref)
	return ref, err
}

// EnqueueUnreferenced queues key only when no row points at it (a copy that lost a race: the row
// may meanwhile have been given the very same key by another writer).
func EnqueueUnreferenced(ctx context.Context, q store.DB, key string) error {
	ref, err := Referenced(ctx, q, key)
	if err != nil || ref {
		return err
	}
	return Enqueue(ctx, q, key)
}

// EnqueuePrefix queues every object under prefix (a deleted project's projects/<pid>/).
func EnqueuePrefix(ctx context.Context, q store.DB, prefix string) error {
	if prefix == "" {
		return errors.New("blobgc: empty prefix")
	}
	_, err := store.Exec(ctx, q, `INSERT INTO at_blob_deletions (key, prefix) VALUES ($1, true)
		ON CONFLICT (key) DO UPDATE SET prefix = true`, prefix)
	return err
}

// Worker runs the maintenance passes.
type Worker struct {
	DB    store.DB
	Blobs blobstore.Store
	Log   *slog.Logger
	Now   func() time.Time
	// Batch is how many rows one pass handles per kind (default 50).
	Batch int

	mu sync.Mutex
	// retry holds rows whose hash failed for a passing reason (store unreachable…): skipped
	// until the time given, so they do not block the head of the queue.
	retry map[string]retryAt
}

type retryAt struct {
	at       time.Time
	attempts int
}

// later puts a row aside after a passing hash failure, with an exponential backoff.
func (w *Worker) later(id string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.retry == nil {
		w.retry = map[string]retryAt{}
	}
	r := w.retry[id]
	r.attempts++
	r.at = w.now().Add(min(time.Duration(1<<min(r.attempts, 10))*time.Minute, 6*time.Hour))
	w.retry[id] = r
}

// waiting lists the rows still put aside (and forgets the ones whose time came).
func (w *Worker) waiting() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := []string{}
	now := w.now()
	for id, r := range w.retry {
		if r.at.After(now) {
			out = append(out, id)
		}
	}
	return out
}

func (w *Worker) done(id string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.retry, id)
}

// Stats is what one pass did.
type Stats struct {
	Deleted, DeleteFailed, Hashed, HashFailed, Aborted, Expired int
	// Kept are queued keys a row points at again (not deleted).
	Kept int
}

func (w *Worker) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

func (w *Worker) batch() int {
	if w.Batch > 0 {
		return w.Batch
	}
	return 50
}

func (w *Worker) log() *slog.Logger {
	if w.Log != nil {
		return w.Log
	}
	return slog.Default()
}

// RunOnce does every pass once. With no object store configured it does nothing.
func (w *Worker) RunOnce(ctx context.Context) (Stats, error) {
	var st Stats
	if !blobstore.Or(w.Blobs).Configured() {
		return st, nil
	}
	steps := []func(context.Context, *Stats) error{w.expireUploads, w.expireAttachments, w.deletions, w.hashes, w.strayMultipart}
	var first error
	for _, f := range steps {
		if err := f(ctx, &st); err != nil && first == nil {
			first = err
		}
	}
	return st, first
}

// String says what a round did ("" when nothing).
func (st Stats) String() string {
	if st == (Stats{}) {
		return ""
	}
	return fmt.Sprintf("删除 %d（失败 %d），补哈希 %d（失败 %d），中止上传 %d，过期 %d",
		st.Deleted, st.DeleteFailed, st.Hashed, st.HashFailed, st.Aborted, st.Expired)
}

// expireUploads aborts multipart uploads open for longer than Stale: attachments still
// uploading (the row goes) and shared-file uploads (the staging row goes). Each row is deleted and
// its key queued in one statement (a single PUT or a completed multipart may have landed); the
// multipart abort comes after, and if it fails strayMultipart catches the upload later.
func (w *Worker) expireUploads(ctx context.Context, st *Stats) error {
	cutoff := w.now().Add(-Stale)
	type row struct{ Key, UploadID *string }
	var gone []row
	if err := store.Raw(ctx, w.DB, `WITH due AS (
		SELECT id FROM at_attachments WHERE state = 'uploading' AND created_at < $1 ORDER BY created_at LIMIT $2
		FOR UPDATE SKIP LOCKED),
	del AS (DELETE FROM at_attachments a USING due WHERE a.id = due.id AND a.state = 'uploading'
		RETURNING a.storage_key, a.upload_id),
	q AS (INSERT INTO at_blob_deletions (key) SELECT storage_key FROM del WHERE storage_key IS NOT NULL
		ON CONFLICT (key) DO UPDATE SET next_at = LEAST(at_blob_deletions.next_at, now()))
	SELECT storage_key AS key, upload_id FROM del`, cutoff, w.batch()).Scan(&gone).Error; err != nil {
		return err
	}
	var ups []row
	if err := store.Raw(ctx, w.DB, `WITH due AS (
		SELECT id FROM at_shared_uploads WHERE created_at < $1 ORDER BY created_at LIMIT $2 FOR UPDATE SKIP LOCKED),
	del AS (DELETE FROM at_shared_uploads u USING due WHERE u.id = due.id RETURNING u.storage_key, u.s3_upload_id),
	q AS (INSERT INTO at_blob_deletions (key) SELECT storage_key FROM del
		ON CONFLICT (key) DO UPDATE SET next_at = LEAST(at_blob_deletions.next_at, now()))
	SELECT storage_key AS key, s3_upload_id AS upload_id FROM del`, cutoff, w.batch()).Scan(&ups).Error; err != nil {
		return err
	}
	for _, r := range append(gone, ups...) {
		if r.Key != nil && r.UploadID != nil {
			if err := w.Blobs.AbortMultipart(ctx, *r.Key, *r.UploadID); err != nil {
				w.log().Warn("abort multipart upload", "key", *r.Key, "err", err)
			}
		}
		st.Aborted++
	}
	return nil
}

// expireAttachments ends the retention of attachments past expires_at (24 h after the last use,
// core.TouchAttachments): the row stays as state=expired (messages show it, reads answer 410),
// its bytes (data column or object) are dropped and the object is queued for deletion.
func (w *Worker) expireAttachments(ctx context.Context, st *Stats) error {
	var n []struct{ ID string }
	err := store.Raw(ctx, w.DB, `WITH due AS (
		SELECT id, storage_key FROM at_attachments WHERE state IN ('ready', 'failed') AND expires_at < $1
		ORDER BY expires_at LIMIT $2 FOR UPDATE SKIP LOCKED),
	done AS (
		UPDATE at_attachments a SET state = 'expired', data = NULL, storage_key = NULL, upload_id = NULL
		FROM due WHERE a.id = due.id RETURNING a.id),
	q AS (INSERT INTO at_blob_deletions (key) SELECT due.storage_key FROM due JOIN done ON done.id = due.id
		WHERE due.storage_key IS NOT NULL ON CONFLICT (key) DO UPDATE SET next_at = LEAST(at_blob_deletions.next_at, now()))
	SELECT id FROM done`, w.now(), w.batch()).Scan(&n).Error
	if err != nil {
		return err
	}
	st.Expired += len(n)
	return nil
}

// deletions removes queued objects; a failure is retried later with backoff.
func (w *Worker) deletions(ctx context.Context, st *Stats) error {
	type row struct {
		Key      string
		Prefix   bool
		Attempts int
	}
	var rows []row
	if err := store.Raw(ctx, w.DB, `SELECT key, prefix, attempts FROM at_blob_deletions WHERE next_at <= $1
		ORDER BY next_at LIMIT $2`, w.now(), w.batch()).Scan(&rows).Error; err != nil {
		return err
	}
	for _, r := range rows {
		if !r.Prefix {
			// A key a row points at again (a reservation whose transaction committed, a copy
			// that lost a race to the same key) is not deleted.
			ref, err := Referenced(ctx, w.DB, r.Key)
			if err != nil {
				return err
			}
			if ref {
				if _, err := store.Exec(ctx, w.DB, `DELETE FROM at_blob_deletions WHERE key = $1`, r.Key); err != nil {
					return err
				}
				st.Kept++
				continue
			}
		}
		var err error
		if r.Prefix {
			err = w.Blobs.DeletePrefix(ctx, r.Key)
		} else {
			err = w.Blobs.Delete(ctx, r.Key)
		}
		if err == nil {
			if _, err := store.Exec(ctx, w.DB, `DELETE FROM at_blob_deletions WHERE key = $1`, r.Key); err != nil {
				return err
			}
			st.Deleted++
			continue
		}
		st.DeleteFailed++
		backoff := min(time.Duration(1<<min(r.Attempts, 10))*time.Minute, 12*time.Hour)
		if r.Attempts+1 >= 20 {
			w.log().Error("blob delete keeps failing", "key", r.Key, "attempts", r.Attempts+1, "err", err)
		}
		if _, e := store.Exec(ctx, w.DB, `UPDATE at_blob_deletions SET attempts = attempts + 1, last_error = $2, next_at = $3
			WHERE key = $1`, r.Key, err.Error(), w.now().Add(backoff)); e != nil {
			return e
		}
	}
	return nil
}

// hashes fills sha256 of objects uploaded without one (streaming, never buffering the object).
func (w *Worker) hashes(ctx context.Context, st *Stats) error {
	type row struct {
		ID, Key  string
		Size     int64
		Declared *string
	}
	waiting := w.waiting()
	var atts []row
	if err := store.Raw(ctx, w.DB, `SELECT id, storage_key AS key, size FROM at_attachments
		WHERE sha256 IS NULL AND state = 'ready' AND storage_key IS NOT NULL AND NOT (id = ANY($2)) ORDER BY created_at LIMIT $1`,
		w.batch(), waiting).Scan(&atts).Error; err != nil {
		return err
	}
	for _, r := range atts {
		sum, n, err := w.sum(ctx, r.Key)
		if err != nil || n != r.Size {
			st.HashFailed++
			w.log().Warn("hash attachment", "id", r.ID, "size", n, "want", r.Size, "err", err)
			if err == nil || errors.Is(err, blobstore.ErrNotFound) {
				_, err = store.Exec(ctx, w.DB, `UPDATE at_attachments SET state = 'failed' WHERE id = $1 AND sha256 IS NULL`, r.ID)
				if err != nil {
					return err
				}
				w.done(r.ID)
			} else {
				w.later(r.ID) // passing: retry later, the next rows go first
			}
			continue
		}
		w.done(r.ID)
		if _, err := store.Exec(ctx, w.DB, `UPDATE at_attachments SET sha256 = $2 WHERE id = $1 AND sha256 IS NULL`, r.ID, sum); err != nil {
			return err
		}
		st.Hashed++
	}
	return nil
}

func (w *Worker) sum(ctx context.Context, key string) (string, int64, error) {
	rc, err := w.Blobs.Open(ctx, key)
	if err != nil {
		return "", 0, err
	}
	defer rc.Close()
	h := sha256.New()
	n, err := io.Copy(h, rc)
	if err != nil {
		return "", n, fmt.Errorf("read %s: %w", key, err)
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// strayMultipart aborts multipart uploads older than Stale that no row knows (a crash between
// CreateMultipart and the insert, or rows removed by hand).
func (w *Worker) strayMultipart(ctx context.Context, st *Stats) error {
	var pending []blobstore.PendingUpload
	for _, prefix := range []string{"projects/", "users/"} {
		list, err := w.Blobs.ListMultipart(ctx, prefix, w.now().Add(-Stale))
		if err != nil {
			return err
		}
		pending = append(pending, list...)
	}
	for _, p := range pending {
		var known bool
		if err := store.QueryRow(ctx, w.DB, `SELECT EXISTS (SELECT 1 FROM at_attachments WHERE upload_id = $1 AND state = 'uploading')
			OR EXISTS (SELECT 1 FROM at_shared_uploads WHERE s3_upload_id = $1)`, p.UploadID).Scan(&known); err != nil {
			return err
		}
		if known {
			continue // expireUploads handles it with its row
		}
		if err := w.Blobs.AbortMultipart(ctx, p.Key, p.UploadID); err != nil {
			return err
		}
		st.Aborted++
	}
	return nil
}
