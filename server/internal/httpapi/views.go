package httpapi

import (
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// UserView is api.md §2 UserView.
type UserView struct {
	ID                 string    `json:"id"`
	Email              string    `json:"email"`
	DisplayName        string    `json:"display_name"`
	Role               string    `json:"role" enum:"admin,user"`
	Active             bool      `json:"active"`
	MaxNodes           *int      `json:"max_nodes"`
	MustChangePassword bool      `json:"must_change_password"`
	CreatedAt          time.Time `json:"created_at"`
	Locale             string    `json:"locale" enum:",zh-CN,en,ko,ja" doc:"Interface language; empty = follow the browser"`
}

// userView shows an account with its role (from the Casbin enforcer: Server.role).
func userView(u store.User, role string) UserView {
	return UserView{ID: u.ID, Email: u.Email, DisplayName: u.DisplayName, Role: role, Active: u.Active,
		MaxNodes: u.MaxNodes, MustChangePassword: u.MustChangePassword,
		CreatedAt: u.CreatedAt, Locale: u.Locale}
}

// AdminUserView adds the admin list's columns.
type AdminUserView struct {
	UserView
	Nodes             int        `json:"nodes"`
	RunningTasks      int        `json:"running_tasks"`
	MaxRunningThreads *int       `json:"max_running_threads"`
	LastLoginAt       *time.Time `json:"last_login_at"`
}

func adminUserView(u store.UserSummary, role string) AdminUserView {
	return AdminUserView{UserView: userView(u.User, role), Nodes: u.Nodes, RunningTasks: u.RunningTasks,
		MaxRunningThreads: u.MaxRunningThreads, LastLoginAt: u.LastLoginAt}
}
