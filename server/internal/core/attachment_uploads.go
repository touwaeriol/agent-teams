package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/blobgc"
	"github.com/touwaeriol/agent-teams/server/internal/blobstore"
	"github.com/touwaeriol/agent-teams/server/internal/msg"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// Presigned attachment uploads (#58 design §3.1): the browser sends the parts straight to the
// object store. The row exists from the start as state=uploading (it holds the S3 upload id) and
// becomes ready when CompleteAttachmentUpload has checked the object. Attachments do not count
// towards the project's quota (user decision #58); each is at most MaxAttachmentBytes.

var (
	// ErrUploadIncomplete is an upload whose parts are not all in the object store yet.
	ErrUploadIncomplete = msg.Err("文件还没有传完：有分片没传上去，传完再完成上传")
	// ErrUploadSize is an upload whose object does not have the declared size: it is dropped.
	ErrUploadSize = msg.Err("上传的文件大小和声明的不一致，已丢弃，请重新上传")
	// ErrAttachmentTooLarge is an attachment over MaxAttachmentBytes.
	ErrAttachmentTooLarge = msg.Err("附件太大：单个附件最多 {mb} MB", "mb", MaxAttachmentBytes>>20)
	// ErrTooManyUploads is a new upload over the user's limit on uploads in flight.
	ErrTooManyUploads = msg.Err("同时进行中的上传太多")
)

// Limits on a user's uploads in flight (#62 F2): attachments still uploading plus shared-file
// uploads of the user's projects (browsers and node sessions), by declared size. They bound
// what one user can park in the object store before anything is checked; blobgc drops the
// abandoned ones after a day.
const (
	MaxUploadsInFlight     = 16
	MaxUploadBytesInFlight = 2 << 30
)

// CheckUploadsInFlight refuses (ErrTooManyUploads) a new upload of size bytes that would take the
// user past MaxUploadsInFlight or MaxUploadBytesInFlight. It is a soft limit: two concurrent
// begins may both pass by one.
func CheckUploadsInFlight(ctx context.Context, q DB, userID string, size int64) error {
	var n, total int64
	err := store.QueryRow(ctx, q, `SELECT count(*), COALESCE(sum(size), 0) FROM (
		SELECT size::bigint FROM at_attachments WHERE user_id = $1 AND state = 'uploading'
		UNION ALL
		SELECT u.size FROM at_shared_uploads u JOIN at_projects p ON p.id = u.project_id WHERE p.user_id = $1) x`,
		userID).Scan(&n, &total)
	if err != nil {
		return err
	}
	if n+1 > MaxUploadsInFlight || total+size > MaxUploadBytesInFlight {
		return msg.Wrap(ErrTooManyUploads, "同时进行中的上传太多：最多 {max} 个、共 {max_mb} MB（现在 {n} 个、{mb} MB）；请等已有的上传完成或取消，没传完的上传 24 小时后自动清理",
			"max", MaxUploadsInFlight, "max_mb", MaxUploadBytesInFlight>>20, "n", n, "mb", total>>20)
	}
	return nil
}

// AttachmentOwner scopes an attachment: one of a project (ProjectID set), or one a user uploaded
// before its project existed (ProjectID "", UserID set: POST /api/uploads, #58 D6).
type AttachmentOwner struct {
	ProjectID string
	UserID    string
}

// OwnerOfProject is the owner scope of a project's attachments.
func OwnerOfProject(projectID string) AttachmentOwner { return AttachmentOwner{ProjectID: projectID} }

// where is the SQL condition (and its argument) selecting the owner's rows.
func (o AttachmentOwner) where() (string, string) {
	if o.ProjectID != "" {
		return "project_id = ?", o.ProjectID
	}
	return "project_id IS NULL AND user_id = ?", o.UserID
}

// BeginAttachmentUpload records a new attachment (state=uploading, kept for ttl like any other)
// and opens its multipart upload. Any type is accepted; the type is decided at completion from the
// bytes and the name (DetectType). With projectID "" the attachment has no project yet (key
// users/<uid>/attachments/<id>; AdoptAttachments gives it one).
func BeginAttachmentUpload(ctx context.Context, q DB, bs blobstore.Store, ttl time.Duration, projectID, userID, name string, size int64) (Attachment, error) {
	switch {
	case size > MaxAttachmentBytes:
		return Attachment{}, ErrAttachmentTooLarge
	case size <= 0:
		return Attachment{}, msg.Wrap(ErrUploadSize, "空文件不能作为附件")
	}
	bs = blobstore.Or(bs)
	if !bs.Configured() {
		return Attachment{}, blobstore.ErrNotConfigured
	}
	if err := CheckUploadsInFlight(ctx, q, userID, size); err != nil {
		return Attachment{}, err
	}
	a := Attachment{ID: NewID("img"), ProjectID: projectID, UserID: userID, Name: name,
		ContentType: DetectType(name, nil, size), Size: int(size), State: "uploading", ExpiresAt: time.Now().Add(ttlOr(ttl))}
	key := blobstore.AttachmentKey(projectID, a.ID)
	var pid any = projectID
	if projectID == "" {
		key, pid = blobstore.UserAttachmentKey(userID, a.ID), nil
	}
	// A rollback after this leaves an upload no row knows: blobgc aborts it after a day.
	uploadID, err := bs.CreateMultipart(ctx, key, "application/octet-stream")
	if err != nil {
		return Attachment{}, err
	}
	a.StorageKey = &key
	err = q.Gorm(ctx).Raw(`INSERT INTO at_attachments (id, project_id, user_id, name, content_type, size, storage_key, state,
		upload_id, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?, 'uploading', ?, ?) RETURNING created_at`,
		a.ID, pid, userID, name, a.ContentType, size, key, uploadID, a.ExpiresAt).Row().Scan(&a.CreatedAt)
	return a, err
}

// attachmentUpload is an attachment still uploading, with its S3 upload id.
type attachmentUpload struct {
	Attachment
	UploadID *string
}

// UploadingAttachment loads one of the owner's attachments still uploading.
func UploadingAttachment(ctx context.Context, q DB, owner AttachmentOwner, id string) (Attachment, string, error) {
	cond, arg := owner.where()
	rows, err := find[attachmentUpload](q.Gorm(ctx).Table("at_attachments").Select("id, COALESCE(project_id, '') AS project_id, "+
		"user_id, name, content_type, size, COALESCE(sha256, '') AS sha256, created_at, storage_key, state, expires_at, upload_id").
		Where("id = ? AND state = 'uploading'", id).Where(cond, arg))
	if err != nil {
		return Attachment{}, "", err
	}
	if len(rows) == 0 || rows[0].UploadID == nil || rows[0].StorageKey == nil {
		return Attachment{}, "", ErrNotFound
	}
	return rows[0].Attachment, *rows[0].UploadID, nil
}

// CompleteAttachmentUpload completes the multipart upload, checks the object's size against the
// declared one (a mismatch deletes the object and the row: ErrUploadSize), sniffs the first 512
// bytes for the type and marks the row ready (retention pushed to now()+ttl). If the row went
// meanwhile (the project or thread was deleted while the browser uploaded) the object is deleted.
// sha256 stays NULL; blobgc computes it. q is the pool (each step commits on its own).
func CompleteAttachmentUpload(ctx context.Context, q DB, bs blobstore.Store, ttl time.Duration, owner AttachmentOwner, id string, parts []blobstore.Part) (Attachment, error) {
	bs = blobstore.Or(bs)
	if !bs.Configured() {
		return Attachment{}, blobstore.ErrNotConfigured
	}
	a, uploadID, err := UploadingAttachment(ctx, q, owner, id)
	if err != nil {
		return a, err
	}
	key := *a.StorageKey
	if n := blobstore.PartCount(int64(a.Size)); len(parts) == 0 || len(parts) > int(n) {
		return a, msg.Wrap(ErrUploadIncomplete, "文件还没有传完：有分片没传上去，传完再完成上传（应有 {want} 个分片，收到 {got} 个）", "want", n, "got", len(parts))
	}
	if err := bs.CompleteMultipart(ctx, key, uploadID, parts); err != nil {
		if _, herr := bs.Head(ctx, key); herr != nil { // not a repeated complete
			return a, fmt.Errorf("%w：%v", ErrUploadIncomplete, err)
		}
	}
	obj, err := bs.Head(ctx, key)
	if errors.Is(err, blobstore.ErrNotFound) {
		return a, ErrUploadIncomplete
	}
	if err != nil {
		return a, err
	}
	if obj.Size != int64(a.Size) {
		ctx := context.WithoutCancel(ctx)
		if _, err := store.Exec(ctx, q, `DELETE FROM at_attachments WHERE id = $1 AND state = 'uploading'`, a.ID); err != nil {
			return a, err
		}
		if err := bs.Delete(ctx, key); err != nil {
			_ = blobgc.Enqueue(ctx, q, key)
		}
		return a, ErrUploadSize
	}
	head, err := bs.ReadRange(ctx, key, 0, 512)
	if err != nil {
		return a, err
	}
	a.ContentType = DetectType(a.Name, head, obj.Size)
	var done []Attachment
	err = q.Gorm(ctx).Raw(`UPDATE at_attachments SET state = 'ready', content_type = ?, upload_id = NULL,
		expires_at = GREATEST(expires_at, now() + make_interval(secs => ?))
		WHERE id = ? AND user_id = ? AND state = 'uploading' RETURNING id, COALESCE(project_id, '') AS project_id, user_id, name,
		content_type, size, COALESCE(sha256, '') AS sha256, created_at, storage_key, state, expires_at`,
		a.ContentType, ttlOr(ttl).Seconds(), a.ID, a.UserID).Scan(&done).Error
	if err != nil {
		return a, err
	}
	if len(done) == 0 {
		// The row went while the parts were uploading: nothing points at the object any more.
		ctx := context.WithoutCancel(ctx)
		if err := bs.Delete(ctx, key); err != nil {
			_ = blobgc.Enqueue(ctx, q, key)
		}
		return a, ErrNotFound
	}
	return done[0], nil
}

// AbortAttachmentUpload cancels an attachment still uploading: the row goes, the object (if a
// part landed and completed) is queued for deletion, the multipart upload is aborted.
func AbortAttachmentUpload(ctx context.Context, q DB, bs blobstore.Store, owner AttachmentOwner, id string) error {
	var rows []struct {
		StorageKey *string
		UploadID   *string
	}
	cond, arg := owner.where()
	if err := q.Gorm(ctx).Raw(`DELETE FROM at_attachments WHERE id = ? AND state = 'uploading' AND `+cond+`
		RETURNING storage_key, upload_id`, id, arg).Scan(&rows).Error; err != nil {
		return err
	}
	if len(rows) == 0 || rows[0].StorageKey == nil {
		return ErrNotFound
	}
	if err := blobgc.Enqueue(ctx, q, *rows[0].StorageKey); err != nil {
		return err
	}
	if rows[0].UploadID != nil {
		_ = blobstore.Or(bs).AbortMultipart(ctx, *rows[0].StorageKey, *rows[0].UploadID)
	}
	return nil
}

// IsAttachmentUpload tells an attachment upload id (img_…) from a shared-file one (up_…).
func IsAttachmentUpload(id string) bool { return strings.HasPrefix(id, "img_") }
