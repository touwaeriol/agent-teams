package userfs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/credvault"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// Volume is an at_user_fs row without its secrets.
type Volume struct {
	UserID     string    `json:"-"`
	Volume     string    `json:"volume"`
	MetaRole   string    `json:"-"`
	AccessKey  string    `json:"-"`
	Generation int       `json:"generation"`
	State      string    `json:"state"`
	Error      string    `json:"error"`
	CapacityGB int       `json:"capacity_gb"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

const volumeCols = `user_id, volume, meta_role, s3_access_key, generation, state, error, capacity_gb, created_at, updated_at`

func scanVolume(row store.Scanner) (Volume, error) {
	var v Volume
	err := row.Scan(&v.UserID, &v.Volume, &v.MetaRole, &v.AccessKey, &v.Generation, &v.State, &v.Error, &v.CapacityGB, &v.CreatedAt, &v.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return v, ErrNotFound
	}
	return v, err
}

// Get returns the user's volume (ErrNotFound when none).
func Get(ctx context.Context, q store.DB, userID string) (Volume, error) {
	return scanVolume(store.QueryRow(ctx, q, `SELECT `+volumeCols+` FROM at_user_fs WHERE user_id = $1`, userID))
}

// insert creates the user's row (provisioning) unless one exists; it returns the row either way.
func insert(ctx context.Context, q store.DB, v *credvault.Vault, userID string, capacityGB int) (Volume, error) {
	n := NamesFor(userID)
	raw, _ := json.Marshal(NewSecrets())
	sealed, err := v.Seal(credvault.UserFsAAD(userID, n.Volume, 1), raw)
	if err != nil {
		return Volume{}, err
	}
	sj, _ := json.Marshal(sealed)
	if _, err := store.Exec(ctx, q, `INSERT INTO at_user_fs (user_id, volume, meta_role, s3_access_key, secrets, capacity_gb)
		VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT (user_id) DO NOTHING`, userID, n.Volume, n.Role, n.AccessKey, sj, capacityGB); err != nil {
		return Volume{}, err
	}
	return Get(ctx, q, userID)
}

// secrets opens the row's sealed secrets.
func secrets(ctx context.Context, q store.DB, v *credvault.Vault, vol Volume) (Secrets, error) {
	var raw []byte
	if err := store.QueryRow(ctx, q, `SELECT secrets FROM at_user_fs WHERE user_id = $1`, vol.UserID).Scan(&raw); err != nil {
		return Secrets{}, err
	}
	var sealed credvault.Sealed
	if err := json.Unmarshal(raw, &sealed); err != nil {
		return Secrets{}, err
	}
	plain, err := v.Open(credvault.UserFsAAD(vol.UserID, vol.Volume, vol.Generation), sealed)
	if err != nil {
		return Secrets{}, err
	}
	var s Secrets
	return s, json.Unmarshal(plain, &s)
}

func setState(ctx context.Context, q store.DB, userID, state, msg string) error {
	_, err := store.Exec(ctx, q, `UPDATE at_user_fs SET state = $2, error = $3, updated_at = now() WHERE user_id = $1`, userID, state, msg)
	return err
}

func remove(ctx context.Context, q store.DB, userID string) error {
	_, err := store.Exec(ctx, q, `DELETE FROM at_user_fs WHERE user_id = $1`, userID)
	return err
}
