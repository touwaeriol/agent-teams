package artifacts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// ErrBadChunk is a chunk whose data is not base64 of at most ChunkBytes, or that would
// leave the session more unclaimed data than MaxPending.
var ErrBadChunk = errors.New("bad file.chunk data")

// MaxPending is how much unclaimed chunk data one session may leave in the database
// (two full uploads of the largest kind, a 20 MB shared file — sharedfiles.MaxFileBytes), so a
// daemon cannot fill it with chunks no tool.call ever claims.
const MaxPending = 2 * MaxUploadBytes

// MaxUploadBytes is the largest single upload any tool accepts (write_shared_file).
const MaxUploadBytes = 20 << 20

// SaveChunk stores one file.chunk (idempotent by upload_id + index) and drops chunks
// of uploads that were never claimed by a tool.call within a day.
func SaveChunk(ctx context.Context, q store.DB, m *protocol.FileChunk) error {
	data, err := base64.StdEncoding.DecodeString(m.Data)
	if err != nil || len(data) > ChunkBytes {
		return ErrBadChunk
	}
	if err := chunks(ctx, q).Where("created_at < now() - interval '1 day'").Delete(&uploadChunk{}).Error; err != nil {
		return err
	}
	var pending int
	if err := chunks(ctx, q).Select("COALESCE(sum(length(data)), 0)").Where("session_id = ?", m.SessionID).
		Row().Scan(&pending); err != nil {
		return err
	}
	if pending+len(data) > MaxPending {
		return ErrBadChunk
	}
	return chunks(ctx, q).Clauses(clause.OnConflict{DoNothing: true}).Create(&uploadChunk{UploadID: string(m.UploadID),
		Idx: m.Index, SessionID: string(m.SessionID), Data: data}).Error
}

// uploadChunk is a row of at_upload_chunks.
type uploadChunk struct {
	UploadID  string
	Idx       int
	SessionID string
	Data      []byte
}

func (uploadChunk) TableName() string { return "at_upload_chunks" }

func chunks(ctx context.Context, q store.DB) *gorm.DB { return q.Gorm(ctx).Model(&uploadChunk{}) }

// LoadUpload reads an upload's chunks (of this session only) and splits them into files.
func LoadUpload(ctx context.Context, q store.DB, sessionID string, up *protocol.ToolUpload) ([]ArtifactFile, error) {
	all, err := LoadChunks(ctx, q, sessionID, up)
	if err != nil {
		return nil, err
	}
	return Split(all, up.Files)
}

// LoadChunks reads an upload's chunks (of this session only) joined in order, checking that all
// up.Chunks of them arrived; the caller checks the files' sizes and checksums.
func LoadChunks(ctx context.Context, q store.DB, sessionID string, up *protocol.ToolUpload) ([]byte, error) {
	var parts [][]byte
	err := chunks(ctx, q).Where("upload_id = ? AND session_id = ?", up.UploadID, sessionID).Order("idx").
		Pluck("data", &parts).Error
	if err != nil {
		return nil, err
	}
	all := bytes.Join(parts, nil)
	n := len(parts)
	if n != up.Chunks {
		return nil, fmt.Errorf("文件内容不完整：收到 %d/%d 段", n, up.Chunks)
	}
	return all, nil
}

// DeleteUpload removes an upload's chunks once its files are stored.
func DeleteUpload(ctx context.Context, q store.DB, uploadID string) error {
	return chunks(ctx, q).Where("upload_id = ?", uploadID).Delete(&uploadChunk{}).Error
}

// Split cuts the concatenated bytes into the declared files and checks every limit:
// count, total size, paths and types, sizes and checksums. Content types come from the
// extension, never from the daemon.
func Split(data []byte, decl []protocol.UploadFile) ([]ArtifactFile, error) {
	if len(decl) == 0 || len(decl) > MaxFiles {
		return nil, fmt.Errorf("一次最多发布 %d 个文件", MaxFiles)
	}
	if len(data) > MaxBytes {
		return nil, fmt.Errorf("文件合计超过 %d MB", MaxBytes>>20)
	}
	out := make([]ArtifactFile, 0, len(decl))
	off := 0
	for _, d := range decl {
		f, err := cut(data, off, d)
		if err != nil {
			return nil, err
		}
		off += d.Size
		out = append(out, f)
	}
	if off != len(data) {
		return nil, fmt.Errorf("文件大小与内容不符")
	}
	return out, nil
}

func cut(data []byte, off int, d protocol.UploadFile) (ArtifactFile, error) {
	if !ValidPath(d.Path) {
		return ArtifactFile{}, fmt.Errorf("不支持的文件：%s（支持 html、md、svg、png、jpg、gif、webp、pdf、json、csv、txt，网站目录里还可以有 css、js、字体）", d.Path)
	}
	if d.Size < 0 || off+d.Size > len(data) {
		return ArtifactFile{}, fmt.Errorf("文件大小与内容不符：%s", d.Path)
	}
	body := data[off : off+d.Size]
	sum := sha256.Sum256(body)
	if hex.EncodeToString(sum[:]) != d.SHA256 {
		return ArtifactFile{}, fmt.Errorf("文件校验失败：%s", d.Path)
	}
	return ArtifactFile{Path: d.Path, ContentType: ContentType(d.Path), Size: d.Size, SHA256: d.SHA256, Data: body}, nil
}
