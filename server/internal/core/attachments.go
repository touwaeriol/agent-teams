package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"gorm.io/gorm/clause"
)

// Attachment limits (api.md §3.4): what Claude accepts per image or PDF, a text file's size
// (its text goes into the message), and attachments per message.
const (
	MaxAttachmentBytes  = 5 << 20
	MaxTextBytes        = 1 << 20
	MaxMessageImages    = 8
	AttachmentURLPrefix = "/api/attachments/"
)

// Attachment content types besides the images.
const (
	PDFType  = "application/pdf"
	TextType = "text/plain"
)

// ErrNotImage is returned for uploads that are not an image, a PDF or a text file.
var ErrNotImage = errors.New("只支持图片（PNG、JPG、GIF、WebP）、PDF 和文本文件（md、txt、json、csv、代码等）")

// ErrTooLarge is returned for images and PDFs over MaxAttachmentBytes and text files over MaxTextBytes.
var ErrTooLarge = errors.New("文件太大：图片和 PDF 最多 5 MB，文本文件最多 1 MB")

// textExts are the file name extensions accepted as text files (the bytes must also be UTF-8).
var textExts = map[string]bool{}

func init() {
	for _, e := range strings.Fields(`md markdown txt text log json jsonl ndjson csv tsv yaml yml toml ini cfg conf xml html htm css scss
		less js mjs cjs ts tsx jsx vue svelte go py rb rs java kt kts scala swift c h cc cpp hpp cs php sh bash zsh fish ps1 sql
		graphql gql proto lua r dart ex exs erl hs ml clj tf hcl gradle properties rst adoc tex diff patch`) {
		textExts[e] = true
	}
}

// IsTextName reports whether a file name has one of the accepted text extensions
// (or is a well-known extensionless text file such as Makefile or Dockerfile).
func IsTextName(name string) bool {
	base := strings.ToLower(path.Base(strings.ReplaceAll(name, "\\", "/")))
	switch base {
	case "makefile", "dockerfile", "readme", "license", "gemfile", "procfile":
		return true
	}
	ext := strings.TrimPrefix(path.Ext(base), ".")
	return ext != "" && textExts[ext]
}

// sniffText returns TextType for a text file: an accepted name and UTF-8 bytes without NUL.
func sniffText(name string, data []byte) string {
	if !IsTextName(name) || !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return ""
	}
	return TextType
}

// IsInlineText reports whether an attachment's text is put into the message itself.
func IsInlineText(contentType string) bool { return contentType == TextType }

// Attachment is a row of at_attachments without its bytes.
type Attachment struct {
	ID          string    `json:"id"`
	ProjectID   string    `json:"-"`
	UserID      string    `json:"-"`
	Name        string    `json:"name"`
	ContentType string    `json:"content_type" enum:"image/png,image/jpeg,image/gif,image/webp,application/pdf,text/plain"`
	Size        int       `json:"size"`
	SHA256      string    `json:"sha256" gorm:"column:sha256"`
	CreatedAt   time.Time `json:"created_at" gorm:"autoCreateTime:false;default:now()"`
}

// TableName maps Attachment to at_attachments.
func (Attachment) TableName() string { return "at_attachments" }

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

// SniffAttachment returns the type of an accepted attachment ("" = refused): an image or a
// PDF from its own bytes, else a text file by name and content.
func SniffAttachment(name string, data []byte) string {
	if typ := SniffImage(data); typ != "" {
		return typ
	}
	if bytes.HasPrefix(data, []byte("%PDF-")) {
		return PDFType
	}
	return sniffText(name, data)
}

// NewAttachment checks an image, PDF or text file and returns the row to store.
func NewAttachment(projectID, userID, name string, data []byte) (Attachment, error) {
	typ := SniffAttachment(name, data)
	limit := MaxAttachmentBytes
	if typ == TextType {
		limit = MaxTextBytes
	}
	switch {
	case typ == "":
		return Attachment{}, ErrNotImage
	case len(data) > limit:
		return Attachment{}, ErrTooLarge
	}
	sum := sha256.Sum256(data)
	return Attachment{ID: NewID("img"), ProjectID: projectID, UserID: userID, Name: name, ContentType: typ,
		Size: len(data), SHA256: hex.EncodeToString(sum[:])}, nil
}

// InsertAttachment stores an image.
func InsertAttachment(ctx context.Context, q DB, a Attachment, data []byte) (Attachment, error) {
	row := struct {
		Attachment
		Data []byte
	}{a, data}
	err := q.Gorm(ctx).Table("at_attachments").Clauses(clause.Returning{Columns: []clause.Column{{Name: "created_at"}}}).
		Create(&row).Error
	return row.Attachment, err
}

// ProjectAttachments loads the given images of a project, in the order asked; unknown ids are ErrNotFound.
func ProjectAttachments(ctx context.Context, q DB, projectID string, ids []string) ([]Attachment, error) {
	if len(ids) == 0 {
		return []Attachment{}, nil
	}
	found, err := find[Attachment](q.Gorm(ctx).Omit("data").Where("project_id = ? AND id IN ?", projectID, ids))
	if err != nil {
		return nil, err
	}
	byID := make(map[string]Attachment, len(found))
	for _, a := range found {
		byID[a.ID] = a
	}
	out := make([]Attachment, 0, len(ids))
	for _, id := range ids {
		a, ok := byID[id]
		if !ok {
			return nil, ErrNotFound
		}
		out = append(out, a)
	}
	return out, nil
}

// AttachmentTexts returns the bytes of a project's text attachments among ids, by id.
func AttachmentTexts(ctx context.Context, q DB, projectID string, ids []string) (map[string][]byte, error) {
	out := map[string][]byte{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := find[struct {
		ID   string
		Data []byte
	}](q.Gorm(ctx).Table("at_attachments").Select("id, data").
		Where("project_id = ? AND id IN ? AND content_type = ?", projectID, ids, TextType))
	for _, r := range rows {
		out[r.ID] = r.Data
	}
	return out, err
}

// AttachmentData returns an image and its bytes if its project belongs to the user.
func AttachmentData(ctx context.Context, q DB, userID, id string) (Attachment, []byte, error) {
	row, err := take[struct {
		Attachment
		Data []byte
	}](q.Gorm(ctx).Table("at_attachments a").Select("a.*").Joins("JOIN at_projects p ON p.id = a.project_id").
		Where("a.id = ? AND p.user_id = ?", id, userID))
	return row.Attachment, row.Data, err
}
