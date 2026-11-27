package memfiles

import (
	"bytes"
	"io"
	"strings"

	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// A memory file in the folder: MEMORY.md is plain Markdown; a topic file starts with frontmatter
// carrying what the rows keep beside the content (the same shape as Claude Code's own memory):
//
//	---
//	name: testing
//	description: 跑测试的命令和已知不稳定的用例
//	type: project
//	---
//
//	正文…

// Render is the file of a memory row.
func Render(f store.MemoryFile) []byte {
	body := f.Content
	if body != "" && !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	if f.Name == store.MemoryIndex {
		return []byte(body)
	}
	var b strings.Builder
	b.WriteString("---\nname: " + strings.TrimSuffix(f.Name, ".md") + "\n")
	b.WriteString("description: " + f.Description + "\n")
	if f.Type != "" {
		b.WriteString("type: " + f.Type + "\n")
	}
	b.WriteString("---\n\n" + body)
	return []byte(b.String())
}

// Parsed is a file read back: its frontmatter fields and the content after it.
type Parsed struct{ Description, Type, Content string }

// Parse reads a file: the frontmatter of a topic file (missing or broken frontmatter leaves the
// whole text as content), MEMORY.md as it is.
func Parse(name string, data []byte) Parsed {
	text := strings.ReplaceAll(strings.TrimPrefix(string(data), "\ufeff"), "\r\n", "\n")
	if name == store.MemoryIndex {
		return Parsed{Content: strings.Trim(text, "\n")}
	}
	head, body, ok := splitFrontmatter(text)
	if !ok {
		return Parsed{Content: strings.Trim(text, "\n")}
	}
	p := Parsed{Content: strings.Trim(body, "\n")}
	for _, line := range strings.Split(head, "\n") {
		key, val, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		switch strings.TrimSpace(strings.ToLower(key)) {
		case "description":
			p.Description = unquote(val)
		case "type":
			p.Type = unquote(val)
		}
	}
	return p
}

func splitFrontmatter(text string) (head, body string, ok bool) {
	if !strings.HasPrefix(text, "---\n") {
		return "", "", false
	}
	rest := text[len("---\n"):]
	if strings.HasPrefix(rest, "---\n") || rest == "---" {
		return "", strings.TrimPrefix(rest, "---"), true
	}
	end := strings.Index(rest, "\n---\n")
	if end < 0 {
		if !strings.HasSuffix(rest, "\n---") {
			return "", "", false
		}
		return rest[:len(rest)-len("\n---")], "", true
	}
	return rest[:end], rest[end+len("\n---\n"):], true
}

func unquote(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
		v = v[1 : len(v)-1]
	}
	return v
}

func bytesReader(b []byte) io.Reader { return bytes.NewReader(b) }
