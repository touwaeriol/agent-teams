package orchapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/gin-gonic/gin"

	"github.com/touwaeriol/agent-teams/server/internal/blobstore"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/httpapi"
	"github.com/touwaeriol/agent-teams/server/internal/sharedfiles"
)

// Presigned upload and download lifetimes (#58 design §10).
const (
	partURLTTL     = time.Hour
	pageGetTTL     = 5 * time.Minute
	nodeGetTTL     = 15 * time.Minute
	maxPartsPerAsk = 100
	// uploadKeep is how long an unfinished upload is kept before blobgc aborts it.
	uploadKeep = 24 * time.Hour
)

// UploadCreate is POST /api/projects/{pid}/uploads.
type UploadCreate struct {
	Kind string `json:"kind" enum:"attachment,shared" doc:"attachment: a file for a message (not counted in the project's quota); shared: a shared file"`
	Name string `json:"name" minLength:"1" maxLength:"300" doc:"File name (an attachment shows it; a shared file uses it as its path when path is not given)"`
	Size int64  `json:"size" minimum:"0" doc:"Exact size in bytes (at most 200 MB: more answers 413 file_too_large); the object must have it at completion"`
	Path string `json:"path,omitempty" maxLength:"255" doc:"Shared files: the target path (default: name)"`
	// Overwrite is a pointer so that "not given" keeps the page's old behaviour (overwrite).
	Overwrite *bool `json:"overwrite,omitempty" doc:"Shared files: replace a file at the path (default true); false answers 409 file_exists"`
}

// UploadSession is an upload in flight: PUT each part (part_size bytes, the last one shorter) to
// the URLs of POST …/parts, then POST …/complete with the ETags.
type UploadSession struct {
	UploadID  string    `json:"upload_id" doc:"img_… for an attachment (also its id), up_… for a shared file"`
	Kind      string    `json:"kind" enum:"attachment,shared"`
	ID        string    `json:"id" doc:"The attachment id, or the shared file's object id"`
	PartSize  int64     `json:"part_size" doc:"Bytes per part (16 MB)"`
	Parts     int32     `json:"parts" doc:"Number of parts"`
	ExpiresAt time.Time `json:"expires_at" doc:"Unfinished uploads are dropped after this"`
}

type uploadCreateInput struct {
	PID  string `path:"pid" maxLength:"64"`
	Body UploadCreate
}

type uploadCreateOutput struct{ Body UploadSession }

// UploadPartsAsk is POST …/uploads/{upload_id}/parts.
type UploadPartsAsk struct {
	Numbers []int32 `json:"numbers" minItems:"1" maxItems:"100" doc:"Part numbers, 1 to parts"`
}

// PartURL is a presigned PUT for one part.
type PartURL struct {
	Number int32  `json:"number"`
	URL    string `json:"url"`
}

// UploadParts are presigned part URLs.
type UploadParts struct {
	URLs      []PartURL `json:"urls"`
	ExpiresAt time.Time `json:"expires_at" doc:"The URLs stop working after this (1 hour); ask again for new ones"`
}

type uploadPartsInput struct {
	PID      string `path:"pid" maxLength:"64"`
	UploadID string `path:"upload_id" maxLength:"64"`
	Body     UploadPartsAsk
}

type uploadPartsOutput struct{ Body UploadParts }

// UploadComplete is POST …/uploads/{upload_id}/complete.
type UploadComplete struct {
	Parts []blobstore.Part `json:"parts" minItems:"1" maxItems:"10000" doc:"Every part with the ETag its PUT answered"`
}

// UploadDone is a finished upload.
type UploadDone struct {
	Kind       string              `json:"kind" enum:"attachment,shared"`
	Attachment *AttachmentView     `json:"attachment,omitempty"`
	SharedFile *UploadedSharedFile `json:"shared_file,omitempty"`
}

type uploadCompleteInput struct {
	PID      string `path:"pid" maxLength:"64"`
	UploadID string `path:"upload_id" maxLength:"64"`
	Body     UploadComplete
}

type uploadCompleteOutput struct{ Body UploadDone }

type uploadAbortInput struct {
	PID      string `path:"pid" maxLength:"64"`
	UploadID string `path:"upload_id" maxLength:"64"`
}

func (a *API) registerUploads(api huma.API, r gin.IRoutes) {
	t := "uploads"
	huma.Register(api, op("create-upload", "POST", "/api/projects/{pid}/uploads", t, status(201),
		summary("Start a presigned upload of an attachment (any type, at most 200 MB) or a shared file (at most 200 MB, project quota 5 GB)")),
		a.createUpload)
	huma.Register(api, op("upload-part-urls", "POST", "/api/projects/{pid}/uploads/{upload_id}/parts", t,
		summary("Presigned PUT URLs for parts of an upload (1 hour)")), a.uploadParts)
	huma.Register(api, op("complete-upload", "POST", "/api/projects/{pid}/uploads/{upload_id}/complete", t,
		summary("Finish an upload: the object's size is checked, an attachment's type sniffed, a shared file put at its path")),
		a.completeUpload)
	huma.Register(api, op("abort-upload", "DELETE", "/api/projects/{pid}/uploads/{upload_id}", t, status(204),
		summary("Cancel an upload in flight")), a.abortUpload)
	// Attachments uploaded before their project exists (a new plain session's first message):
	// POST /api/sessions moves them into the session's project (SessionInput.attachments).
	huma.Register(api, op("create-user-upload", "POST", "/api/uploads", t, status(201),
		summary("Start a presigned upload of an attachment without a project (kind attachment only; for POST /api/sessions attachments; dropped 24 hours after the last use if never sent)")),
		a.createUserUpload)
	huma.Register(api, op("user-upload-part-urls", "POST", "/api/uploads/{upload_id}/parts", t,
		summary("Presigned PUT URLs for parts of an upload without a project (1 hour)")), a.userUploadParts)
	huma.Register(api, op("complete-user-upload", "POST", "/api/uploads/{upload_id}/complete", t,
		summary("Finish an upload without a project: the object's size is checked and its type sniffed")), a.completeUserUpload)
	huma.Register(api, op("abort-user-upload", "DELETE", "/api/uploads/{upload_id}", t, status(204),
		summary("Cancel an upload without a project")), a.abortUserUpload)
	if a.e == nil {
		r.GET("/nodes/attachments/:id/url", offline)
		r.POST("/nodes/uploads", offline)
		return
	}
	r.GET("/nodes/attachments/:id/url", a.nodeAttachmentURL)
	r.POST("/nodes/uploads", a.nodeUpload)
}

// uploadFail maps the upload errors of both kinds.
func uploadFail(err error) error {
	switch {
	case errors.Is(err, core.ErrUploadIncomplete), errors.Is(err, sharedfiles.ErrIncomplete):
		return httpapi.Fail(http.StatusConflict, "upload_incomplete", err.Error())
	case errors.Is(err, core.ErrUploadSize), errors.Is(err, sharedfiles.ErrSizeMismatch):
		return httpapi.Fail(http.StatusUnprocessableEntity, "size_mismatch", err.Error())
	case errors.Is(err, sharedfiles.ErrChecksum):
		return httpapi.Fail(http.StatusUnprocessableEntity, "checksum_mismatch", err.Error())
	case errors.Is(err, core.ErrAttachmentTooLarge):
		return httpapi.Fail(http.StatusRequestEntityTooLarge, "file_too_large", err.Error())
	case errors.Is(err, sharedfiles.ErrExists):
		return httpapi.Fail(http.StatusConflict, "file_exists", "这个路径已经有共享文件了；要覆盖请选择覆盖")
	case errors.Is(err, sharedfiles.ErrUploadNotFound), errors.Is(err, core.ErrNotFound):
		return httpapi.Fail(http.StatusNotFound, "not_found", "没有这个上传（已完成、已取消或超过 24 小时已清理），请重新上传")
	}
	return fail(sharedFail(err), "没有这个上传")
}

func (a *API) createUpload(ctx context.Context, in *uploadCreateInput) (*uploadCreateOutput, error) {
	u, p, err := a.project(ctx, in.PID)
	if err != nil {
		return nil, err
	}
	name := uploadName(in.Body.Name)
	if name == "" || name == "." || name == "/" {
		return nil, httpapi.Invalid("name", "文件名是空的")
	}
	out := UploadSession{Kind: in.Body.Kind, PartSize: blobstore.PartSize, Parts: blobstore.PartCount(in.Body.Size),
		ExpiresAt: time.Now().Add(uploadKeep)}
	if in.Body.Kind == "attachment" {
		return a.beginAttachment(ctx, p.ID, u.ID, name, in.Body.Size, out)
	}
	target := in.Body.Path
	if target == "" {
		target = name
	}
	overwrite := in.Body.Overwrite == nil || *in.Body.Overwrite
	var up sharedfiles.Upload
	err = a.e.Core.InTx(ctx, func(tx *core.Tx) error {
		var err error
		up, err = sharedfiles.BeginUpload(ctx, tx, a.e.Core.Blobs, p.ID, sharedfiles.BeginRequest{Path: target, Size: in.Body.Size,
			Overwrite: overwrite, Multipart: true, By: sharedfiles.Uploader{Name: sharedfiles.UserName}})
		return err
	})
	if err != nil {
		return nil, uploadFail(err)
	}
	out.UploadID, out.ID = up.ID, up.ObjectID
	return &uploadCreateOutput{Body: out}, nil
}

func (a *API) beginAttachment(ctx context.Context, pid, uid, name string, size int64, out UploadSession) (*uploadCreateOutput, error) {
	att, err := core.BeginAttachmentUpload(ctx, a.e.Core.Pool, a.e.Core.Blobs, a.e.Core.AttachmentTTL, pid, uid, name, size)
	if err != nil {
		return nil, uploadFail(err)
	}
	out.UploadID, out.ID = att.ID, att.ID
	return &uploadCreateOutput{Body: out}, nil
}

// inFlight is an upload's object, its S3 upload id and its part count; owner.ProjectID "" = an
// attachment without a project (no shared-file uploads there).
func (a *API) inFlight(ctx context.Context, owner core.AttachmentOwner, id string) (key, uploadID string, parts int32, err error) {
	if core.IsAttachmentUpload(id) {
		att, upID, err := core.UploadingAttachment(ctx, a.e.Core.Pool, owner, id)
		if err != nil {
			return "", "", 0, err
		}
		return *att.StorageKey, upID, blobstore.PartCount(int64(att.Size)), nil
	}
	if owner.ProjectID == "" {
		return "", "", 0, core.ErrNotFound
	}
	up, err := sharedfiles.GetUpload(ctx, a.e.Core.Pool, owner.ProjectID, id)
	if err != nil {
		return "", "", 0, err
	}
	if up.S3UploadID == nil {
		return "", "", 0, sharedfiles.ErrUploadNotFound // a node's single PUT
	}
	return up.StorageKey, *up.S3UploadID, blobstore.PartCount(up.Size), nil
}

func (a *API) uploadParts(ctx context.Context, in *uploadPartsInput) (*uploadPartsOutput, error) {
	_, p, err := a.project(ctx, in.PID)
	if err != nil {
		return nil, err
	}
	return a.partURLs(ctx, core.OwnerOfProject(p.ID), in.UploadID, in.Body.Numbers)
}

func (a *API) partURLs(ctx context.Context, owner core.AttachmentOwner, id string, numbers []int32) (*uploadPartsOutput, error) {
	key, uploadID, n, err := a.inFlight(ctx, owner, id)
	if err != nil {
		return nil, uploadFail(err)
	}
	out := UploadParts{URLs: make([]PartURL, 0, len(numbers)), ExpiresAt: time.Now().Add(partURLTTL)}
	for _, num := range numbers {
		if num < 1 || num > n {
			return nil, httpapi.Invalid("numbers", fmt.Sprintf("分片号要在 1 到 %d 之间（收到 %d）", n, num))
		}
		u, err := a.e.Core.Blobs.PresignPart(ctx, key, uploadID, num, partURLTTL)
		if err != nil {
			return nil, uploadFail(err)
		}
		out.URLs = append(out.URLs, PartURL{Number: num, URL: u})
	}
	return &uploadPartsOutput{Body: out}, nil
}

func (a *API) completeUpload(ctx context.Context, in *uploadCompleteInput) (*uploadCompleteOutput, error) {
	_, p, err := a.project(ctx, in.PID)
	if err != nil {
		return nil, err
	}
	if core.IsAttachmentUpload(in.UploadID) {
		return a.completeAttachment(ctx, core.OwnerOfProject(p.ID), in.UploadID, in.Body.Parts)
	}
	var done UploadedSharedFile
	err = a.e.Core.InTx(ctx, func(tx *core.Tx) error {
		f, overwritten, err := sharedfiles.FinalizeUpload(ctx, tx, a.e.Core.Blobs, p.ID, in.UploadID,
			sharedfiles.FinalizeOptions{Parts: in.Body.Parts})
		if err != nil {
			return err
		}
		done = UploadedSharedFile{SharedFile: f, Overwritten: overwritten}
		return tx.EmitSharedFile(ctx, p.ID, sharedfiles.UserName, "write", f.Path)
	})
	if errors.Is(err, sharedfiles.ErrExists) || errors.Is(err, sharedfiles.ErrQuota) || errors.Is(err, sharedfiles.ErrInvalidPath) {
		// Refused for good: cancel it now, so it does not hold the quota for a day.
		_ = sharedfiles.AbortUpload(ctx, a.e.Core.Pool, a.e.Core.Blobs, p.ID, in.UploadID)
	}
	if err != nil {
		return nil, uploadFail(err)
	}
	return &uploadCompleteOutput{Body: UploadDone{Kind: "shared", SharedFile: &done}}, nil
}

func (a *API) completeAttachment(ctx context.Context, owner core.AttachmentOwner, id string, parts []blobstore.Part) (*uploadCompleteOutput, error) {
	att, err := core.CompleteAttachmentUpload(ctx, a.e.Core.Pool, a.e.Core.Blobs, a.e.Core.AttachmentTTL, owner, id, parts)
	if err != nil {
		return nil, uploadFail(err)
	}
	return &uploadCompleteOutput{Body: UploadDone{Kind: "attachment",
		Attachment: &AttachmentView{Attachment: att, URL: core.AttachmentURLPrefix + att.ID}}}, nil
}

func (a *API) abortUpload(ctx context.Context, in *uploadAbortInput) (*struct{}, error) {
	_, p, err := a.project(ctx, in.PID)
	if err != nil {
		return nil, err
	}
	if core.IsAttachmentUpload(in.UploadID) {
		err = core.AbortAttachmentUpload(ctx, a.e.Core.Pool, a.e.Core.Blobs, core.OwnerOfProject(p.ID), in.UploadID)
	} else {
		err = sharedfiles.AbortUpload(ctx, a.e.Core.Pool, a.e.Core.Blobs, p.ID, in.UploadID)
	}
	return nil, uploadFail(err)
}

type userUploadCreateInput struct {
	Body UploadCreate
}

type userUploadPartsInput struct {
	UploadID string `path:"upload_id" maxLength:"64"`
	Body     UploadPartsAsk
}

type userUploadCompleteInput struct {
	UploadID string `path:"upload_id" maxLength:"64"`
	Body     UploadComplete
}

type userUploadAbortInput struct {
	UploadID string `path:"upload_id" maxLength:"64"`
}

func (a *API) createUserUpload(ctx context.Context, in *userUploadCreateInput) (*uploadCreateOutput, error) {
	u, err := a.user(ctx)
	if err != nil {
		return nil, err
	}
	if in.Body.Kind != "attachment" {
		return nil, httpapi.Invalid("kind", "没有项目时只能上传附件（kind=attachment）；共享文件要传到项目里")
	}
	name := uploadName(in.Body.Name)
	if name == "" || name == "." || name == "/" {
		return nil, httpapi.Invalid("name", "文件名是空的")
	}
	out := UploadSession{Kind: in.Body.Kind, PartSize: blobstore.PartSize, Parts: blobstore.PartCount(in.Body.Size),
		ExpiresAt: time.Now().Add(uploadKeep)}
	return a.beginAttachment(ctx, "", u.ID, name, in.Body.Size, out)
}

func (a *API) userOwner(ctx context.Context) (core.AttachmentOwner, error) {
	u, err := a.user(ctx)
	if err != nil {
		return core.AttachmentOwner{}, err
	}
	return core.AttachmentOwner{UserID: u.ID}, nil
}

func (a *API) userUploadParts(ctx context.Context, in *userUploadPartsInput) (*uploadPartsOutput, error) {
	owner, err := a.userOwner(ctx)
	if err != nil {
		return nil, err
	}
	return a.partURLs(ctx, owner, in.UploadID, in.Body.Numbers)
}

func (a *API) completeUserUpload(ctx context.Context, in *userUploadCompleteInput) (*uploadCompleteOutput, error) {
	owner, err := a.userOwner(ctx)
	if err != nil {
		return nil, err
	}
	if !core.IsAttachmentUpload(in.UploadID) {
		return nil, uploadFail(core.ErrNotFound)
	}
	return a.completeAttachment(ctx, owner, in.UploadID, in.Body.Parts)
}

func (a *API) abortUserUpload(ctx context.Context, in *userUploadAbortInput) (*struct{}, error) {
	owner, err := a.userOwner(ctx)
	if err != nil {
		return nil, err
	}
	if !core.IsAttachmentUpload(in.UploadID) {
		return nil, uploadFail(core.ErrNotFound)
	}
	return nil, uploadFail(core.AbortAttachmentUpload(ctx, a.e.Core.Pool, a.e.Core.Blobs, owner, in.UploadID))
}

// contentDisposition is a Content-Disposition for name that every browser reads right, Chinese
// names included: an ASCII fallback and the RFC 5987 filename* (no header injection either way).
func contentDisposition(kind, name string) string {
	fallback := make([]rune, 0, len(name))
	for _, r := range name {
		if r < 0x20 || r >= 0x7f || r == '"' || r == '\\' || r == '%' || r == ';' {
			r = '_'
		}
		fallback = append(fallback, r)
	}
	return fmt.Sprintf(`%s; filename="%s"; filename*=UTF-8''%s`, kind, string(fallback), pctEncode(name))
}

// pctEncode keeps the RFC 5987 attr-chars and percent-encodes every other byte.
func pctEncode(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("!#$&+-.^_`|~", c) >= 0 {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}

// presignedGet signs a page download of an object: safe types (images, PDF) inline with their type
// unless download is asked, everything else as an application/octet-stream attachment.
func (a *API) presignedGet(ctx context.Context, key, name, contentType string, download bool) (string, error) {
	base, _, _ := strings.Cut(contentType, ";")
	base = strings.TrimSpace(base)
	o := blobstore.GetOptions{ContentType: "application/octet-stream", ContentDisposition: contentDisposition("attachment", name)}
	if core.SafeInline[base] {
		o.ContentType = base
		if !download {
			o.ContentDisposition = contentDisposition("inline", name)
		}
	}
	return a.e.Core.Blobs.PresignGet(ctx, key, pageGetTTL, o)
}

// NodeAttachmentURL is GET /nodes/attachments/{id}/url (design §4.2): a daemon fetches the object
// itself when it delivers the message. url is null for attachments still in the database (rows
// from before #58): the daemon falls back to GET /nodes/attachments/{id}.
type NodeAttachmentURL struct {
	URL         *string    `json:"url"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	Size        int        `json:"size"`
	SHA256      string     `json:"sha256,omitempty" doc:"Empty until computed: check the size only"`
	Name        string     `json:"name"`
	ContentType string     `json:"content_type"`
}

// nodeError is the {detail, code} body of the raw node routes (the daemon passes detail to the AI).
func nodeError(c *gin.Context, status int, code, detail string) {
	c.JSON(status, gin.H{"detail": detail, "code": code, "status": status})
}

func (a *API) nodeAttachmentURL(c *gin.Context) {
	ctx := c.Request.Context()
	node, err := a.e.Gateway.Authenticate(c.Request)
	if err != nil {
		nodeError(c, http.StatusUnauthorized, "unauthorized", "token rejected")
		return
	}
	att, err := core.AttachmentOfUser(ctx, a.e.Core.Pool, node.UserID, c.Param("id"))
	switch {
	case errors.Is(err, core.ErrAttachmentExpired):
		nodeError(c, http.StatusGone, "attachment_expired", fmt.Sprintf("附件已过期：%s 超过保留期没有再使用，文件已删除；请让用户重新上传", att.Name))
		return
	case err != nil:
		nodeError(c, http.StatusNotFound, "not_found", "没有这个附件")
		return
	}
	if err := core.TouchAttachments(ctx, a.e.Core.Pool, att.ProjectID, []string{att.ID}, a.e.Core.AttachmentTTL); err != nil {
		nodeError(c, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	out := NodeAttachmentURL{Size: att.Size, SHA256: att.SHA256, Name: att.Name, ContentType: att.ContentType}
	if att.StorageKey != nil {
		u, err := a.e.Core.Blobs.PresignGet(ctx, *att.StorageKey, nodeGetTTL, blobstore.GetOptions{})
		if errors.Is(err, blobstore.ErrNotConfigured) {
			nodeError(c, http.StatusServiceUnavailable, "storage_unavailable", "文件存储未配置")
			return
		}
		if err != nil {
			nodeError(c, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		exp := time.Now().Add(nodeGetTTL)
		out.URL, out.ExpiresAt = &u, &exp
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, out)
}

// NodeUploadRequest is POST /nodes/uploads (design §4.3): a daemon asks for a presigned PUT before
// it calls write_shared_file with upload: {upload_id}.
// The daemon (node/src/toolhost/transfer.ts UploadRequest) sends the target shared path as name;
// path is accepted as well (name wins when both are given, design §4.3). call_id and content_type are informative
// (the type comes from the path).
type NodeUploadRequest struct {
	SessionID   string `json:"session_id"`
	CallID      string `json:"call_id,omitempty"`
	Name        string `json:"name,omitempty"`
	Path        string `json:"path,omitempty"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`
	ContentType string `json:"content_type,omitempty"`
}

// NodeUpload is the answer: PUT the file to url with exactly these headers before expires_at.
type NodeUpload struct {
	UploadID  string            `json:"upload_id"`
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers"`
	ExpiresAt time.Time         `json:"expires_at"`
}

func validSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func (a *API) nodeUpload(c *gin.Context) {
	ctx := c.Request.Context()
	node, err := a.e.Gateway.Authenticate(c.Request)
	if err != nil {
		nodeError(c, http.StatusUnauthorized, "unauthorized", "token rejected")
		return
	}
	var in NodeUploadRequest
	if err := json.NewDecoder(io.LimitReader(c.Request.Body, 64<<10)).Decode(&in); err != nil {
		nodeError(c, http.StatusBadRequest, "bad_request", "请求不是合法的 JSON")
		return
	}
	if in.Name != "" {
		in.Path = in.Name
	}
	if in.Path == "" {
		nodeError(c, http.StatusUnprocessableEntity, "validation", "缺少目标路径（name）")
		return
	}
	in.SHA256 = strings.ToLower(in.SHA256)
	if !validSHA256(in.SHA256) {
		nodeError(c, http.StatusUnprocessableEntity, "validation", "sha256 要是 64 位十六进制")
		return
	}
	sc, err := core.SessionContextOf(ctx, a.e.Core.Pool, in.SessionID)
	if err != nil || sc.Session.NodeID == nil || *sc.Session.NodeID != node.ID || sc.OwnerID != node.UserID || sc.Session.State == "closed" {
		// Not 404: the daemon reads 404 as "an older console without this route" and falls back.
		nodeError(c, http.StatusConflict, "session_not_on_node", "这个会话不在这个节点上（可能已经关闭或换了节点），不能申请上传")
		return
	}
	emp := sc.Employee
	var up sharedfiles.Upload
	err = a.e.Core.InTx(ctx, func(tx *core.Tx) error {
		var err error
		up, err = sharedfiles.BeginUpload(ctx, tx, a.e.Core.Blobs, emp.ProjectID, sharedfiles.BeginRequest{Path: in.Path, Size: in.Size,
			SHA256: in.SHA256, Overwrite: true, By: sharedfiles.Uploader{Name: emp.Name, EmployeeID: emp.ID, ThreadID: emp.WorkThreadID()}})
		return err
	})
	if err == nil {
		var u string
		var headers map[string]string
		if u, headers, err = a.e.Core.Blobs.PresignPut(ctx, up.StorageKey, sharedfiles.UploadTTL, up.Size, up.ContentType); err == nil {
			c.Header("Cache-Control", "no-store")
			c.JSON(http.StatusCreated, NodeUpload{UploadID: up.ID, URL: u, Headers: headers, ExpiresAt: time.Now().Add(sharedfiles.UploadTTL)})
			return
		}
		// No URL: the upload just begun would hold the project's quota until blobgc expires it.
		if e := sharedfiles.AbortUpload(context.WithoutCancel(ctx), a.e.Core.Pool, a.e.Core.Blobs, emp.ProjectID, up.ID); e != nil {
			slog.Warn("drop a node upload without a URL", "upload", up.ID, "err", e)
		}
	}
	var apiErr *httpapi.APIError
	if errors.As(uploadFail(err), &apiErr) {
		if apiErr.Status == http.StatusNotFound { // see above: never 404 on this route
			apiErr.Status, apiErr.Code = http.StatusConflict, "project_not_found"
		}
		nodeError(c, apiErr.Status, apiErr.Code, apiErr.Detail)
		return
	}
	nodeError(c, http.StatusInternalServerError, "internal", "internal error")
}
