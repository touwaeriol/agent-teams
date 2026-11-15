package store

import (
	"context"
	"time"
)

// GitConnection is a row of at_git_connections (the sealed token stays in the store).
type GitConnection struct {
	ID             string
	UserID         string
	Name           string
	Provider       string
	Login          string
	Kind           string
	InstallationID *int64
	CreatedAt      time.Time
	Permissions    map[string]string
	AccountType    string
	CheckedAt      *time.Time
}

const connCols = `c.id, c.user_id, c.name, c.provider, c.login, c.kind, c.installation_id, c.created_at, c.permissions, c.account_type, c.checked_at`

func scanConn(row interface{ Scan(...any) error }) (GitConnection, error) {
	var c GitConnection
	err := row.Scan(&c.ID, &c.UserID, &c.Name, &c.Provider, &c.Login, &c.Kind, &c.InstallationID,
		&c.CreatedAt, &c.Permissions, &c.AccountType, &c.CheckedAt)
	return c, mapErr(err)
}

// CreateConnection stores a PAT (sealed) or a GitHub App installation (installationID set, secret "").
func (s *Store) CreateConnection(ctx context.Context, userID, name, login, sealed string, installationID *int64) (GitConnection, error) {
	kind := "token"
	if installationID != nil {
		kind = "app"
	}
	row := s.db.QueryRow(ctx, `INSERT INTO at_git_connections AS c (id, user_id, name, login, token_secret, kind, installation_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING `+connCols, NewHexID(), userID, name, login, sealed, kind, installationID)
	return scanConn(row)
}

// Connections lists the user's connections.
func (s *Store) Connections(ctx context.Context, userID string) ([]GitConnection, error) {
	return s.queryConns(ctx, `SELECT `+connCols+` FROM at_git_connections c WHERE c.user_id = $1 ORDER BY c.created_at`, userID)
}

// ConnectionsByInstallation finds every connection of an App installation (webhooks).
func (s *Store) ConnectionsByInstallation(ctx context.Context, installationID int64) ([]GitConnection, error) {
	return s.queryConns(ctx, `SELECT `+connCols+` FROM at_git_connections c WHERE c.installation_id = $1`, installationID)
}

func (s *Store) queryConns(ctx context.Context, sql string, arg any) ([]GitConnection, error) {
	rows, err := s.db.Query(ctx, sql, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []GitConnection{}
	for rows.Next() {
		c, err := scanConn(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Connection loads one of the user's connections.
func (s *Store) Connection(ctx context.Context, userID, id string) (GitConnection, error) {
	return scanConn(s.db.QueryRow(ctx, `SELECT `+connCols+` FROM at_git_connections c WHERE c.id = $1 AND c.user_id = $2`, id, userID))
}

// ConnectionSecret returns the sealed PAT of one of the user's connections.
func (s *Store) ConnectionSecret(ctx context.Context, userID, id string) (string, error) {
	var sealed string
	err := s.db.QueryRow(ctx, `SELECT token_secret FROM at_git_connections WHERE id = $1 AND user_id = $2`, id, userID).Scan(&sealed)
	return sealed, mapErr(err)
}

// SetConnectionPermissions records what the installation actually grants and who owns it.
func (s *Store) SetConnectionPermissions(ctx context.Context, id string, perms map[string]string, accountType string) error {
	return affected(s.db.Exec(ctx, `UPDATE at_git_connections SET permissions = $2,
		account_type = COALESCE(NULLIF($3, ''), account_type), checked_at = now() WHERE id = $1`, id, perms, accountType))
}

// DeleteConnection removes one of the user's connections (projects keep their repo, lose the link).
func (s *Store) DeleteConnection(ctx context.Context, userID, id string) error {
	return affected(s.db.Exec(ctx, `DELETE FROM at_git_connections WHERE id = $1 AND user_id = $2`, id, userID))
}

// SealedSecrets lists every sealed PAT (atctl admin reseal).
func (s *Store) SealedSecrets(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.Query(ctx, `SELECT id, token_secret FROM at_git_connections WHERE token_secret <> ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, sealed string
		if err := rows.Scan(&id, &sealed); err != nil {
			return nil, err
		}
		out[id] = sealed
	}
	return out, rows.Err()
}

// SetConnectionSecret replaces a sealed PAT (reseal).
func (s *Store) SetConnectionSecret(ctx context.Context, id, sealed string) error {
	return affected(s.db.Exec(ctx, `UPDATE at_git_connections SET token_secret = $2 WHERE id = $1`, id, sealed))
}

// ProjectRef names a project for "used by" labels.
type ProjectRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ProjectsByRepo maps lowercase owner/repo to the user's projects using it.
func (s *Store) ProjectsByRepo(ctx context.Context, userID string) (map[string][]ProjectRef, error) {
	rows, err := s.db.Query(ctx, `SELECT lower(repo), id, name FROM at_projects
		WHERE user_id = $1 AND repo IS NOT NULL ORDER BY created_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]ProjectRef{}
	for rows.Next() {
		var repo string
		var p ProjectRef
		if err := rows.Scan(&repo, &p.ID, &p.Name); err != nil {
			return nil, err
		}
		out[repo] = append(out[repo], p)
	}
	return out, rows.Err()
}
