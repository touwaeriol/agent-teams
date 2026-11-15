package store

import (
	"context"
	"time"
)

// User is a row of at_users (v1 columns + v2 extensions).
type User struct {
	ID                 string
	Email              string
	Role               string
	Active             bool
	MaxNodes           *int
	CreatedAt          time.Time
	PlatformVersion    int
	DisplayName        string
	MaxRunningThreads  *int
	MustChangePassword bool
	LastLoginAt        *time.Time
}

// UserSummary adds the admin list's counters.
type UserSummary struct {
	User
	Nodes        int
	RunningTasks int
}

const userCols = `u.id, u.email, u.role, u.active, u.max_nodes, u.created_at, u.platform_version,
	u.display_name, u.max_running_threads, u.must_change_password, u.last_login_at`

func scanUser(row interface{ Scan(...any) error }, extra ...any) (User, error) {
	var u User
	var pv int16
	dest := append([]any{&u.ID, &u.Email, &u.Role, &u.Active, &u.MaxNodes, &u.CreatedAt, &pv,
		&u.DisplayName, &u.MaxRunningThreads, &u.MustChangePassword, &u.LastLoginAt}, extra...)
	err := row.Scan(dest...)
	u.PlatformVersion = int(pv)
	return u, mapErr(err)
}

// CountUsers counts all accounts (the first one becomes admin).
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `SELECT count(*) FROM at_users`).Scan(&n)
	return n, err
}

// CreateUser inserts an account; ErrConflict if the email is taken.
func (s *Store) CreateUser(ctx context.Context, email, passwordHash, role string) (User, error) {
	row := s.db.QueryRow(ctx, `INSERT INTO at_users AS u (id, email, password_hash, role, password_changed_at)
		VALUES ($1, $2, $3, $4, now()) RETURNING `+userCols, NewHexID(), email, passwordHash, role)
	return scanUser(row)
}

// UserCredentials returns the user and password hash for an email.
func (s *Store) UserCredentials(ctx context.Context, email string) (User, string, error) {
	var hash string
	row := s.db.QueryRow(ctx, `SELECT `+userCols+`, u.password_hash FROM at_users u WHERE u.email = $1`, email)
	u, err := scanUser(row, &hash)
	return u, hash, err
}

// PasswordHash returns a user's stored hash.
func (s *Store) PasswordHash(ctx context.Context, userID string) (string, error) {
	var hash string
	err := s.db.QueryRow(ctx, `SELECT password_hash FROM at_users WHERE id = $1`, userID).Scan(&hash)
	return hash, mapErr(err)
}

// UserByID loads one account.
func (s *Store) UserByID(ctx context.Context, id string) (User, error) {
	return scanUser(s.db.QueryRow(ctx, `SELECT `+userCols+` FROM at_users u WHERE u.id = $1`, id))
}

// ListUsers returns every account with node and running-task counts.
func (s *Store) ListUsers(ctx context.Context) ([]UserSummary, error) {
	rows, err := s.db.Query(ctx, `SELECT `+userCols+`,
		(SELECT count(*) FROM at_nodes n WHERE n.user_id = u.id),
		(SELECT count(*) FROM at_tasks t JOIN at_conversations c ON c.id = t.conversation_id
		  WHERE c.user_id = u.id AND t.status = 'running')
		FROM at_users u ORDER BY u.created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UserSummary
	for rows.Next() {
		var s UserSummary
		if s.User, err = scanUser(rows, &s.Nodes, &s.RunningTasks); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// UpdateUser applies column changes (role, active, max_nodes, max_running_threads, display_name).
func (s *Store) UpdateUser(ctx context.Context, id string, changes []Change) (User, error) {
	if len(changes) == 0 {
		return s.UserByID(ctx, id)
	}
	set, args := setClause(changes, 2)
	row := s.db.QueryRow(ctx, `UPDATE at_users AS u SET `+set+` WHERE u.id = $1 RETURNING `+userCols,
		append([]any{id}, args...)...)
	return scanUser(row)
}

// SetPassword stores a new hash; mustChange marks admin-issued temporary passwords.
func (s *Store) SetPassword(ctx context.Context, id, hash string, mustChange bool) error {
	return affected(s.db.Exec(ctx, `UPDATE at_users SET password_hash = $2, must_change_password = $3,
		password_changed_at = now() WHERE id = $1`, id, hash, mustChange))
}

// TouchLogin records a successful login.
func (s *Store) TouchLogin(ctx context.Context, id string) error {
	_, err := s.db.Exec(ctx, `UPDATE at_users SET last_login_at = now() WHERE id = $1`, id)
	return err
}

// DeleteUser removes an account and (by cascade) everything it owns.
func (s *Store) DeleteUser(ctx context.Context, id string) error {
	return affected(s.db.Exec(ctx, `DELETE FROM at_users WHERE id = $1`, id))
}
