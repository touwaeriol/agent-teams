package orchapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/touwaeriol/agent-teams/server/internal/artifacts"
	"github.com/touwaeriol/agent-teams/server/internal/blobstore"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/httpapi"
	"github.com/touwaeriol/agent-teams/server/internal/msg"
	"github.com/touwaeriol/agent-teams/server/internal/userfs"
)

// A project's files are its folder in the user's shared folder (projects/<pid>/ of the user's
// bucket, .project-files/ in every employee's working directory). The page lists, uploads,
// downloads and deletes them through the object store with the console's key; paths here are relative
// to the project's folder.

// maxFilesUpload is the body limit of one upload request.
const maxFilesUpload = 1 << 30

// FileEntry is one file or folder of a project.
type FileEntry struct {
	Name        string    `json:"name"`
	Path        string    `json:"path" doc:"Relative to the project's folder"`
	Dir         bool      `json:"dir"`
	Size        int64     `json:"size"`
	ContentType string    `json:"content_type,omitempty"`
	ModTime     time.Time `json:"mod_time"`
}

// ProjectFiles is GET /api/projects/{pid}/files.
type ProjectFiles struct {
	Available bool        `json:"available" doc:"The user's shared folder is ready (otherwise entries is empty and reason says why)"`
	Reason    string      `json:"reason,omitempty"`
	Dir       string      `json:"dir" doc:"The folder listed, relative to the project's folder (\"\" = the project's folder)"`
	Entries   []FileEntry `json:"entries" doc:"Folders first, then files, by name"`
}

// FilesUploaded is POST /api/projects/{pid}/files.
type FilesUploaded struct {
	Files []FileEntry `json:"files"`
}

// FolderCreate is POST /api/projects/{pid}/files/folders.
type FolderCreate struct {
	Path string `json:"path" minLength:"1" maxLength:"1024"`
}

type filesListInput struct {
	PID string `path:"pid" maxLength:"64"`
	Dir string `query:"dir" maxLength:"1024" doc:"Folder to list, relative to the project's folder"`
}

type filesListOutput struct{ Body ProjectFiles }

type filesUploadInput struct {
	PID     string `path:"pid" maxLength:"64"`
	RawBody multipart.Form
}

type filesUploadOutput struct{ Body FilesUploaded }

type filePathInput struct {
	PID  string `path:"pid" maxLength:"64"`
	Path string `query:"path" required:"true" minLength:"1" maxLength:"1024" doc:"Relative to the project's folder, for example data/sample.csv"`
}

type folderCreateInput struct {
	PID  string `path:"pid" maxLength:"64"`
	Body FolderCreate
}

func (a *API) registerProjectFiles(api huma.API) {
	t := "files"
	huma.Register(api, op("list-project-files", "GET", "/api/projects/{pid}/files", t,
		summary("A folder of the project's files (in the user's shared folder)")), a.listProjectFiles)
	huma.Register(api, op("upload-project-files", "POST", "/api/projects/{pid}/files", t, status(201),
		summary("Upload files into the project's files (multipart: file… parts; optional dir; optional path… per file, in order); a file with the same path is replaced"),
		func(o *huma.Operation) { o.MaxBodyBytes = maxFilesUpload }), a.uploadProjectFiles)
	huma.Register(api, op("download-project-file", "GET", "/api/projects/{pid}/files/download", t,
		summary("Download one of the project's files (always as an attachment, never rendered)"),
		func(o *huma.Operation) {
			o.Responses = map[string]*huma.Response{"200": {Description: "The file", Content: map[string]*huma.MediaType{"application/octet-stream": {}}}}
		}), a.downloadProjectFile)
	huma.Register(api, op("delete-project-file", "DELETE", "/api/projects/{pid}/files", t, status(204),
		summary("Delete a file, or a folder with everything in it")), a.deleteProjectFile)
	huma.Register(api, op("create-project-folder", "POST", "/api/projects/{pid}/files/folders", t, status(204),
		summary("Create a folder in the project's files")), a.createProjectFolder)
}

// filesFail maps the volume's refusals.
func filesFail(err error) error {
	switch {
	case errors.Is(err, userfs.ErrBadPath):
		return httpapi.InvalidMsg("path", msg.Of(err))
	case errors.Is(err, userfs.ErrNoFile):
		return httpapi.FailErr(http.StatusNotFound, "not_found", err)
	case userfs.Unavailable(err):
		return httpapi.Fail(http.StatusConflict, "folder_unavailable", userfs.UnavailableMessage)
	}
	return err
}

// errManaged refuses page writes into the deliverables' folder, which the platform keeps.
var errManaged = httpapi.Fail(http.StatusForbidden, "managed_folder", "交付物目录（.deliverables）由平台维护：发布新版本用 publish_file，删除交付物在资料库里删")

// managed tells whether rel is the deliverables' folder or inside it.
func managed(rel string) bool {
	first, _, _ := strings.Cut(strings.Trim(rel, "/"), "/")
	return first == userfs.DeliverablesDir
}

// writableFiles is projectFiles for a write: never the project's folder itself (rel "", "/", "//")
// nor into the deliverables' folder.
func (a *API) writableFiles(ctx context.Context, userID, projectID, rel string) (userfs.Files, string, error) {
	clean, err := userfs.CleanPath(rel)
	if err != nil {
		return nil, "", err
	}
	if clean == "" {
		return nil, "", httpapi.Invalid("path", "要给出项目文件里的一个文件或目录，不能是项目文件的根目录")
	}
	if managed(clean) {
		return nil, "", errManaged
	}
	return a.projectFiles(ctx, userID, projectID, clean)
}

// projectFiles opens the owner's volume and resolves rel inside the project's folder.
func (a *API) projectFiles(ctx context.Context, userID, projectID, rel string) (userfs.Files, string, error) {
	p, err := userfs.Join(userfs.ProjectDir(projectID), rel)
	if err != nil {
		return nil, "", err
	}
	if a.e.FS == nil {
		return nil, "", userfs.ErrDisabled
	}
	f, err := a.e.FS.Files(ctx, userID)
	return f, p, err
}

func entryOf(dir string, e userfs.Entry) FileEntry {
	p := path.Join(dir, e.Name)
	out := FileEntry{Name: e.Name, Path: p, Dir: e.Dir, Size: e.Size, ModTime: e.ModTime}
	if !e.Dir {
		out.ContentType = artifacts.FileType(e.Name)
	}
	return out
}

func (a *API) listProjectFiles(ctx context.Context, in *filesListInput) (*filesListOutput, error) {
	u, p, err := a.project(ctx, in.PID)
	if err != nil {
		return nil, err
	}
	dir, err := userfs.CleanPath(in.Dir)
	if err != nil {
		return nil, filesFail(err)
	}
	out := ProjectFiles{Dir: dir, Entries: []FileEntry{}}
	f, abs, err := a.projectFiles(ctx, u.ID, p.ID, dir)
	if userfs.Unavailable(err) {
		out.Reason = userfs.UnavailableMessage
		return &filesListOutput{Body: out}, nil
	}
	if err != nil {
		return nil, filesFail(err)
	}
	out.Available = true
	entries, err := f.List(ctx, abs)
	if errors.Is(err, userfs.ErrNoFile) && dir == "" {
		entries, err = nil, nil // the project's folder appears with its first file
	}
	if err != nil {
		return nil, filesFail(err)
	}
	for _, e := range entries {
		out.Entries = append(out.Entries, entryOf(dir, e))
	}
	return &filesListOutput{Body: out}, nil
}

// uploadTargets pairs the file parts with their paths: path[i] when given (then one per file),
// else the part's file name, under dir either way.
func uploadTargets(form *multipart.Form) ([]string, []*multipart.FileHeader, error) {
	heads := form.File["file"]
	if len(heads) == 0 {
		return nil, nil, httpapi.Invalid("file", "没有文件：用 file 字段上传")
	}
	paths := form.Value["path"]
	if len(paths) > 0 && len(paths) != len(heads) {
		return nil, nil, httpapi.Invalid("path", "path 要么不给，要么每个文件给一个（按顺序）")
	}
	dir := ""
	if d := form.Value["dir"]; len(d) > 0 {
		dir = d[0]
	}
	seen := map[string]bool{}
	out := make([]string, len(heads))
	for i, h := range heads {
		name := h.Filename
		if len(paths) > 0 {
			name = paths[i]
		}
		p, err := userfs.Join(dir, name)
		if err != nil || p == "" {
			return nil, nil, httpapi.Invalidf("path", "路径不合法：{path}", "path", name)
		}
		if seen[p] {
			return nil, nil, httpapi.Invalidf("path", "同一次上传里有两个 {path}", "path", p)
		}
		if managed(p) {
			return nil, nil, errManaged
		}
		seen[p], out[i] = true, p
	}
	return out, heads, nil
}

func (a *API) uploadProjectFiles(ctx context.Context, in *filesUploadInput) (*filesUploadOutput, error) {
	u, p, err := a.project(ctx, in.PID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = in.RawBody.RemoveAll() }()
	targets, heads, err := uploadTargets(&in.RawBody)
	if err != nil {
		return nil, err
	}
	f, base, err := a.projectFiles(ctx, u.ID, p.ID, "")
	if err != nil {
		return nil, filesFail(err)
	}
	out := FilesUploaded{Files: make([]FileEntry, 0, len(targets))}
	for i, rel := range targets {
		if err := putPart(ctx, f, base+"/"+rel, heads[i]); err != nil {
			return nil, partialUpload(err, rel, targets[:i])
		}
		out.Files = append(out.Files, entryOf(path.Dir(rel), userfs.Entry{Name: path.Base(rel), Size: heads[i].Size, ModTime: time.Now()}))
	}
	return &filesUploadOutput{Body: out}, nil
}

// partialUpload is a failed upload: the files before the failing one stay written (uploads are not
// atomic), and the error names them.
func partialUpload(err error, rel string, written []string) error {
	err = filesFail(err)
	if len(written) == 0 {
		return err
	}
	status, why := http.StatusInternalServerError, msg.T("写入失败")
	var ae *httpapi.APIError
	if errors.As(err, &ae) {
		status, why = ae.Status, msg.T(ae.Detail)
		if ae.Message != nil {
			why = *ae.Message
		}
	}
	return httpapi.Failf(status, "upload_partial", "{file} 没有上传（{why}）；这次上传里已经写入的文件：{written}", "file", rel, "why", why,
		"written", strings.Join(written, "、"))
}

func putPart(ctx context.Context, f userfs.Files, name string, h *multipart.FileHeader) error {
	r, err := h.Open()
	if err != nil {
		return err
	}
	defer r.Close()
	return f.Put(ctx, name, r, h.Size)
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

func (a *API) downloadProjectFile(ctx context.Context, in *filePathInput) (*huma.StreamResponse, error) {
	u, p, err := a.project(ctx, in.PID)
	if err != nil {
		return nil, err
	}
	f, abs, err := a.projectFiles(ctx, u.ID, p.ID, in.Path)
	if err != nil {
		return nil, filesFail(err)
	}
	r, e, err := f.Open(ctx, abs)
	if err != nil {
		return nil, filesFail(err)
	}
	return &huma.StreamResponse{Body: func(hc huma.Context) {
		defer r.Close()
		hc.SetHeader("Content-Type", downloadType(artifacts.FileType(e.Name)))
		hc.SetHeader("Content-Disposition", contentDisposition("attachment", e.Name))
		hc.SetHeader("Content-Length", fmt.Sprint(e.Size))
		hc.SetHeader("Cache-Control", "private, no-store")
		hc.SetHeader("X-Content-Type-Options", "nosniff")
		hc.SetHeader("Content-Security-Policy", "default-src 'none'; sandbox")
		_, _ = io.Copy(hc.BodyWriter(), r)
	}}, nil
}

func (a *API) deleteProjectFile(ctx context.Context, in *filePathInput) (*struct{}, error) {
	u, p, err := a.project(ctx, in.PID)
	if err != nil {
		return nil, err
	}
	f, abs, err := a.writableFiles(ctx, u.ID, p.ID, in.Path)
	if err != nil {
		return nil, filesFail(err)
	}
	return nil, filesFail(f.Remove(ctx, abs))
}

func (a *API) createProjectFolder(ctx context.Context, in *folderCreateInput) (*struct{}, error) {
	u, p, err := a.project(ctx, in.PID)
	if err != nil {
		return nil, err
	}
	f, abs, err := a.writableFiles(ctx, u.ID, p.ID, in.Body.Path)
	if err != nil {
		return nil, filesFail(err)
	}
	return nil, filesFail(f.Mkdir(ctx, abs))
}

// saveAttachmentFile copies an attachment of one of the caller's projects into that project's
// files (the page's "存到项目文件"); it is a use of the attachment.
func (a *API) saveAttachmentFile(ctx context.Context, in *saveSharedInput) (*saveSharedOutput, error) {
	u, err := a.user(ctx)
	if err != nil {
		return nil, err
	}
	att, err := core.AttachmentOfUser(ctx, a.e.Core.Pool, u.ID, in.ID)
	if err == nil && att.ProjectID == "" {
		err = core.ErrNotFound // not sent yet: no project to keep it in
	}
	if err != nil {
		return nil, fail(err, "没有这个附件")
	}
	f, abs, err := a.writableFiles(ctx, u.ID, att.ProjectID, in.Body.Path)
	if err != nil {
		return nil, filesFail(err)
	}
	if !in.Body.Overwrite {
		if r, _, err := f.Open(ctx, abs); err == nil {
			r.Close()
			return nil, httpapi.Fail(http.StatusConflict, "file_exists", "这个路径已经有文件了；要覆盖请选择覆盖")
		}
	}
	if err := core.TouchAttachments(ctx, a.e.Core.Pool, att.ProjectID, []string{att.ID}, a.e.Core.AttachmentTTL); err != nil {
		return nil, err
	}
	_, rc, err := core.OpenAttachment(ctx, a.e.Core.Pool, blobstore.Or(a.e.Core.Blobs), u.ID, att.ID)
	if errors.Is(err, blobstore.ErrNotFound) || errors.Is(err, core.ErrAttachmentExpired) || errors.Is(err, core.ErrNotFound) {
		return nil, fail(core.ErrAttachmentExpired, "没有这个附件")
	}
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	if err := f.Put(ctx, abs, rc, int64(att.Size)); err != nil {
		return nil, filesFail(err)
	}
	rel, _ := userfs.CleanPath(in.Body.Path)
	return &saveSharedOutput{Body: entryOf(path.Dir(rel), userfs.Entry{Name: path.Base(rel), Size: int64(att.Size), ModTime: time.Now()})}, nil
}
