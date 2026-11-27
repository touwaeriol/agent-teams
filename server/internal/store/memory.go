package store

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Auto memory (architecture.md §6.7): MEMORY.md is the index every session reads at start; topic
// files (name, description, content) are read on demand. Writing a topic file keeps its line
// "- [name](name) — description" in MEMORY.md, deleting it drops the line. A scope is a project's
// memory or the user's own (every project of the user reads it). A write marks the row pending
// until something else (memfiles) has stored it elsewhere.

// MemoryIndex is the index file's name.
const MemoryIndex = "MEMORY.md"

// ThreadSummaries collects the leads' finish summaries.
const ThreadSummaries = "thread-summaries.md"

// Memory limits: characters per file, per description, files per project, summaries kept.
const (
	MemoryContentMax  = 32000
	MemoryDescMax     = 300
	MemoryFilesMax    = 200
	threadSummaryKeep = 30
)

// Memory scopes.
const (
	MemoryProject = "project"
	MemoryUser    = "user"
)

// MemoryScope is whose memory: a project's (ID = project id) or the user's own (ID = user id).
type MemoryScope struct{ Kind, ID string }

// ProjectMemory is a project's memory.
func ProjectMemory(projectID string) MemoryScope {
	return MemoryScope{Kind: MemoryProject, ID: projectID}
}

// UserMemory is the user's own memory, read by every project of the user.
func UserMemory(userID string) MemoryScope { return MemoryScope{Kind: MemoryUser, ID: userID} }

// memoryScopes is each scope kind's table, owner column and MEMORY.md heading.
var memoryScopes = map[string]struct{ table, key, heading string }{
	MemoryProject: {"at_memory_files", "project_id", "# 项目记忆\n"},
	MemoryUser:    {"at_user_memory_files", "user_id", "# 用户记忆\n"},
}

func (s MemoryScope) table() string   { return memoryScopes[s.Kind].table }
func (s MemoryScope) key() string     { return memoryScopes[s.Kind].key }
func (s MemoryScope) heading() string { return memoryScopes[s.Kind].heading }

// MemoryFile is a memory file (a row of at_memory_files or at_user_memory_files).
type MemoryFile struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Type        string     `json:"type" doc:"Optional kind of a topic file: project, feedback, reference, user"`
	Content     string     `json:"content"`
	UpdatedBy   string     `json:"updated_by"`
	Pending     bool       `json:"pending" doc:"Changed here and not yet written into the shared folder"`
	FileMtime   *time.Time `json:"-"`
	FileSize    int64      `json:"-"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// TableName maps MemoryFile to at_memory_files (a user's own memory reads at_user_memory_files
// through MemoryScope, with the same columns).
func (MemoryFile) TableName() string { return "at_memory_files" }

// MemoryBrief is a file without its content (lists).
type MemoryBrief struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Type        string    `json:"type"`
	Size        int       `json:"size" doc:"Characters of content"`
	UpdatedBy   string    `json:"updated_by"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// MemoryError is a rejected write (bad name, too long, too many files); the message is for people.
type MemoryError struct{ Msg string }

func (e *MemoryError) Error() string { return e.Msg }

func memoryErr(format string, a ...any) error { return &MemoryError{Msg: fmt.Sprintf(format, a...)} }

// NewMemoryError is a rejected change with a message for people.
func NewMemoryError(msg string) error { return &MemoryError{Msg: msg} }

// IsMemoryError reports whether err is a rejected write.
func IsMemoryError(err error) bool {
	var me *MemoryError
	return errors.As(err, &me)
}

var memoryName = regexp.MustCompile(`^[\p{L}\p{N}][\p{L}\p{N}._ -]{0,79}$`)

// MemoryName normalizes a file name: trimmed, ".md" appended when missing.
func MemoryName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if strings.EqualFold(name, MemoryIndex) || strings.EqualFold(name, "MEMORY") {
		return MemoryIndex, nil
	}
	if !strings.HasSuffix(strings.ToLower(name), ".md") {
		name += ".md"
	}
	if !memoryName.MatchString(name) || strings.Contains(name, "..") {
		return "", memoryErr("文件名只能用字母、数字、空格、. _ -，不能有路径，最长 80 个字符")
	}
	return name, nil
}

// memoryTypes are the kinds a topic file may name (empty: none).
var memoryTypes = []string{"project", "feedback", "reference", "user"}

// MemoryType normalizes a kind: one of memoryTypes, anything else is dropped.
func MemoryType(t string) string {
	t = strings.ToLower(strings.TrimSpace(t))
	if slices.Contains(memoryTypes, t) {
		return t
	}
	return ""
}

// normalizeMemory is a file's description and type as stored: one line, a known kind, neither
// for MEMORY.md.
func normalizeMemory(name, desc, typ string) (string, string) {
	if name == MemoryIndex {
		return "", ""
	}
	return oneLine(desc), MemoryType(typ)
}

func memoryRows(ctx context.Context, q DB, s MemoryScope) *gorm.DB {
	return q.Gorm(ctx).Table(s.table()).Where(s.key()+" = ?", s.ID)
}

// ListMemoryFiles lists a scope's memory files, MEMORY.md first, then by name.
func ListMemoryFiles(ctx context.Context, q DB, s MemoryScope) ([]MemoryBrief, error) {
	out := []MemoryBrief{}
	err := memoryRows(ctx, q, s).Select("name, description, type, char_length(content) AS size, updated_by, updated_at").
		Order("CASE WHEN name = '" + MemoryIndex + "' THEN 0 ELSE 1 END, name").Scan(&out).Error
	return out, err
}

// GetMemoryFile loads one file (ErrNotFound when missing).
func GetMemoryFile(ctx context.Context, q DB, s MemoryScope, name string) (MemoryFile, error) {
	var f MemoryFile
	name, err := MemoryName(name)
	if err != nil {
		return f, err
	}
	err = memoryRows(ctx, q, s).Where("name = ?", name).Take(&f).Error
	return f, MapErr(err)
}

// MemoryWrite is one create-or-replace.
type MemoryWrite struct {
	Name, Description, Type, Content, By string
}

// WriteMemoryFile creates or replaces a file; a topic file also gets its index line. created
// reports a new file.
func WriteMemoryFile(ctx context.Context, q DB, s MemoryScope, w MemoryWrite) (f MemoryFile, created bool, err error) {
	if w.Name, err = MemoryName(w.Name); err != nil {
		return f, false, err
	}
	w.Description, w.Type = normalizeMemory(w.Name, w.Description, w.Type)
	if err := checkMemoryWrite(w); err != nil {
		return f, false, err
	}
	err = InTx(ctx, q, func(tx DB) error {
		var e error
		if created, e = upsertMemory(ctx, tx, s, w); e != nil || w.Name == MemoryIndex {
			return e
		}
		return editIndex(ctx, tx, s, w.By, func(lines []string) []string {
			return setIndexLine(lines, w.Name, w.Description)
		})
	})
	if err != nil {
		return f, false, err
	}
	f, err = GetMemoryFile(ctx, q, s, w.Name)
	return f, created, err
}

func checkMemoryWrite(w MemoryWrite) error {
	if n := utf8.RuneCountInString(w.Content); n > MemoryContentMax {
		return memoryErr("内容 %d 个字符，超过上限 %d；拆成几个文件，或删掉过时的内容", n, MemoryContentMax)
	}
	if utf8.RuneCountInString(w.Description) > MemoryDescMax {
		return memoryErr("描述最多 %d 个字符", MemoryDescMax)
	}
	if w.Name != MemoryIndex && w.Description == "" {
		return memoryErr("请写一句描述（会列在 MEMORY.md 里，帮别人判断要不要读这个文件）")
	}
	return nil
}

// upsertMemory writes the row (pending: it goes to the folder next); created = it did not exist.
// Rejects a new file past the limit.
func upsertMemory(ctx context.Context, tx DB, s MemoryScope, w MemoryWrite) (bool, error) {
	created, err := newMemoryFile(ctx, tx, s, w.Name)
	if err != nil {
		return false, err
	}
	row := map[string]any{s.key(): s.ID, "name": w.Name, "description": w.Description, "type": w.Type, "content": w.Content,
		"updated_by": w.By, "pending": true, "updated_at": gorm.Expr("now()")}
	err = tx.Gorm(ctx).Table(s.table()).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: s.key()}, {Name: "name"}},
		DoUpdates: clause.AssignmentColumns([]string{"description", "type", "content", "updated_by", "pending", "updated_at"}),
	}).Create(row).Error
	return created, err
}

// newMemoryFile reports whether name is not a file of the scope yet; a MemoryError when a new
// file would pass MemoryFilesMax.
func newMemoryFile(ctx context.Context, tx DB, s MemoryScope, name string) (bool, error) {
	var n, total int64
	if err := memoryRows(ctx, tx, s).Where("name = ?", name).Count(&n).Error; err != nil || n > 0 {
		return false, err
	}
	if err := memoryRows(ctx, tx, s).Count(&total).Error; err != nil {
		return true, err
	}
	if total >= MemoryFilesMax {
		return true, memoryErr("已经有 %d 个记忆文件，先合并或删掉一些", total)
	}
	return true, nil
}

// DeleteMemoryFile removes a topic file and its index line; MEMORY.md itself cannot be deleted.
func DeleteMemoryFile(ctx context.Context, q DB, s MemoryScope, name, by string) error {
	name, err := MemoryName(name)
	if err != nil {
		return err
	}
	if name == MemoryIndex {
		return memoryErr("MEMORY.md 不能删除，可以清空或改写内容")
	}
	return InTx(ctx, q, func(tx DB) error {
		res := memoryRows(ctx, tx, s).Where("name = ?", name).Delete(&MemoryFile{})
		if err := Changed(res); err != nil {
			return err
		}
		return editIndex(ctx, tx, s, by, func(lines []string) []string { return dropIndexLine(lines, name) })
	})
}

// AppendMemoryNote adds "- text" to MEMORY.md (the remember tool).
func AppendMemoryNote(ctx context.Context, q DB, s MemoryScope, text, by string) error {
	text = oneLine(text)
	if text == "" {
		return memoryErr("内容不能为空")
	}
	return InTx(ctx, q, func(tx DB) error {
		return editIndex(ctx, tx, s, by, func(lines []string) []string { return append(lines, "- "+text) })
	})
}

// AppendThreadSummary adds a "## #<no> title" section to thread-summaries.md (keeping the latest
// threadSummaryKeep) and makes sure MEMORY.md links it.
func AppendThreadSummary(ctx context.Context, q DB, projectID string, no int, title, summary string) error {
	s := ProjectMemory(projectID)
	return InTx(ctx, q, func(tx DB) error {
		f, err := lockedFile(ctx, tx, s, ThreadSummaries)
		if err != nil {
			return err
		}
		section := strings.TrimSpace(fmt.Sprintf("## #%d %s\n%s", no, title, strings.TrimSpace(summary)))
		content := keepSections(strings.TrimSpace(f.Content+"\n\n"+section), threadSummaryKeep)
		if utf8.RuneCountInString(content) > MemoryContentMax {
			content = keepSections(content, threadSummaryKeep/3)
		}
		desc := fmt.Sprintf("已结束讨论串的总结（最近 %d 个）", threadSummaryKeep)
		if _, err := upsertMemory(ctx, tx, s, MemoryWrite{Name: ThreadSummaries, Description: desc, Type: f.Type, Content: content, By: "platform"}); err != nil {
			return err
		}
		return editIndex(ctx, tx, s, "platform", func(lines []string) []string { return setIndexLine(lines, ThreadSummaries, desc) })
	})
}

// keepSections keeps the last n "## " sections of a document.
func keepSections(doc string, n int) string {
	parts := strings.Split("\n"+doc, "\n## ")
	head, secs := parts[0], parts[1:]
	if len(secs) <= n {
		return doc
	}
	secs = secs[len(secs)-n:]
	return strings.TrimSpace(strings.TrimSpace(head) + "\n\n## " + strings.Join(secs, "\n\n## "))
}

// lockedFile loads a file FOR UPDATE, creating an empty one first when missing.
func lockedFile(ctx context.Context, tx DB, s MemoryScope, name string) (MemoryFile, error) {
	err := tx.Gorm(ctx).Table(s.table()).Clauses(clause.OnConflict{DoNothing: true}).
		Create(map[string]any{s.key(): s.ID, "name": name, "updated_by": "platform", "pending": true}).Error
	if err != nil {
		return MemoryFile{}, err
	}
	var f MemoryFile
	err = memoryRows(ctx, tx, s).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("name = ?", name).Take(&f).Error
	return f, err
}

// editIndex rewrites MEMORY.md's lines under a row lock (created with a heading when missing).
func editIndex(ctx context.Context, tx DB, s MemoryScope, by string, edit func([]string) []string) error {
	f, err := lockedFile(ctx, tx, s, MemoryIndex)
	if err != nil {
		return err
	}
	content := f.Content
	if strings.TrimSpace(content) == "" {
		content = s.heading()
	}
	lines := edit(strings.Split(strings.TrimRight(content, "\n"), "\n"))
	next := strings.Join(lines, "\n") + "\n"
	if strings.TrimRight(next, "\n") == strings.TrimRight(f.Content, "\n") { // a file read back has no final newline
		return nil
	}
	if utf8.RuneCountInString(next) > MemoryContentMax {
		return memoryErr("MEMORY.md 已满（%d 个字符），先整理精简它", MemoryContentMax)
	}
	return memoryRows(ctx, tx, s).Where("name = ?", MemoryIndex).
		Updates(map[string]any{"content": next, "updated_by": by, "pending": true, "updated_at": gorm.Expr("now()")}).Error
}

func indexLink(name string) string { return "](" + name + ")" }

// setIndexLine replaces the line linking name, or appends one.
func setIndexLine(lines []string, name, desc string) []string {
	line := fmt.Sprintf("- [%s](%s) — %s", name, name, desc)
	for i, l := range lines {
		if strings.Contains(l, indexLink(name)) {
			lines[i] = line
			return lines
		}
	}
	return append(lines, line)
}

func dropIndexLine(lines []string, name string) []string {
	out := lines[:0]
	for _, l := range lines {
		if !strings.Contains(l, indexLink(name)) {
			out = append(out, l)
		}
	}
	return out
}

// Prompt bounds for MEMORY.md (Claude Code reads the first 200 lines of its MEMORY.md).
const (
	memoryPromptLines = 200
	memoryPromptBytes = 12 << 10
	memoryPromptFiles = 50
)

// MemoryPrompt renders what every session reads at start: MEMORY.md (bounded) and the files it
// does not link, with how to read them. "" when the scope has no memory yet.
func MemoryPrompt(ctx context.Context, q DB, s MemoryScope) (string, error) {
	files, err := ListMemoryFiles(ctx, q, s)
	if err != nil || len(files) == 0 {
		return "", err
	}
	index := ""
	if files[0].Name == MemoryIndex {
		f, err := GetMemoryFile(ctx, q, s, MemoryIndex)
		if err != nil {
			return "", err
		}
		index = clipIndex(f.Content)
	}
	var b strings.Builder
	if index != "" {
		b.WriteString("### MEMORY.md\n" + index + "\n")
	}
	if other := unlinked(files, index); len(other) > 0 {
		b.WriteString("\n其他记忆文件：\n" + strings.Join(other, "\n") + "\n")
	}
	return strings.TrimSpace(b.String()), nil
}

func clipIndex(content string) string {
	content = strings.TrimSpace(content)
	lines := strings.Split(content, "\n")
	cut := false
	if len(lines) > memoryPromptLines {
		lines, cut = lines[:memoryPromptLines], true
	}
	out := strings.Join(lines, "\n")
	if len(out) > memoryPromptBytes {
		out, cut = strings.ToValidUTF8(out[:memoryPromptBytes], ""), true
	}
	if cut {
		out += "\n…（MEMORY.md 后面的内容没有载入，用 memory_read(\"MEMORY.md\") 读全文，并把它整理精简）"
	}
	return out
}

// unlinked lists topic files MEMORY.md does not mention.
func unlinked(files []MemoryBrief, index string) []string {
	var out []string
	for _, f := range files {
		if f.Name == MemoryIndex || strings.Contains(index, f.Name) {
			continue
		}
		if len(out) == memoryPromptFiles {
			out = append(out, "- …")
			break
		}
		out = append(out, fmt.Sprintf("- %s — %s", f.Name, f.Description))
	}
	return out
}
