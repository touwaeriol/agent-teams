package toolsvc

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"

	protocol "github.com/touwaeriol/agent-teams/protocol"

	"github.com/touwaeriol/agent-teams/server/internal/artifacts"
	"github.com/touwaeriol/agent-teams/server/internal/blobstore"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/delivery"
)

// HandleChunk stores one file.chunk of a publish_file upload. A malformed chunk is
// logged and dropped (the tool call then fails as incomplete) instead of making the
// link replay it forever.
func (s *Service) HandleChunk(ctx context.Context, m *protocol.FileChunk) error {
	err := artifacts.SaveChunk(ctx, s.core.Pool, m)
	if errors.Is(err, artifacts.ErrBadChunk) {
		s.log.Warn("dropping malformed file.chunk", "session_id", m.SessionID, "upload_id", m.UploadID, "index", m.Index)
		return nil
	}
	return err
}

// saveRefusal turns a shared folder that is not ready yet into a tool refusal (the model retries
// later) instead of a platform failure.
func saveRefusal(err error) error {
	if errors.Is(err, artifacts.ErrFolderNotReady) {
		return refuse("%s", err.Error())
	}
	return err
}

// publishFile stores the uploaded files as a new deliverable or as the next version of one.
func (s *Service) publishFile(ctx context.Context, tx *core.Tx, c *call) (Result, error) {
	var in protocol.PublishFileInput
	if err := c.decode(&in); err != nil {
		return Result{}, err
	}
	if delivery.IsPRKind(in.Kind) {
		return Result{}, delivery.ErrPRKind
	}
	if c.upload == nil {
		return Result{}, refuse("no file content received: the node daemon needs an upgrade to a version with publish_file")
	}
	files, err := artifacts.LoadUpload(ctx, tx, c.sc.Session.ID, c.upload)
	if err != nil {
		return Result{}, refuse("%s", err.Error())
	}
	entry := artifacts.Entry(pathsOf(files))
	if entry == "" {
		return Result{}, refuse("a published directory needs an index.html")
	}
	if ct := artifacts.ContentType(entry); artifacts.Render(ct) == "image" {
		return s.publishImage(ctx, tx, c, in, files[0], ct)
	}
	d, version, err := s.fileDeliverable(ctx, tx, c, in, entry, files)
	if err != nil {
		return Result{}, err
	}
	// Saved only now: fileDeliverable settled the version under the deliverable's row lock.
	if err := s.core.Artifacts().Save(ctx, tx, d.ID, version, artifacts.Publisher{Name: c.emp().Name, EmployeeID: c.emp().ID}, files); err != nil {
		return Result{}, saveRefusal(err)
	}
	if err := artifacts.DeleteUpload(ctx, tx, string(c.upload.UploadID)); err != nil {
		return Result{}, err
	}
	if err := tx.EmitDeliverable(ctx, d); err != nil {
		return Result{}, err
	}
	if err := s.orch.RecordArtifact(ctx, tx, c.emp(), d, version); err != nil {
		return Result{}, err
	}
	text := fmt.Sprintf("Published deliverable %s version %d (%d files). After changes, publish again with deliverable_id=%s for a new version.", d.ID, version, len(files), d.ID)
	return okJSON(text, protocol.PublishFileResult{DeliverableID: d.ID, Version: version}), nil
}

// svgRefusal and imageVersionRefusal explain why a lone image is not a deliverable.
const (
	svgRefusal          = "SVG is not a deliverable and cannot be published on its own. To put it in a report, publish the images with an index.html in one directory as a site with publish_file, or inline them in the HTML (SVG can go straight into the HTML)"
	imageVersionRefusal = "An image cannot be a new version of a deliverable: screenshots and images are not deliverables. To put it in a report, publish the images with an index.html in one directory as a site with publish_file, or inline them in the HTML (SVG can go straight into the HTML)"
)

// publishImage handles a lone bitmap (a screenshot): it is not a deliverable. The image is kept as
// a project attachment and shown in the publisher's conversation as an "image" card; SVG and new
// versions of a deliverable are refused. The upload's chunks are dropped either way.
func (s *Service) publishImage(ctx context.Context, tx *core.Tx, c *call, in protocol.PublishFileInput,
	f artifacts.ArtifactFile, contentType string) (Result, error) {
	if err := artifacts.DeleteUpload(ctx, tx, string(c.upload.UploadID)); err != nil {
		return Result{}, err
	}
	switch {
	case contentType == "image/svg+xml":
		return failText("%s", svgRefusal), nil
	case in.DeliverableID != nil:
		return failText("%s", imageVersionRefusal), nil
	}
	if core.SniffImage(f.Data) == "" {
		return failText("%s has an image extension but is not a PNG, JPEG, GIF or WebP image (a renamed PDF, say), so it was not saved. "+
			"Publish the real file: a PDF or document with its own extension", f.Path), nil
	}
	title := strings.TrimSpace(in.Title)
	a, err := core.NewAttachment(c.emp().ProjectID, c.sc.OwnerID, path.Base(f.Path), f.Data)
	if err != nil {
		return failText("screenshots and images are not deliverables, and this one could not be saved as an attachment either: %s", err.Error()), nil
	}
	if a, err = core.InsertAttachment(ctx, tx, s.core.Blobs, s.core.AttachmentTTL, a, f.Data); errors.Is(err, blobstore.ErrNotConfigured) {
		return failText("screenshots and images are not deliverables; the platform's file storage is not configured, so the image could not be saved as an attachment either"), nil
	} else if err != nil {
		return Result{}, err
	}
	if err := s.orch.RecordImage(ctx, tx, c.emp(), title, a.Ref()); err != nil {
		return Result{}, err
	}
	url := core.AttachmentURLPrefix + a.ID
	text := fmt.Sprintf("Screenshots and images are not deliverables, so it was not published as one. The image is saved as attachment %s in a thread message (%s), where the user sees it. "+
		"To put it in a report, publish the images with an index.html in one directory as a site with publish_file, or inline them in the HTML (SVG can go straight into the HTML); "+
		"refer to attachments only in written reports (attachment id or address): Markdown and site deliverables cannot reach them.", a.ID, url)
	return okText(text), nil
}

// fileDeliverable creates the deliverable (version 1) or moves an earlier one to its next version.
func (s *Service) fileDeliverable(ctx context.Context, tx *core.Tx, c *call, in protocol.PublishFileInput, entry string,
	files []artifacts.ArtifactFile) (core.Deliverable, int, error) {
	if in.DeliverableID == nil {
		detail := artifacts.Detail(c.emp().Name, 1, entry, files, nil)
		d, err := core.InsertDeliverable(ctx, tx, core.NewDeliverable{ProjectID: c.emp().ProjectID, ThreadID: c.emp().WorkThreadID(),
			Kind: strings.TrimSpace(in.Kind), Title: strings.TrimSpace(in.Title), Detail: detail, PublishedBy: c.emp().Name})
		return d, 1, err
	}
	d, err := core.DeliverableByID(ctx, tx, c.sc.OwnerID, *in.DeliverableID)
	if errors.Is(err, core.ErrNotFound) || (err == nil && (d.ProjectID != c.emp().ProjectID || d.Detail["hosted"] != true)) {
		return d, 0, refuse("no deliverable published with publish_file has the id %s", *in.DeliverableID)
	}
	if err != nil {
		return d, 0, err
	}
	version, err := artifacts.NextVersion(ctx, tx, d.ID)
	if err != nil {
		return d, 0, err
	}
	title := strings.TrimSpace(in.Title)
	d, err = core.UpdateDeliverable(ctx, tx, d.ID, &title, artifacts.Detail(c.emp().Name, version, entry, files, d.Detail))
	return d, version, err
}

func pathsOf(files []artifacts.ArtifactFile) []string {
	out := make([]string, len(files))
	for i, f := range files {
		out[i] = f.Path
	}
	return out
}
