package sharedfiles

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/touwaeriol/agent-teams/server/internal/blobgc"
	"github.com/touwaeriol/agent-teams/server/internal/blobstore"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// Uploads in flight (#58 design §3.1, §4.3): a browser's presigned multipart upload or a node's
// single presigned PUT goes to a new object; at_shared_uploads keeps it until FinalizeUpload puts
// it at its path, so readers see the old file until then. Uploads in flight count towards the
// project's limits with their declared size.

var (
	// ErrUploadNotFound is an upload id the project does not have (finished, aborted, expired).
	// It wraps ErrNotFound.
	ErrUploadNotFound = fmt.Errorf("%w：没有这个上传（已完成、已取消或超过 24 小时已清理），请重新上传", ErrNotFound)
	// ErrIncomplete is an upload whose bytes are not (all) in the object store yet.
	ErrIncomplete = errors.New("文件还没有传完：有分片没传上去，传完再完成上传")
	// ErrSizeMismatch is an upload whose object does not have the declared size: it is dropped.
	ErrSizeMismatch = errors.New("上传的文件大小和声明的不一致，已丢弃，请重新上传")
	// ErrChecksum is an upload whose object does not have the declared sha256: it is dropped.
	ErrChecksum = errors.New("上传的文件 sha256 和声明的不一致（上传时文件可能被改过），已丢弃，请重新上传")
)

// UploadTTL is how long a node's presigned PUT is valid (design §10).
const UploadTTL = 30 * time.Minute

// Upload is a row of at_shared_uploads.
type Upload struct {
	ID               string
	ProjectID        string
	Path             string
	ObjectID         string
	StorageKey       string
	S3UploadID       *string `gorm:"column:s3_upload_id"`
	Size             int64
	ContentType      string
	DeclaredSHA256   *string `gorm:"column:declared_sha256"`
	Overwrite        bool
	UploadedBy       string
	UploaderID       *string
	UploaderThreadID *string
	CreatedAt        time.Time `gorm:"autoCreateTime:false;default:now()"`
}

// TableName maps Upload to at_shared_uploads.
func (Upload) TableName() string { return "at_shared_uploads" }

// NewUploadID is the id of a new upload (up_ + ULID).
func NewUploadID() string { return "up_" + strings.ToLower(ulid.Make().String()) }

// BeginRequest is what an upload declares up front.
type BeginRequest struct {
	Path string
	Size int64
	// SHA256 is what the uploader says the bytes hash to ("" = unknown: blobgc computes it).
	SHA256 string
	// Overwrite allows replacing an existing file at Path (else ErrExists).
	Overwrite bool
	// Multipart opens an S3 multipart upload (browsers); false = one presigned PUT (nodes).
	Multipart bool
	By        Uploader
}

// usageWithPending is the project's usage plus its uploads in flight (by declared size), leaving
// out the upload except (its own row when it completes).
func usageWithPending(ctx context.Context, q store.DB, projectID, except string) (Usage, error) {
	u, err := ProjectUsage(ctx, q, projectID)
	if err != nil {
		return u, err
	}
	var p Usage
	err = store.QueryRow(ctx, q, `SELECT count(*), COALESCE(sum(size), 0) FROM at_shared_uploads WHERE project_id = $1 AND id <> $2`,
		projectID, except).Scan(&p.Files, &p.Bytes)
	return Usage{Files: u.Files + p.Files, Bytes: u.Bytes + p.Bytes}, err
}

// fits checks the limits for size bytes at p, the file it would replace counted out.
func fits(ctx context.Context, q store.DB, projectID, p string, size int64, except string, overwrite bool) error {
	u, err := usageWithPending(ctx, q, projectID, except)
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

// BeginUpload checks the path and the limits (uploads in flight included), opens the upload in the
// object store and records it. The caller hands out presigned part URLs (PresignParts) or a
// presigned PUT (PresignPut) for it.
func BeginUpload(ctx context.Context, q store.DB, bs blobstore.Store, projectID string, r BeginRequest) (Upload, error) {
	p, err := CleanPath(r.Path)
	if err != nil {
		return Upload{}, err
	}
	if r.Size < 0 {
		return Upload{}, fmt.Errorf("%w：大小不能是负数", ErrInvalidPath)
	}
	if r.Size > MaxFileBytes {
		return Upload{}, Check(Usage{}, r.Size, -1)
	}
	bs = blobstore.Or(bs)
	if !bs.Configured() {
		return Upload{}, blobstore.ErrNotConfigured
	}
	if err := lockProject(ctx, q, projectID); err != nil {
		return Upload{}, err
	}
	if err := fits(ctx, q, projectID, p, r.Size, "", r.Overwrite); err != nil {
		return Upload{}, err
	}
	up := Upload{ID: NewUploadID(), ProjectID: projectID, Path: p, ObjectID: NewObjectID(), Size: r.Size,
		ContentType: ContentType(p), Overwrite: r.Overwrite, UploadedBy: r.By.Name, UploaderThreadID: r.By.ThreadID}
	up.StorageKey = blobstore.SharedKey(projectID, up.ObjectID)
	if r.SHA256 != "" {
		sum := strings.ToLower(r.SHA256)
		up.DeclaredSHA256 = &sum
	}
	if r.By.EmployeeID != "" {
		up.UploaderID = &r.By.EmployeeID
	}
	if r.Multipart {
		// A rollback after this leaves an upload no row knows: blobgc aborts it after a day.
		id, err := bs.CreateMultipart(ctx, up.StorageKey, up.ContentType)
		if err != nil {
			return Upload{}, err
		}
		up.S3UploadID = &id
	}
	err = q.Gorm(ctx).Create(&up).Error
	return up, err
}

// GetUpload loads one of the project's uploads in flight.
func GetUpload(ctx context.Context, q store.DB, projectID, id string) (Upload, error) {
	var out []Upload
	if err := q.Gorm(ctx).Where("id = ? AND project_id = ?", id, projectID).Limit(1).Find(&out).Error; err != nil {
		return Upload{}, err
	}
	if len(out) == 0 {
		return Upload{}, ErrUploadNotFound
	}
	return out[0], nil
}

// FinalizeOptions are how an upload is completed.
type FinalizeOptions struct {
	// Parts are the ETags of a multipart upload (browsers); empty for a single PUT.
	Parts []blobstore.Part
	// Path, when set, replaces the path declared at BeginUpload (write_shared_file: the tool call
	// names it); checked again.
	Path string
	// By, when set, replaces the uploader recorded at BeginUpload.
	By *Uploader
	// Overwrite, when set, replaces the overwrite choice recorded at BeginUpload (a caller that
	// changes Path decides again); else the recorded one holds (ErrExists for an existing path).
	Overwrite *bool
	// EmployeeID, when set, must be the employee that asked for the upload (POST /nodes/uploads
	// records the session's employee); another one gets ErrUploadNotFound.
	EmployeeID string
}

// FinalizeUpload puts a finished upload at its path: it completes the multipart upload, checks the
// object's size against the declared one (and its sha256 when one was declared: a node hashes the
// file before uploading it, so it may have changed in between), then — under the project lock,
// limits checked again — upserts at_shared_files (an overwritten file's old object is queued for
// deletion) and drops the staging row. A size or sha256 mismatch deletes the object and the
// staging row (outside q's transaction, which the caller rolls back on the error) and returns
// ErrSizeMismatch or ErrChecksum. sha256 stays NULL when nothing was declared; blobgc computes it.
//
// The object is read (hashed) before any lock is taken; q may be the caller's transaction. A
// refusal under the lock (ErrExists, ErrQuota) leaves the upload in place: the caller cancels it
// (AbortUpload) after its transaction, so it does not hold the quota.
func FinalizeUpload(ctx context.Context, q store.DB, bs blobstore.Store, projectID, uploadID string, o FinalizeOptions) (SharedFile, bool, error) {
	bs = blobstore.Or(bs)
	if !bs.Configured() {
		return SharedFile{}, false, blobstore.ErrNotConfigured
	}
	up, err := GetUpload(ctx, q, projectID, uploadID)
	if err != nil {
		return SharedFile{}, false, err
	}
	if o.EmployeeID != "" && (up.UploaderID == nil || *up.UploaderID != o.EmployeeID) {
		return SharedFile{}, false, ErrUploadNotFound
	}
	p := up.Path
	if o.Path != "" {
		if p, err = CleanPath(o.Path); err != nil {
			return SharedFile{}, false, err
		}
	}
	if up.S3UploadID != nil {
		if len(o.Parts) == 0 || len(o.Parts) > int(blobstore.PartCount(up.Size)) {
			return SharedFile{}, false, fmt.Errorf("%w（应有 %d 个分片，收到 %d 个）", ErrIncomplete, blobstore.PartCount(up.Size), len(o.Parts))
		}
		if err := bs.CompleteMultipart(ctx, up.StorageKey, *up.S3UploadID, o.Parts); err != nil {
			if _, herr := bs.Head(ctx, up.StorageKey); herr != nil { // not a repeated complete
				return SharedFile{}, false, fmt.Errorf("%w：%v", ErrIncomplete, err)
			}
		}
	}
	obj, err := bs.Head(ctx, up.StorageKey)
	if errors.Is(err, blobstore.ErrNotFound) {
		return SharedFile{}, false, ErrIncomplete
	}
	if err != nil {
		return SharedFile{}, false, err
	}
	if obj.Size != up.Size {
		return SharedFile{}, false, dropUpload(ctx, q, bs, up, ErrSizeMismatch)
	}
	key, objectID := up.StorageKey, up.ObjectID
	if up.S3UploadID == nil {
		// A node's single presigned PUT stays valid for UploadTTL after the file is registered, so
		// it could be replayed to change the bytes behind the checks. The object is copied to a key
		// no URL was ever signed for and checked there; the uploaded key is deleted once the URL
		// has expired (Reserve: ReserveDelay > UploadTTL), so a replay leaves nothing behind.
		// (Recording the ETag instead would need a column, and every reader would have to compare.)
		objectID = NewObjectID()
		key = blobstore.SharedKey(projectID, objectID)
		for _, k := range []string{up.StorageKey, key} {
			if err := blobgc.Reserve(ctx, q, k); err != nil {
				return SharedFile{}, false, err
			}
		}
		if err := bs.Copy(ctx, up.StorageKey, key); err != nil {
			return SharedFile{}, false, err
		}
		if c, err := bs.Head(ctx, key); err != nil || c.Size != up.Size {
			_ = bs.Delete(context.WithoutCancel(ctx), key)
			if err != nil {
				return SharedFile{}, false, err
			}
			return SharedFile{}, false, dropUpload(ctx, q, bs, up, ErrSizeMismatch)
		}
	}
	var sum *string
	if up.DeclaredSHA256 != nil && *up.DeclaredSHA256 != "" {
		got, n, err := hashObject(ctx, bs, key)
		if err != nil {
			return SharedFile{}, false, err
		}
		if n != up.Size || got != *up.DeclaredSHA256 {
			if key != up.StorageKey {
				_ = bs.Delete(context.WithoutCancel(ctx), key)
			}
			return SharedFile{}, false, dropUpload(ctx, q, bs, up, ErrChecksum)
		}
		sum = &got
	}
	if err := lockProject(ctx, q, projectID); errors.Is(err, ErrNotFound) {
		// The project went while the file was uploading: nothing will point at the object.
		_ = blobgc.Enqueue(ctx, store.Detached(q), up.StorageKey, key)
		return SharedFile{}, false, ErrUploadNotFound
	} else if err != nil {
		return SharedFile{}, false, err
	}
	// Locked again: a concurrent finalize or abort of the same upload may have won.
	var still []Upload
	if err := q.Gorm(ctx).Raw(`SELECT * FROM at_shared_uploads WHERE id = ? FOR UPDATE`, up.ID).Scan(&still).Error; err != nil {
		return SharedFile{}, false, err
	}
	if len(still) == 0 {
		return SharedFile{}, false, ErrUploadNotFound
	}
	overwrite := up.Overwrite
	if o.Overwrite != nil {
		overwrite = *o.Overwrite
	}
	if err := fits(ctx, q, projectID, p, up.Size, up.ID, overwrite); err != nil {
		return SharedFile{}, false, err
	}
	by := Uploader{Name: up.UploadedBy, ThreadID: up.UploaderThreadID}
	if up.UploaderID != nil {
		by.EmployeeID = *up.UploaderID
	}
	if o.By != nil {
		by = *o.By
	}
	var empID *string
	if by.EmployeeID != "" {
		empID = &by.EmployeeID
	}
	var prev []struct{ StorageKey *string }
	if err := q.Gorm(ctx).Raw(`SELECT storage_key FROM at_shared_files WHERE project_id = ? AND path = ? FOR UPDATE`, projectID, p).
		Scan(&prev).Error; err != nil {
		return SharedFile{}, false, err
	}
	err = q.Gorm(ctx).Exec(`INSERT INTO at_shared_files (project_id, path, content_type, size, sha256, data, uploaded_by,
		uploader_id, uploader_thread_id, storage_key, object_id, declared_sha256, state) VALUES (?, ?, ?, ?, ?, NULL, ?, ?, ?, ?, ?, ?, 'ready')
		ON CONFLICT (project_id, path) DO UPDATE SET content_type = EXCLUDED.content_type, size = EXCLUDED.size,
		sha256 = EXCLUDED.sha256, data = NULL, uploaded_by = EXCLUDED.uploaded_by, uploader_id = EXCLUDED.uploader_id,
		uploader_thread_id = EXCLUDED.uploader_thread_id, storage_key = EXCLUDED.storage_key, object_id = EXCLUDED.object_id,
		declared_sha256 = EXCLUDED.declared_sha256, state = 'ready', updated_at = now()`,
		projectID, p, ContentType(p), up.Size, sum, by.Name, empID, by.ThreadID, key, objectID, up.DeclaredSHA256).Error
	if err != nil {
		return SharedFile{}, false, err
	}
	if _, err := store.Exec(ctx, q, `DELETE FROM at_shared_uploads WHERE id = $1`, up.ID); err != nil {
		return SharedFile{}, false, err
	}
	if len(prev) > 0 && prev[0].StorageKey != nil && *prev[0].StorageKey != key {
		if err := blobgc.Enqueue(ctx, q, *prev[0].StorageKey); err != nil {
			return SharedFile{}, false, err
		}
	}
	f, err := Stat(ctx, q, projectID, p)
	return f, len(prev) > 0, err
}

// dropUpload removes an upload that failed its checks, outside q's transaction: the object and the
// staging row go, and err is returned.
func dropUpload(ctx context.Context, q store.DB, bs blobstore.Store, up Upload, err error) error {
	ctx = context.WithoutCancel(ctx)
	d := store.Detached(q)
	if _, e := store.Exec(ctx, d, `DELETE FROM at_shared_uploads WHERE id = $1`, up.ID); e != nil {
		return errors.Join(err, e)
	}
	e := bs.Delete(ctx, up.StorageKey)
	if up.S3UploadID == nil {
		// A node's single PUT: its URL may be replayed until it expires; delete the key again later.
		_ = blobgc.RedeleteLater(ctx, d, up.StorageKey)
	} else if e != nil {
		_ = blobgc.Enqueue(ctx, d, up.StorageKey)
	}
	return err
}

// AbortUpload cancels an upload in flight: the staging row goes, the object (if any) is queued for
// deletion and the multipart upload is aborted (a node's single PUT: see RedeleteLater).
func AbortUpload(ctx context.Context, q store.DB, bs blobstore.Store, projectID, id string) error {
	var rows []struct {
		StorageKey string
		S3UploadID *string `gorm:"column:s3_upload_id"`
	}
	if err := q.Gorm(ctx).Raw(`DELETE FROM at_shared_uploads WHERE id = ? AND project_id = ? RETURNING storage_key, s3_upload_id`,
		id, projectID).Scan(&rows).Error; err != nil {
		return err
	}
	if len(rows) == 0 {
		return ErrUploadNotFound
	}
	if rows[0].S3UploadID == nil {
		// A node's single presigned PUT: the object (if it was sent) goes now, and the key once
		// more after the URL has expired, so a replayed PUT leaves nothing behind. Inside a
		// transaction the delete waits for the commit (no network while rows are locked).
		key := rows[0].StorageKey
		if err := blobgc.RedeleteLater(ctx, q, key); err != nil {
			return err
		}
		del := func(ctx context.Context) { _ = blobstore.Or(bs).Delete(context.WithoutCancel(ctx), key) }
		if tx, ok := q.(interface{ AfterCommit(func(context.Context)) }); ok {
			tx.AfterCommit(del)
		} else {
			del(ctx)
		}
		return nil
	}
	if err := blobgc.Enqueue(ctx, q, rows[0].StorageKey); err != nil {
		return err
	}
	if rows[0].S3UploadID != nil {
		// Best effort: an upload left open is aborted by blobgc after a day.
		_ = blobstore.Or(bs).AbortMultipart(ctx, rows[0].StorageKey, *rows[0].S3UploadID)
	}
	return nil
}

func hashObject(ctx context.Context, bs blobstore.Store, key string) (string, int64, error) {
	rc, err := bs.Open(ctx, key)
	if err != nil {
		return "", 0, err
	}
	defer rc.Close()
	h := sha256.New()
	n, err := io.Copy(h, rc)
	return hex.EncodeToString(h.Sum(nil)), n, err
}
