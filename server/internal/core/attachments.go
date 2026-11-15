package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"
)

// Image input limits (api.md §3.4): what Claude accepts per image, and per message.
const (
	MaxAttachmentBytes  = 5 << 20
	MaxMessageImages    = 8
	AttachmentURLPrefix = "/api/attachments/"
)

// ErrNotImage is returned for uploads that are not PNG, JPEG, GIF or WebP.
var ErrNotImage = errors.New("只支持 PNG、JPG、GIF、WebP 图片")

// ErrTooLarge is returned for images over MaxAttachmentBytes.
var ErrTooLarge = errors.New("图片超过 5 MB")

// Attachment is a row of at_attachments without its bytes.
type Attachment struct {
	ID          string    `json:"id"`
	ProjectID   string    `json:"-"`
	UserID      string    `json:"-"`
	Name        string    `json:"name"`
	ContentType string    `json:"content_type" enum:"image/png,image/jpeg,image/gif,image/webp"`
	Size        int       `json:"size"`
	SHA256      string    `json:"sha256"`
	CreatedAt   time.Time `json:"created_at"`
}

// AttachmentRef is what a message's meta.attachments keeps of an image.
type AttachmentRef struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	ContentType string `json:"content_type"`
	Size        int    `json:"size"`
}

// Ref is the message-meta form of an attachment.
func (a Attachment) Ref() AttachmentRef {
	return AttachmentRef{ID: a.ID, Name: a.Name, ContentType: a.ContentType, Size: a.Size}
}

// imageMagic are the leading bytes of each accepted format.
var imageMagic = []struct {
	prefix []byte
	typ    string
}{
	{[]byte("\x89PNG\r\n\x1a\n"), "image/png"},
	{[]byte("\xff\xd8\xff"), "image/jpeg"},
	{[]byte("GIF87a"), "image/gif"},
	{[]byte("GIF89a"), "image/gif"},
}

// SniffImage returns the image type from the file's own bytes ("" = not an accepted image).
func SniffImage(data []byte) string {
	for _, m := range imageMagic {
		if bytes.HasPrefix(data, m.prefix) {
			return m.typ
		}
	}
	if len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP" {
		return "image/webp"
	}
	return ""
}

// NewAttachment checks an image and returns the row to store.
func NewAttachment(projectID, userID, name string, data []byte) (Attachment, error) {
	typ := SniffImage(data)
	switch {
	case typ == "":
		return Attachment{}, ErrNotImage
	case len(data) > MaxAttachmentBytes:
		return Attachment{}, ErrTooLarge
	}
	sum := sha256.Sum256(data)
	return Attachment{ID: NewID("img"), ProjectID: projectID, UserID: userID, Name: name, ContentType: typ,
		Size: len(data), SHA256: hex.EncodeToString(sum[:])}, nil
}

// InsertAttachment stores an image.
func InsertAttachment(ctx context.Context, q DB, a Attachment, data []byte) (Attachment, error) {
	err := q.QueryRow(ctx, `INSERT INTO at_attachments (id, project_id, user_id, name, content_type, size, sha256, data)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING created_at`, a.ID, a.ProjectID, a.UserID, a.Name, a.ContentType, a.Size,
		a.SHA256, data).Scan(&a.CreatedAt)
	return a, err
}

const attachmentCols = `id, project_id, user_id, name, content_type, size, sha256, created_at`

func scanAttachment(r Scanner) (Attachment, error) {
	var a Attachment
	err := r.Scan(&a.ID, &a.ProjectID, &a.UserID, &a.Name, &a.ContentType, &a.Size, &a.SHA256, &a.CreatedAt)
	return a, MapErr(err)
}

// ProjectAttachments loads the given images of a project, in the order asked; unknown ids are ErrNotFound.
func ProjectAttachments(ctx context.Context, q DB, projectID string, ids []string) ([]Attachment, error) {
	out := make([]Attachment, 0, len(ids))
	for _, id := range ids {
		a, err := scanAttachment(q.QueryRow(ctx, `SELECT `+attachmentCols+` FROM at_attachments WHERE id = $1 AND project_id = $2`,
			id, projectID))
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

// AttachmentData returns an image and its bytes if its project belongs to the user.
func AttachmentData(ctx context.Context, q DB, userID, id string) (Attachment, []byte, error) {
	var data []byte
	a, err := scanAttachment(scanWith(q.QueryRow(ctx, `SELECT a.id, a.project_id, a.user_id, a.name, a.content_type, a.size,
		a.sha256, a.created_at, a.data FROM at_attachments a JOIN at_projects p ON p.id = a.project_id
		WHERE a.id = $1 AND p.user_id = $2`, id, userID), &data))
	return a, data, err
}

// scanWith appends extra destinations to a row scan.
type extraScan struct {
	r     Scanner
	extra []any
}

func scanWith(r Scanner, extra ...any) Scanner { return extraScan{r: r, extra: extra} }

func (s extraScan) Scan(dest ...any) error { return s.r.Scan(append(dest, s.extra...)...) }
