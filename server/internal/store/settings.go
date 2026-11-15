package store

import (
	"context"
	"encoding/json"
	"errors"
)

// Setting decodes at_settings[key] into dest; found is false when the key is absent.
func (s *Store) Setting(ctx context.Context, key string, dest any) (bool, error) {
	var raw []byte
	err := mapErr(s.db.QueryRow(ctx, `SELECT value FROM at_settings WHERE key = $1`, key).Scan(&raw))
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal(raw, dest)
}

// SaveSetting upserts at_settings[key].
func (s *Store) SaveSetting(ctx context.Context, key string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO at_settings (key, value) VALUES ($1, $2::jsonb)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, key, string(raw))
	return err
}

// DeleteSetting removes at_settings[key].
func (s *Store) DeleteSetting(ctx context.Context, key string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM at_settings WHERE key = $1`, key)
	return err
}
