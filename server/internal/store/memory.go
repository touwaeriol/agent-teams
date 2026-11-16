package store

import (
	"context"
	"time"

	"gorm.io/gorm"
)

// MemoryItem is a row of at_project_memory.
type MemoryItem struct {
	ID        string    `json:"id"`
	Text      string    `json:"text"`
	Source    string    `json:"source"`
	Pinned    bool      `json:"pinned"`
	CreatedAt time.Time `json:"created_at"`
}

// TableName maps MemoryItem to at_project_memory.
func (MemoryItem) TableName() string { return "at_project_memory" }

func (s *Store) memory(ctx context.Context, projectID string) *gorm.DB {
	return s.g(ctx).Model(&MemoryItem{}).Where("project_id = ?", projectID)
}

// ListMemory returns a project's memory, pinned first then newest.
func (s *Store) ListMemory(ctx context.Context, projectID string) ([]MemoryItem, error) {
	out := []MemoryItem{}
	err := s.memory(ctx, projectID).Order("pinned DESC, created_at DESC").Find(&out).Error
	return out, err
}

// AddMemory stores one item.
func (s *Store) AddMemory(ctx context.Context, projectID, text, source string) (MemoryItem, error) {
	id := NewID("me")
	err := s.g(ctx).Table("at_project_memory").Create(map[string]any{"id": id, "project_id": projectID,
		"text": text, "source": source}).Error
	if err != nil {
		return MemoryItem{}, mapErr(err)
	}
	return s.memoryItem(ctx, projectID, id)
}

func (s *Store) memoryItem(ctx context.Context, projectID, id string) (MemoryItem, error) {
	var m MemoryItem
	err := s.memory(ctx, projectID).Where("id = ?", id).Take(&m).Error
	return m, mapErr(err)
}

// UpdateMemory edits text and/or pinned.
func (s *Store) UpdateMemory(ctx context.Context, projectID, id string, changes []Change) (MemoryItem, error) {
	if len(changes) > 0 {
		res := s.g(ctx).Table("at_project_memory").Where("id = ? AND project_id = ?", id, projectID).Updates(changeMap(changes))
		if err := Changed(res); err != nil {
			return MemoryItem{}, err
		}
	}
	return s.memoryItem(ctx, projectID, id)
}

// DeleteMemory removes one item.
func (s *Store) DeleteMemory(ctx context.Context, projectID, id string) error {
	return Changed(s.g(ctx).Where("id = ? AND project_id = ?", id, projectID).Delete(&MemoryItem{}))
}
