// Package blobgc keeps the object store and the database in step (#58 design §3.2, §3.4, §8):
//
//   - deletions: rows are deleted in a transaction that also queues their object keys in
//     at_blob_deletions (Enqueue / EnqueuePrefix); the objects are removed after commit, with
//     retries, so a rolled-back transaction never loses bytes;
//   - hashes: uploads finished after #58 get their sha256 by streaming the object (the control
//     plane never trusts the client's hash and never holds the object in memory); a shared file
//     whose hash does not match what its uploader declared becomes state=failed;
//   - cleanup: multipart uploads left open for a day are aborted, attachments nobody referenced
//     within a day are removed, and stray multipart uploads the database does not know are aborted.
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
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/blobstore"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// Stale is how long an unfinished upload or an unreferenced attachment is kept.
const Stale = 24 * time.Hour

// Enqueue queues object keys for deletion after the transaction commits ("" keys are skipped).
func Enqueue(ctx context.Context, q store.DB, keys ...string) error {
	for _, k := range keys {
		if k == "" {
			continue
		}
		if _, err := store.Exec(ctx, q, `INSERT INTO at_blob_deletions (key) VALUES ($1) ON CONFLICT (key) DO NOTHING`, k); err != nil {
			return err
		}
	}
	return nil
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
}

// Stats is what one pass did.
type Stats struct {
	Deleted, DeleteFailed, Hashed, HashFailed, Aborted, Expired int
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
	steps := []func(context.Context, *Stats) error{w.expireUploads, w.expireUnclaimed, w.deletions, w.hashes, w.strayMultipart}
	var first error
	for _, f := range steps {
		if err := f(ctx, &st); err != nil && first == nil {
			first = err
		}
	}
	return st, first
}

// Run loops RunOnce every interval until ctx ends (call it under a leader lock).
func (w *Worker) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if st, err := w.RunOnce(ctx); err != nil {
			w.log().Warn("blob maintenance", "err", err)
		} else if st != (Stats{}) {
			w.log().Info("blob maintenance", "deleted", st.Deleted, "delete_failed", st.DeleteFailed, "hashed", st.Hashed,
				"hash_failed", st.HashFailed, "aborted", st.Aborted, "expired", st.Expired)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// expireUploads aborts multipart uploads open for longer than Stale: attachments still
// uploading (the row goes) and shared-file uploads (the staging row goes).
func (w *Worker) expireUploads(ctx context.Context, st *Stats) error {
	cutoff := w.now().Add(-Stale)
	type row struct {
		ID, Key  string
		UploadID *string
	}
	var atts []row
	if err := store.Raw(ctx, w.DB, `SELECT id, storage_key AS key, upload_id FROM at_attachments
		WHERE state = 'uploading' AND created_at < $1 AND storage_key IS NOT NULL ORDER BY created_at LIMIT $2`, cutoff, w.batch()).
		Scan(&atts).Error; err != nil {
		return err
	}
	for _, r := range atts {
		if r.UploadID != nil {
			if err := w.Blobs.AbortMultipart(ctx, r.Key, *r.UploadID); err != nil {
				return err
			}
		}
		if _, err := store.Exec(ctx, w.DB, `DELETE FROM at_attachments WHERE id = $1 AND state = 'uploading'`, r.ID); err != nil {
			return err
		}
		if err := Enqueue(ctx, w.DB, r.Key); err != nil { // a single PUT may have landed
			return err
		}
		st.Aborted++
	}
	var ups []row
	if err := store.Raw(ctx, w.DB, `SELECT id, storage_key AS key, s3_upload_id AS upload_id FROM at_shared_uploads
		WHERE created_at < $1 ORDER BY created_at LIMIT $2`, cutoff, w.batch()).Scan(&ups).Error; err != nil {
		return err
	}
	for _, r := range ups {
		if r.UploadID != nil {
			if err := w.Blobs.AbortMultipart(ctx, r.Key, *r.UploadID); err != nil {
				return err
			}
		}
		if _, err := store.Exec(ctx, w.DB, `DELETE FROM at_shared_uploads WHERE id = $1`, r.ID); err != nil {
			return err
		}
		if err := Enqueue(ctx, w.DB, r.Key); err != nil {
			return err
		}
		st.Aborted++
	}
	return nil
}

// expireUnclaimed removes finished attachments no message or task referenced within Stale.
func (w *Worker) expireUnclaimed(ctx context.Context, st *Stats) error {
	var keys []*string
	err := store.Raw(ctx, w.DB, `DELETE FROM at_attachments WHERE id IN (
		SELECT id FROM at_attachments WHERE claimed_at IS NULL AND state <> 'uploading' AND created_at < $1
		ORDER BY created_at LIMIT $2) RETURNING storage_key`, w.now().Add(-Stale), w.batch()).Scan(&keys).Error
	if err != nil {
		return err
	}
	for _, k := range keys {
		if k != nil {
			if err := Enqueue(ctx, w.DB, *k); err != nil {
				return err
			}
		}
	}
	st.Expired += len(keys)
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
	var atts []row
	if err := store.Raw(ctx, w.DB, `SELECT id, storage_key AS key, size FROM at_attachments
		WHERE sha256 IS NULL AND state = 'ready' AND storage_key IS NOT NULL ORDER BY created_at LIMIT $1`, w.batch()).
		Scan(&atts).Error; err != nil {
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
			}
			continue
		}
		if _, err := store.Exec(ctx, w.DB, `UPDATE at_attachments SET sha256 = $2 WHERE id = $1 AND sha256 IS NULL`, r.ID, sum); err != nil {
			return err
		}
		st.Hashed++
	}
	type shared struct {
		ProjectID, Path, Key string
		Size                 int64
		Declared             *string
	}
	var files []shared
	if err := store.Raw(ctx, w.DB, `SELECT project_id, path, storage_key AS key, size, declared_sha256 AS declared FROM at_shared_files
		WHERE sha256 IS NULL AND state = 'ready' AND storage_key IS NOT NULL ORDER BY updated_at LIMIT $1`, w.batch()).
		Scan(&files).Error; err != nil {
		return err
	}
	for _, f := range files {
		sum, n, err := w.sum(ctx, f.Key)
		bad := err == nil && (n != f.Size || (f.Declared != nil && *f.Declared != "" && *f.Declared != sum))
		if err != nil && !errors.Is(err, blobstore.ErrNotFound) {
			st.HashFailed++
			w.log().Warn("hash shared file", "path", f.Path, "err", err)
			continue
		}
		if bad || err != nil {
			st.HashFailed++
			w.log().Warn("shared file does not match its upload", "project", f.ProjectID, "path", f.Path, "size", n, "want", f.Size)
			// The key guards against a newer upload of the same path having replaced the row.
			if _, err := store.Exec(ctx, w.DB, `UPDATE at_shared_files SET state = 'failed' WHERE project_id = $1 AND path = $2
				AND storage_key = $3`, f.ProjectID, f.Path, f.Key); err != nil {
				return err
			}
			continue
		}
		if _, err := store.Exec(ctx, w.DB, `UPDATE at_shared_files SET sha256 = $4 WHERE project_id = $1 AND path = $2
			AND storage_key = $3 AND sha256 IS NULL`, f.ProjectID, f.Path, f.Key, sum); err != nil {
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
	pending, err := w.Blobs.ListMultipart(ctx, "projects/", w.now().Add(-Stale))
	if err != nil {
		return err
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
