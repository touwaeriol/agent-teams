package toolsvc

import (
	"context"
	"errors"
	"fmt"
	"strings"

	protocol "github.com/touwaeriol/agent-teams/protocol"

	"github.com/touwaeriol/agent-teams/server/internal/artifacts"
	"github.com/touwaeriol/agent-teams/server/internal/blobstore"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw"
	"github.com/touwaeriol/agent-teams/server/internal/sharedfiles"
)

// SharedDir is where read_shared_file puts a file, relative to the session's working directory
// (<cwd>/.shared/<path>; the daemon writes a .gitignore of "*" into it so it is never committed).
const SharedDir = ".shared"

// FeatureSharedFiles is the hello feature of daemons that upload write_shared_file and write a
// read_shared_file shared_download to disk (older daemons drop shared_download).
const FeatureSharedFiles = "shared_files"

// noSecrets ends every shared-files tool text.
const noSecrets = "共享文件项目里所有会话和用户都能看到，不要放密钥、密码、令牌等凭证。"

// sharedUploader is the calling employee as the uploader of a shared file: its name and the thread
// its session works in (listed as "dev-1 #31"; none for the coordinator).
func sharedUploader(c *call) sharedfiles.Uploader {
	return sharedfiles.Uploader{Name: c.emp().Name, EmployeeID: c.emp().ID, ThreadID: c.emp().WorkThreadID()}
}

// listSharedFiles lists the project's shared files with the project's total and limit.
func (s *Service) listSharedFiles(ctx context.Context, tx *core.Tx, c *call) (Result, error) {
	var in protocol.ListSharedFilesInput
	if err := c.decode(&in); err != nil {
		return Result{}, err
	}
	prefix := ""
	if in.Prefix != nil {
		prefix = *in.Prefix
	}
	pid := c.emp().ProjectID
	files, err := sharedfiles.List(ctx, tx, pid, prefix)
	if err != nil {
		return Result{}, err
	}
	u, err := sharedfiles.ProjectUsage(ctx, tx, pid)
	if err != nil {
		return Result{}, err
	}
	out := protocol.ListSharedFilesResult{Files: make([]protocol.SharedFileInfo, len(files)), TotalBytes: int(u.Bytes),
		LimitBytes: sharedfiles.MaxProjectBytes}
	var b strings.Builder
	for i, f := range files {
		out.Files[i] = protocol.SharedFileInfo{Path: protocol.SharedPath(f.Path), Size: int(f.Size), ContentType: f.ContentType,
			SHA256: f.SHA256, UploadedBy: f.UploadedBy, UploadedByThreadNo: f.UploaderThreadNo, UpdatedAt: f.UpdatedAt}
		fmt.Fprintf(&b, "- %s（%s，%s，%s 上传于 %s）\n", f.Path, human(f.Size), f.ContentType, f.Label(), stamp(f.UpdatedAt))
	}
	total := fmt.Sprintf("项目共享文件 %d 个，合计 %s / 上限 %s（单个文件不超过 %s）。", u.Files, human(u.Bytes),
		human(sharedfiles.MaxProjectBytes), human(sharedfiles.MaxFileBytes))
	if len(files) == 0 {
		head := "这个项目还没有共享文件。"
		if prefix != "" {
			head = fmt.Sprintf("没有以 %q 开头的共享文件。", prefix)
		}
		return okJSON(head+total+"用 write_shared_file(file) 上传。"+noSecrets, out), nil
	}
	text := b.String() + total + "用 read_shared_file(path) 取到工作目录的 " + SharedDir + "/ 下再用文件工具读。" + noSecrets
	return okJSON(text, out), nil
}

// sharedReadResult is the structured result of read_shared_file: never the content.
type sharedReadResult struct {
	Path      string `json:"path"`
	Size      int    `json:"size"`
	SHA256    string `json:"sha256"`
	LocalPath string `json:"local_path"`
}

// readSharedFile sends one shared file to the session's working directory as <cwd>/.shared/<path>
// (file.download frames before the tool.result, like read_deliverable). The result names the local
// path only; the file's content is never put in the result (the model reads the local file).
func (s *Service) readSharedFile(ctx context.Context, tx *core.Tx, c *call) (Result, error) {
	var in protocol.ReadSharedFileInput
	if err := c.decode(&in); err != nil {
		return Result{}, err
	}
	p, err := sharedfiles.CleanPath(string(in.Path))
	if err != nil {
		return Result{}, refuse("%s", err.Error())
	}
	f, data, err := sharedfiles.Load(ctx, tx, s.core.Blobs, c.emp().ProjectID, p)
	if errors.Is(err, sharedfiles.ErrNotFound) {
		return Result{}, refuse("这个项目没有共享文件 %s（用 list_shared_files 查看）", p)
	}
	if errors.Is(err, sharedfiles.ErrFailed) {
		return Result{}, refuse("共享文件 %s 上传后校验没有通过（内容和上传时不一致），读不了；请重新上传", p)
	}
	if errors.Is(err, blobstore.ErrNotConfigured) {
		return Result{}, refuse("平台的文件存储还没有配置，暂时读不了共享文件 %s", p)
	}
	if err != nil {
		return Result{}, err
	}
	dl, err := s.sendSharedDownload(ctx, tx, c, f, data)
	if err != nil {
		return Result{}, err
	}
	out := sharedReadResult{Path: f.Path, Size: len(data), SHA256: f.SHA256, LocalPath: SharedDir + "/" + f.Path}
	text := fmt.Sprintf("已把共享文件 %s（%s，sha256 %s，%s 上传）取到工作目录的 %s（不会被 git 提交）。用文件工具读取它；"+
		"改完后用 write_shared_file(file=%q) 写回同一路径。%s", f.Path, human(int64(len(data))), f.SHA256, f.Label(), out.LocalPath,
		out.LocalPath, noSecrets)
	return Result{OK: true, Text: text, Structured: out, SharedDownload: dl}, nil
}

// sendSharedDownload queues the file as file.download chunks for the session's node. Every session
// (the coordinator too) runs in a working directory on a node; a session without a node, or on a
// daemon without the shared_files feature, gets an explanation instead of the content.
func (s *Service) sendSharedDownload(ctx context.Context, tx *core.Tx, c *call, f sharedfiles.SharedFile,
	data []byte) (*protocol.ToolSharedDownload, error) {
	sess, err := core.SessionByID(ctx, tx, c.sc.Session.ID)
	if err != nil {
		return nil, err
	}
	if sess.NodeID == nil {
		return nil, refuse("会话不在任何节点上，没有工作目录可以放共享文件；共享文件只会下载到会话的工作目录，不会把内容放进工具结果")
	}
	ok, err := core.NodeHasFeature(ctx, tx, *sess.NodeID, FeatureSharedFiles)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, refuse("节点守护进程需要升级到支持共享文件（read_shared_file）的版本")
	}
	dl := &protocol.ToolSharedDownload{DownloadID: protocol.DownloadID(core.NewID("dn")),
		Files: []protocol.DownloadFile{{Path: f.Path, Size: len(data), SHA256: f.SHA256}}}
	for off := 0; off < len(data); off += artifacts.ChunkBytes {
		body := protocol.FileDownload{SessionID: protocol.SessionID(sess.ID), CallID: protocol.CallID(c.callID),
			DownloadID: dl.DownloadID, Index: dl.Chunks, Data: encode(data[off:min(off+artifacts.ChunkBytes, len(data))])}
		if _, err := s.gw.Enqueue(ctx, tx, *sess.NodeID, nodegw.Command{Type: protocol.TypeFileDownload, Body: body,
			SessionID: sess.ID, OpenGen: sess.OpenGen}); err != nil {
			return nil, err
		}
		dl.Chunks++
	}
	return dl, nil
}

// writeSharedFile stores the one file the daemon uploaded (file.chunk + upload) at its shared path,
// overwriting a file of the same path; the path and the limits are checked again here.
func (s *Service) writeSharedFile(ctx context.Context, tx *core.Tx, c *call) (Result, error) {
	var in protocol.WriteSharedFileInput
	if err := c.decode(&in); err != nil {
		return Result{}, err
	}
	if c.upload == nil {
		return Result{}, refuse("没有收到文件内容：节点守护进程需要升级到支持共享文件（write_shared_file）的版本")
	}
	// A refusal is answered as a failed result (not an error) so the chunks are dropped either way:
	// an error would roll the handler's savepoint back, deletion included.
	res, err := s.storeShared(ctx, tx, c, in)
	var ue userError
	if errors.As(err, &ue) {
		res, err = failText("%s", ue.msg), nil
	}
	if err != nil {
		return Result{}, err
	}
	return res, artifacts.DeleteUpload(ctx, tx, string(c.upload.UploadID))
}

func (s *Service) storeShared(ctx context.Context, tx *core.Tx, c *call, in protocol.WriteSharedFileInput) (Result, error) {
	if len(c.upload.Files) != 1 {
		return Result{}, refuse("write_shared_file 一次只能上传一个文件")
	}
	decl := c.upload.Files[0]
	target := decl.Path
	if in.Path != nil {
		target = string(*in.Path)
	}
	p, err := sharedfiles.CleanPath(target)
	if err != nil {
		return Result{}, refuse("%s", err.Error())
	}
	if decl.Size > sharedfiles.MaxFileBytes {
		return Result{}, refuse("%s", sharedfiles.Check(sharedfiles.Usage{}, int64(decl.Size), -1).Error())
	}
	data, err := artifacts.LoadChunks(ctx, tx, c.sc.Session.ID, c.upload)
	if err != nil {
		return Result{}, refuse("%s", err.Error())
	}
	if len(data) != decl.Size {
		return Result{}, refuse("文件大小与内容不符：%s", p)
	}
	if sharedfiles.Sum(data) != decl.SHA256 {
		return Result{}, refuse("文件校验失败：%s", p)
	}
	f, overwritten, err := sharedfiles.Put(ctx, tx, s.core.Blobs, c.emp().ProjectID, p, data, sharedUploader(c))
	if errors.Is(err, sharedfiles.ErrInvalidPath) || errors.Is(err, sharedfiles.ErrFileTooLarge) || errors.Is(err, sharedfiles.ErrQuota) {
		return Result{}, refuse("%s", err.Error())
	}
	if errors.Is(err, blobstore.ErrNotConfigured) {
		return Result{}, refuse("平台的文件存储还没有配置，暂时写不了共享文件")
	}
	if err != nil {
		return Result{}, err
	}
	if err := tx.EmitSharedFile(ctx, c.emp().ProjectID, f.Label(), "write", f.Path); err != nil {
		return Result{}, err
	}
	verb := "已上传"
	if overwritten {
		verb = "已覆盖"
	}
	text := fmt.Sprintf("%s共享文件 %s（%s，sha256 %s）。项目里所有会话和用户都能用 list_shared_files / read_shared_file 取到它；"+
		"同名再写会直接覆盖。%s", verb, f.Path, human(f.Size), f.SHA256, noSecrets)
	return okJSON(text, protocol.WriteSharedFileResult{Path: protocol.SharedPath(f.Path), Size: int(f.Size), SHA256: f.SHA256,
		Overwritten: overwritten}), nil
}

// deleteSharedFile removes one shared file (copies already fetched into working directories stay).
func (s *Service) deleteSharedFile(ctx context.Context, tx *core.Tx, c *call) (Result, error) {
	var in protocol.DeleteSharedFileInput
	if err := c.decode(&in); err != nil {
		return Result{}, err
	}
	p, err := sharedfiles.CleanPath(string(in.Path))
	if err != nil {
		return Result{}, refuse("%s", err.Error())
	}
	deleted, err := sharedfiles.Delete(ctx, tx, c.emp().ProjectID, p)
	if err != nil {
		return Result{}, err
	}
	out := protocol.DeleteSharedFileResult{Path: protocol.SharedPath(p), Deleted: deleted}
	if !deleted {
		return okJSON(fmt.Sprintf("没有共享文件 %s，什么也没删。", p), out), nil
	}
	label := sharedfiles.SharedFile{UploadedBy: c.emp().Name}
	if th := c.thread; th != nil {
		label.UploaderThreadNo = &th.No
	}
	if err := tx.EmitSharedFile(ctx, c.emp().ProjectID, label.Label(), "delete", p); err != nil {
		return Result{}, err
	}
	return okJSON(fmt.Sprintf("已删除共享文件 %s（已经取到各工作目录里的副本不受影响）。", p), out), nil
}

// human prints a byte count as B / KB / MB.
func human(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
