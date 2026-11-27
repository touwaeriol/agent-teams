package memfiles

import (
	"regexp"
	"strconv"
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
	b.WriteString("description: " + quoteValue(f.Description) + "\n")
	if f.Type != "" {
		b.WriteString("type: " + f.Type + "\n")
	}
	b.WriteString("---\n\n" + body)
	return []byte(b.String())
}

// quoteValue quotes a value that would not read back as it is (it starts with a quote).
func quoteValue(v string) string {
	if strings.HasPrefix(v, `"`) || strings.HasPrefix(v, "'") {
		return strconv.Quote(v)
	}
	return v
}

// Parsed is a file read back: its frontmatter fields and the content after it. Frontmatter is
// false when the file has none (its description and type are then unknown, not empty).
type Parsed struct {
	Description, Type, Content string
	Frontmatter                bool
}

// Parse reads a file: the frontmatter of a topic file (missing or broken frontmatter, or a
// leading horizontal rule, leaves the whole text as content), MEMORY.md as it is.
func Parse(name string, data []byte) Parsed {
	text := strings.ReplaceAll(strings.TrimPrefix(string(data), "\ufeff"), "\r\n", "\n")
	if name == store.MemoryIndex {
		return Parsed{Content: strings.Trim(text, "\n")}
	}
	head, body, ok := splitFrontmatter(text)
	if !ok || !isFrontmatter(head) {
		return Parsed{Content: strings.Trim(text, "\n")}
	}
	p := Parsed{Content: strings.Trim(body, "\n"), Frontmatter: true}
	for _, line := range strings.Split(head, "\n") {
		key, val, _ := strings.Cut(line, ":")
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

// isFrontmatter: every line is "key: value" (or blank), so a leading "---" rule is not taken.
func isFrontmatter(head string) bool {
	for _, line := range strings.Split(head, "\n") {
		key, _, found := strings.Cut(line, ":")
		if strings.TrimSpace(line) != "" && (!found || !frontmatterKey.MatchString(strings.TrimSpace(key))) {
			return false
		}
	}
	return true
}

var frontmatterKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

func unquote(v string) string {
	v = strings.TrimSpace(v)
	if u, err := strconv.Unquote(v); err == nil && strings.HasPrefix(v, `"`) {
		return u
	}
	if len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'' {
		v = v[1 : len(v)-1]
	}
	return v
}
