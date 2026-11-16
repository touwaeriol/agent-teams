package httpapi

import (
	"context"
	"crypto/subtle"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/touwaeriol/agent-teams/server/internal/auth"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// Cookie and header names (same session cookie as v1, api.md §0).
const (
	SessionCookie   = "agent_teams_session"
	CSRFCookie      = "agent_teams_csrf"
	CSRFHeader      = "X-CSRF-Token"
	sessionLifetime = 30 * 24 * time.Hour
	renewBelow      = 7 * 24 * time.Hour
	touchEvery      = 5 * time.Minute
)

// csrfExempt are writes that carry no session yet, or use their own signature.
var csrfExempt = map[string]bool{
	"/auth/login": true, "/auth/register": true, "/api/git/github-app/webhook": true,
}

// passwordExempt stay reachable while an admin-issued temporary password must be changed.
var passwordExempt = map[string]bool{
	"/api/me": true, "/api/me/password": true, "/auth/logout": true,
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
}

func sessionToken(r *http.Request) (string, bool) {
	if t := bearerToken(r); t != "" {
		return t, true
	}
	c, err := r.Cookie(SessionCookie)
	if err != nil || c.Value == "" {
		return "", false
	}
	return c.Value, false
}

// session resolves the caller, keeps the session fresh and enforces CSRF.
func (s *Server) session(c *gin.Context) {
	r := c.Request
	ctx := withRequest(r.Context(), r)
	p, token, err := s.authenticate(ctx, r)
	if err != nil {
		s.log.Error("session lookup failed", "err", err)
		abortWith(c, fail(http.StatusServiceUnavailable, "unavailable", "数据库暂时不可用"))
		return
	}
	if p != nil {
		if blocked := s.guard(ctx, c.Writer, r, p, token); blocked != nil {
			abortWith(c, blocked)
			return
		}
		ctx = withPrincipal(ctx, p)
	}
	c.Request = r.WithContext(ctx)
	c.Next()
}

func (s *Server) authenticate(ctx context.Context, r *http.Request) (*principal, string, error) {
	token, bearer := sessionToken(r)
	if token == "" {
		return nil, "", nil
	}
	sess, user, err := s.store.SessionUser(ctx, auth.HashToken(token))
	if errors.Is(err, store.ErrNotFound) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	return &principal{User: user, Session: sess, Bearer: bearer}, token, nil
}

func (s *Server) guard(ctx context.Context, w http.ResponseWriter, r *http.Request, p *principal, token string) error {
	s.keepFresh(ctx, w, r, p, token)
	if !p.Bearer && unsafeMethod(r.Method) && !csrfExempt[r.URL.Path] && !validCSRF(r, p.Session.CSRFSecret) {
		return fail(http.StatusForbidden, "csrf", "安全校验失败，请刷新页面后重试")
	}
	if p.User.MustChangePassword && strings.HasPrefix(r.URL.Path, "/api/") && !passwordExempt[r.URL.Path] {
		return fail(http.StatusForbidden, "password_change_required", "请先修改临时密码")
	}
	return nil
}

func unsafeMethod(m string) bool {
	return m != http.MethodGet && m != http.MethodHead && m != http.MethodOptions
}

func validCSRF(r *http.Request, secret string) bool {
	got := r.Header.Get(CSRFHeader)
	return secret != "" && got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(secret)) == 1
}

// keepFresh gives the session a CSRF secret, slides its expiry and records activity.
func (s *Server) keepFresh(ctx context.Context, w http.ResponseWriter, r *http.Request, p *principal, token string) {
	if p.Session.CSRFSecret == "" {
		secret, _ := auth.NewToken()
		if err := s.store.SetCSRFSecret(ctx, p.Session.TokenHash, secret); err == nil {
			p.Session.CSRFSecret = secret
		}
	}
	if !p.Bearer && cookieValue(r, CSRFCookie) != p.Session.CSRFSecret {
		http.SetCookie(w, s.csrfCookie(p.Session.CSRFSecret))
	}
	s.touch(ctx, w, p, token)
}

func (s *Server) touch(ctx context.Context, w http.ResponseWriter, p *principal, token string) {
	now := time.Now()
	renew := p.Session.ExpiresAt.Sub(now) < renewBelow
	stale := p.Session.LastSeenAt == nil || now.Sub(*p.Session.LastSeenAt) > touchEvery
	if !renew && !stale {
		return
	}
	var expires *time.Time
	if renew {
		e := now.Add(sessionLifetime)
		expires = &e
	}
	if err := s.store.TouchSession(ctx, p.Session.TokenHash, expires); err != nil {
		s.log.Warn("session touch failed", "err", err)
		return
	}
	if renew && !p.Bearer {
		http.SetCookie(w, s.sessionCookie(token))
	}
}

func cookieValue(r *http.Request, name string) string {
	c, err := r.Cookie(name)
	if err != nil {
		return ""
	}
	return c.Value
}

func (s *Server) sessionCookie(token string) *http.Cookie {
	return &http.Cookie{Name: SessionCookie, Value: token, Path: "/", MaxAge: int(sessionLifetime / time.Second),
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: s.cfg.SecureCookies()}
}

func (s *Server) csrfCookie(secret string) *http.Cookie {
	return &http.Cookie{Name: CSRFCookie, Value: secret, Path: "/", MaxAge: int(sessionLifetime / time.Second),
		SameSite: http.SameSiteLaxMode, Secure: s.cfg.SecureCookies()}
}

func (s *Server) clearCookies() []http.Cookie {
	out := []http.Cookie{}
	for _, name := range []string{SessionCookie, CSRFCookie} {
		out = append(out, http.Cookie{Name: name, Value: "", Path: "/", MaxAge: -1, SameSite: http.SameSiteLaxMode,
			Secure: s.cfg.SecureCookies(), HttpOnly: name == SessionCookie})
	}
	return out
}

// startSession creates a session and returns the cookies to set.
func (s *Server) startSession(ctx context.Context, userID string) ([]http.Cookie, error) {
	token, hash := auth.NewToken()
	secret, _ := auth.NewToken()
	err := s.store.CreateSession(ctx, store.Session{TokenHash: hash, UserID: userID,
		ExpiresAt: time.Now().Add(sessionLifetime), UserAgent: truncate(requestOf(ctx).UserAgent, 300), CSRFSecret: secret})
	if err != nil {
		return nil, err
	}
	return []http.Cookie{*s.sessionCookie(token), *s.csrfCookie(secret)}, nil
}

func forwardedFor(r *http.Request) string {
	peer := net.ParseIP(clientIP(r))
	if peer == nil || !(peer.IsLoopback() || peer.IsPrivate()) {
		return ""
	}
	parts := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	last := strings.TrimSpace(parts[len(parts)-1])
	if net.ParseIP(last) == nil {
		return ""
	}
	return last
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
