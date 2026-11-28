package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/touwaeriol/agent-teams/server/internal/auth"
	"github.com/touwaeriol/agent-teams/server/internal/config"
	"github.com/touwaeriol/agent-teams/server/internal/office"
	"github.com/touwaeriol/agent-teams/server/internal/platform"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

type adminUsersOutput struct{ Body []AdminUserView }

type userIDInput struct {
	ID string `path:"id" maxLength:"64"`
}

type userPatchInput struct {
	ID   string `path:"id" maxLength:"64"`
	Body struct {
		Role                   *string `json:"role,omitempty" enum:"admin,user"`
		Active                 *bool   `json:"active,omitempty"`
		MaxNodes               *int    `json:"max_nodes,omitempty" minimum:"0"`
		ClearMaxNodes          bool    `json:"clear_max_nodes,omitempty"`
		MaxRunningThreads      *int    `json:"max_running_threads,omitempty" minimum:"0" doc:"0 = no limit"`
		ClearMaxRunningThreads bool    `json:"clear_max_running_threads,omitempty"`
		DisplayName            *string `json:"display_name,omitempty" maxLength:"100"`
	}
}

type adminCreateUserInput struct {
	Body struct {
		Email              string `json:"email" maxLength:"320"`
		DisplayName        string `json:"display_name,omitempty" maxLength:"100"`
		Role               string `json:"role,omitempty" enum:"admin,user" default:"user"`
		Password           string `json:"password,omitempty" maxLength:"1024" doc:"Empty: the server generates a temporary password and returns it once"`
		MustChangePassword *bool  `json:"must_change_password,omitempty" doc:"Default true: the user must change the password after signing in"`
	}
}

// AdminCreatedUser is the answer to POST /api/admin/users.
type AdminCreatedUser struct {
	User         AdminUserView `json:"user"`
	TempPassword string        `json:"temp_password,omitempty" doc:"Only when the server generated the password; shown once"`
}

type adminCreateUserOutput struct{ Body AdminCreatedUser }

type tempPasswordOutput struct {
	Body struct {
		TempPassword string `json:"temp_password" doc:"Shown once; the user must change it after signing in"`
	}
}

// AdminSettings is GET/PUT /api/admin/settings.
type AdminSettings struct {
	RegistrationOpen  bool                  `json:"registration_open"`
	DefaultMaxNodes   int                   `json:"default_max_nodes" minimum:"0"`
	MaxRunningThreads int                   `json:"max_running_threads" minimum:"0" doc:"0 = no limit"`
	DeliveryDefault   Delivery              `json:"delivery_default"`
	OfficeDefault     string                `json:"office_default" doc:"Default office layout id"`
	NodeRelease       *platform.NodeRelease `json:"node_release,omitempty" doc:"Omit to clear"`
	NodeURL           string                `json:"node_url" maxLength:"200" doc:"Address nodes register and connect with (an origin such as https://agent-backend.example.com, not a CDN in front of it); empty = node_url_default"`
	NodeURLDefault    string                `json:"node_url_default,omitempty" readOnly:"true" doc:"What nodes use when node_url is empty: AGENT_TEAMS_NODE_URL, else the public URL"`
}

type adminSettingsOutput struct{ Body AdminSettings }

type adminSettingsInput struct{ Body AdminSettings }

type auditInput struct {
	User   string `query:"user"`
	Action string `query:"action"`
	Cursor string `query:"cursor"`
	Limit  int    `query:"limit" minimum:"0" maximum:"200"`
}

type auditOutput struct {
	Body struct {
		Items      []store.AuditEntry `json:"items"`
		NextCursor *string            `json:"next_cursor"`
	}
}

func (s *Server) registerAdmin(api huma.API) {
	tags := []string{"admin"}
	huma.Register(api, huma.Operation{OperationID: "admin-list-users", Method: "GET", Path: "/api/admin/users", Tags: tags}, s.adminUsers)
	huma.Register(api, huma.Operation{OperationID: "admin-create-user", Method: "POST", Path: "/api/admin/users",
		Summary: "Create an account (works with registration closed)", Tags: tags, DefaultStatus: 201}, s.adminCreateUser)
	huma.Register(api, huma.Operation{OperationID: "admin-update-user", Method: "PATCH", Path: "/api/admin/users/{id}", Tags: tags}, s.adminUpdateUser)
	huma.Register(api, huma.Operation{OperationID: "admin-delete-user", Method: "DELETE", Path: "/api/admin/users/{id}", Tags: tags, DefaultStatus: 204}, s.adminDeleteUser)
	huma.Register(api, huma.Operation{OperationID: "admin-reset-password", Method: "POST", Path: "/api/admin/users/{id}/password-reset", Tags: tags}, s.adminResetPassword)
	huma.Register(api, huma.Operation{OperationID: "admin-get-settings", Method: "GET", Path: "/api/admin/settings", Tags: tags}, s.adminGetSettings)
	huma.Register(api, huma.Operation{OperationID: "admin-save-settings", Method: "PUT", Path: "/api/admin/settings", Tags: tags}, s.adminSaveSettings)
	huma.Register(api, huma.Operation{OperationID: "admin-audit", Method: "GET", Path: "/api/admin/audit", Tags: tags}, s.adminAudit)
}

func (s *Server) adminUsers(ctx context.Context, _ *struct{}) (*adminUsersOutput, error) {
	if _, err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	rows, err := s.store.ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]AdminUserView, len(rows))
	for i, u := range rows {
		out[i] = adminUserView(u, s.authz.role(u.ID))
	}
	return &adminUsersOutput{Body: out}, nil
}

// adminCreateUser creates an account for someone else. Without a password the server makes
// up a temporary one and answers it once; the password itself is never audited or logged.
func (s *Server) adminCreateUser(ctx context.Context, in *adminCreateUserInput) (*adminCreateUserOutput, error) {
	admin, err := requireAdmin(ctx)
	if err != nil {
		return nil, err
	}
	b := in.Body
	email := auth.NormalizeEmail(b.Email)
	if reason := auth.CheckEmail(email); reason != "" {
		return nil, fail(http.StatusUnprocessableEntity, "invalid_email", reason)
	}
	role := b.Role
	if role == "" {
		role = roleUser
	}
	password, generated := b.Password, b.Password == ""
	if generated {
		password = auth.RandomPassword()
	} else if reason := auth.CheckPassword(password); reason != "" {
		return nil, fail(http.StatusUnprocessableEntity, "weak_password", reason)
	}
	mustChange := b.MustChangePassword == nil || *b.MustChangePassword
	hash, err := auth.HashPassword(password)
	if err != nil {
		return nil, err
	}
	user, err := s.store.CreateUserWithOptions(ctx, store.NewUser{Email: email, PasswordHash: hash,
		DisplayName: strings.TrimSpace(b.DisplayName), MustChangePassword: mustChange})
	if errors.Is(err, store.ErrConflict) {
		return nil, fail(http.StatusConflict, "email_taken", "这个邮箱已经注册过了")
	}
	if err != nil {
		return nil, err
	}
	if err := s.authz.setRole(user.ID, role); err != nil {
		// No account without a role: take the row back so the address can be used again.
		if derr := s.store.DeleteUser(ctx, user.ID); derr != nil {
			s.log.Error("admin create user: remove account without role", "user_id", user.ID, "err", derr)
		}
		return nil, err
	}
	s.audit(ctx, admin.User.ID, "user_create", "user", user.ID, map[string]any{"email": email, "role": role,
		"generated_password": generated, "must_change_password": mustChange})
	out := &adminCreateUserOutput{}
	out.Body.User = AdminUserView{UserView: userView(user, s.authz.role(user.ID)), MaxRunningThreads: user.MaxRunningThreads,
		LastLoginAt: user.LastLoginAt}
	if generated {
		out.Body.TempPassword = password
	}
	return out, nil
}

func (s *Server) adminUpdateUser(ctx context.Context, in *userPatchInput) (*userOutput, error) {
	admin, err := requireAdmin(ctx)
	if err != nil {
		return nil, err
	}
	b := in.Body
	if in.ID == admin.User.ID && ((b.Role != nil && *b.Role == "user") || (b.Active != nil && !*b.Active)) {
		return nil, fail(http.StatusBadRequest, "self_demote", "不能停用自己或取消自己的管理员身份")
	}
	user, err := s.store.UpdateUser(ctx, in.ID, userChanges(in))
	if err != nil {
		return nil, storeErr(err, "没有这个用户")
	}
	if b.Role != nil {
		if err := s.authz.setRole(in.ID, *b.Role); err != nil {
			return nil, err
		}
		s.audit(ctx, admin.User.ID, "role_change", "user", in.ID, map[string]string{"role": *b.Role})
	}
	if !user.Active {
		s.cutOff(ctx, in.ID)
	}
	return &userOutput{Body: userView(user, s.authz.role(user.ID))}, nil
}

func userChanges(in *userPatchInput) []store.Change {
	b := in.Body
	var ch []store.Change
	add := func(cond bool, col string, val any) {
		if cond {
			ch = append(ch, store.Change{Col: col, Val: val})
		}
	}
	add(b.Active != nil, "active", deref(b.Active))
	add(b.MaxNodes != nil && !b.ClearMaxNodes, "max_nodes", b.MaxNodes)
	add(b.ClearMaxNodes, "max_nodes", nil)
	add(b.MaxRunningThreads != nil && !b.ClearMaxRunningThreads, "max_running_threads", b.MaxRunningThreads)
	add(b.ClearMaxRunningThreads, "max_running_threads", nil)
	add(b.DisplayName != nil, "display_name", deref(b.DisplayName))
	return ch
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

// cutOff signs the user out everywhere, stops their engines and drops their node links.
func (s *Server) cutOff(ctx context.Context, userID string) {
	if err := s.store.DeleteUserSessions(ctx, userID, ""); err != nil {
		s.log.Warn("cut off: sessions", "user_id", userID, "err", err)
	}
	if err := s.hooks.UserCutOff(ctx, userID); err != nil {
		s.log.Warn("cut off: hook", "user_id", userID, "err", err)
	}
	nodes, err := s.store.ListNodes(ctx, userID)
	if err != nil {
		s.log.Warn("cut off: nodes", "user_id", userID, "err", err)
	}
	for _, n := range nodes {
		s.nodes.Disconnect(ctx, n.ID, "user_cut_off")
	}
}

func (s *Server) adminDeleteUser(ctx context.Context, in *userIDInput) (*struct{}, error) {
	admin, err := requireAdmin(ctx)
	if err != nil {
		return nil, err
	}
	if in.ID == admin.User.ID {
		return nil, fail(http.StatusBadRequest, "self_delete", "不能删除自己")
	}
	if _, err := s.store.UserByID(ctx, in.ID); err != nil {
		return nil, storeErr(err, "没有这个用户")
	}
	// The folder first: when it is being switched on the deletion is refused before anything else
	// happened to the account.
	if err := s.hooks.UserDeleting(ctx, in.ID); err != nil {
		return nil, fail(http.StatusConflict, "folder_busy", err.Error())
	}
	s.cutOff(ctx, in.ID)
	// Grouping rules first: if deleting the row then fails, a retry finishes the job and no
	// rule outlives its user.
	if err := s.authz.forget(in.ID); err != nil {
		return nil, err
	}
	if err := s.store.DeleteUser(ctx, in.ID); err != nil {
		return nil, storeErr(err, "没有这个用户")
	}
	s.audit(ctx, admin.User.ID, "user_delete", "user", in.ID, nil)
	return nil, nil
}

func (s *Server) adminResetPassword(ctx context.Context, in *userIDInput) (*tempPasswordOutput, error) {
	admin, err := requireAdmin(ctx)
	if err != nil {
		return nil, err
	}
	temp := auth.RandomPassword()
	hash, err := auth.HashPassword(temp)
	if err != nil {
		return nil, err
	}
	if err := s.store.SetPassword(ctx, in.ID, hash, true); err != nil {
		return nil, storeErr(err, "没有这个用户")
	}
	_ = s.store.DeleteUserSessions(ctx, in.ID, "")
	s.audit(ctx, admin.User.ID, "password_reset", "user", in.ID, nil)
	out := &tempPasswordOutput{}
	out.Body.TempPassword = temp
	return out, nil
}

func (s *Server) adminGetSettings(ctx context.Context, _ *struct{}) (*adminSettingsOutput, error) {
	if _, err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	settings, err := s.adminSettings(ctx)
	return &adminSettingsOutput{Body: settings}, err
}

func (s *Server) adminSettings(ctx context.Context) (AdminSettings, error) {
	p, err := s.platformSettings(ctx)
	if err != nil {
		return AdminSettings{}, err
	}
	out := AdminSettings{RegistrationOpen: p.RegistrationOpen, DefaultMaxNodes: p.DefaultMaxNodes,
		MaxRunningThreads: p.MaxRunningThreads, DeliveryDefault: s.deliveryDefault(ctx), OfficeDefault: s.officeDefault(ctx),
		NodeURL: p.NodeURL, NodeURLDefault: s.nodeURLDefault(ctx)}
	var release platform.NodeRelease
	if found, err := s.store.Setting(ctx, "node_release", &release); err == nil && found {
		out.NodeRelease = &release
	}
	return out, nil
}

func (s *Server) adminSaveSettings(ctx context.Context, in *adminSettingsInput) (*adminSettingsOutput, error) {
	admin, err := requireAdmin(ctx)
	if err != nil {
		return nil, err
	}
	b := in.Body
	b.NodeURL = strings.TrimRight(strings.TrimSpace(b.NodeURL), "/")
	if b.NodeURL != "" && !config.IsOrigin(b.NodeURL) {
		return nil, invalid([]FieldError{{"node_url", "要填 https://域名 这样的地址，不带路径"}}, "节点注册地址要填 https://域名 这样的地址，不带路径")
	}
	if _, err := s.office.Get(b.OfficeDefault); err != nil {
		return nil, invalid([]FieldError{{"office_default", "没有这个布局"}}, "没有这个布局："+b.OfficeDefault)
	}
	err = s.store.Tx(ctx, func(tx *store.Store) error {
		return saveSettings(ctx, tx, b)
	})
	if err != nil {
		return nil, err
	}
	s.audit(ctx, admin.User.ID, "settings_change", "settings", "platform", nil)
	return s.adminGetSettings(ctx, nil)
}

func saveSettings(ctx context.Context, tx *store.Store, b AdminSettings) error {
	if err := tx.SaveSetting(ctx, "platform", PlatformSettings{RegistrationOpen: b.RegistrationOpen,
		DefaultMaxNodes: b.DefaultMaxNodes, MaxRunningThreads: b.MaxRunningThreads, NodeURL: b.NodeURL}); err != nil {
		return err
	}
	if err := tx.SaveSetting(ctx, "delivery_default", b.DeliveryDefault); err != nil {
		return err
	}
	if err := tx.SaveSetting(ctx, "office_default", map[string]string{"layout": b.OfficeDefault}); err != nil {
		return err
	}
	if b.NodeRelease == nil {
		return tx.DeleteSetting(ctx, "node_release")
	}
	return tx.SaveSetting(ctx, "node_release", b.NodeRelease)
}

func (s *Server) officeDefault(ctx context.Context) string {
	var v struct {
		Layout string `json:"layout"`
	}
	if found, err := s.store.Setting(ctx, "office_default", &v); err == nil && found && v.Layout != "" {
		return v.Layout
	}
	return office.DefaultID
}

func (s *Server) adminAudit(ctx context.Context, in *auditInput) (*auditOutput, error) {
	if _, err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	before, _ := strconv.ParseInt(in.Cursor, 10, 64)
	limit := pageLimit(in.Limit)
	rows, err := s.store.ListAudit(ctx, store.AuditFilter{UserID: in.User, Action: in.Action, Before: before, Limit: limit})
	if err != nil {
		return nil, err
	}
	out := &auditOutput{}
	out.Body.Items = rows
	if len(rows) == limit {
		out.Body.NextCursor = cursorString(rows[len(rows)-1].ID)
	}
	return out, nil
}
