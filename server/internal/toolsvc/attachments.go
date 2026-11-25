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
	"github.com/touwaeriol/agent-teams/server/internal/sharedfiles"
)

// Attachments in tools (#58 design §6.3, §7): assign_task, send_message and the other messaging
// tools take attachments=[img_…] and deliver them with the message; read_attachment downloads one
// into the session's .attachments/; save_attachment keeps one as a shared file.

// AttachmentURLTTL is how long the presigned GET a daemon gets for read_attachment or
// read_shared_file stays valid (it downloads at once).
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

// saveAttachment keeps an attachment as a shared file. The object is copied inside the store,
// after the call's transaction (Later: no network work while the project row is locked); the
// bytes never pass through the working directory. Rows from before #58 are written from their
// data column.
func (s *Service) saveAttachment(ctx context.Context, tx *core.Tx, c *call) (Result, error) {
	var in protocol.SaveAttachmentInput
	if err := c.decode(&in); err != nil {
		return Result{}, err
	}
	p, err := sharedfiles.CleanPath(string(in.Path))
	if err != nil {
		return Result{}, refuse("%s", err.Error())
	}
	bs := blobstore.Or(s.core.Blobs)
	if !bs.Configured() {
		return Result{}, refuse("平台的文件存储还没有配置，暂时存不了共享文件")
	}
	list, err := s.loadAttachments(ctx, tx, c, protocol.AttachmentIds{in.ID})
	if err != nil {
		return Result{}, err
	}
	a, pid := list[0], c.emp().ProjectID
	if err := sharedfiles.CheckSpace(ctx, tx, pid, p, int64(a.Size), in.Overwrite); err != nil {
		return Result{}, refuseShared(err, p)
	}
	by, owner := sharedUploader(c), c.sc.OwnerID
	return Result{Later: func(ctx context.Context) Result { return s.copyAttachment(ctx, owner, p, a, by, in.Overwrite) }}, nil
}

// refuseShared explains a shared-file refusal to the model.
func refuseShared(err error, p string) error {
	switch {
	case errors.Is(err, sharedfiles.ErrExists):
		return refuse("共享文件 %s 已经存在：换一个路径，或者确认要替换时带 overwrite=true", p)
	case errors.Is(err, sharedfiles.ErrInvalidPath), errors.Is(err, sharedfiles.ErrFileTooLarge), errors.Is(err, sharedfiles.ErrQuota):
		return refuse("%s", err.Error())
	}
	return err
}

func (s *Service) copyAttachment(ctx context.Context, owner, p string, a core.Attachment, by sharedfiles.Uploader, overwrite bool) Result {
	f, overwritten, err := s.core.SaveAttachmentShared(ctx, owner, a, p, by, overwrite)
	if err != nil {
		var ue userError
		switch {
		case errors.Is(err, blobstore.ErrNotFound) || errors.Is(err, core.ErrNotFound):
			s.log.Warn("save_attachment copy", "attachment", a.ID, "err", err)
			return failText("附件 %s 的文件已经不在存储里了（可能刚过期），存不了", a.ID)
		case errors.Is(err, core.ErrAttachmentExpired):
			return failText("%s", err.Error())
		case errors.As(refuseShared(err, p), &ue):
			return failText("%s", ue.msg)
		}
		s.log.Warn("save_attachment", "attachment", a.ID, "err", err)
		return failText("存为共享文件失败：%v", err)
	}
	out := protocol.SaveAttachmentResult{ID: protocol.AttachmentID(a.ID), Path: protocol.SharedPath(f.Path), Size: int(f.Size),
		Overwritten: overwritten}
	if f.SHA256 != "" {
		sum := f.SHA256
		out.SHA256 = &sum
	}
	verb := "已存为"
	if overwritten {
		verb = "已覆盖"
	}
	return okJSON(fmt.Sprintf("%s共享文件 %s（附件 %s %s，%s）。项目里所有会话和用户都能用 list_shared_files / read_shared_file 取到它，"+
		"附件过期后它也一直保留。%s", verb, f.Path, a.ID, a.Name, human(f.Size), noSecrets), out)
}

// carried tells the caller how many attachments went along (appended to a tool's result text).
func carried(files []core.AttachmentRef) string {
	if len(files) == 0 {
		return ""
	}
	return fmt.Sprintf("（附带 %d 个附件）", len(files))
}
