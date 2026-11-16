package store

import (
	"context"
	"time"

	"gorm.io/gorm"
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
	Permissions    map[string]string `gorm:"serializer:jsonb"`
	AccountType    string
	CheckedAt      *time.Time
}

// TableName maps GitConnection to at_git_connections.
func (GitConnection) TableName() string { return "at_git_connections" }

const connCols = `id, user_id, name, provider, login, kind, installation_id, created_at, permissions, account_type, checked_at`

func (s *Store) conns(ctx context.Context) *gorm.DB { return s.g(ctx).Select(connCols) }

// CreateConnection stores a PAT (sealed) or a GitHub App installation (installationID set, secret "").
func (s *Store) CreateConnection(ctx context.Context, userID, name, login, sealed string, installationID *int64) (GitConnection, error) {
	kind := "token"
	if installationID != nil {
		kind = "app"
	}
	id := NewHexID()
	err := s.g(ctx).Table("at_git_connections").Create(map[string]any{"id": id, "user_id": userID, "name": name,
		"login": login, "token_secret": sealed, "kind": kind, "installation_id": installationID}).Error
	if err != nil {
		return GitConnection{}, mapErr(err)
	}
	return s.Connection(ctx, userID, id)
}

// Connections lists the user's connections.
func (s *Store) Connections(ctx context.Context, userID string) ([]GitConnection, error) {
	out := []GitConnection{}
	err := s.conns(ctx).Where("user_id = ?", userID).Order("created_at").Find(&out).Error
	return out, err
}

// ConnectionsByInstallation finds every connection of an App installation (webhooks).
func (s *Store) ConnectionsByInstallation(ctx context.Context, installationID int64) ([]GitConnection, error) {
	out := []GitConnection{}
	err := s.conns(ctx).Where("installation_id = ?", installationID).Find(&out).Error
	return out, err
}

// Connection loads one of the user's connections.
func (s *Store) Connection(ctx context.Context, userID, id string) (GitConnection, error) {
	var c GitConnection
	err := s.conns(ctx).Where("id = ? AND user_id = ?", id, userID).Take(&c).Error
	return c, mapErr(err)
}

// ConnectionSecret returns the sealed PAT of one of the user's connections.
func (s *Store) ConnectionSecret(ctx context.Context, userID, id string) (string, error) {
	var row struct{ TokenSecret string }
	err := s.g(ctx).Table("at_git_connections").Select("token_secret").Where("id = ? AND user_id = ?", id, userID).
		Take(&row).Error
	return row.TokenSecret, mapErr(err)
}

// SetConnectionPermissions records what the installation actually grants and who owns it.
func (s *Store) SetConnectionPermissions(ctx context.Context, id string, perms map[string]string, accountType string) error {
	if perms == nil {
		perms = map[string]string{}
	}
	return Changed(s.g(ctx).Table("at_git_connections").Where("id = ?", id).Updates(map[string]any{
		"permissions":  Param(perms),
		"account_type": gorm.Expr("COALESCE(NULLIF(?, ''), account_type)", accountType),
		"checked_at":   gorm.Expr("now()")}))
}

// DeleteConnection removes one of the user's connections (projects keep their repo, lose the link).
func (s *Store) DeleteConnection(ctx context.Context, userID, id string) error {
	return Changed(s.g(ctx).Where("id = ? AND user_id = ?", id, userID).Delete(&GitConnection{}))
}

// SealedSecrets lists every sealed PAT (atctl admin reseal).
func (s *Store) SealedSecrets(ctx context.Context) (map[string]string, error) {
	var rows []struct{ ID, TokenSecret string }
	err := s.g(ctx).Table("at_git_connections").Select("id, token_secret").Where("token_secret <> ''").Find(&rows).Error
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		out[r.ID] = r.TokenSecret
	}
	return out, err
}

// SetConnectionSecret replaces a sealed PAT (reseal).
func (s *Store) SetConnectionSecret(ctx context.Context, id, sealed string) error {
	return Changed(s.g(ctx).Table("at_git_connections").Where("id = ?", id).Update("token_secret", sealed))
}

// ProjectRef names a project for "used by" labels.
type ProjectRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ProjectsByRepo maps lowercase owner/repo to the user's projects using it.
func (s *Store) ProjectsByRepo(ctx context.Context, userID string) (map[string][]ProjectRef, error) {
	var rows []struct{ Repo, ID, Name string }
	err := s.g(ctx).Table("at_project_repos r").Select("lower(r.repo) AS repo, p.id, p.name").
		Joins("JOIN at_projects p ON p.id = r.project_id").Where("p.user_id = ?", userID).Order("p.created_at").
		Find(&rows).Error
	out := map[string][]ProjectRef{}
	for _, r := range rows {
		out[r.Repo] = append(out[r.Repo], ProjectRef{ID: r.ID, Name: r.Name})
	}
	return out, err
}
