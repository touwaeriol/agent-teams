package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"gorm.io/gorm/clause"

	"github.com/touwaeriol/agent-teams/server/internal/blobgc"
	"github.com/touwaeriol/agent-teams/server/internal/blobstore"
	"github.com/touwaeriol/agent-teams/server/internal/sharedfiles"
)

// Attachment limits (api.md §3.4): what Claude accepts per image or PDF, a text file's size
// (its text goes into the message), and attachments per message.
const (
	// MaxAttachmentBytes is the largest attachment (#58: any type, uploaded straight to the
	// object store with presigned parts).
	MaxAttachmentBytes = 200 << 20
	// MaxInlineAttachmentBytes is the largest image or PDF of the paths that carry the bytes in
	// the request itself (the old base64 upload, publish_file images).
	MaxInlineAttachmentBytes = 5 << 20
	MaxTextBytes             = 1 << 20
	MaxMessageImages         = 20
	AttachmentURLPrefix      = "/api/attachments/"
	// MaxToolAttachments is how many attachments one tool call (assign_task, send_message …),
	// forward or delivery carries (protocol AttachmentIds and session.send attachments maxItems).
	MaxToolAttachments = 20
)

// Attachment content types besides the images.
const (
	PDFType  = "application/pdf"
	TextType = "text/plain"
)

// ErrNotImage is returned for uploads that are not an image, a PDF or a text file.
var ErrNotImage = errors.New("只支持图片（PNG、JPG、GIF、WebP）、PDF 和文本文件（md、txt、json、csv、代码等）")

// ErrTooLarge is returned for images and PDFs over MaxInlineAttachmentBytes and text files over
// MaxTextBytes on the paths that carry the bytes in the request.
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
	ID        string `json:"id"`
	ProjectID string `json:"-"`
	UserID    string `json:"-"`
	Name      string `json:"name"`
	// ContentType is sniffed or derived from the name (DetectType); any type since #58.
	ContentType string `json:"content_type" doc:"image/png, image/jpeg, image/gif, image/webp, application/pdf and text/plain (a text file whose content goes into the message) are recognised; any other type is a plain file"`
	Size        int    `json:"size"`
	// SHA256 is "" until the blob_hash job has streamed an object uploaded without one (#58).
	SHA256    string    `json:"sha256" gorm:"column:sha256"`
	CreatedAt time.Time `json:"created_at" gorm:"autoCreateTime:false;default:now()"`
	// StorageKey is the object holding the bytes; nil = still in the data column (before #58).
	StorageKey *string `json:"-"`
	// State is uploading, ready, failed or expired (at_attachments.state).
	State string `json:"-" gorm:"default:ready"`
	// ExpiresAt is when the attachment is deleted unless used again (TouchAttachments).
	ExpiresAt time.Time `json:"-"`
}

// DefaultAttachmentTTL is how long an attachment is kept after its last use (user decision #58;
// AGENT_TEAMS_ATTACHMENT_TTL overrides it).
const DefaultAttachmentTTL = 24 * time.Hour

// ErrAttachmentExpired is an attachment whose retention ran out (state expired): its bytes are
// gone, messages show it as expired, the API answers 410.
var ErrAttachmentExpired = errors.New("附件已过期（超过保留期没有再使用，文件已删除）")

func ttlOr(ttl time.Duration) time.Duration {
	if ttl <= 0 {
		return DefaultAttachmentTTL
	}
	return ttl
}

// TouchAttachments pushes the retention of a project's attachments to now()+ttl (never earlier
// than it already is). Every use calls it: sending, forwarding, assigning, opening a thread on a
// message, node and page downloads. Expired and unknown ids are left alone (callers check with
// UseAttachments or OpenAttachment first).
func TouchAttachments(ctx context.Context, q DB, projectID string, ids []string, ttl time.Duration) error {
	if len(ids) == 0 {
		return nil
	}
	if projectID == "" { // attachments uploaded without a project (ids already checked by the caller)
		return q.Gorm(ctx).Exec(`UPDATE at_attachments SET expires_at = GREATEST(expires_at, now() + make_interval(secs => ?))
			WHERE project_id IS NULL AND id IN ? AND state IN ('ready', 'failed', 'uploading')`, ttlOr(ttl).Seconds(), ids).Error
	}
	return q.Gorm(ctx).Exec(`UPDATE at_attachments SET expires_at = GREATEST(expires_at, now() + make_interval(secs => ?))
		WHERE project_id = ? AND id IN ? AND state IN ('ready', 'failed', 'uploading')`, ttlOr(ttl).Seconds(), projectID, ids).Error
}

// ExpiredAttachment returns the name of the first expired attachment among ids ("" = none).
func ExpiredAttachment(ctx context.Context, q DB, projectID string, ids []string) (string, error) {
	if len(ids) == 0 {
		return "", nil
	}
	var names []string
	err := q.Gorm(ctx).Table("at_attachments").Where("project_id = ? AND id IN ? AND state = 'expired'", projectID, ids).
		Order("name").Limit(1).Pluck("name", &names).Error
	if err != nil || len(names) == 0 {
		return "", err
	}
	return names[0], nil
}

// UseAttachments loads a project's attachments for a new use (ProjectAttachments), refuses
// expired ones (ErrAttachmentExpired) and ones not ready (ErrNotFound), and pushes the retention
// of the rest.
func UseAttachments(ctx context.Context, q DB, projectID string, ids []string, ttl time.Duration) ([]Attachment, error) {
	list, err := ProjectAttachments(ctx, q, projectID, ids)
	if err != nil {
		return nil, err
	}
	for _, a := range list {
		switch a.State {
		case "expired":
			return nil, fmt.Errorf("%w：%s", ErrAttachmentExpired, a.Name)
		case "uploading", "failed":
			return nil, ErrNotFound
		}
	}
	return list, TouchAttachments(ctx, q, projectID, ids, ttl)
}

// TableName maps Attachment to at_attachments.
func (Attachment) TableName() string { return "at_attachments" }

// AttachmentRef is what a message's meta.attachments keeps of an image.
type AttachmentRef struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	ContentType string `json:"content_type"`
	Size        int    `json:"size"`
	// SHA256 is the hex digest when known (the daemon checks its download against it).
	SHA256 string `json:"sha256,omitempty"`
	// Expired is set on reads (never stored) once the attachment's retention ran out or its row
	// is gone: the page shows it as expired without asking (MarkExpiredRefs, MarkExpiredMessages).
	Expired bool `json:"expired,omitempty"`
}

// goneAttachments returns which of ids are expired or no longer exist.
func goneAttachments(ctx context.Context, q DB, ids []string) (map[string]bool, error) {
	gone := map[string]bool{}
	if len(ids) == 0 {
		return gone, nil
	}
	var live []struct{ ID string }
	if err := q.Gorm(ctx).Table("at_attachments").Select("id").Where("id IN ? AND state <> 'expired'", ids).
		Find(&live).Error; err != nil {
		return nil, err
	}
	alive := make(map[string]bool, len(live))
	for _, r := range live {
		alive[r.ID] = true
	}
	for _, id := range ids {
		if !alive[id] {
			gone[id] = true
		}
	}
	return gone, nil
}

// MarkExpiredRefs sets Expired on the refs (in place) whose attachment expired or was deleted.
func MarkExpiredRefs(ctx context.Context, q DB, lists ...[]AttachmentRef) error {
	var ids []string
	for _, l := range lists {
		for _, r := range l {
			ids = append(ids, r.ID)
		}
	}
	gone, err := goneAttachments(ctx, q, ids)
	if err != nil {
		return err
	}
	for _, l := range lists {
		for i := range l {
			l[i].Expired = gone[l[i].ID]
		}
	}
	return nil
}

// MarkExpiredMessages adds "expired": true to the meta.attachments items (in place: each touched
// meta is copied first) whose attachment expired or was deleted.
func MarkExpiredMessages(ctx context.Context, q DB, views []MessageView) error {
	var ids []string
	for _, v := range views {
		for _, it := range metaAttachments(v.Meta) {
			if id, _ := it["id"].(string); id != "" {
				ids = append(ids, id)
			}
		}
	}
	if len(ids) == 0 {
		return nil
	}
	gone, err := goneAttachments(ctx, q, ids)
	if err != nil || len(gone) == 0 {
		return err
	}
	for i, v := range views {
		items := metaAttachments(v.Meta)
		hit := false
		for _, it := range items {
			if id, _ := it["id"].(string); gone[id] {
				hit = true
			}
		}
		if !hit {
			continue
		}
		meta := make(map[string]any, len(v.Meta))
		for k, x := range v.Meta {
			meta[k] = x
		}
		out := make([]any, len(items))
		for j, it := range items {
			c := make(map[string]any, len(it)+1)
			for k, x := range it {
				c[k] = x
			}
			if id, _ := it["id"].(string); gone[id] {
				c["expired"] = true
			}
			out[j] = c
		}
		meta["attachments"] = out
		views[i].Meta = meta
	}
	return nil
}

// metaAttachments are a message meta's attachments as JSON objects (meta read from the
// database: []any of map[string]any; nil when there are none or they have another form).
func metaAttachments(meta map[string]any) []map[string]any {
	list, ok := meta["attachments"].([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(list))
	for _, x := range list {
		if m, ok := x.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// Ref is the message-meta form of an attachment.
func (a Attachment) Ref() AttachmentRef {
	return AttachmentRef{ID: a.ID, Name: a.Name, ContentType: a.ContentType, Size: a.Size, SHA256: a.SHA256}
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

// DetectType is the content type of an attachment uploaded to the object store (#58 design §3.3):
// an image or a PDF sniffed from its first bytes wins; a text file (accepted name, UTF-8 without
// NUL in head, at most MaxTextBytes) is TextType; otherwise the type of the name's extension or
// application/octet-stream. What the browser declared is never used.
func DetectType(name string, head []byte, size int64) string {
	if typ := SniffImage(head); typ != "" {
		return typ
	}
	if bytes.HasPrefix(head, []byte("%PDF-")) {
		return PDFType
	}
	if size <= MaxTextBytes && IsTextName(name) && bytes.IndexByte(head, 0) < 0 && validPrefix(head, size) {
		return TextType
	}
	// The shared files' extension table (fixed in code, not the host's mime.types).
	return sharedfiles.ContentType(name)
}

// validPrefix: head is UTF-8, allowing a rune cut at its end when the file goes on.
func validPrefix(head []byte, size int64) bool {
	if int64(len(head)) < size {
		for i := 0; i < utf8.UTFMax && len(head) > 0 && !utf8.Valid(head); i++ {
			head = head[:len(head)-1]
		}
	}
	return utf8.Valid(head)
}

// SafeInline are the types a page may show in place (img, PDF viewer); every other type is
// downloaded as application/octet-stream (#58 design §10).
var SafeInline = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true, PDFType: true}

// NewAttachment checks an image, PDF or text file and returns the row to store.
func NewAttachment(projectID, userID, name string, data []byte) (Attachment, error) {
	typ := SniffAttachment(name, data)
	limit := MaxInlineAttachmentBytes
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

// InsertAttachment stores an attachment, kept for ttl (0 = DefaultAttachmentTTL) unless used
// again: the bytes go to the object store (key
// projects/<pid>/attachments/<id>), the row keeps the key. With no store configured it fails
// with blobstore.ErrNotConfigured. The object is written first; if the insert fails it is
// deleted again (best effort). Callers pass at most MaxInlineAttachmentBytes; big files use presigned
// uploads instead.
func InsertAttachment(ctx context.Context, q DB, bs blobstore.Store, ttl time.Duration, a Attachment, data []byte) (Attachment, error) {
	bs = blobstore.Or(bs)
	if !bs.Configured() {
		return Attachment{}, blobstore.ErrNotConfigured
	}
	key := blobstore.AttachmentKey(a.ProjectID, a.ID)
	// Reserved outside the caller's transaction: if it rolls back, blobgc removes the object.
	if err := blobgc.Reserve(ctx, q, key); err != nil {
		return Attachment{}, err
	}
	if err := bs.Put(ctx, key, bytes.NewReader(data), int64(len(data)), a.ContentType); err != nil {
		return Attachment{}, err
	}
	a.StorageKey, a.State, a.ExpiresAt = &key, "ready", time.Now().Add(ttlOr(ttl))
	err := q.Gorm(ctx).Table("at_attachments").Clauses(clause.Returning{Columns: []clause.Column{{Name: "created_at"}}}).
		Create(&a).Error
	if err != nil {
		_ = bs.Delete(context.WithoutCancel(ctx), key)
	}
	return a, err
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

// textReadTimeout bounds AttachmentTexts' object-store reads as a whole: its callers hold a
// transaction (often the project lock), so a slow store must not hold it longer (#62 M3).
var textReadTimeout = 3 * time.Second // a variable for tests

// AttachmentTexts returns the bytes of a project's text attachments among ids, by id: from the
// data column of older rows, or from the object store (a ranged read of at most MaxTextBytes).
// The object reads run concurrently within textReadTimeout; one that fails or runs out of time is
// left out (logged): the caller just does not inline it. Only the database query returns an error.
func AttachmentTexts(ctx context.Context, q DB, bs blobstore.Store, projectID string, ids []string) (map[string][]byte, error) {
	out := map[string][]byte{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := find[struct {
		ID         string
		StorageKey *string
		Data       []byte
	}](q.Gorm(ctx).Table("at_attachments").Select("id, storage_key, data").
		Where("project_id = ? AND id IN ? AND content_type = ? AND state = 'ready'", projectID, ids, TextType))
	if err != nil {
		return out, err
	}
	keys := map[string]string{}
	for _, r := range rows {
		if r.StorageKey == nil {
			out[r.ID] = r.Data
			continue
		}
		keys[r.ID] = *r.StorageKey
	}
	for id, data := range readTexts(ctx, blobstore.Or(bs), keys) {
		out[id] = data
	}
	return out, nil
}

// readTexts reads the objects (id → key) concurrently within textReadTimeout; failures are left out.
func readTexts(ctx context.Context, bs blobstore.Store, keys map[string]string) map[string][]byte {
	out := make(map[string][]byte, len(keys))
	if len(keys) == 0 {
		return out
	}
	ctx, cancel := context.WithTimeout(ctx, textReadTimeout)
	defer cancel()
	var mu sync.Mutex
	var wg sync.WaitGroup
	for id, key := range keys {
		wg.Add(1)
		go func() {
			defer wg.Done()
			data, err := bs.ReadRange(ctx, key, 0, MaxTextBytes)
			if err != nil {
				slog.WarnContext(ctx, "read text attachment: not inlined", "attachment", id, "err", err)
				return
			}
			mu.Lock()
			out[id] = data
			mu.Unlock()
		}()
	}
	wg.Wait()
	return out
}

// OpenAttachment returns a ready attachment and a reader of its bytes if its project belongs to
// the user: the object (streamed) or the data column of older rows. The caller closes it (and
// touches it: a download is a use). Expired attachments are ErrAttachmentExpired.
func OpenAttachment(ctx context.Context, q DB, bs blobstore.Store, userID, id string) (Attachment, io.ReadCloser, error) {
	row, err := take[struct {
		Attachment
		Data []byte
	}](q.Gorm(ctx).Table("at_attachments a").Select("a.id, COALESCE(a.project_id, '') AS project_id, a.user_id, a.name, "+
		"a.content_type, a.size, COALESCE(a.sha256, '') AS sha256, a.created_at, a.storage_key, a.state, a.expires_at, a.data").
		Joins("LEFT JOIN at_projects p ON p.id = a.project_id").
		Where("a.id = ? AND "+ownedBy+" AND a.state IN ('ready', 'expired')", id, userID, userID))
	if err != nil {
		return Attachment{}, nil, err
	}
	if row.State == "expired" {
		return row.Attachment, nil, ErrAttachmentExpired
	}
	if row.StorageKey == nil {
		return row.Attachment, io.NopCloser(bytes.NewReader(row.Data)), nil
	}
	rc, err := blobstore.Or(bs).Open(ctx, *row.StorageKey)
	if errors.Is(err, blobstore.ErrNotFound) {
		err = ErrNotFound
	}
	return row.Attachment, rc, err
}

// ownedBy is the condition (two user id arguments) for an attachment of one of the user's
// projects, or one the user uploaded without a project (at_attachments a LEFT JOIN at_projects p).
const ownedBy = "(p.user_id = ? OR (a.project_id IS NULL AND a.user_id = ?))"

// AttachmentOfUser returns a ready attachment of one of the user's projects without opening it
// (presigned downloads); expired ones are returned with ErrAttachmentExpired, uploading and failed
// ones are ErrNotFound.
func AttachmentOfUser(ctx context.Context, q DB, userID, id string) (Attachment, error) {
	a, err := take[Attachment](q.Gorm(ctx).Table("at_attachments a").Select("a.id, COALESCE(a.project_id, '') AS project_id, a.user_id, "+
		"a.name, a.content_type, a.size, COALESCE(a.sha256, '') AS sha256, a.created_at, a.storage_key, a.state, a.expires_at").
		Joins("LEFT JOIN at_projects p ON p.id = a.project_id").
		Where("a.id = ? AND "+ownedBy+" AND a.state IN ('ready', 'expired')", id, userID, userID))
	if err == nil && a.State == "expired" {
		err = ErrAttachmentExpired
	}
	return a, err
}

// AttachmentData is OpenAttachment read into memory (rows kept in the data column, at most
// MaxInlineAttachmentBytes; objects are served with presigned URLs instead).
func AttachmentData(ctx context.Context, q DB, bs blobstore.Store, userID, id string) (Attachment, []byte, error) {
	a, rc, err := OpenAttachment(ctx, q, bs, userID, id)
	if err != nil {
		return a, nil, err
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, int64(a.Size)+1))
	if err == nil && len(data) != a.Size {
		err = fmt.Errorf("attachment %s has %d bytes, want %d", a.ID, len(data), a.Size)
	}
	return a, data, err
}
