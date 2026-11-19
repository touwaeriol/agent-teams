// Package sharedfiles stores a project's shared files (讨论串 #33, architecture.md §6.3, protocol.md §6.5):
// files every session (coordinator, leads, members of all threads) and every user of the project can
// list, fetch, overwrite and delete. The bytes live in PG (at_shared_files), never in git; a path is
// overwritten in place (no versions). Paths and limits are checked here, whoever sent the file
// (daemon or browser) — callers must not trust their own checks.
package sharedfiles

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/touwaeriol/agent-teams/server/internal/artifacts"
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
)

// CleanPath checks a shared-files path and returns it normalised (surrounding spaces of the whole
// path trimmed, a leading "./" removed). It refuses absolute paths, '..' and '.' segments, empty
// segments ("a//b", a trailing '/'), segments starting with '.', backslashes, control characters,
// invalid UTF-8, more than MaxDepth segments and more than MaxPathBytes bytes — the SharedPath rule
// of protocol/schemas/tools/common.json.
func CleanPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	for strings.HasPrefix(p, "./") {
		p = p[2:]
	}
	bad := func(why string) (string, error) { return "", fmt.Errorf("%w：%q %s", ErrInvalidPath, p, why) }
	switch {
	case p == "":
		return bad("是空的")
	case len(p) > MaxPathBytes:
		return bad(fmt.Sprintf("超过 %d 字节", MaxPathBytes))
	case !utf8.ValidString(p):
		return bad("不是合法的 UTF-8")
	case strings.HasPrefix(p, "/"):
		return bad("不能是绝对路径")
	case strings.Contains(p, `\`):
		return bad("不能有反斜杠")
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f {
			return bad("不能有控制字符")
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
		}
	}
	return p, nil
}

// Join puts name under the folder dir (both checked); dir "" leaves name as it is.
func Join(dir, name string) (string, error) {
	dir = strings.Trim(strings.TrimSpace(dir), "/")
	if dir == "" {
		return CleanPath(name)
	}
	if _, err := CleanPath(dir); err != nil {
		return "", err
	}
	return CleanPath(dir + "/" + strings.TrimSpace(name))
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

// File is one shared file without its bytes.
type File struct {
	Path             string    `json:"path"`
	ContentType      string    `json:"content_type"`
	Size             int64     `json:"size"`
	SHA256           string    `json:"sha256" gorm:"column:sha256"`
	UploadedBy       string    `json:"uploaded_by" doc:"Employee name (dev-1) or user"`
	UploaderID       *string   `json:"-"`
	UploaderThreadNo *int      `json:"uploaded_by_thread_no,omitempty" doc:"Thread the employee uploaded it from (shown as dev-1 #31)"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// Label is how the uploader is shown: "dev-1 #31", "dev-1" or "user".
func (f File) Label() string {
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

const listColumns = `f.path, f.content_type, f.size, f.sha256, f.uploaded_by, f.uploader_id, t.no AS uploader_thread_no,
	f.created_at, f.updated_at`

// List returns the project's files by path; prefix (optional) keeps paths starting with it.
func List(ctx context.Context, q store.DB, projectID, prefix string) ([]File, error) {
	out := []File{}
	db := q.Gorm(ctx).Table("at_shared_files f").Select(listColumns).
		Joins("LEFT JOIN at_threads t ON t.id = f.uploader_thread_id").Where("f.project_id = ?", projectID)
	if prefix = strings.TrimSpace(prefix); prefix != "" {
		db = db.Where("starts_with(f.path, ?)", prefix)
	}
	err := db.Order("f.path COLLATE \"C\"").Scan(&out).Error
	return out, err
}

// Stat returns one file without its bytes.
func Stat(ctx context.Context, q store.DB, projectID, p string) (File, error) {
	var out []File
	err := q.Gorm(ctx).Table("at_shared_files f").Select(listColumns).
		Joins("LEFT JOIN at_threads t ON t.id = f.uploader_thread_id").
		Where("f.project_id = ? AND f.path = ?", projectID, p).Limit(1).Scan(&out).Error
	if err != nil {
		return File{}, err
	}
	if len(out) == 0 {
		return File{Path: p}, ErrNotFound
	}
	return out[0], nil
}

// Load returns one file with its bytes.
func Load(ctx context.Context, q store.DB, projectID, p string) (File, []byte, error) {
	f, err := Stat(ctx, q, projectID, p)
	if err != nil {
		return f, nil, err
	}
	var data []byte
	err = q.Gorm(ctx).Raw(`SELECT data FROM at_shared_files WHERE project_id = ? AND path = ?`, projectID, p).Row().Scan(&data)
	if store.IsNoRows(err) {
		return f, nil, ErrNotFound
	}
	return f, data, err
}

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
// the path and the limits. It takes the project row lock first so concurrent uploads of a project
// see each other's totals. It returns the stored file and whether it replaced one.
func Put(ctx context.Context, q store.DB, projectID, rawPath string, data []byte, by Uploader) (File, bool, error) {
	p, err := CleanPath(rawPath)
	if err != nil {
		return File{}, false, err
	}
	if len(data) > MaxFileBytes {
		return File{}, false, Check(Usage{}, int64(len(data)), -1)
	}
	if err := lockProject(ctx, q, projectID); err != nil {
		return File{}, false, err
	}
	u, err := ProjectUsage(ctx, q, projectID)
	if err != nil {
		return File{}, false, err
	}
	old := int64(-1)
	if prev, err := Stat(ctx, q, projectID, p); err == nil {
		old = prev.Size
	} else if !errors.Is(err, ErrNotFound) {
		return File{}, false, err
	}
	if err := Check(u, int64(len(data)), old); err != nil {
		return File{}, false, err
	}
	var empID *string
	if by.EmployeeID != "" {
		empID = &by.EmployeeID
	}
	err = q.Gorm(ctx).Exec(`INSERT INTO at_shared_files (project_id, path, content_type, size, sha256, data, uploaded_by,
		uploader_id, uploader_thread_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (project_id, path) DO UPDATE SET content_type = EXCLUDED.content_type, size = EXCLUDED.size,
		sha256 = EXCLUDED.sha256, data = EXCLUDED.data, uploaded_by = EXCLUDED.uploaded_by, uploader_id = EXCLUDED.uploader_id,
		uploader_thread_id = EXCLUDED.uploader_thread_id, updated_at = now()`,
		projectID, p, ContentType(p), len(data), Sum(data), data, by.Name, empID, by.ThreadID).Error
	if err != nil {
		return File{}, false, err
	}
	f, err := Stat(ctx, q, projectID, p)
	return f, old >= 0, err
}

// Delete removes one file; false when there was none.
func Delete(ctx context.Context, q store.DB, projectID, p string) (bool, error) {
	res := q.Gorm(ctx).Exec(`DELETE FROM at_shared_files WHERE project_id = ? AND path = ?`, projectID, p)
	return res.RowsAffected > 0, res.Error
}

func lockProject(ctx context.Context, q store.DB, projectID string) error {
	var id string
	err := q.Gorm(ctx).Raw(`SELECT id FROM at_projects WHERE id = ? FOR UPDATE`, projectID).Row().Scan(&id)
	if store.IsNoRows(err) {
		return ErrNotFound
	}
	return err
}
