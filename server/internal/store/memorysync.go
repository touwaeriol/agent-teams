package store

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"
)

// What memfiles needs to keep the memory rows in step with the files in the owner's shared folder.

// MemoryTouchFresh is how long a touch credits changes and keeps its scope read every sweep.
const MemoryTouchFresh = 10 * time.Minute

// MemoryState is what the folder sync compares a file with.
type MemoryState struct {
	Name      string
	Pending   bool
	FileMtime *time.Time
	FileSize  int64
	UpdatedAt time.Time
}

// MemoryStates is every file of a scope by name.
func MemoryStates(ctx context.Context, q DB, s MemoryScope) (map[string]MemoryState, error) {
	var rows []MemoryState
	err := memoryRows(ctx, q, s).Select("name, pending, file_mtime, file_size, updated_at").Scan(&rows).Error
	out := make(map[string]MemoryState, len(rows))
	for _, r := range rows {
		out[r.Name] = r
	}
	return out, err
}

// PendingMemory is the files written here and not yet in the folder.
func PendingMemory(ctx context.Context, q DB, s MemoryScope) ([]MemoryFile, error) {
	var out []MemoryFile
	err := memoryRows(ctx, q, s).Where("pending").Order("name").Find(&out).Error
	return out, err
}

// MarkMemoryFlushed records that a pending file is in the folder as written (mtime, size as the
// folder reports them). A row changed again since it was read (updated_at moved) stays pending.
func MarkMemoryFlushed(ctx context.Context, q DB, s MemoryScope, name string, updatedAt, mtime time.Time, size int64) error {
	return memoryRows(ctx, q, s).Where("name = ? AND pending AND updated_at = ?", name, updatedAt).
		Updates(map[string]any{"pending": false, "file_mtime": mtime, "file_size": size}).Error
}

// MarkMemoryPending has a file written out again (MEMORY.md removed from the folder).
func MarkMemoryPending(ctx context.Context, q DB, s MemoryScope, name string) error {
	return memoryRows(ctx, q, s).Where("name = ?", name).Update("pending", true).Error
}

// MemoryImport is a file as read from the folder.
type MemoryImport struct {
	Name, Description, Type, Content, By string
	ModTime                              time.Time
	Size                                 int64
}

// ImportMemoryFile stores a file changed in the folder (a topic file also gets its index line).
// It leaves a row alone that was written here meanwhile (pending: that write is newer) and skips a
// new file past the limit; ok reports whether the row now holds the file.
func ImportMemoryFile(ctx context.Context, q DB, s MemoryScope, in MemoryImport) (ok bool, err error) {
	in.Description = clipRunes(oneLine(in.Description), MemoryDescMax)
	in.Content = clipRunes(in.Content, MemoryContentMax)
	in.Type = MemoryType(in.Type)
	if in.Name == MemoryIndex {
		in.Description, in.Type = "", ""
	}
	err = InTx(ctx, q, func(tx DB) error {
		var e error
		if ok, e = importRow(ctx, tx, s, in); e != nil || !ok || in.Name == MemoryIndex {
			return e
		}
		return editIndex(ctx, tx, s, "platform", func(lines []string) []string { return setIndexLine(lines, in.Name, in.Description) })
	})
	return ok, err
}

func importRow(ctx context.Context, tx DB, s MemoryScope, in MemoryImport) (bool, error) {
	var n int64
	if err := memoryRows(ctx, tx, s).Where("name = ?", in.Name).Count(&n).Error; err != nil {
		return false, err
	}
	if n == 0 && checkMemoryCount(ctx, tx, s) != nil {
		return false, nil
	}
	t := s.table()
	res := tx.Gorm(ctx).Exec(`INSERT INTO `+t+` (`+s.key()+`, name, description, type, content, updated_by, pending, file_mtime, file_size, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, false, ?, ?, ?)
		ON CONFLICT (`+s.key()+`, name) DO UPDATE SET description = EXCLUDED.description, type = EXCLUDED.type,
			content = EXCLUDED.content, updated_by = EXCLUDED.updated_by, file_mtime = EXCLUDED.file_mtime,
			file_size = EXCLUDED.file_size, updated_at = EXCLUDED.updated_at
		WHERE NOT `+t+`.pending`,
		s.ID, in.Name, in.Description, in.Type, in.Content, in.By, in.ModTime, in.Size, in.ModTime)
	return res.RowsAffected > 0, res.Error
}

// DropMemoryFile forgets a topic file deleted from the folder (row and index line); a row written
// here meanwhile stays (it is written out again).
func DropMemoryFile(ctx context.Context, q DB, s MemoryScope, name string) (bool, error) {
	var dropped bool
	err := InTx(ctx, q, func(tx DB) error {
		res := memoryRows(ctx, tx, s).Where("name = ? AND NOT pending", name).Delete(&MemoryFile{})
		if res.Error != nil || res.RowsAffected == 0 {
			return res.Error
		}
		dropped = true
		return editIndex(ctx, tx, s, "platform", func(lines []string) []string { return dropIndexLine(lines, name) })
	})
	return dropped, err
}

// TouchMemory records that by is editing a memory file on a node.
func TouchMemory(ctx context.Context, q DB, s MemoryScope, name, by string) error {
	return q.Gorm(ctx).Exec(`INSERT INTO at_memory_touches (scope, scope_id, name, by) VALUES (?, ?, ?, ?)
		ON CONFLICT (scope, scope_id, name) DO UPDATE SET by = EXCLUDED.by, at = now()`, s.Kind, s.ID, name, by).Error
}

// MemoryTouches is who touched which file of a scope lately, by name.
func MemoryTouches(ctx context.Context, q DB, s MemoryScope) (map[string]string, error) {
	var rows []struct{ Name, By string }
	err := q.Gorm(ctx).Raw(`SELECT name, by FROM at_memory_touches WHERE scope = ? AND scope_id = ? AND at > ?`,
		s.Kind, s.ID, time.Now().Add(-MemoryTouchFresh)).Scan(&rows).Error
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		out[r.Name] = r.By
	}
	return out, err
}

// PruneMemoryTouches drops the touches that no longer count.
func PruneMemoryTouches(ctx context.Context, q DB) error {
	return q.Gorm(ctx).Exec(`DELETE FROM at_memory_touches WHERE at <= ?`, time.Now().Add(-MemoryTouchFresh)).Error
}

// MemoryScopesToSync is the scopes whose owner has a ready shared folder and that have rows to
// write out or a fresh touch; all adds every such scope (the occasional full pass).
func MemoryScopesToSync(ctx context.Context, q DB, all bool) ([]MemoryScope, error) {
	sql := `SELECT 'project' AS kind, m.project_id AS id FROM at_memory_files m
			JOIN at_projects p ON p.id = m.project_id JOIN at_user_fs f ON f.user_id = p.user_id AND f.state = 'ready' WHERE m.pending
		UNION SELECT 'user', m.user_id FROM at_user_memory_files m JOIN at_user_fs f ON f.user_id = m.user_id AND f.state = 'ready' WHERE m.pending
		UNION SELECT scope, scope_id FROM at_memory_touches WHERE at > ?`
	if all {
		sql += `
		UNION SELECT 'project', p.id FROM at_projects p JOIN at_user_fs f ON f.user_id = p.user_id AND f.state = 'ready'
		UNION SELECT 'user', user_id FROM at_user_fs WHERE state = 'ready'`
	}
	var out []MemoryScope
	err := q.Gorm(ctx).Raw(sql, time.Now().Add(-MemoryTouchFresh)).Scan(&out).Error
	return out, err
}

// MemoryOwner is the user whose shared folder holds a scope's files.
func MemoryOwner(ctx context.Context, q DB, s MemoryScope) (string, error) {
	if s.Kind == MemoryUser {
		return s.ID, nil
	}
	var userID string
	err := QueryRow(ctx, q, `SELECT user_id FROM at_projects WHERE id = $1`, s.ID).Scan(&userID)
	return userID, MapErr(err)
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func clipRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// MemoryFiles keeps memory in the owners' shared folders (memfiles.Syncer); NoMemoryFiles when
// the console has none (the rows are then all there is).
type MemoryFiles interface {
	// Removing deletes a file from the folder before its row goes (a MemoryError when it cannot).
	Removing(ctx context.Context, s MemoryScope, name string) error
	// Kick syncs the scope after a while: rows were written, or an employee is editing a file.
	Kick(s MemoryScope, after time.Duration)
	// Sync reads the folder into the rows and writes pending rows out, now.
	Sync(ctx context.Context, s MemoryScope) error
}

// NoMemoryFiles is MemoryFiles without shared folders.
type NoMemoryFiles struct{}

// Removing implements MemoryFiles.
func (NoMemoryFiles) Removing(context.Context, MemoryScope, string) error { return nil }

// Kick implements MemoryFiles.
func (NoMemoryFiles) Kick(MemoryScope, time.Duration) {}

// Sync implements MemoryFiles.
func (NoMemoryFiles) Sync(context.Context, MemoryScope) error { return nil }
