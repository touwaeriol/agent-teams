package store

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Project auto memory (architecture.md §6.7): MEMORY.md is the index every session reads at
// start; topic files (name, description, content) are read on demand. Writing a topic file keeps
// its line "- [name](name) — description" in MEMORY.md, deleting it drops the line.

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

// MemoryFile is a row of at_memory_files.
type MemoryFile struct {
	ProjectID   string    `json:"-"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Content     string    `json:"content"`
	UpdatedBy   string    `json:"updated_by"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// TableName maps MemoryFile to at_memory_files.
func (MemoryFile) TableName() string { return "at_memory_files" }

// MemoryBrief is a file without its content (lists).
type MemoryBrief struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Size        int       `json:"size" doc:"Characters of content"`
	UpdatedBy   string    `json:"updated_by"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// MemoryError is a rejected write (bad name, too long, too many files); the message is for people.
type MemoryError struct{ Msg string }

func (e *MemoryError) Error() string { return e.Msg }

func memoryErr(format string, a ...any) error { return &MemoryError{Msg: fmt.Sprintf(format, a...)} }

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

func memoryRows(ctx context.Context, q DB, projectID string) *gorm.DB {
	return q.Gorm(ctx).Model(&MemoryFile{}).Where("project_id = ?", projectID)
}

// ListMemoryFiles lists a project's memory files, MEMORY.md first, then by name.
func ListMemoryFiles(ctx context.Context, q DB, projectID string) ([]MemoryBrief, error) {
	out := []MemoryBrief{}
	err := memoryRows(ctx, q, projectID).Select("name, description, char_length(content) AS size, updated_by, updated_at").
		Order("CASE WHEN name = '" + MemoryIndex + "' THEN 0 ELSE 1 END, name").Scan(&out).Error
	return out, err
}

// GetMemoryFile loads one file (ErrNotFound when missing).
func GetMemoryFile(ctx context.Context, q DB, projectID, name string) (MemoryFile, error) {
	var f MemoryFile
	name, err := MemoryName(name)
	if err != nil {
		return f, err
	}
	err = memoryRows(ctx, q, projectID).Where("name = ?", name).Take(&f).Error
	return f, MapErr(err)
}

// MemoryWrite is one create-or-replace.
type MemoryWrite struct {
	Name, Description, Content, By string
}

// WriteMemoryFile creates or replaces a file; a topic file also gets its index line. created
// reports a new file.
func WriteMemoryFile(ctx context.Context, q DB, projectID string, w MemoryWrite) (f MemoryFile, created bool, err error) {
	if w.Name, err = MemoryName(w.Name); err != nil {
		return f, false, err
	}
	w.Description = strings.Join(strings.Fields(w.Description), " ")
	if w.Name == MemoryIndex {
		w.Description = ""
	}
	if err := checkMemoryWrite(w); err != nil {
		return f, false, err
	}
	err = InTx(ctx, q, func(tx DB) error {
		var e error
		if created, e = upsertMemory(ctx, tx, projectID, w); e != nil || w.Name == MemoryIndex {
			return e
		}
		return editIndex(ctx, tx, projectID, w.By, func(lines []string) []string {
			return setIndexLine(lines, w.Name, w.Description)
		})
	})
	if err != nil {
		return f, false, err
	}
	f, err = GetMemoryFile(ctx, q, projectID, w.Name)
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

// upsertMemory writes the row; created = it did not exist. Rejects a new file past the limit.
func upsertMemory(ctx context.Context, tx DB, projectID string, w MemoryWrite) (bool, error) {
	var n int64
	if err := memoryRows(ctx, tx, projectID).Where("name = ?", w.Name).Count(&n).Error; err != nil {
		return false, err
	}
	if n == 0 {
		if err := checkMemoryCount(ctx, tx, projectID); err != nil {
			return false, err
		}
	}
	row := map[string]any{"project_id": projectID, "name": w.Name, "description": w.Description, "content": w.Content,
		"updated_by": w.By, "updated_at": gorm.Expr("now()")}
	err := tx.Gorm(ctx).Table("at_memory_files").Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "project_id"}, {Name: "name"}},
		DoUpdates: clause.AssignmentColumns([]string{"description", "content", "updated_by", "updated_at"}),
	}).Create(row).Error
	return n == 0, err
}

func checkMemoryCount(ctx context.Context, tx DB, projectID string) error {
	var total int64
	if err := memoryRows(ctx, tx, projectID).Count(&total).Error; err != nil {
		return err
	}
	if total >= MemoryFilesMax {
		return memoryErr("这个项目已经有 %d 个记忆文件，先合并或删掉一些", total)
	}
	return nil
}

// DeleteMemoryFile removes a topic file and its index line; MEMORY.md itself cannot be deleted.
func DeleteMemoryFile(ctx context.Context, q DB, projectID, name, by string) error {
	name, err := MemoryName(name)
	if err != nil {
		return err
	}
	if name == MemoryIndex {
		return memoryErr("MEMORY.md 不能删除，可以清空或改写内容")
	}
	return InTx(ctx, q, func(tx DB) error {
		res := memoryRows(ctx, tx, projectID).Where("name = ?", name).Delete(&MemoryFile{})
		if err := Changed(res); err != nil {
			return err
		}
		return editIndex(ctx, tx, projectID, by, func(lines []string) []string { return dropIndexLine(lines, name) })
	})
}

// AppendMemoryNote adds "- text" to MEMORY.md (the remember tool).
func AppendMemoryNote(ctx context.Context, q DB, projectID, text, by string) error {
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		return memoryErr("内容不能为空")
	}
	return InTx(ctx, q, func(tx DB) error {
		return editIndex(ctx, tx, projectID, by, func(lines []string) []string { return append(lines, "- "+text) })
	})
}

// AppendThreadSummary adds a "## #<no> title" section to thread-summaries.md (keeping the latest
// threadSummaryKeep) and makes sure MEMORY.md links it.
func AppendThreadSummary(ctx context.Context, q DB, projectID string, no int, title, summary string) error {
	return InTx(ctx, q, func(tx DB) error {
		f, err := lockedFile(ctx, tx, projectID, ThreadSummaries)
		if err != nil {
			return err
		}
		section := strings.TrimSpace(fmt.Sprintf("## #%d %s\n%s", no, title, strings.TrimSpace(summary)))
		content := keepSections(strings.TrimSpace(f.Content+"\n\n"+section), threadSummaryKeep)
		if utf8.RuneCountInString(content) > MemoryContentMax {
			content = keepSections(content, threadSummaryKeep/3)
		}
		desc := fmt.Sprintf("已结束讨论串的总结（最近 %d 个）", threadSummaryKeep)
		if _, err := upsertMemory(ctx, tx, projectID, MemoryWrite{Name: ThreadSummaries, Description: desc, Content: content, By: "platform"}); err != nil {
			return err
		}
		return editIndex(ctx, tx, projectID, "platform", func(lines []string) []string { return setIndexLine(lines, ThreadSummaries, desc) })
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
func lockedFile(ctx context.Context, tx DB, projectID, name string) (MemoryFile, error) {
	err := tx.Gorm(ctx).Table("at_memory_files").Clauses(clause.OnConflict{DoNothing: true}).
		Create(map[string]any{"project_id": projectID, "name": name, "updated_by": "platform"}).Error
	if err != nil {
		return MemoryFile{}, err
	}
	var f MemoryFile
	err = memoryRows(ctx, tx, projectID).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("name = ?", name).Take(&f).Error
	return f, err
}

// editIndex rewrites MEMORY.md's lines under a row lock (created with a heading when missing).
func editIndex(ctx context.Context, tx DB, projectID, by string, edit func([]string) []string) error {
	f, err := lockedFile(ctx, tx, projectID, MemoryIndex)
	if err != nil {
		return err
	}
	content := f.Content
	if strings.TrimSpace(content) == "" {
		content = "# 项目记忆\n"
	}
	lines := edit(strings.Split(strings.TrimRight(content, "\n"), "\n"))
	next := strings.Join(lines, "\n") + "\n"
	if next == f.Content {
		return nil
	}
	if utf8.RuneCountInString(next) > MemoryContentMax {
		return memoryErr("MEMORY.md 已满（%d 个字符），先整理精简它", MemoryContentMax)
	}
	return memoryRows(ctx, tx, projectID).Where("name = ?", MemoryIndex).
		Updates(map[string]any{"content": next, "updated_by": by, "updated_at": gorm.Expr("now()")}).Error
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
// does not link, with how to read them. "" when the project has no memory yet.
func MemoryPrompt(ctx context.Context, q DB, projectID string) (string, error) {
	files, err := ListMemoryFiles(ctx, q, projectID)
	if err != nil || len(files) == 0 {
		return "", err
	}
	index := ""
	if files[0].Name == MemoryIndex {
		f, err := GetMemoryFile(ctx, q, projectID, MemoryIndex)
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
