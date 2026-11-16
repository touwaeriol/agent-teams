package orchapi

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/gin-gonic/gin"

	"github.com/touwaeriol/agent-teams/server/internal/artifacts"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/httpapi"
	"github.com/touwaeriol/agent-teams/server/internal/orchestrator"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// WithOrigins sets where published files are served: artifactOrigin is
// AGENT_TEAMS_ARTIFACT_ORIGIN ("" = /files/ on the console) and publicURL the web app,
// which is the only page allowed to frame them.
func (a *API) WithOrigins(artifactOrigin, publicURL string) *API {
	a.artifactOrigin, a.publicURL = artifactOrigin, publicURL
	return a
}

func (a *API) registerFiles(api huma.API, r gin.IRoutes) {
	t := "deliverables"
	huma.Register(api, op("deliverable-files", "GET", "/api/deliverables/{id}/files", t,
		summary("Files of one version (default: latest) with a short-lived signed URL, and the version list")), a.deliverableFiles)
	huma.Register(api, op("list-deliverable-comments", "GET", "/api/deliverables/{id}/comments", t), a.listComments)
	huma.Register(api, op("add-deliverable-comment", "POST", "/api/deliverables/{id}/comments", t, status(201),
		summary("Comment on a deliverable; delivered to the employee who published it (or the thread lead)")), a.addComment)
	if a.e == nil {
		return
	}
	a.files = artifacts.NewServer(a.e.Core.Pool, artifacts.NewSigner(a.fileKeys()), a.artifactOrigin, originOf(a.publicURL), slog.Default())
	r.GET(artifacts.Prefix+"*path", gin.WrapH(a.files))
	r.HEAD(artifacts.Prefix+"*path", gin.WrapH(a.files))
}

// fileKeys derive from the platform secret; without one a random per-process key is used.
func (a *API) fileKeys() [][]byte {
	if a.e.GH != nil && a.e.GH.Box != nil && a.e.GH.Box.Ready() {
		return a.e.GH.Box.DeriveAll("artifact-url")
	}
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	return [][]byte{key}
}

func originOf(publicURL string) string {
	scheme, rest, ok := strings.Cut(publicURL, "://")
	if !ok {
		return ""
	}
	host, _, _ := strings.Cut(rest, "/")
	return scheme + "://" + host
}

type filesInput struct {
	ID      string `path:"id" maxLength:"64"`
	Version int    `query:"version" minimum:"0"`
}

// DeliverableFiles is one version of a hosted deliverable.
type DeliverableFiles struct {
	DeliverableID string                      `json:"deliverable_id"`
	Version       int                         `json:"version"`
	Versions      []artifacts.ArtifactVersion `json:"versions"`
	Entry         string                      `json:"entry"`
	Render        string                      `json:"render" enum:"html,markdown,image,pdf,text"`
	Files         []artifacts.ArtifactFile    `json:"files"`
	BaseURL       string                      `json:"base_url" doc:"Signed directory URL; append a file path"`
	EntryURL      string                      `json:"entry_url"`
	ExpiresAt     time.Time                   `json:"expires_at"`
	Isolated      bool                        `json:"isolated" doc:"Files have their own origin (AGENT_TEAMS_ARTIFACT_ORIGIN)"`
}

type filesOutput struct{ Body DeliverableFiles }

func (a *API) deliverableFiles(ctx context.Context, in *filesInput) (*filesOutput, error) {
	d, err := a.ownDeliverable(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	versions, err := artifacts.Versions(ctx, a.e.Core.Pool, d.ID)
	if err != nil {
		return nil, err
	}
	if len(versions) == 0 {
		return nil, httpapi.Fail(http.StatusNotFound, "no_files", "这个交付物没有托管的文件")
	}
	version := pickVersion(versions, in.Version)
	if version == 0 {
		return nil, httpapi.Fail(http.StatusNotFound, "not_found", "没有这个版本")
	}
	files, err := artifacts.Files(ctx, a.e.Core.Pool, d.ID, version)
	if err != nil {
		return nil, err
	}
	return &filesOutput{Body: a.filesBody(d.ID, version, versions, files)}, nil
}

func pickVersion(versions []artifacts.ArtifactVersion, want int) int {
	if want == 0 {
		return versions[0].Version
	}
	for _, v := range versions {
		if v.Version == want {
			return want
		}
	}
	return 0
}

func (a *API) filesBody(id string, version int, versions []artifacts.ArtifactVersion, files []artifacts.ArtifactFile) DeliverableFiles {
	paths := make([]string, len(files))
	for i, f := range files {
		paths[i] = f.Path
	}
	entry := artifacts.Entry(paths)
	render := "text"
	for _, f := range files {
		if f.Path == entry {
			render = artifacts.Render(f.ContentType)
		}
	}
	base, exp := a.files.BaseURL(id, version)
	return DeliverableFiles{DeliverableID: id, Version: version, Versions: versions, Entry: entry, Render: render, Files: files,
		BaseURL: base, EntryURL: base + escapePath(entry), ExpiresAt: time.Unix(exp, 0).UTC(), Isolated: a.files.Isolated()}
}

func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

// CommentView is a comment on a deliverable.
type CommentView struct {
	ID        string    `json:"id"`
	Version   *int      `json:"version"`
	Text      string    `json:"text"`
	SentTo    string    `json:"sent_to" doc:"Employee the comment was delivered to; empty when nobody could receive it"`
	CreatedAt time.Time `json:"created_at"`
}

type commentsOutput struct {
	Body struct {
		Items []CommentView `json:"items"`
	}
}

type commentOutput struct{ Body CommentView }

type newCommentInput struct {
	ID   string `path:"id" maxLength:"64"`
	Body struct {
		Text    string `json:"text" minLength:"1" maxLength:"10000"`
		Version *int   `json:"version,omitempty" minimum:"1"`
	}
}

func (a *API) listComments(ctx context.Context, in *deliverableIDInput) (*commentsOutput, error) {
	d, err := a.ownDeliverable(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	out := &commentsOutput{}
	out.Body.Items = []CommentView{}
	err = a.e.Core.Pool.Gorm(ctx).Table("at_deliverable_comments").Select("id, version, text, sent_to, created_at").
		Where("deliverable_id = ?", d.ID).Order("created_at, id").Find(&out.Body.Items).Error
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (a *API) addComment(ctx context.Context, in *newCommentInput) (*commentOutput, error) {
	u, err := a.user(ctx)
	if err != nil {
		return nil, err
	}
	d, err := a.ownDeliverable(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	c := CommentView{ID: core.NewID("dc"), Version: in.Body.Version, Text: strings.TrimSpace(in.Body.Text)}
	err = a.e.Core.InTx(ctx, func(tx *core.Tx) error {
		if c.SentTo, err = a.deliverComment(ctx, tx, d, c); err != nil {
			return err
		}
		return store.QueryRow(ctx, tx, `INSERT INTO at_deliverable_comments (id, deliverable_id, version, author, text, sent_to)
			VALUES ($1,$2,$3,$4,$5,$6) RETURNING created_at`, c.ID, d.ID, c.Version, u.ID, c.Text, c.SentTo).Scan(&c.CreatedAt)
	})
	return &commentOutput{Body: c}, err
}

// deliverComment sends the comment to the publishing employee, else the thread lead;
// "" when the deliverable has no thread or nobody is there any more.
func (a *API) deliverComment(ctx context.Context, tx *core.Tx, d core.Deliverable, c CommentView) (string, error) {
	if d.ThreadID == nil {
		return "", nil
	}
	emp, err := commentRecipient(ctx, tx, d)
	if errors.Is(err, core.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	text := commentText(d, c)
	rec := &core.NewMessage{ConversationID: *d.ThreadID, Role: "user", Author: "user", Kind: "chat", Content: text,
		Meta: map[string]any{"to": emp.Name, "deliverable_id": d.ID}}
	_, err = a.e.Orch.Deliver(ctx, tx, emp, orchestrator.Delivery{Text: text, Kind: "user", Priority: "next", Record: rec})
	return emp.Name, err
}

func commentRecipient(ctx context.Context, tx *core.Tx, d core.Deliverable) (core.Employee, error) {
	if d.PublishedBy != "" && d.PublishedBy != "user" && d.PublishedBy != "platform" {
		emp, err := core.EmployeeByName(ctx, tx, d.ProjectID, d.ThreadID, d.PublishedBy)
		if err == nil || !errors.Is(err, core.ErrNotFound) {
			return emp, err
		}
	}
	return core.Lead(ctx, tx, *d.ThreadID)
}

func commentText(d core.Deliverable, c CommentView) string {
	version := ""
	if c.Version != nil {
		version = fmt.Sprintf(" 第 %d 版", *c.Version)
	}
	text := fmt.Sprintf("[交付物批注] 对「%s」%s的批注：%s", d.Title, version, c.Text)
	if d.Detail["hosted"] == true {
		text += fmt.Sprintf("\n改好后用 publish_file 并带 deliverable_id=%s 发布新版本。", d.ID)
	}
	return text
}
