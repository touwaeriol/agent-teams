package plugins

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"errors"
	"io"
	"io/fs"
	"strings"
	"unicode/utf8"
)

// FromZip unpacks a plugin zip. A single top-level folder around .claude-plugin is stripped;
// directories, macOS metadata, .git and .DS_Store are skipped; symlinks, unsafe paths and
// oversized files are refused. Sizes count what was really decompressed (headers can lie),
// so a zip bomb stops at the limit.
func FromZip(data []byte) ([]File, error) {
	if len(data) > MaxZipBytes {
		return nil, problem("压缩包不能超过 {mb} MB", "mb", MaxZipBytes>>20)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil && !errors.Is(err, zip.ErrInsecurePath) {
		return nil, problem("不是有效的 zip 文件")
	}
	var files []File
	total := 0
	for _, zf := range zr.File {
		if skipEntry(zf) {
			continue
		}
		f, n, err := readEntry(zf)
		if err != nil {
			return nil, err
		}
		if len(files) >= MaxFiles {
			return nil, problem("插件最多 {n} 个文件", "n", MaxFiles)
		}
		if total += n; total > MaxBytes {
			return nil, problem("插件文件合计不能超过 {kb} KB", "kb", MaxBytes>>10)
		}
		files = append(files, f)
	}
	return StripCommonDir(files), nil
}

func skipEntry(zf *zip.File) bool {
	if zf.FileInfo().IsDir() {
		return true
	}
	for _, part := range strings.Split(zf.Name, "/") {
		if part == "__MACOSX" || part == ".git" || part == ".DS_Store" {
			return true
		}
	}
	return false
}

func readEntry(zf *zip.File) (File, int, error) {
	switch {
	case zf.Mode()&fs.ModeSymlink != 0:
		return File{}, 0, problem("压缩包里不能有符号链接：{name}", "name", zf.Name)
	case !zf.Mode().IsRegular():
		return File{}, 0, problem("压缩包里只能有普通文件：{name}", "name", zf.Name)
	case !ValidPath(zf.Name):
		return File{}, 0, problem("文件路径不合法：“{name}”（只能是相对路径，不能含 . 或 .. 段）", "name", zf.Name)
	case zf.UncompressedSize64 > MaxFileBytes:
		return File{}, 0, problem("{name} 超过 {kb} KB", "name", zf.Name, "kb", MaxFileBytes>>10)
	}
	raw, err := readLimited(zf)
	if err != nil {
		return File{}, 0, err
	}
	f := File{Path: zf.Name, Content: string(raw), Executable: zf.Mode()&0o111 != 0}
	if !utf8.Valid(raw) {
		f.Content, f.Encoding = base64.StdEncoding.EncodeToString(raw), "base64"
	}
	return f, len(raw), nil
}

func readLimited(zf *zip.File) ([]byte, error) {
	rc, err := zf.Open()
	if err != nil {
		return nil, problem("无法读取 {name}", "name", zf.Name)
	}
	defer rc.Close()
	raw, err := io.ReadAll(io.LimitReader(rc, MaxFileBytes+1))
	if err != nil {
		return nil, problem("无法读取 {name}", "name", zf.Name)
	}
	if len(raw) > MaxFileBytes {
		return nil, problem("{name} 超过 {kb} KB", "name", zf.Name, "kb", MaxFileBytes>>10)
	}
	return raw, nil
}

// StripCommonDir removes folders that wrap the whole package (a zipped plugin folder).
func StripCommonDir(files []File) []File {
	for len(files) > 0 && !hasManifest(files) {
		top, _, ok := strings.Cut(files[0].Path, "/")
		if !ok || !allUnder(files, top+"/") {
			return files
		}
		files = trimPrefix(files, top+"/")
	}
	return files
}

func hasManifest(files []File) bool {
	for _, f := range files {
		if f.Path == ManifestPath {
			return true
		}
	}
	return false
}

func allUnder(files []File, prefix string) bool {
	for _, f := range files {
		if !strings.HasPrefix(f.Path, prefix) {
			return false
		}
	}
	return true
}

func trimPrefix(files []File, prefix string) []File {
	out := make([]File, len(files))
	for i, f := range files {
		f.Path = strings.TrimPrefix(f.Path, prefix)
		out[i] = f
	}
	return out
}
