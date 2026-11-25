// Package blobstore keeps the bytes of attachments and shared files in an S3-compatible object
// store (RustFS in production, #58 design §1). The control plane never holds a whole object in
// memory: browsers and node daemons move the bytes with presigned URLs, the server only signs,
// checks sizes, reads small ranges and streams.
//
// Every key starts with projects/<project_id>/ (see AttachmentKey, SharedKey) so deleting a
// project is one DeletePrefix.
package blobstore

import (
	"context"
	"errors"
	"io"
	"time"
)

// ErrNotConfigured is returned by every method of a nil or unconfigured store: no
// AGENT_TEAMS_S3_* settings. New uploads then fail with 503; bytes still in the database
// (rows without a storage key) stay readable.
var ErrNotConfigured = errors.New("文件存储未配置")

// ErrNotFound is a missing object (or multipart upload).
var ErrNotFound = errors.New("object not found")

// Object is what HEAD returns.
type Object struct {
	Key         string
	Size        int64
	ContentType string
	ETag        string
}

// GetOptions override the response headers of a presigned GET (response-content-*), so a
// browser downloads instead of rendering, with the original file name.
type GetOptions struct {
	ContentType        string
	ContentDisposition string
}

// Part is one finished part of a multipart upload.
type Part struct {
	Number int32  `json:"number"`
	ETag   string `json:"etag"`
}

// PendingUpload is an unfinished multipart upload (for cleanup).
type PendingUpload struct {
	Key       string
	UploadID  string
	Initiated time.Time
}

// Store is the object store. Implementations: S3 (aws-sdk-go-v2) and, in tests, the same S3
// client against an in-process fake (blobtest).
type Store interface {
	// Configured is false for the placeholder used when no AGENT_TEAMS_S3_* is set.
	Configured() bool

	// PresignGet is a GET URL for one object, valid for ttl.
	PresignGet(ctx context.Context, key string, ttl time.Duration, o GetOptions) (string, error)
	// PresignPut is a single PUT URL for one object of exactly size bytes, valid for ttl. The
	// returned headers must be sent with the request (they are signed).
	PresignPut(ctx context.Context, key string, ttl time.Duration, size int64, contentType string) (string, map[string]string, error)

	// CreateMultipart starts a multipart upload and returns its id.
	CreateMultipart(ctx context.Context, key, contentType string) (string, error)
	// PresignPart is a PUT URL for part number part (1-based) of the upload, valid for ttl.
	PresignPart(ctx context.Context, key, uploadID string, part int32, ttl time.Duration) (string, error)
	// CompleteMultipart assembles the parts (in number order).
	CompleteMultipart(ctx context.Context, key, uploadID string, parts []Part) error
	// AbortMultipart drops an unfinished upload and its parts (a missing one is not an error).
	AbortMultipart(ctx context.Context, key, uploadID string) error
	// ListMultipart lists unfinished uploads under prefix started before olderThan.
	ListMultipart(ctx context.Context, prefix string, olderThan time.Time) ([]PendingUpload, error)

	// Head returns an object's size and type; ErrNotFound when missing.
	Head(ctx context.Context, key string) (Object, error)
	// ReadRange reads n bytes from off (fewer at the end of the object).
	ReadRange(ctx context.Context, key string, off, n int64) ([]byte, error)
	// Open streams the whole object; the caller closes it.
	Open(ctx context.Context, key string) (io.ReadCloser, error)
	// Put stores size bytes from r (migration, small server-side writes).
	Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	// Copy copies an object inside the bucket (server side: no bytes through the control plane).
	Copy(ctx context.Context, srcKey, dstKey string) error
	// Delete removes objects; missing ones are not an error.
	Delete(ctx context.Context, keys ...string) error
	// DeletePrefix removes every object whose key starts with prefix.
	DeletePrefix(ctx context.Context, prefix string) error
}

// AttachmentKey is where an attachment's bytes live.
func AttachmentKey(projectID, attachmentID string) string {
	return ProjectPrefix(projectID) + "attachments/" + attachmentID
}

// SharedKey is where a shared file's bytes live: by object id, never by path (paths are
// overwritten; a new upload gets a new id and the old object is deleted after commit).
func SharedKey(projectID, objectID string) string {
	return ProjectPrefix(projectID) + "shared/" + objectID
}

// ProjectPrefix is the prefix of every object of a project.
func ProjectPrefix(projectID string) string { return "projects/" + projectID + "/" }

// None is the store used when nothing is configured: every call fails with ErrNotConfigured.
type None struct{}

var _ Store = None{}

func (None) Configured() bool { return false }
func (None) PresignGet(context.Context, string, time.Duration, GetOptions) (string, error) {
	return "", ErrNotConfigured
}
func (None) PresignPut(context.Context, string, time.Duration, int64, string) (string, map[string]string, error) {
	return "", nil, ErrNotConfigured
}
func (None) CreateMultipart(context.Context, string, string) (string, error) {
	return "", ErrNotConfigured
}
func (None) PresignPart(context.Context, string, string, int32, time.Duration) (string, error) {
	return "", ErrNotConfigured
}
func (None) CompleteMultipart(context.Context, string, string, []Part) error { return ErrNotConfigured }
func (None) AbortMultipart(context.Context, string, string) error            { return ErrNotConfigured }
func (None) ListMultipart(context.Context, string, time.Time) ([]PendingUpload, error) {
	return nil, ErrNotConfigured
}
func (None) Head(context.Context, string) (Object, error) { return Object{}, ErrNotConfigured }
func (None) ReadRange(context.Context, string, int64, int64) ([]byte, error) {
	return nil, ErrNotConfigured
}
func (None) Open(context.Context, string) (io.ReadCloser, error) { return nil, ErrNotConfigured }
func (None) Put(context.Context, string, io.Reader, int64, string) error {
	return ErrNotConfigured
}
func (None) Copy(context.Context, string, string) error { return ErrNotConfigured }
func (None) Delete(context.Context, ...string) error    { return ErrNotConfigured }
func (None) DeletePrefix(context.Context, string) error { return ErrNotConfigured }

// Or returns s, or None when s is nil.
func Or(s Store) Store {
	if s == nil {
		return None{}
	}
	return s
}
