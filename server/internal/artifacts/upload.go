package artifacts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"

	protocol "github.com/touwaeriol/agent-teams/protocol"

	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// ErrBadChunk is a chunk whose data is not base64 of at most ChunkBytes.
var ErrBadChunk = errors.New("bad file.chunk data")

// SaveChunk stores one file.chunk (idempotent by upload_id + index) and drops chunks
// of uploads that were never claimed by a tool.call within a day.
func SaveChunk(ctx context.Context, q store.DB, m *protocol.FileChunk) error {
	data, err := base64.StdEncoding.DecodeString(m.Data)
	if err != nil || len(data) > ChunkBytes {
		return ErrBadChunk
	}
	if _, err := q.Exec(ctx, `DELETE FROM at_upload_chunks WHERE created_at < now() - interval '1 day'`); err != nil {
		return err
	}
	_, err = q.Exec(ctx, `INSERT INTO at_upload_chunks (upload_id, idx, session_id, data) VALUES ($1,$2,$3,$4)
		ON CONFLICT (upload_id, idx) DO NOTHING`, m.UploadID, m.Index, m.SessionID, data)
	return err
}

// LoadUpload reads an upload's chunks (of this session only) and splits them into files.
func LoadUpload(ctx context.Context, q store.DB, sessionID string, up *protocol.ToolUpload) ([]ArtifactFile, error) {
	rows, err := q.Query(ctx, `SELECT data FROM at_upload_chunks WHERE upload_id = $1 AND session_id = $2 ORDER BY idx`,
		up.UploadID, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var all bytes.Buffer
	n := 0
	for rows.Next() {
		var part []byte
		if err := rows.Scan(&part); err != nil {
			return nil, err
		}
		all.Write(part)
		n++
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if n != up.Chunks {
		return nil, fmt.Errorf("文件内容不完整：收到 %d/%d 段", n, up.Chunks)
	}
	return Split(all.Bytes(), up.Files)
}

// DeleteUpload removes an upload's chunks once its files are stored.
func DeleteUpload(ctx context.Context, q store.DB, uploadID string) error {
	_, err := q.Exec(ctx, `DELETE FROM at_upload_chunks WHERE upload_id = $1`, uploadID)
	return err
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
