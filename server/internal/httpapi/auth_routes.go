package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/touwaeriol/agent-teams/server/internal/auth"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// Credentials is the login/register body.
type Credentials struct {
	Email    string `json:"email" maxLength:"320"`
	Password string `json:"password" maxLength:"1024"`
}

type credentialsInput struct {
	Body Credentials
}

type userWithCookies struct {
	SetCookie []http.Cookie `header:"Set-Cookie"`
	Body      UserView
}

type cookiesOnly struct {
	SetCookie []http.Cookie `header:"Set-Cookie"`
}

type userOutput struct{ Body UserView }

type passwordInput struct {
	Body struct {
		Current string `json:"current" maxLength:"1024"`
		New     string `json:"new" maxLength:"1024"`
	}
}

// SessionView is one of the caller's login sessions.
type SessionView struct {
	ID         string     `json:"id" doc:"First 16 hex chars of the token hash"`
	Current    bool       `json:"current"`
	UserAgent  string     `json:"user_agent"`
	CreatedAt  time.Time  `json:"created_at"`
	LastSeenAt *time.Time `json:"last_seen_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
}

type sessionsOutput struct{ Body []SessionView }

type sessionIDInput struct {
	ID string `path:"id" minLength:"8" maxLength:"64"`
}

// PlatformSettings is at_settings.platform (v1 fields + max_running_threads + node_url).
type PlatformSettings struct {
	RegistrationOpen  bool   `json:"registration_open"`
	DefaultMaxNodes   int    `json:"default_max_nodes"`
	MaxRunningThreads int    `json:"max_running_threads"`
	NodeURL           string `json:"node_url,omitempty"`
}

func defaultPlatform() PlatformSettings {
	return PlatformSettings{RegistrationOpen: true, DefaultMaxNodes: 5}
}

func (s *Server) platformSettings(ctx context.Context) (PlatformSettings, error) {
	p := defaultPlatform()
	_, err := s.store.Setting(ctx, "platform", &p)
	return p, err
}

func (s *Server) registerAuth(api huma.API) {
	tags := []string{"auth"}
	huma.Register(api, huma.Operation{OperationID: "register", Method: "POST", Path: "/auth/register",
		Summary: "Create an account (first user or AGENT_TEAMS_ADMIN_EMAILS become admin)", Tags: tags,
		DefaultStatus: 201}, s.registerUser)
	huma.Register(api, huma.Operation{OperationID: "login", Method: "POST", Path: "/auth/login", Summary: "Sign in", Tags: tags}, s.login)
	huma.Register(api, huma.Operation{OperationID: "logout", Method: "POST", Path: "/auth/logout", Summary: "Sign out",
		Tags: tags, DefaultStatus: 204}, s.logout)
	huma.Register(api, huma.Operation{OperationID: "me", Method: "GET", Path: "/api/me", Summary: "The signed-in user", Tags: tags}, s.me)
	huma.Register(api, huma.Operation{OperationID: "change-password", Method: "POST", Path: "/api/me/password",
		Summary: "Change password and sign out other sessions", Tags: tags, DefaultStatus: 204}, s.changePassword)
	huma.Register(api, huma.Operation{OperationID: "list-my-sessions", Method: "GET", Path: "/api/me/sessions",
		Summary: "My login sessions", Tags: tags}, s.mySessions)
	huma.Register(api, huma.Operation{OperationID: "delete-my-session", Method: "DELETE", Path: "/api/me/sessions/{id}",
		Summary: "Sign out one of my sessions", Tags: tags, DefaultStatus: 204}, s.deleteMySession)
}

func (s *Server) registerUser(ctx context.Context, in *credentialsInput) (*userWithCookies, error) {
	if err := s.rateLimit(ctx, "auth:"+requestOf(ctx).IP, 10, time.Minute); err != nil {
		return nil, err
	}
	email := auth.NormalizeEmail(in.Body.Email)
	if err := auth.CheckCredentials(email, in.Body.Password); err != nil {
		return nil, failErr(http.StatusUnprocessableEntity, "invalid_credentials", err)
	}
	role, err := s.newUserRole(ctx, email)
	if err != nil {
		return nil, err
	}
	hash, err := auth.HashPassword(in.Body.Password)
	if err != nil {
		return nil, err
	}
	user, err := s.store.CreateUser(ctx, email, hash)
	if errors.Is(err, store.ErrConflict) {
		return nil, fail(http.StatusConflict, "email_taken", "这个邮箱已经注册过了")
	}
	if err != nil {
		return nil, err
	}
	if err := s.authz.setRole(user.ID, role); err != nil {
		// No account without a role: take the row back so the address can register again.
		if derr := s.store.DeleteUser(ctx, user.ID); derr != nil {
			s.log.Error("registration: remove account without role", "user_id", user.ID, "err", derr)
		}
		return nil, err
	}
	return s.signedIn(ctx, user, "register")
}

func (s *Server) newUserRole(ctx context.Context, email string) (string, error) {
	count, err := s.store.CountUsers(ctx)
	if err != nil {
		return "", err
	}
	settings, err := s.platformSettings(ctx)
	if err != nil {
		return "", err
	}
	if count > 0 && !settings.RegistrationOpen {
		return "", fail(http.StatusForbidden, "registration_closed", "注册已关闭，请联系管理员")
	}
	if count == 0 || s.cfg.AdminEmails[email] {
		return roleAdmin, nil
	}
	return roleUser, nil
}

func (s *Server) signedIn(ctx context.Context, user store.User, action string) (*userWithCookies, error) {
	cookies, err := s.startSession(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	_ = s.store.TouchLogin(ctx, user.ID)
	s.audit(ctx, user.ID, action, "user", user.ID, nil)
	return &userWithCookies{SetCookie: cookies, Body: userView(user, s.authz.role(user.ID))}, nil
}

func (s *Server) login(ctx context.Context, in *credentialsInput) (*userWithCookies, error) {
	if err := s.rateLimit(ctx, "auth:"+requestOf(ctx).IP, 10, time.Minute); err != nil {
		return nil, err
	}
	email := auth.NormalizeEmail(in.Body.Email)
	// Per account too, so many addresses cannot share the guessing of one password.
	if err := s.rateLimit(ctx, "auth-account:"+email, 30, 15*time.Minute); err != nil {
		return nil, err
	}
	user, hash, err := s.store.UserCredentials(ctx, email)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	if err != nil || !auth.VerifyPassword(in.Body.Password, hash) {
		return nil, fail(http.StatusUnauthorized, "bad_credentials", "邮箱或密码不对")
	}
	if !user.Active {
		return nil, fail(http.StatusForbidden, "inactive", "账号已停用，请联系管理员")
	}
	return s.signedIn(ctx, user, "login")
}

func (s *Server) logout(ctx context.Context, _ *struct{}) (*cookiesOnly, error) {
	if p := principalOf(ctx); p != nil {
		if err := s.store.DeleteSession(ctx, p.Session.TokenHash); err != nil {
			return nil, err
		}
	}
	return &cookiesOnly{SetCookie: s.clearCookies()}, nil
}

func (s *Server) me(ctx context.Context, _ *struct{}) (*userOutput, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	return &userOutput{Body: userView(p.User, p.Role)}, nil
}

func (s *Server) changePassword(ctx context.Context, in *passwordInput) (*struct{}, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	hash, err := s.store.PasswordHash(ctx, p.User.ID)
	if err != nil {
		return nil, err
	}
	if !auth.VerifyPassword(in.Body.Current, hash) {
		return nil, fail(http.StatusUnprocessableEntity, "bad_password", "当前密码不对")
	}
	if err := auth.CheckPassword(in.Body.New); err != nil {
		return nil, failErr(http.StatusUnprocessableEntity, "weak_password", err)
	}
	newHash, err := auth.HashPassword(in.Body.New)
	if err != nil {
		return nil, err
	}
	if err := s.store.SetPassword(ctx, p.User.ID, newHash, false); err != nil {
		return nil, err
	}
	s.audit(ctx, p.User.ID, "password_change", "user", p.User.ID, nil)
	return nil, s.store.DeleteUserSessions(ctx, p.User.ID, p.Session.TokenHash)
}

func (s *Server) mySessions(ctx context.Context, _ *struct{}) (*sessionsOutput, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.store.ListUserSessions(ctx, p.User.ID)
	if err != nil {
		return nil, err
	}
	out := make([]SessionView, len(rows))
	for i, x := range rows {
		out[i] = SessionView{ID: x.TokenHash[:16], Current: x.TokenHash == p.Session.TokenHash, UserAgent: x.UserAgent,
			CreatedAt: x.CreatedAt, LastSeenAt: x.LastSeenAt, ExpiresAt: x.ExpiresAt}
	}
	return &sessionsOutput{Body: out}, nil
}

func (s *Server) deleteMySession(ctx context.Context, in *sessionIDInput) (*struct{}, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	err = s.store.DeleteUserSessionByPrefix(ctx, p.User.ID, strings.ToLower(in.ID))
	return nil, storeErr(err, "没有这个会话")
}
