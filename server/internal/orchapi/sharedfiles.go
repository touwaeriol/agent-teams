package orchapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"path"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/httpapi"
	"github.com/touwaeriol/agent-teams/server/internal/sharedfiles"
)

// maxSharedUpload is the body limit of one upload request: five files of the per-file limit.
const maxSharedUpload = 5*sharedfiles.MaxFileBytes + 1<<20

// SharedFileLimits are the shared-files limits of a project.
type SharedFileLimits struct {
	FileBytes    int64 `json:"file_bytes" doc:"Largest single file"`
	ProjectBytes int64 `json:"project_bytes" doc:"Total of all files of the project"`
	ProjectFiles int   `json:"project_files" doc:"Number of files of the project"`
}

// SharedFileList is GET /api/projects/{pid}/shared-files.
type SharedFileList struct {
	Files      []sharedfiles.SharedFile `json:"files"`
	TotalBytes int64                    `json:"total_bytes"`
	FileCount  int                      `json:"file_count"`
	Limits     SharedFileLimits         `json:"limits"`
}

// UploadedSharedFile is one file of an upload.
type UploadedSharedFile struct {
	sharedfiles.SharedFile
	Overwritten bool `json:"overwritten" doc:"A file with this path existed and was replaced"`
}

// SharedFileUpload is the answer of POST /api/projects/{pid}/shared-files.
type SharedFileUpload struct {
	Files      []UploadedSharedFile `json:"files"`
	TotalBytes int64                `json:"total_bytes"`
}

type sharedListInput struct {
	PID    string `path:"pid" maxLength:"64"`
	Prefix string `query:"prefix" maxLength:"255" doc:"Only paths starting with this text"`
}

type sharedListOutput struct{ Body SharedFileList }

type sharedUploadInput struct {
	PID     string `path:"pid" maxLength:"64"`
	RawBody multipart.Form
}

type sharedUploadOutput struct{ Body SharedFileUpload }

type sharedPathInput struct {
	PID  string `path:"pid" maxLength:"64"`
	Path string `query:"path" required:"true" minLength:"1" maxLength:"255" doc:"Shared-files path, for example data/sample.csv"`
}

type sharedDownloadOutput struct {
	ContentType        string `header:"Content-Type"`
	ContentDisposition string `header:"Content-Disposition"`
	CacheControl       string `header:"Cache-Control"`
	NoSniff            string `header:"X-Content-Type-Options"`
	CSP                string `header:"Content-Security-Policy"`
	Body               []byte
}

func (a *API) registerSharedFiles(api huma.API) {
	t := "shared-files"
	huma.Register(api, op("list-shared-files", "GET", "/api/projects/{pid}/shared-files", t,
		summary("The project's shared files (no content) with the total and the limits")), a.listSharedFiles)
	huma.Register(api, op("upload-shared-files", "POST", "/api/projects/{pid}/shared-files", t, status(201),
		summary("Upload files to the project's shared files (multipart: file… parts; optional dir prefix; optional path… per file, in order); a file with the same path is overwritten"),
		func(o *huma.Operation) { o.MaxBodyBytes = maxSharedUpload }), a.uploadSharedFiles)
	huma.Register(api, op("download-shared-file", "GET", "/api/projects/{pid}/shared-files/download", t,
		summary("Download one shared file (always as an attachment, never rendered)"), func(o *huma.Operation) {
			o.Responses = map[string]*huma.Response{"200": {Description: "The file", Content: map[string]*huma.MediaType{
				"application/octet-stream": {}}}}
		}), a.downloadSharedFile)
	huma.Register(api, op("delete-shared-file", "DELETE", "/api/projects/{pid}/shared-files", t, status(204),
		summary("Delete one shared file")), a.deleteSharedFile)
}

func (a *API) listSharedFiles(ctx context.Context, in *sharedListInput) (*sharedListOutput, error) {
	_, p, err := a.project(ctx, in.PID)
	if err != nil {
		return nil, err
	}
	files, err := sharedfiles.List(ctx, a.e.Core.Pool, p.ID, in.Prefix)
	if err != nil {
		return nil, err
	}
	u, err := sharedfiles.ProjectUsage(ctx, a.e.Core.Pool, p.ID)
	if err != nil {
		return nil, err
	}
	return &sharedListOutput{Body: SharedFileList{Files: files, TotalBytes: u.Bytes, FileCount: u.Files, Limits: sharedLimits()}}, nil
}

func sharedLimits() SharedFileLimits {
	return SharedFileLimits{FileBytes: sharedfiles.MaxFileBytes, ProjectBytes: sharedfiles.MaxProjectBytes,
		ProjectFiles: sharedfiles.MaxProjectFiles}
}

// sharedPart is one file of an upload with its checked target path.
type sharedPart struct {
	path string
	head *multipart.FileHeader
}

// sharedParts pairs the file parts with their target paths: path[i] when given (then one per file),
// else the part's file name, under the dir prefix either way.
func sharedParts(form *multipart.Form) ([]sharedPart, error) {
	heads := form.File["file"]
	if len(heads) == 0 {
		return nil, httpapi.Invalid("file", "没有文件：用 file 字段上传")
	}
	paths := form.Value["path"]
	if len(paths) > 0 && len(paths) != len(heads) {
		return nil, httpapi.Invalid("path", "path 要么不给，要么每个文件给一个（按顺序）")
	}
	dir := ""
	if d := form.Value["dir"]; len(d) > 0 {
		dir = d[0]
	}
	seen := map[string]bool{}
	out := make([]sharedPart, len(heads))
	for i, h := range heads {
		name := h.Filename
		if len(paths) > 0 {
			name = paths[i]
		}
		p, err := sharedfiles.Join(dir, name)
		if err != nil {
			return nil, httpapi.Invalid("path", strings.TrimPrefix(err.Error(), sharedfiles.ErrInvalidPath.Error()+"："))
		}
		if seen[p] {
			return nil, httpapi.Invalid("path", fmt.Sprintf("同一次上传里有两个 %s", p))
		}
		if h.Size > sharedfiles.MaxFileBytes {
			return nil, sharedFail(sharedfiles.Check(sharedfiles.Usage{}, h.Size, -1))
		}
		seen[p] = true
		out[i] = sharedPart{path: p, head: h}
	}
	return out, nil
}

func readPart(h *multipart.FileHeader) ([]byte, error) {
	f, err := h.Open()
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, sharedfiles.MaxFileBytes+1))
	if err == nil && len(data) > sharedfiles.MaxFileBytes {
		return nil, sharedFail(sharedfiles.Check(sharedfiles.Usage{}, int64(len(data)), -1))
	}
	return data, err
}

func (a *API) uploadSharedFiles(ctx context.Context, in *sharedUploadInput) (*sharedUploadOutput, error) {
	_, p, err := a.project(ctx, in.PID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = in.RawBody.RemoveAll() }()
	parts, err := sharedParts(&in.RawBody)
	if err != nil {
		return nil, err
	}
	out := SharedFileUpload{Files: make([]UploadedSharedFile, 0, len(parts))}
	err = a.e.Core.InTx(ctx, func(tx *core.Tx) error {
		for _, part := range parts {
			data, err := readPart(part.head)
			if err != nil {
				return err
			}
			f, overwritten, err := sharedfiles.Put(ctx, tx, p.ID, part.path, data, sharedfiles.Uploader{Name: sharedfiles.UserName})
			if err != nil {
				return err
			}
			if err := tx.EmitSharedFile(ctx, p.ID, sharedfiles.UserName, "write", f.Path); err != nil {
				return err
			}
			out.Files = append(out.Files, UploadedSharedFile{SharedFile: f, Overwritten: overwritten})
		}
		u, err := sharedfiles.ProjectUsage(ctx, tx, p.ID)
		out.TotalBytes = u.Bytes
		return err
	})
	if err != nil {
		return nil, sharedFail(err)
	}
	return &sharedUploadOutput{Body: out}, nil
}

// sharedFail maps the store's refusals: a bad path 422, a file over the limit 413, the project's
// limits 409 quota_exceeded, a missing file 404.
func sharedFail(err error) error {
	switch {
	case errors.Is(err, sharedfiles.ErrInvalidPath):
		return httpapi.Invalid("path", err.Error())
	case errors.Is(err, sharedfiles.ErrFileTooLarge):
		return httpapi.Fail(http.StatusRequestEntityTooLarge, "file_too_large", err.Error())
	case errors.Is(err, sharedfiles.ErrQuota):
		return httpapi.Fail(http.StatusConflict, "quota_exceeded", err.Error())
	case errors.Is(err, sharedfiles.ErrNotFound):
		return httpapi.Fail(http.StatusNotFound, "not_found", "没有这个共享文件")
	}
	return err
}

// unsafeInline are types a browser could run if it ever displayed them; they are sent as bytes.
var unsafeInline = []string{"text/html", "image/svg+xml", "application/xml", "text/xml", "text/javascript", "application/javascript"}

func downloadType(ct string) string {
	base, _, _ := strings.Cut(ct, ";")
	for _, t := range unsafeInline {
		if strings.TrimSpace(base) == t {
			return "application/octet-stream"
		}
	}
	return ct
}

func (a *API) downloadSharedFile(ctx context.Context, in *sharedPathInput) (*sharedDownloadOutput, error) {
	_, p, err := a.project(ctx, in.PID)
	if err != nil {
		return nil, err
	}
	sp, err := sharedfiles.CleanPath(in.Path)
	if err != nil {
		return nil, sharedFail(err)
	}
	f, data, err := sharedfiles.Load(ctx, a.e.Core.Pool, p.ID, sp)
	if err != nil {
		return nil, sharedFail(err)
	}
	disp := mime.FormatMediaType("attachment", map[string]string{"filename": path.Base(f.Path)})
	if disp == "" {
		disp = "attachment"
	}
	return &sharedDownloadOutput{ContentType: downloadType(f.ContentType), ContentDisposition: disp, CacheControl: "private, no-store",
		NoSniff: "nosniff", CSP: "default-src 'none'; sandbox", Body: data}, nil
}

func (a *API) deleteSharedFile(ctx context.Context, in *sharedPathInput) (*struct{}, error) {
	_, p, err := a.project(ctx, in.PID)
	if err != nil {
		return nil, err
	}
	sp, err := sharedfiles.CleanPath(in.Path)
	if err != nil {
		return nil, sharedFail(err)
	}
	return nil, a.e.Core.InTx(ctx, func(tx *core.Tx) error {
		deleted, err := sharedfiles.Delete(ctx, tx, p.ID, sp)
		if err != nil {
			return err
		}
		if !deleted {
			return sharedFail(sharedfiles.ErrNotFound)
		}
		return tx.EmitSharedFile(ctx, p.ID, sharedfiles.UserName, "delete", sp)
	})
}
