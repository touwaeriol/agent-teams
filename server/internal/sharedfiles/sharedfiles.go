// Package sharedfiles stores a project's shared files (讨论串 #33, architecture.md §6.3, protocol.md §6.5):
// files every session (coordinator, leads, members of all threads) and every user of the project can
// list, fetch, overwrite and delete. The rows live in PG (at_shared_files), never in git; since #58
// the bytes live in the object store (blobstore, key projects/<pid>/shared/<object_id>) and rows
// from before keep them in the data column until `atctl storage migrate`. A path is overwritten in
// place (no versions): a new upload gets a new object id and the old object is queued for deletion. Paths and limits are checked here, whoever sent the file
// (daemon or browser) — callers must not trust their own checks.
package sharedfiles

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/oklog/ulid/v2"

	"github.com/touwaeriol/agent-teams/server/internal/artifacts"
	"github.com/touwaeriol/agent-teams/server/internal/blobgc"
	"github.com/touwaeriol/agent-teams/server/internal/blobstore"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// Limits of a project's shared files.
const (
	MaxFileBytes    = 20 << 20  // one file
	MaxProjectBytes = 500 << 20 // all files of a project
	MaxProjectFiles = 1000      // files of a project
	MaxDepth        = 8         // path segments
	MaxPathBytes    = 255       // path length in bytes
)

// UserName is uploaded_by for files the user uploaded on the web page (the convention of deliverables
// and memory files the user wrote).
const UserName = "user"

var (
	// ErrInvalidPath is a path that is not a clean relative shared-files path.
	ErrInvalidPath = errors.New("invalid shared file path")
	// ErrFileTooLarge is a single file over MaxFileBytes.
	ErrFileTooLarge = errors.New("shared file too large")
	// ErrQuota is an upload that would take the project over MaxProjectBytes or MaxProjectFiles.
	ErrQuota = errors.New("shared files quota exceeded")
	// ErrNotFound is a path the project has no file at.
	ErrNotFound = errors.New("shared file not found")
	// ErrFailed is a file whose stored bytes did not match its upload (state=failed): it is listed
	// but cannot be read; upload it again.
	ErrFailed = errors.New("shared file upload failed verification")
)

// CleanPath checks a shared-files path and returns it normalised (a leading "./" removed). It
// refuses absolute paths, '..' and '.' segments, empty segments ("a//b", a trailing '/'), segments
// starting with '.' or starting or ending with a space, backslashes, control characters (C0, DEL,
// C1), U+2028/U+2029, invalid UTF-8, more than MaxDepth segments and more than MaxPathBytes bytes
// of UTF-8 — the SharedPath rule of protocol/schemas/tools/common.json (whose maxLength can only
// count characters; this byte check is the authoritative one).
func CleanPath(p string) (string, error) {
	for strings.HasPrefix(p, "./") {
		p = p[2:]
	}
	bad := func(why string) (string, error) { return "", fmt.Errorf("%w：%q %s", ErrInvalidPath, p, why) }
	switch {
	case p == "":
		return bad("是空的")
	case len(p) > MaxPathBytes:
		return bad(fmt.Sprintf("超过 %d 字节（UTF-8）", MaxPathBytes))
	case !utf8.ValidString(p):
		return bad("不是合法的 UTF-8")
	case strings.HasPrefix(p, "/"):
		return bad("不能是绝对路径")
	case strings.Contains(p, `\`):
		return bad("不能有反斜杠")
	}
	for _, r := range p {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) || r == 0x2028 || r == 0x2029 {
			return bad("不能有控制字符或行分隔符")
		}
	}
	segs := strings.Split(p, "/")
	if len(segs) > MaxDepth {
		return bad(fmt.Sprintf("超过 %d 层", MaxDepth))
	}
	for _, s := range segs {
		switch {
		case s == "":
			return bad("有空的路径段")
		case s == "." || s == "..":
			return bad("不能有 . 或 .. 段")
		case strings.HasPrefix(s, "."):
			return bad("路径段不能以 . 开头")
		case strings.HasPrefix(s, " ") || strings.HasSuffix(s, " "):
			return bad("路径段不能以空格开头或结尾")
		}
	}
	return p, nil
}

// Join puts name under the folder dir (both checked; a trailing "/" of dir is fine); dir "" leaves name as it is.
func Join(dir, name string) (string, error) {
	dir = strings.TrimRight(dir, "/")
	if dir == "" {
		return CleanPath(name)
	}
	if _, err := CleanPath(dir); err != nil {
		return "", err
	}
	return CleanPath(dir + "/" + name)
}

// extraTypes are content types of common data files the deliverable table does not list.
var extraTypes = map[string]string{
	".xml": "application/xml", ".yaml": "application/yaml", ".yml": "application/yaml",
	".zip": "application/zip", ".gz": "application/gzip", ".tar": "application/x-tar", ".tgz": "application/gzip",
	".log": "text/plain; charset=utf-8", ".tsv": "text/tab-separated-values; charset=utf-8",
	".sql": "text/plain; charset=utf-8", ".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	".pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	".mp3":  "audio/mpeg", ".wav": "audio/wav", ".mp4": "video/mp4", ".webm": "video/webm",
}

// ContentType is the type a path is listed and downloaded with, from its extension only; unknown
// extensions are application/octet-stream. Any type may be stored.
func ContentType(p string) string {
	if ct := artifacts.ContentType(p); ct != "" {
		return ct
	}
	if ct := extraTypes[strings.ToLower(path.Ext(p))]; ct != "" {
		return ct
	}
	return "application/octet-stream"
}

// SharedFile is one shared file without its bytes.
type SharedFile struct {
	Path        string `json:"path"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
	// SHA256 is "" until the blob_hash job has streamed an object uploaded without one (#58):
	// readers then check the size only.
	SHA256           string    `json:"sha256" gorm:"column:sha256"`
	UploadedBy       string    `json:"uploaded_by" doc:"Employee name (dev-1) or user"`
	UploaderID       *string   `json:"-"`
	UploaderThreadNo *int      `json:"uploaded_by_thread_no,omitempty" doc:"Thread the employee uploaded it from (shown as dev-1 #31)"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
	// StorageKey is the object holding the bytes; nil = still in the data column (before #58).
	StorageKey *string `json:"-"`
	// State is ready or failed (see ErrFailed).
	State string `json:"-"`
}

// Label is how the uploader is shown: "dev-1 #31", "dev-1" or "user".
func (f SharedFile) Label() string {
	if f.UploaderThreadNo != nil {
		return fmt.Sprintf("%s #%d", f.UploadedBy, *f.UploaderThreadNo)
	}
	return f.UploadedBy
}

// Uploader is who stores a file: an employee (name, id, the thread its session works in) or the user.
type Uploader struct {
	Name       string
	EmployeeID string
	ThreadID   *string
}

// Usage is a project's shared-files total.
type Usage struct {
	Files int   `json:"files"`
	Bytes int64 `json:"total_bytes"`
}

const listColumns = `f.path, f.content_type, f.size, COALESCE(f.sha256, '') AS sha256, f.uploaded_by, f.uploader_id,
	t.no AS uploader_thread_no, f.created_at, f.updated_at, f.storage_key, f.state`

// List returns the project's files by path; prefix (optional) keeps paths starting with it.
func List(ctx context.Context, q store.DB, projectID, prefix string) ([]SharedFile, error) {
	out := []SharedFile{}
	db := q.Gorm(ctx).Table("at_shared_files f").Select(listColumns).
		Joins("LEFT JOIN at_threads t ON t.id = f.uploader_thread_id").Where("f.project_id = ?", projectID)
	if prefix = strings.TrimSpace(prefix); prefix != "" {
		db = db.Where("starts_with(f.path, ?)", prefix)
	}
	err := db.Order("f.path COLLATE \"C\"").Scan(&out).Error
	return out, err
}

// Stat returns one file without its bytes.
func Stat(ctx context.Context, q store.DB, projectID, p string) (SharedFile, error) {
	var out []SharedFile
	err := q.Gorm(ctx).Table("at_shared_files f").Select(listColumns).
		Joins("LEFT JOIN at_threads t ON t.id = f.uploader_thread_id").
		Where("f.project_id = ? AND f.path = ?", projectID, p).Limit(1).Scan(&out).Error
	if err != nil {
		return SharedFile{}, err
	}
	if len(out) == 0 {
		return SharedFile{Path: p}, ErrNotFound
	}
	return out[0], nil
}

// Load returns one file with its bytes: from the object store, or from the data column for rows
// from before #58. It is for files up to MaxFileBytes (the tools and pages that still carry bytes);
// big files are fetched with presigned URLs instead.
func Load(ctx context.Context, q store.DB, bs blobstore.Store, projectID, p string) (SharedFile, []byte, error) {
	f, err := Stat(ctx, q, projectID, p)
	if err != nil {
		return f, nil, err
	}
	if f.State == "failed" {
		return f, nil, ErrFailed
	}
	if f.StorageKey != nil {
		data, err := readObject(ctx, bs, *f.StorageKey, f.Size)
		return f, data, err
	}
	var data []byte
	err = q.Gorm(ctx).Raw(`SELECT data FROM at_shared_files WHERE project_id = ? AND path = ?`, projectID, p).Row().Scan(&data)
	if store.IsNoRows(err) {
		return f, nil, ErrNotFound
	}
	return f, data, err
}

// readObject reads a whole (small) object and checks its size.
func readObject(ctx context.Context, bs blobstore.Store, key string, size int64) ([]byte, error) {
	if size > MaxFileBytes {
		return nil, fmt.Errorf("%w：%d MB 以上的文件要用预签名地址下载", ErrFileTooLarge, MaxFileBytes>>20)
	}
	rc, err := blobstore.Or(bs).Open(ctx, key)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, size+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != size {
		return nil, fmt.Errorf("shared file object %s has %d bytes, want %d", key, len(data), size)
	}
	return data, nil
}

// NewObjectID is the id of a new shared-file object (sf_ + ULID).
func NewObjectID() string { return "sf_" + strings.ToLower(ulid.Make().String()) }

// ProjectUsage counts the project's files and bytes.
func ProjectUsage(ctx context.Context, q store.DB, projectID string) (Usage, error) {
	var u Usage
	err := q.Gorm(ctx).Table("at_shared_files").Select("count(*) AS files, COALESCE(sum(size), 0) AS bytes").
		Where("project_id = ?", projectID).Scan(&u).Error
	return u, err
}

// Check tells whether storing size bytes at path fits the limits, given the project's usage and the
// size of the file it replaces (oldSize, -1 when the path is new): an overwrite counts the difference.
func Check(u Usage, size, oldSize int64) error {
	if size > MaxFileBytes {
		return fmt.Errorf("%w：单个文件不能超过 %d MB（这个文件 %.1f MB）", ErrFileTooLarge, MaxFileBytes>>20, float64(size)/(1<<20))
	}
	files, bytes := u.Files, u.Bytes+size
	if oldSize >= 0 {
		bytes -= oldSize
	} else {
		files++
	}
	if files > MaxProjectFiles {
		return fmt.Errorf("%w：项目最多 %d 个共享文件，先删掉不用的", ErrQuota, MaxProjectFiles)
	}
	if bytes > MaxProjectBytes {
		return fmt.Errorf("%w：项目共享文件合计不能超过 %d MB（现在 %.1f MB），先删掉不用的", ErrQuota, MaxProjectBytes>>20,
			float64(u.Bytes)/(1<<20))
	}
	return nil
}

// Sum is the hex sha256 of data.
func Sum(data []byte) string {
	s := sha256.Sum256(data)
	return hex.EncodeToString(s[:])
}

// Put stores data at path (inserted, or overwritten with the new uploader and time) after checking
// the path and the limits. The bytes go to the object store under a new object id (an overwritten
// file's old object is queued for deletion after commit); with no store configured it fails with
// blobstore.ErrNotConfigured. It takes the project row lock first so concurrent uploads of a
// project see each other's totals. It returns the stored file and whether it replaced one.
//
// The object is written inside the caller's transaction: Put carries at most MaxFileBytes (the
// daemon's file.chunk upload and the page's multipart form); big files use presigned uploads.
func Put(ctx context.Context, q store.DB, bs blobstore.Store, projectID, rawPath string, data []byte, by Uploader) (SharedFile, bool, error) {
	p, err := CleanPath(rawPath)
	if err != nil {
		return SharedFile{}, false, err
	}
	if len(data) > MaxFileBytes {
		return SharedFile{}, false, Check(Usage{}, int64(len(data)), -1)
	}
	bs = blobstore.Or(bs)
	if !bs.Configured() {
		return SharedFile{}, false, blobstore.ErrNotConfigured
	}
	if err := lockProject(ctx, q, projectID); err != nil {
		return SharedFile{}, false, err
	}
	u, err := ProjectUsage(ctx, q, projectID)
	if err != nil {
		return SharedFile{}, false, err
	}
	old, oldKey := int64(-1), ""
	if prev, err := Stat(ctx, q, projectID, p); err == nil {
		old = prev.Size
		if prev.StorageKey != nil {
			oldKey = *prev.StorageKey
		}
	} else if !errors.Is(err, ErrNotFound) {
		return SharedFile{}, false, err
	}
	if err := Check(u, int64(len(data)), old); err != nil {
		return SharedFile{}, false, err
	}
	obj := NewObjectID()
	key := blobstore.SharedKey(projectID, obj)
	if err := bs.Put(ctx, key, bytes.NewReader(data), int64(len(data)), ContentType(p)); err != nil {
		return SharedFile{}, false, err
	}
	var empID *string
	if by.EmployeeID != "" {
		empID = &by.EmployeeID
	}
	sum := Sum(data)
	err = q.Gorm(ctx).Exec(`INSERT INTO at_shared_files (project_id, path, content_type, size, sha256, data, uploaded_by,
		uploader_id, uploader_thread_id, storage_key, object_id, declared_sha256, state) VALUES (?, ?, ?, ?, ?, NULL, ?, ?, ?, ?, ?, ?, 'ready')
		ON CONFLICT (project_id, path) DO UPDATE SET content_type = EXCLUDED.content_type, size = EXCLUDED.size,
		sha256 = EXCLUDED.sha256, data = NULL, uploaded_by = EXCLUDED.uploaded_by, uploader_id = EXCLUDED.uploader_id,
		uploader_thread_id = EXCLUDED.uploader_thread_id, storage_key = EXCLUDED.storage_key, object_id = EXCLUDED.object_id,
		declared_sha256 = EXCLUDED.declared_sha256, state = 'ready', updated_at = now()`,
		projectID, p, ContentType(p), len(data), sum, by.Name, empID, by.ThreadID, key, obj, sum).Error
	if err != nil {
		_ = bs.Delete(context.WithoutCancel(ctx), key) // best effort; blob_gc does not know this key
		return SharedFile{}, false, err
	}
	if err := blobgc.Enqueue(ctx, q, oldKey); err != nil {
		return SharedFile{}, false, err
	}
	f, err := Stat(ctx, q, projectID, p)
	return f, old >= 0, err
}

// Delete removes one file (its object is queued for deletion after commit); false when there was none.
func Delete(ctx context.Context, q store.DB, projectID, p string) (bool, error) {
	var keys []*string
	err := q.Gorm(ctx).Raw(`DELETE FROM at_shared_files WHERE project_id = ? AND path = ? RETURNING storage_key`, projectID, p).
		Scan(&keys).Error
	if err != nil || len(keys) == 0 {
		return false, err
	}
	if keys[0] != nil {
		err = blobgc.Enqueue(ctx, q, *keys[0])
	}
	return true, err
}

func lockProject(ctx context.Context, q store.DB, projectID string) error {
	var id string
	err := q.Gorm(ctx).Raw(`SELECT id FROM at_projects WHERE id = ? FOR UPDATE`, projectID).Row().Scan(&id)
	if store.IsNoRows(err) {
		return ErrNotFound
	}
	return err
}
