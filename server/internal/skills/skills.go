// Package skills is the skill library (architecture.md §6.5): built-in skills shipped
// with the server (builtin/<name>/SKILL.md) and skills a user uploads (at_skills, text
// files only). Role templates name the skills their employees get; session.open carries
// them to the daemon as SkillBundles.
package skills

import (
	"archive/zip"
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"
)

// Limits of one skill.
const (
	MaxFiles     = 64
	MaxFileBytes = 256 << 10
	MaxBytes     = 512 << 10
	MaxZipBytes  = 2 << 20
	EntryFile    = "SKILL.md"
)

var (
	namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	pathPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]*(/[A-Za-z0-9_][A-Za-z0-9._-]*)*$`)
)

//go:embed builtin
var builtinFS embed.FS

// File is one text file of a skill.
type File struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// Skill is a library entry. ID is empty for built-in skills.
type Skill struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Builtin     bool   `json:"builtin"`
	Size        int    `json:"size"`
	Files       []File `json:"-"`
}

// Problem is a validation error shown to the user.
type Problem struct{ msg string }

func (p Problem) Error() string { return p.msg }

func problem(format string, a ...any) error { return Problem{fmt.Sprintf(format, a...)} }

// IsProblem reports whether err is a validation error.
func IsProblem(err error) bool {
	var p Problem
	return errors.As(err, &p)
}

var builtins = mustLoadBuiltins()

func mustLoadBuiltins() []Skill {
	out, err := loadBuiltins(builtinFS)
	if err != nil {
		panic(err)
	}
	return out
}

func loadBuiltins(fsys fs.FS) ([]Skill, error) {
	dirs, err := fs.ReadDir(fsys, "builtin")
	if err != nil {
		return nil, err
	}
	out := []Skill{}
	for _, d := range dirs {
		files, err := readDir(fsys, path.Join("builtin", d.Name()))
		if err != nil {
			return nil, err
		}
		s, err := Parse(files)
		if err != nil {
			return nil, fmt.Errorf("built-in skill %s: %w", d.Name(), err)
		}
		s.Builtin = true
		out = append(out, s)
	}
	return out, nil
}

func readDir(fsys fs.FS, root string) ([]File, error) {
	var files []File
	err := fs.WalkDir(fsys, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := fs.ReadFile(fsys, p)
		files = append(files, File{Path: strings.TrimPrefix(p, root+"/"), Content: string(raw)})
		return err
	})
	return files, err
}

// Builtin returns the built-in skills.
func Builtin() []Skill { return slices.Clone(builtins) }

// BuiltinNames are the names no uploaded skill may take.
func BuiltinNames() []string {
	out := make([]string, len(builtins))
	for i, s := range builtins {
		out[i] = s.Name
	}
	return out
}

func builtin(name string) (Skill, bool) {
	for _, s := range builtins {
		if s.Name == name {
			return s, true
		}
	}
	return Skill{}, false
}

// Parse validates a skill's files and reads name/description from SKILL.md's front matter.
func Parse(files []File) (Skill, error) {
	size, err := checkFiles(files)
	if err != nil {
		return Skill{}, err
	}
	i := slices.IndexFunc(files, func(f File) bool { return f.Path == EntryFile })
	if i < 0 {
		return Skill{}, problem("技能必须在根目录有 %s", EntryFile)
	}
	meta := frontMatter(files[i].Content)
	name, desc := meta["name"], meta["description"]
	if !namePattern.MatchString(name) {
		return Skill{}, problem("SKILL.md 的 front matter 需要 name（小写字母、数字和 -，最多 64 个字符），现在是 %q", name)
	}
	if desc == "" {
		return Skill{}, problem("SKILL.md 的 front matter 需要 description（模型靠它决定何时使用这个技能）")
	}
	sorted := slices.Clone(files)
	sort.Slice(sorted, func(a, b int) bool { return sorted[a].Path < sorted[b].Path })
	return Skill{Name: name, Description: desc, Size: size, Files: sorted}, nil
}

func checkFiles(files []File) (int, error) {
	if len(files) == 0 || len(files) > MaxFiles {
		return 0, problem("技能需要 1 到 %d 个文件", MaxFiles)
	}
	total := 0
	seen := map[string]bool{}
	for _, f := range files {
		if err := checkFile(f); err != nil {
			return 0, err
		}
		if seen[f.Path] {
			return 0, problem("文件重复：%s", f.Path)
		}
		seen[f.Path] = true
		total += len(f.Content)
	}
	if total > MaxBytes {
		return 0, problem("技能文件合计不能超过 %d KB", MaxBytes>>10)
	}
	return total, nil
}

func checkFile(f File) error {
	switch {
	case len(f.Path) > 200 || !pathPattern.MatchString(f.Path):
		return problem("文件路径不合法：%q（只能是相对路径，不能以 . 开头）", f.Path)
	case len(f.Content) > MaxFileBytes:
		return problem("%s 超过 %d KB", f.Path, MaxFileBytes>>10)
	case !utf8.ValidString(f.Content) || strings.ContainsRune(f.Content, 0):
		return problem("%s 不是文本文件（技能只能包含 UTF-8 文本）", f.Path)
	}
	return nil
}

// frontMatter reads simple "key: value" lines between the leading --- fences.
func frontMatter(text string) map[string]string {
	out := map[string]string{}
	text = strings.TrimPrefix(text, "\ufeff")
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return out
	}
	for _, line := range lines[1:] {
		if strings.TrimSpace(line) == "---" {
			return out
		}
		key, value, ok := strings.Cut(line, ":")
		if ok && !strings.HasPrefix(line, " ") {
			out[strings.TrimSpace(key)] = unquote(strings.TrimSpace(value))
		}
	}
	return map[string]string{} // no closing fence: not front matter
}

func unquote(v string) string {
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
		return v[1 : len(v)-1]
	}
	return v
}

// FromZip unpacks a zip of text files. A single top-level folder is stripped, and
// directories, hidden files and macOS metadata are skipped.
func FromZip(data []byte) ([]File, error) {
	if len(data) > MaxZipBytes {
		return nil, problem("压缩包不能超过 %d MB", MaxZipBytes>>20)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, problem("不是有效的 zip 文件")
	}
	var files []File
	total := 0
	for _, zf := range zr.File {
		if skipEntry(zf.Name) || zf.FileInfo().IsDir() {
			continue
		}
		if len(files) >= MaxFiles {
			return nil, problem("技能最多 %d 个文件", MaxFiles)
		}
		f, err := readEntry(zf)
		if err != nil {
			return nil, err
		}
		// Count what was really decompressed (headers can lie) so a zip bomb stops early.
		if total += len(f.Content); total > MaxBytes {
			return nil, problem("技能文件合计不能超过 %d KB", MaxBytes>>10)
		}
		files = append(files, f)
	}
	return StripCommonDir(files), nil
}

func skipEntry(name string) bool {
	for _, part := range strings.Split(name, "/") {
		if strings.HasPrefix(part, ".") || part == "__MACOSX" {
			return true
		}
	}
	return false
}

func readEntry(zf *zip.File) (File, error) {
	if zf.UncompressedSize64 > MaxFileBytes {
		return File{}, problem("%s 超过 %d KB", zf.Name, MaxFileBytes>>10)
	}
	rc, err := zf.Open()
	if err != nil {
		return File{}, problem("无法读取 %s", zf.Name)
	}
	defer rc.Close()
	raw, err := io.ReadAll(io.LimitReader(rc, MaxFileBytes+1))
	if err != nil {
		return File{}, problem("无法读取 %s", zf.Name)
	}
	return File{Path: zf.Name, Content: string(raw)}, nil
}

// StripCommonDir removes a folder that every file sits in (a zipped or picked folder).
func StripCommonDir(files []File) []File {
	if len(files) == 0 || slices.ContainsFunc(files, func(f File) bool { return f.Path == EntryFile }) {
		return files
	}
	top, _, ok := strings.Cut(files[0].Path, "/")
	if !ok {
		return files
	}
	for _, f := range files {
		if !strings.HasPrefix(f.Path, top+"/") {
			return files
		}
	}
	out := make([]File, len(files))
	for i, f := range files {
		out[i] = File{Path: strings.TrimPrefix(f.Path, top+"/"), Content: f.Content}
	}
	return StripCommonDir(out)
}
