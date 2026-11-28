package store

import (
	"context"
	"time"

	"gorm.io/gorm"
)

// Session is a row of at_sessions; TokenHash is sha256(token), never the token.
type Session struct {
	TokenHash  string
	UserID     string
	ExpiresAt  time.Time
	CreatedAt  time.Time
	LastSeenAt *time.Time
	UserAgent  string
	CSRFSecret string `gorm:"column:csrf_secret"`
}

// TableName maps Session to at_sessions.
func (Session) TableName() string { return "at_sessions" }

const sessionCols = `s.token_hash, s.user_id, s.expires_at, s.created_at, s.last_seen_at, s.user_agent, s.csrf_secret`

// sessionUserCols selects the user columns with a u_ prefix (embedded below).
const sessionUserCols = `u.id AS u_id, u.email AS u_email, u.active AS u_active,
	u.max_nodes AS u_max_nodes, u.created_at AS u_created_at,
	u.display_name AS u_display_name, u.max_running_threads AS u_max_running_threads,
	u.must_change_password AS u_must_change_password, u.last_login_at AS u_last_login_at,
	u.locale AS u_locale`

func (s *Store) sessions(ctx context.Context) *gorm.DB { return s.g(ctx).Table("at_sessions s") }

// CreateSession stores a login session.
func (s *Store) CreateSession(ctx context.Context, x Session) error {
	return mapErr(s.g(ctx).Table("at_sessions").Create(map[string]any{"token_hash": x.TokenHash, "user_id": x.UserID,
		"expires_at": x.ExpiresAt, "user_agent": x.UserAgent, "csrf_secret": x.CSRFSecret,
		"last_seen_at": gorm.Expr("now()")}).Error)
}

// SessionUser resolves a live session of an active user (v1 semantics).
func (s *Store) SessionUser(ctx context.Context, tokenHash string) (Session, User, error) {
	var row struct {
		Session `gorm:"embedded"`
		User    User `gorm:"embedded;embeddedPrefix:u_"`
	}
	err := s.sessions(ctx).Select(sessionCols+", "+sessionUserCols).Joins("JOIN at_users u ON u.id = s.user_id").
		Where("s.token_hash = ? AND s.expires_at > now() AND u.active", tokenHash).Take(&row).Error
	return row.Session, row.User, mapErr(err)
}

// TouchSession records activity and optionally slides the expiry.
func (s *Store) TouchSession(ctx context.Context, tokenHash string, expires *time.Time) error {
	set := map[string]any{"last_seen_at": gorm.Expr("now()")}
	if expires != nil {
		set["expires_at"] = *expires
	}
	return s.g(ctx).Table("at_sessions").Where("token_hash = ?", tokenHash).Updates(set).Error
}

// SetCSRFSecret fills the session's CSRF secret (generated on first use).
func (s *Store) SetCSRFSecret(ctx context.Context, tokenHash, secret string) error {
	return s.g(ctx).Table("at_sessions").Where("token_hash = ?", tokenHash).Update("csrf_secret", secret).Error
}

// DeleteSession signs one session out.
func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	return s.g(ctx).Where("token_hash = ?", tokenHash).Delete(&Session{}).Error
}

// DeleteUserSessions signs a user out everywhere except keepHash ("" = all).
func (s *Store) DeleteUserSessions(ctx context.Context, userID, keepHash string) error {
	return s.g(ctx).Where("user_id = ? AND token_hash <> ?", userID, keepHash).Delete(&Session{}).Error
}

// ListUserSessions returns a user's live sessions, newest first.
func (s *Store) ListUserSessions(ctx context.Context, userID string) ([]Session, error) {
	var out []Session
	err := s.sessions(ctx).Select(sessionCols).Where("s.user_id = ? AND s.expires_at > now()", userID).
		Order("s.created_at DESC").Find(&out).Error
	return out, err
}

// DeleteUserSessionByPrefix removes the user's session whose hash starts with prefix.
func (s *Store) DeleteUserSessionByPrefix(ctx context.Context, userID, prefix string) error {
	return Changed(s.g(ctx).Where("user_id = ? AND left(token_hash, ?) = ?", userID, len(prefix), prefix).
		Delete(&Session{}))
}

// DeleteExpiredSessions is the daily cleanup.
func (s *Store) DeleteExpiredSessions(ctx context.Context) (int64, error) {
	res := s.g(ctx).Where("expires_at <= now()").Delete(&Session{})
	return res.RowsAffected, res.Error
}
