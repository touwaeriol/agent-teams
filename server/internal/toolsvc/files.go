package toolsvc

import (
	"context"
	"errors"
	"fmt"
	"strings"

	protocol "github.com/touwaeriol/agent-teams/protocol"

	"github.com/touwaeriol/agent-teams/server/internal/artifacts"
	"github.com/touwaeriol/agent-teams/server/internal/core"
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

// publishFile stores the uploaded files as a new deliverable or as the next version of one.
func (s *Service) publishFile(ctx context.Context, tx *core.Tx, c *call) (Result, error) {
	var in protocol.PublishFileInput
	if err := c.decode(&in); err != nil {
		return Result{}, err
	}
	if c.upload == nil {
		return Result{}, refuse("没有收到文件内容：节点守护进程需要升级到支持 publish_file 的版本")
	}
	files, err := artifacts.LoadUpload(ctx, tx, c.sc.Session.ID, c.upload)
	if err != nil {
		return Result{}, refuse("%s", err.Error())
	}
	entry := artifacts.Entry(pathsOf(files))
	if entry == "" {
		return Result{}, refuse("发布目录时目录里需要有 index.html")
	}
	d, version, err := s.fileDeliverable(ctx, tx, c, in, entry, files)
	if err != nil {
		return Result{}, err
	}
	if err := artifacts.Save(ctx, tx, d.ID, version, c.emp().Name, files); err != nil {
		return Result{}, err
	}
	if err := artifacts.DeleteUpload(ctx, tx, string(c.upload.UploadID)); err != nil {
		return Result{}, err
	}
	if err := tx.EmitDeliverable(ctx, d); err != nil {
		return Result{}, err
	}
	text := fmt.Sprintf("已发布交付物 %s 第 %d 版（%d 个文件）。改稿后用 deliverable_id=%s 再发布，会成为新版本。", d.ID, version, len(files), d.ID)
	return okJSON(text, protocol.PublishFileResult{DeliverableID: d.ID, Version: version}), nil
}

// fileDeliverable creates the deliverable (version 1) or moves an earlier one to its next version.
func (s *Service) fileDeliverable(ctx context.Context, tx *core.Tx, c *call, in protocol.PublishFileInput, entry string,
	files []artifacts.ArtifactFile) (core.Deliverable, int, error) {
	if in.DeliverableID == nil {
		detail := fileDetail(c.emp().Name, 1, entry, files, nil)
		d, err := core.InsertDeliverable(ctx, tx, core.NewDeliverable{ProjectID: c.emp().ProjectID, ThreadID: c.emp().ThreadID,
			Kind: strings.TrimSpace(in.Kind), Title: strings.TrimSpace(in.Title), Detail: detail, PublishedBy: c.emp().Name})
		return d, 1, err
	}
	d, err := core.DeliverableByID(ctx, tx, c.sc.OwnerID, *in.DeliverableID)
	if errors.Is(err, core.ErrNotFound) || (err == nil && (d.ProjectID != c.emp().ProjectID || d.Detail["hosted"] != true)) {
		return d, 0, refuse("没有这个用 publish_file 发布的交付物：%s", *in.DeliverableID)
	}
	if err != nil {
		return d, 0, err
	}
	version, err := artifacts.NextVersion(ctx, tx, d.ID)
	if err != nil {
		return d, 0, err
	}
	title := strings.TrimSpace(in.Title)
	d, err = core.UpdateDeliverable(ctx, tx, d.ID, &title, fileDetail(c.emp().Name, version, entry, files, d.Detail))
	return d, version, err
}

// fileDetail is at_deliverables.detail of a hosted deliverable; earlier keys (summary…) are kept.
func fileDetail(by string, version int, entry string, files []artifacts.ArtifactFile, prev map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range prev {
		out[k] = v
	}
	size := 0
	ct := ""
	for _, f := range files {
		size += f.Size
		if f.Path == entry {
			ct = f.ContentType
		}
	}
	out["by"], out["author"], out["hosted"], out["version"] = by, by, true, version
	out["entry"], out["content_type"], out["render"], out["files"], out["size"] = entry, ct, artifacts.Render(ct), len(files), size
	return out
}

func pathsOf(files []artifacts.ArtifactFile) []string {
	out := make([]string, len(files))
	for i, f := range files {
		out[i] = f.Path
	}
	return out
}
