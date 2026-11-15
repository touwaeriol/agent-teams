package store

import (
	"context"
	"time"
)

// Session is a row of at_sessions; TokenHash is sha256(token), never the token.
type Session struct {
	TokenHash  string
	UserID     string
	ExpiresAt  time.Time
	CreatedAt  time.Time
	LastSeenAt *time.Time
	UserAgent  string
	CSRFSecret string
}

const sessionCols = `s.token_hash, s.user_id, s.expires_at, s.created_at, s.last_seen_at, s.user_agent, s.csrf_secret`

func scanSession(row interface{ Scan(...any) error }, extra ...any) (Session, error) {
	var x Session
	dest := append([]any{&x.TokenHash, &x.UserID, &x.ExpiresAt, &x.CreatedAt, &x.LastSeenAt,
		&x.UserAgent, &x.CSRFSecret}, extra...)
	return x, mapErr(row.Scan(dest...))
}

// CreateSession stores a login session.
func (s *Store) CreateSession(ctx context.Context, x Session) error {
	_, err := s.db.Exec(ctx, `INSERT INTO at_sessions (token_hash, user_id, expires_at, user_agent, csrf_secret, last_seen_at)
		VALUES ($1, $2, $3, $4, $5, now())`, x.TokenHash, x.UserID, x.ExpiresAt, x.UserAgent, x.CSRFSecret)
	return mapErr(err)
}

// SessionUser resolves a live session of an active user (v1 semantics).
func (s *Store) SessionUser(ctx context.Context, tokenHash string) (Session, User, error) {
	row := s.db.QueryRow(ctx, `SELECT `+sessionCols+`, `+userCols+`
		FROM at_sessions s JOIN at_users u ON u.id = s.user_id
		WHERE s.token_hash = $1 AND s.expires_at > now() AND u.active`, tokenHash)
	var u User
	var pv int16
	x, err := scanSession(row, &u.ID, &u.Email, &u.Role, &u.Active, &u.MaxNodes, &u.CreatedAt, &pv,
		&u.DisplayName, &u.MaxRunningThreads, &u.MustChangePassword, &u.LastLoginAt)
	u.PlatformVersion = int(pv)
	return x, u, err
}

// TouchSession records activity and optionally slides the expiry.
func (s *Store) TouchSession(ctx context.Context, tokenHash string, expires *time.Time) error {
	_, err := s.db.Exec(ctx, `UPDATE at_sessions SET last_seen_at = now(),
		expires_at = COALESCE($2, expires_at) WHERE token_hash = $1`, tokenHash, expires)
	return err
}

// SetCSRFSecret fills the session's CSRF secret (generated on first use).
func (s *Store) SetCSRFSecret(ctx context.Context, tokenHash, secret string) error {
	_, err := s.db.Exec(ctx, `UPDATE at_sessions SET csrf_secret = $2 WHERE token_hash = $1`, tokenHash, secret)
	return err
}

// DeleteSession signs one session out.
func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM at_sessions WHERE token_hash = $1`, tokenHash)
	return err
}

// DeleteUserSessions signs a user out everywhere except keepHash ("" = all).
func (s *Store) DeleteUserSessions(ctx context.Context, userID, keepHash string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM at_sessions WHERE user_id = $1 AND token_hash <> $2`, userID, keepHash)
	return err
}

// ListUserSessions returns a user's live sessions, newest first.
func (s *Store) ListUserSessions(ctx context.Context, userID string) ([]Session, error) {
	rows, err := s.db.Query(ctx, `SELECT `+sessionCols+` FROM at_sessions s
		WHERE s.user_id = $1 AND s.expires_at > now() ORDER BY s.created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		x, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// DeleteUserSessionByPrefix removes the user's session whose hash starts with prefix.
func (s *Store) DeleteUserSessionByPrefix(ctx context.Context, userID, prefix string) error {
	return affected(s.db.Exec(ctx, `DELETE FROM at_sessions WHERE user_id = $1 AND left(token_hash, $3) = $2`,
		userID, prefix, len(prefix)))
}

// DeleteExpiredSessions is the daily cleanup.
func (s *Store) DeleteExpiredSessions(ctx context.Context) (int64, error) {
	tag, err := s.db.Exec(ctx, `DELETE FROM at_sessions WHERE expires_at <= now()`)
	return tag.RowsAffected(), err
}
