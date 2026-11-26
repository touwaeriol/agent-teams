package toolsvc

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/blobstore"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/orchestrator"
)

// Attachments in tools (#58 design §6.3, §7): assign_task, send_message and the other messaging
// tools take attachments=[img_…] and deliver them with the message; read_attachment downloads one
// into the session's .attachments/ (with the shared folder, a link to the project's attachments/).

// AttachmentURLTTL is how long the presigned GET a daemon gets for read_attachment stays valid
// (it downloads at once).
const AttachmentURLTTL = 15 * time.Minute

// useAttachments checks a tool's attachments argument: every id an attachment of the caller's
// project that is ready (not expired, not still uploading); their retention restarts (a hand-over
// is a use). Duplicate ids count once. The refs come back in the order given.
func (s *Service) useAttachments(ctx context.Context, tx *core.Tx, c *call, ids protocol.AttachmentIds) ([]core.AttachmentRef, error) {
	list, err := s.loadAttachments(ctx, tx, c, ids)
	if err != nil {
		return nil, err
	}
	out := make([]core.AttachmentRef, len(list))
	for i, a := range list {
		out[i] = a.Ref()
	}
	return out, nil
}

func (s *Service) loadAttachments(ctx context.Context, tx *core.Tx, c *call, ids protocol.AttachmentIds) ([]core.Attachment, error) {
	var uniq []string
	for _, id := range ids {
		if !slices.Contains(uniq, string(id)) {
			uniq = append(uniq, string(id))
		}
	}
	if len(uniq) == 0 {
		return nil, nil
	}
	if len(uniq) > core.MaxToolAttachments {
		return nil, refuse("一次最多带 %d 个附件", core.MaxToolAttachments)
	}
	pid := c.emp().ProjectID
	list, err := core.UseAttachments(ctx, tx, pid, uniq, s.core.AttachmentTTL)
	switch {
	case errors.Is(err, core.ErrAttachmentExpired):
		return nil, refuse("%s。请让用户重新上传后再发", err.Error())
	case errors.Is(err, core.ErrNotFound):
		return nil, refuse("%s", s.missingAttachments(ctx, tx, pid, uniq))
	case err != nil:
		return nil, err
	}
	return list, nil
}

// missingAttachments explains which ids are not ready attachments of the project.
func (s *Service) missingAttachments(ctx context.Context, tx *core.Tx, projectID string, ids []string) string {
	var bad []string
	for _, id := range ids {
		list, err := core.ProjectAttachments(ctx, tx, projectID, []string{id})
		if err != nil || len(list) == 0 || list[0].State != "ready" {
			bad = append(bad, id)
		}
	}
	if len(bad) == 0 {
		bad = ids
	}
	return fmt.Sprintf("附件 %s 不存在、不属于这个项目或还没上传完（用消息 [附件] 块里的 img_… id）", strings.Join(bad, "、"))
}

// sessionNodeV2 is the node of the calling session when its daemon has attachments_v2; else a
// refusal explaining why the tool cannot work there.
func (s *Service) sessionNodeV2(ctx context.Context, tx *core.Tx, c *call, tool string) (string, error) {
	sess, err := core.SessionByID(ctx, tx, c.sc.Session.ID)
	if err != nil {
		return "", err
	}
	if sess.NodeID == nil {
		return "", refuse("会话不在任何节点上，没有工作目录可以放附件")
	}
	ok, err := core.NodeHasFeature(ctx, tx, *sess.NodeID, orchestrator.FeatureAttachmentsV2)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", refuse("节点守护进程版本过旧，不支持 %s：请升级节点守护进程", tool)
	}
	return *sess.NodeID, nil
}

// readAttachment hands the daemon a short-lived download of one attachment (attachment_download);
// the daemon writes it to <cwd>/.attachments/<id>-<name>, puts the real path in local_path and
// never shows the AI the URL. Rows from before #58 (bytes in the database) have no URL: the daemon
// fetches GET /nodes/attachments/<id> instead.
func (s *Service) readAttachment(ctx context.Context, tx *core.Tx, c *call) (Result, error) {
	var in protocol.ReadAttachmentInput
	if err := c.decode(&in); err != nil {
		return Result{}, err
	}
	if _, err := s.sessionNodeV2(ctx, tx, c, "read_attachment"); err != nil {
		return Result{}, err
	}
	list, err := s.loadAttachments(ctx, tx, c, protocol.AttachmentIds{in.ID})
	if err != nil {
		return Result{}, err
	}
	a := list[0]
	ref := protocol.AttachmentRef{ID: a.ID, Name: a.Name, ContentType: a.ContentType, Size: a.Size}
	if a.SHA256 != "" {
		sum := a.SHA256
		ref.SHA256 = &sum
	}
	dl := &protocol.ToolAttachmentDownload{Attachment: ref}
	if a.StorageKey != nil {
		url, err := blobstore.Or(s.core.Blobs).PresignGet(ctx, *a.StorageKey, AttachmentURLTTL,
			blobstore.GetOptions{ContentType: "application/octet-stream"})
		if errors.Is(err, blobstore.ErrNotConfigured) {
			return Result{}, refuse("平台的文件存储还没有配置，暂时取不了附件")
		}
		if err != nil {
			return Result{}, err
		}
		exp := time.Now().Add(AttachmentURLTTL).UTC()
		dl.URL, dl.ExpiresAt = &url, &exp
	}
	out := protocol.ReadAttachmentResult{ID: protocol.AttachmentID(a.ID), Name: a.Name, ContentType: a.ContentType, Size: a.Size,
		SHA256: ref.SHA256, LocalPath: ".attachments/" + a.ID}
	text := fmt.Sprintf("附件 %s（%s，%s，%s）正在下载到工作目录的 .attachments/ 下（不会被 git 提交），用文件或命令行工具读取。",
		a.ID, a.Name, a.ContentType, human(int64(a.Size)))
	return Result{OK: true, Text: text, Structured: out, AttachmentDownload: dl}, nil
}

// carried tells the caller how many attachments went along (appended to a tool's result text).
func carried(files []core.AttachmentRef) string {
	if len(files) == 0 {
		return ""
	}
	return fmt.Sprintf("（附带 %d 个附件）", len(files))
}

// human is a byte count for people (1.5 MB).
func human(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
