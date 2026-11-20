package store

import (
	"context"
	"encoding/json"
	"errors"

	"gorm.io/gorm/clause"
)

// Setting decodes at_settings[key] into dest; found is false when the key is absent.
func (s *Store) Setting(ctx context.Context, key string, dest any) (bool, error) {
	var row struct{ Value []byte }
	err := mapErr(s.g(ctx).Table("at_settings").Select("value").Where("key = ?", key).Take(&row).Error)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal(row.Value, dest)
}

// SaveSetting upserts at_settings[key].
func (s *Store) SaveSetting(ctx context.Context, key string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return s.g(ctx).Table("at_settings").Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value"}),
	}).Create(map[string]any{"key": key, "value": raw}).Error
}

// InsertSettingIfAbsent stores at_settings[key] unless the key exists; inserted reports which.
func (s *Store) InsertSettingIfAbsent(ctx context.Context, key string, value any) (bool, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return false, err
	}
	res := s.g(ctx).Table("at_settings").Clauses(clause.OnConflict{DoNothing: true}).
		Create(map[string]any{"key": key, "value": raw})
	return res.RowsAffected == 1, res.Error
}

// DeleteSetting removes at_settings[key].
func (s *Store) DeleteSetting(ctx context.Context, key string) error {
	return s.g(ctx).Exec(`DELETE FROM at_settings WHERE key = ?`, key).Error
}
