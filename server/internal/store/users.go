package store

import (
	"context"
	"time"

	"gorm.io/gorm"
)

// User is a row of at_users (v1 columns + v2 extensions). Its role (admin | user) is not a
// column: it is a Casbin grouping rule in casbin_rule, read through the enforcer (httpapi).
type User struct {
	ID                 string
	Email              string
	Active             bool
	MaxNodes           *int
	CreatedAt          time.Time
	PlatformVersion    int
	DisplayName        string
	MaxRunningThreads  *int
	MustChangePassword bool
	LastLoginAt        *time.Time
}

// TableName maps User to at_users.
func (User) TableName() string { return "at_users" }

// UserSummary adds the admin list's counters.
type UserSummary struct {
	User
	Nodes        int `gorm:"->"`
	RunningTasks int `gorm:"->"`
}

const userCols = `u.id, u.email, u.active, u.max_nodes, u.created_at, u.platform_version,
	u.display_name, u.max_running_threads, u.must_change_password, u.last_login_at`

func (s *Store) users(ctx context.Context) *gorm.DB {
	return s.g(ctx).Table("at_users u").Select(userCols)
}

// CountUsers counts all accounts (the first one becomes admin).
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int64
	err := s.g(ctx).Model(&User{}).Count(&n).Error
	return int(n), err
}

// CreateUser inserts an account; ErrConflict if the email is taken. The caller assigns its
// role through the Casbin enforcer.
func (s *Store) CreateUser(ctx context.Context, email, passwordHash string) (User, error) {
	id := NewHexID()
	err := s.g(ctx).Table("at_users").Create(map[string]any{"id": id, "email": email, "password_hash": passwordHash,
		"password_changed_at": gorm.Expr("now()")}).Error
	if err != nil {
		return User{}, mapErr(err)
	}
	return s.UserByID(ctx, id)
}

// UserCredentials returns the user and password hash for an email.
func (s *Store) UserCredentials(ctx context.Context, email string) (User, string, error) {
	var row struct {
		User
		PasswordHash string
	}
	err := s.users(ctx).Select(userCols+", u.password_hash").Where("u.email = ?", email).Take(&row).Error
	return row.User, row.PasswordHash, mapErr(err)
}

// PasswordHash returns a user's stored hash.
func (s *Store) PasswordHash(ctx context.Context, userID string) (string, error) {
	var row struct{ PasswordHash string }
	err := s.g(ctx).Table("at_users").Select("password_hash").Where("id = ?", userID).Take(&row).Error
	return row.PasswordHash, mapErr(err)
}

// UserByID loads one account.
func (s *Store) UserByID(ctx context.Context, id string) (User, error) {
	var u User
	err := s.users(ctx).Where("u.id = ?", id).Take(&u).Error
	return u, mapErr(err)
}

// ListUsers returns every account with node and running-task counts.
func (s *Store) ListUsers(ctx context.Context) ([]UserSummary, error) {
	var out []UserSummary
	err := s.users(ctx).Select(userCols + `,
		(SELECT count(*) FROM at_nodes n WHERE n.user_id = u.id) AS nodes,
		(SELECT count(*) FROM at_tasks t JOIN at_conversations c ON c.id = t.conversation_id
		  WHERE c.user_id = u.id AND t.status = 'running') AS running_tasks`).Order("u.created_at").Find(&out).Error
	return out, err
}

// UpdateUser applies column changes (active, max_nodes, max_running_threads, display_name).
func (s *Store) UpdateUser(ctx context.Context, id string, changes []Change) (User, error) {
	if len(changes) > 0 {
		if err := Changed(s.g(ctx).Table("at_users").Where("id = ?", id).Updates(changeMap(changes))); err != nil {
			return User{}, err
		}
	}
	return s.UserByID(ctx, id)
}

// SetPassword stores a new hash; mustChange marks admin-issued temporary passwords.
func (s *Store) SetPassword(ctx context.Context, id, hash string, mustChange bool) error {
	return Changed(s.g(ctx).Table("at_users").Where("id = ?", id).Updates(map[string]any{
		"password_hash": hash, "must_change_password": mustChange, "password_changed_at": gorm.Expr("now()")}))
}

// TouchLogin records a successful login.
func (s *Store) TouchLogin(ctx context.Context, id string) error {
	return s.g(ctx).Table("at_users").Where("id = ?", id).Update("last_login_at", gorm.Expr("now()")).Error
}

// DeleteUser removes an account and (by cascade) everything it owns.
func (s *Store) DeleteUser(ctx context.Context, id string) error {
	return Changed(s.g(ctx).Where("id = ?", id).Delete(&User{}))
}
