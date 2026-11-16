package orchapi

import (
	"context"
	"encoding/base64"
	"net/http"
	"strconv"

	"github.com/danielgtaylor/huma/v2"
	"github.com/gin-gonic/gin"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/httpapi"
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
	ID string `path:"id" maxLength:"64"`
}

type attachmentFileOutput struct {
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
		summary("The bytes of an attachment"), func(o *huma.Operation) {
			o.Responses = map[string]*huma.Response{"200": {Description: "The file", Content: map[string]*huma.MediaType{
				"image/png": {}, "image/jpeg": {}, "image/gif": {}, "image/webp": {}, "application/pdf": {}, "text/plain": {}}}}
		}), a.attachmentFile)
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
	if att, err = core.InsertAttachment(ctx, a.e.Core.Pool, att, data); err != nil {
		return nil, err
	}
	return &attachmentOutput{Body: AttachmentView{Attachment: att, URL: core.AttachmentURLPrefix + att.ID}}, nil
}

func (a *API) attachmentFile(ctx context.Context, in *attachmentFileInput) (*attachmentFileOutput, error) {
	u, err := a.user(ctx)
	if err != nil {
		return nil, err
	}
	att, data, err := core.AttachmentData(ctx, a.e.Core.Pool, u.ID, in.ID)
	if err != nil {
		return nil, fail(err, "没有这张图片")
	}
	typ := att.ContentType
	if core.IsInlineText(typ) {
		typ += "; charset=utf-8"
	}
	return &attachmentFileOutput{ContentType: typ, CacheControl: "private, max-age=31536000, immutable",
		NoSniff: "nosniff", CSP: "default-src 'none'; sandbox", Body: data}, nil
}

// nodeAttachment is GET /nodes/attachments/{id}: a daemon fetches an image of its owner's projects.
func (a *API) nodeAttachment(c *gin.Context) {
	w, r := c.Writer, c.Request
	node, err := a.e.Gateway.Authenticate(r)
	if err != nil {
		http.Error(w, "token rejected", http.StatusUnauthorized)
		return
	}
	att, data, err := core.AttachmentData(r.Context(), a.e.Core.Pool, node.UserID, c.Param("id"))
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", att.ContentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}
