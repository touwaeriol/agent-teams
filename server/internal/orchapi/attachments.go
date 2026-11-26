package orchapi

import (
	"errors"

	"io"

	"context"
	"encoding/base64"
	"net/http"
	"strconv"

	"github.com/danielgtaylor/huma/v2"
	"github.com/gin-gonic/gin"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/httpapi"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw"
)

// AttachmentBody is POST /api/projects/{pid}/attachments: one image, PDF or text file for a message.
type AttachmentBody struct {
	Name string `json:"name" minLength:"1" maxLength:"300" doc:"File name, shown under the thumbnail; a text file is recognised by its extension"`
	Data string `json:"data" minLength:"1" maxLength:"6990508" doc:"Base64 of a PNG, JPEG, GIF or WebP image or a PDF (at most 5 MB), or a UTF-8 text file (at most 1 MB)"`
}

type attachmentInput struct {
	PID  string `path:"pid" maxLength:"64"`
	Body AttachmentBody
}

// AttachmentView is an uploaded image; url serves it to the signed-in owner.
type AttachmentView struct {
	core.Attachment
	URL string `json:"url"`
}

type attachmentOutput struct{ Body AttachmentView }

type attachmentFileInput struct {
	ID       string `path:"id" maxLength:"64"`
	Download bool   `query:"download" doc:"Always as an attachment (the page's download button)"`
}

type attachmentFileOutput struct {
	Status       int
	Location     string `header:"Location"`
	ContentType  string `header:"Content-Type"`
	CacheControl string `header:"Cache-Control"`
	NoSniff      string `header:"X-Content-Type-Options"`
	CSP          string `header:"Content-Security-Policy"`
	Body         []byte
}

func (a *API) registerAttachments(api huma.API, r gin.IRoutes) {
	t := "conversations"
	huma.Register(api, op("upload-attachment", "POST", "/api/projects/{pid}/attachments", t, status(201),
		summary("Upload a file to attach to a message: an image or PDF (at most 5 MB) or a text file (at most 1 MB)"),
		func(o *huma.Operation) { o.MaxBodyBytes = 8 << 20 }), a.uploadAttachment)
	huma.Register(api, op("get-attachment", "GET", "/api/attachments/{id}", t,
		summary("An attachment: 302 to a presigned URL valid 5 minutes (images and PDF inline, anything else downloaded as application/octet-stream); the bytes themselves for attachments from before #58; 410 once expired"),
		func(o *huma.Operation) {
			o.Responses = map[string]*huma.Response{
				"200": {Description: "The file (attachments still kept in the database)", Content: map[string]*huma.MediaType{
					"image/png": {}, "image/jpeg": {}, "image/gif": {}, "image/webp": {}, "application/pdf": {}, "text/plain": {}}},
				"302": {Description: "Redirect to the object store", Headers: map[string]*huma.Param{
					"Location": {Description: "Presigned URL", Schema: &huma.Schema{Type: "string"}}}},
			}
		}), a.attachmentFile)
	huma.Register(api, op("save-attachment-shared", "POST", "/api/attachments/{id}/save-shared", t,
		summary("Keep an attachment in its project's files (409 file_exists unless overwrite, 409 folder_unavailable without a shared folder, 410 attachment_expired; 404 for one still uploading or not yet in a project)")),
		a.saveAttachmentFile)
	if a.e == nil {
		r.GET("/nodes/attachments/:id", offline)
		return
	}
	r.GET("/nodes/attachments/:id", a.nodeAttachment)
}

func (a *API) uploadAttachment(ctx context.Context, in *attachmentInput) (*attachmentOutput, error) {
	u, p, err := a.project(ctx, in.PID)
	if err != nil {
		return nil, err
	}
	data, err := base64.StdEncoding.DecodeString(in.Body.Data)
	if err != nil || len(data) == 0 {
		return nil, httpapi.Invalid("data", "图片内容不对")
	}
	att, err := core.NewAttachment(p.ID, u.ID, uploadName(in.Body.Name), data)
	if err != nil {
		return nil, fail(err, "")
	}
	if att, err = core.InsertAttachment(ctx, a.e.Core.Pool, a.e.Core.Blobs, a.e.Core.AttachmentTTL, att, data); err != nil {
		return nil, fail(err, "")
	}
	return &attachmentOutput{Body: AttachmentView{Attachment: att, URL: core.AttachmentURLPrefix + att.ID}}, nil
}

func (a *API) attachmentFile(ctx context.Context, in *attachmentFileInput) (*attachmentFileOutput, error) {
	u, err := a.user(ctx)
	if err != nil {
		return nil, err
	}
	att, err := core.AttachmentOfUser(ctx, a.e.Core.Pool, u.ID, in.ID)
	if err != nil {
		return nil, fail(err, "没有这个附件")
	}
	// A page download or preview is a use: the retention starts again.
	if err := core.TouchAttachments(ctx, a.e.Core.Pool, att.ProjectID, []string{att.ID}, a.e.Core.AttachmentTTL); err != nil {
		return nil, err
	}
	if att.StorageKey != nil {
		loc, err := a.presignedGet(ctx, *att.StorageKey, att.Name, att.ContentType, in.Download)
		if err != nil {
			return nil, fail(err, "")
		}
		// Shorter than the signature, so a cached redirect never points at an expired URL.
		return &attachmentFileOutput{Status: http.StatusFound, Location: loc, CacheControl: "private, max-age=240"}, nil
	}
	att, data, err := core.AttachmentData(ctx, a.e.Core.Pool, a.e.Core.Blobs, u.ID, in.ID)
	if err != nil {
		return nil, fail(err, "没有这个附件")
	}
	typ := att.ContentType
	if core.IsInlineText(typ) {
		typ += "; charset=utf-8"
	}
	return &attachmentFileOutput{Status: http.StatusOK, ContentType: typ, CacheControl: "private, max-age=31536000, immutable",
		NoSniff: "nosniff", CSP: "default-src 'none'; sandbox", Body: data}, nil
}

// nodeAttachment is GET /nodes/attachments/{id}: a daemon fetches an image of its owner's projects.
func (a *API) nodeAttachment(c *gin.Context) {
	w, r := c.Writer, c.Request
	node, err := a.e.Gateway.Authenticate(r)
	if err != nil {
		nodegw.WriteAuthError(w, err)
		return
	}
	// Streamed: the object never sits in the control plane's memory (old daemons; new ones fetch
	// a presigned URL instead, #58 D2).
	att, rc, err := core.OpenAttachment(r.Context(), a.e.Core.Pool, a.e.Core.Blobs, node.UserID, c.Param("id"))
	if errors.Is(err, core.ErrAttachmentExpired) {
		http.Error(w, "attachment expired", http.StatusGone)
		return
	}
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	defer rc.Close()
	if err := core.TouchAttachments(r.Context(), a.e.Core.Pool, att.ProjectID, []string{att.ID}, a.e.Core.AttachmentTTL); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", att.ContentType)
	w.Header().Set("Content-Length", strconv.Itoa(att.Size))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.Copy(w, rc)
}

// SaveSharedBody is POST /api/attachments/{id}/save-shared.
type SaveSharedBody struct {
	Path      string `json:"path" minLength:"1" maxLength:"1024" doc:"Where to save it, relative to the project's folder"`
	Overwrite bool   `json:"overwrite,omitempty" doc:"Replace a file at the path (default false: 409 file_exists)"`
}

type saveSharedInput struct {
	ID   string `path:"id" maxLength:"64"`
	Body SaveSharedBody
}

type saveSharedOutput struct{ Body FileEntry }
