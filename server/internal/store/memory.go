package store

import (
	"context"
	"time"
)

// MemoryItem is a row of at_project_memory.
type MemoryItem struct {
	ID        string    `json:"id"`
	Text      string    `json:"text"`
	Source    string    `json:"source"`
	Pinned    bool      `json:"pinned"`
	CreatedAt time.Time `json:"created_at"`
}

const memoryCols = `id, text, source, pinned, created_at`

func scanMemory(row interface{ Scan(...any) error }) (MemoryItem, error) {
	var m MemoryItem
	return m, mapErr(row.Scan(&m.ID, &m.Text, &m.Source, &m.Pinned, &m.CreatedAt))
}

// ListMemory returns a project's memory, pinned first then newest.
func (s *Store) ListMemory(ctx context.Context, projectID string) ([]MemoryItem, error) {
	rows, err := s.db.Query(ctx, `SELECT `+memoryCols+` FROM at_project_memory WHERE project_id = $1
		ORDER BY pinned DESC, created_at DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MemoryItem{}
	for rows.Next() {
		m, err := scanMemory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// AddMemory stores one item.
func (s *Store) AddMemory(ctx context.Context, projectID, text, source string) (MemoryItem, error) {
	return scanMemory(s.db.QueryRow(ctx, `INSERT INTO at_project_memory (id, project_id, text, source)
		VALUES ($1, $2, $3, $4) RETURNING `+memoryCols, NewID("me"), projectID, text, source))
}

// UpdateMemory edits text and/or pinned.
func (s *Store) UpdateMemory(ctx context.Context, projectID, id string, changes []Change) (MemoryItem, error) {
	if len(changes) == 0 {
		return scanMemory(s.db.QueryRow(ctx, `SELECT `+memoryCols+` FROM at_project_memory WHERE id = $1 AND project_id = $2`, id, projectID))
	}
	set, args := setClause(changes, 3)
	return scanMemory(s.db.QueryRow(ctx, `UPDATE at_project_memory SET `+set+` WHERE id = $1 AND project_id = $2 RETURNING `+memoryCols,
		append([]any{id, projectID}, args...)...))
}

// DeleteMemory removes one item.
func (s *Store) DeleteMemory(ctx context.Context, projectID, id string) error {
	return affected(s.db.Exec(ctx, `DELETE FROM at_project_memory WHERE id = $1 AND project_id = $2`, id, projectID))
}
