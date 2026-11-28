package orchapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/gin-gonic/gin"

	"github.com/touwaeriol/agent-teams/server/internal/blobstore"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/httpapi"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw"
	"github.com/touwaeriol/agent-teams/server/internal/store"
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
	Kind string `json:"kind" enum:"attachment" doc:"attachment: a file for a message"`
	Name string `json:"name" minLength:"1" maxLength:"300" doc:"File name, shown with the attachment"`
	Size int64  `json:"size" minimum:"0" doc:"Exact size in bytes (at most 200 MB: more answers 413 file_too_large); the object must have it at completion"`
}

// UploadSession is an upload in flight: PUT each part (part_size bytes, the last one shorter) to
// the URLs of POST …/parts, then POST …/complete with the ETags.
type UploadSession struct {
	UploadID  string    `json:"upload_id" doc:"img_…, also the attachment's id"`
	Kind      string    `json:"kind" enum:"attachment"`
	ID        string    `json:"id" doc:"The attachment id"`
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
	Kind       string          `json:"kind" enum:"attachment"`
	Attachment *AttachmentView `json:"attachment,omitempty"`
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
		summary("Start a presigned upload of an attachment (any type, at most 200 MB)")),
		a.createUpload)
	huma.Register(api, op("upload-part-urls", "POST", "/api/projects/{pid}/uploads/{upload_id}/parts", t,
		summary("Presigned PUT URLs for parts of an upload (1 hour)")), a.uploadParts)
	huma.Register(api, op("complete-upload", "POST", "/api/projects/{pid}/uploads/{upload_id}/complete", t,
		summary("Finish an upload: the object's size is checked and its type sniffed")),
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
		return
	}
	r.GET("/nodes/attachments/:id/url", a.nodeAttachmentURL)
}

// uploadFail maps the upload errors.
func uploadFail(err error) error {
	switch {
	case errors.Is(err, core.ErrUploadIncomplete):
		return httpapi.FailErr(http.StatusConflict, "upload_incomplete", err)
	case errors.Is(err, core.ErrUploadSize):
		return httpapi.FailErr(http.StatusUnprocessableEntity, "size_mismatch", err)
	case errors.Is(err, core.ErrAttachmentTooLarge):
		return httpapi.FailErr(http.StatusRequestEntityTooLarge, "file_too_large", err)
	case errors.Is(err, core.ErrNotFound):
		return httpapi.Fail(http.StatusNotFound, "not_found", "没有这个上传（已完成、已取消或超过 24 小时已清理），请重新上传")
	case errors.Is(err, blobstore.ErrNotConfigured):
		return httpapi.Fail(http.StatusServiceUnavailable, "storage_unavailable", "文件存储未配置")
	}
	return fail(err, "没有这个上传")
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
	return a.beginAttachment(ctx, p.ID, u.ID, name, in.Body.Size, out)
}

func (a *API) beginAttachment(ctx context.Context, pid, uid, name string, size int64, out UploadSession) (*uploadCreateOutput, error) {
	att, err := core.BeginAttachmentUpload(ctx, a.e.Core.Pool, a.e.Core.Blobs, a.e.Core.AttachmentTTL, pid, uid, name, size)
	if err != nil {
		return nil, uploadFail(err)
	}
	out.UploadID, out.ID = att.ID, att.ID
	return &uploadCreateOutput{Body: out}, nil
}

// inFlight is an attachment upload's object, its S3 upload id and its declared size.
func (a *API) inFlight(ctx context.Context, owner core.AttachmentOwner, id string) (key, uploadID string, size int64, err error) {
	if !core.IsAttachmentUpload(id) {
		return "", "", 0, core.ErrNotFound
	}
	att, upID, err := core.UploadingAttachment(ctx, a.e.Core.Pool, owner, id)
	if err != nil {
		return "", "", 0, err
	}
	return *att.StorageKey, upID, int64(att.Size), nil
}

func (a *API) uploadParts(ctx context.Context, in *uploadPartsInput) (*uploadPartsOutput, error) {
	_, p, err := a.project(ctx, in.PID)
	if err != nil {
		return nil, err
	}
	return a.partURLs(ctx, core.OwnerOfProject(p.ID), in.UploadID, in.Body.Numbers)
}

func (a *API) partURLs(ctx context.Context, owner core.AttachmentOwner, id string, numbers []int32) (*uploadPartsOutput, error) {
	key, uploadID, size, err := a.inFlight(ctx, owner, id)
	if err != nil {
		return nil, uploadFail(err)
	}
	n := blobstore.PartCount(size)
	out := UploadParts{URLs: make([]PartURL, 0, len(numbers)), ExpiresAt: time.Now().Add(partURLTTL)}
	for _, num := range numbers {
		if num < 1 || num > n {
			return nil, httpapi.Invalidf("numbers", "分片号要在 1 到 {max} 之间（收到 {got}）", "max", n, "got", num)
		}
		// The part's exact length is signed: the declared size is what the store accepts (#62 F2).
		u, err := a.e.Core.Blobs.PresignPart(ctx, key, uploadID, num, blobstore.PartLength(size, num), partURLTTL)
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
	return a.completeAttachment(ctx, core.OwnerOfProject(p.ID), in.UploadID, in.Body.Parts)
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
	if !core.IsAttachmentUpload(in.UploadID) {
		return nil, uploadFail(core.ErrNotFound)
	}
	return nil, uploadFail(core.AbortAttachmentUpload(ctx, a.e.Core.Pool, a.e.Core.Blobs, core.OwnerOfProject(p.ID), in.UploadID))
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
		return nil, httpapi.Invalid("kind", "只能上传附件（kind=attachment）")
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

// nodeAuth authenticates the daemon of a raw node route; on failure it has answered with
// nodegw.AuthStatus in the {detail, code} body.
func (a *API) nodeAuth(c *gin.Context) (store.Node, bool) {
	node, err := a.e.Gateway.Authenticate(c.Request)
	if err == nil {
		return node, true
	}
	status, code := nodegw.AuthStatus(c.Writer, err)
	nodeError(c, status, code, strings.ReplaceAll(code, "_", " "))
	return node, false
}

func (a *API) nodeAttachmentURL(c *gin.Context) {
	ctx := c.Request.Context()
	node, ok := a.nodeAuth(c)
	if !ok {
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
