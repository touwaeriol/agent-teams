package artifacts

import (
	"path"
	"regexp"
	"strings"
)

// Publish limits (the daemon checks them too, before reading files).
const (
	MaxBytes   = 5 << 20
	MaxFiles   = 64
	ChunkBytes = 1 << 20
	EntryHTML  = "index.html"
)

var filePattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._ -]*(/[A-Za-z0-9_][A-Za-z0-9._ -]*)*$`)

// types maps a lower-case extension to the Content-Type the file is served with.
var types = map[string]string{
	".html": "text/html; charset=utf-8", ".htm": "text/html; charset=utf-8",
	".md": "text/markdown; charset=utf-8", ".markdown": "text/markdown; charset=utf-8",
	".svg": "image/svg+xml", ".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg",
	".gif": "image/gif", ".webp": "image/webp", ".pdf": "application/pdf",
	".json": "application/json; charset=utf-8", ".csv": "text/csv; charset=utf-8", ".txt": "text/plain; charset=utf-8",
	".css": "text/css; charset=utf-8", ".js": "text/javascript; charset=utf-8", ".mjs": "text/javascript; charset=utf-8",
	".woff": "font/woff", ".woff2": "font/woff2",
}

// ContentType returns the served type of a path, or "" when the extension is not allowed.
func ContentType(p string) string { return types[strings.ToLower(path.Ext(p))] }

// ValidPath reports whether p may be stored: relative, no hidden or parent segments,
// an allowed extension.
func ValidPath(p string) bool {
	return len(p) <= 300 && filePattern.MatchString(p) && ContentType(p) != ""
}

// Entry is the file a viewer opens: the only file, or index.html of a site.
func Entry(paths []string) string {
	if len(paths) == 1 {
		return paths[0]
	}
	for _, p := range paths {
		if p == EntryHTML {
			return p
		}
	}
	return ""
}

// Kind of rendering for a content type: html, markdown, image, pdf or text.
func Render(contentType string) string {
	ct, _, _ := strings.Cut(contentType, ";")
	switch {
	case ct == "text/html":
		return "html"
	case ct == "text/markdown":
		return "markdown"
	case ct == "application/pdf":
		return "pdf"
	case strings.HasPrefix(ct, "image/"):
		return "image"
	}
	return "text"
}

// Detail is at_deliverables.detail of a hosted deliverable; earlier keys (summary…) are kept.
func Detail(by string, version int, entry string, files []ArtifactFile, prev map[string]any) map[string]any {
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
	out["entry"], out["content_type"], out["render"], out["files"], out["size"] = entry, ct, Render(ct), len(files), size
	return out
}

// fileTypes are content types of common data files the deliverable table does not list.
var fileTypes = map[string]string{
	".xml": "application/xml", ".yaml": "application/yaml", ".yml": "application/yaml",
	".zip": "application/zip", ".gz": "application/gzip", ".tar": "application/x-tar", ".tgz": "application/gzip",
	".log": "text/plain; charset=utf-8", ".tsv": "text/tab-separated-values; charset=utf-8",
	".sql": "text/plain; charset=utf-8", ".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	".pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	".mp3":  "audio/mpeg", ".wav": "audio/wav", ".mp4": "video/mp4", ".webm": "video/webm",
}

// FileType is the type any file is listed and downloaded with, from its extension only; unknown
// extensions are application/octet-stream.
func FileType(p string) string {
	if ct := ContentType(p); ct != "" {
		return ct
	}
	if ct := fileTypes[strings.ToLower(path.Ext(p))]; ct != "" {
		return ct
	}
	return "application/octet-stream"
}
