package httpapi

import (
	"context"
	"testing"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/auth"
)

func TestRegisterLoginLogout(t *testing.T) {
	h := newHarness(t)
	admin, first := h.signUp("first@example.com")
	if first.Role != "admin" {
		t.Fatalf("first user role %s", first.Role)
	}
	if admin.cookie(SessionCookie) == "" || admin.cookie(CSRFCookie) == "" {
		t.Fatal("cookies not set")
	}
	_, second := h.signUp("second@example.com")
	_, boss := h.signUp("Boss@Example.com")
	if second.Role != "user" || boss.Role != "admin" || boss.Email != "boss@example.com" {
		t.Fatalf("roles %s %s", second.Role, boss.Role)
	}
	c := h.client()
	if r := c.do("POST", "/auth/register", Credentials{"second@example.com", "password123"}); r.Status != 409 {
		t.Fatalf("duplicate = %d", r.Status)
	}
	if r := c.do("POST", "/auth/register", Credentials{"bad", "password123"}); r.Status != 422 || r.apiError(t).Detail != "请输入有效的邮箱地址" {
		t.Fatalf("bad email = %d %s", r.Status, r.Body)
	}
	if r := c.do("POST", "/auth/login", Credentials{"second@example.com", "wrong-pass"}); r.Status != 401 || r.apiError(t).Detail != "邮箱或密码不对" {
		t.Fatalf("wrong password = %d %s", r.Status, r.Body)
	}
	if r := c.do("GET", "/api/me", nil); r.Status != 401 || r.apiError(t).Detail != "请先登录" {
		t.Fatalf("anonymous me = %d %s", r.Status, r.Body)
	}
	var me UserView
	c.must(200, "POST", "/auth/login", Credentials{"SECOND@example.com", "password123"}, nil)
	c.must(200, "GET", "/api/me", nil, &me)
	if me.ID != second.ID {
		t.Fatal("wrong user")
	}
	c.must(204, "POST", "/auth/logout", nil, nil)
	if r := c.do("GET", "/api/me", nil); r.Status != 401 {
		t.Fatalf("after logout = %d", r.Status)
	}
}

func TestCSRFAndBearer(t *testing.T) {
	h := newHarness(t)
	c, _ := h.signUp("a@example.com")
	c.noCSRF = true
	r := c.do("POST", "/api/projects", map[string]string{"name": "x"})
	if r.Status != 403 || r.apiError(t).Code != "csrf" {
		t.Fatalf("write without token = %d %s", r.Status, r.Body)
	}
	c.noCSRF = false
	c.must(201, "POST", "/api/projects", map[string]string{"name": "x"}, nil)
	bearer := h.client()
	bearer.bearer, bearer.noCSRF = c.cookie(SessionCookie), true
	bearer.must(201, "POST", "/api/projects", map[string]string{"name": "y"}, nil)
}

func TestV1SessionKeepsWorkingAndGetsCSRF(t *testing.T) {
	h := newHarness(t)
	_, u := h.signUp("a@example.com")
	ctx := context.Background()
	// A session row written by v1: no csrf secret, expiring in 2 days.
	token, hash := auth.NewToken()
	if _, err := h.db.Pool.Exec(ctx, `INSERT INTO at_sessions (token_hash, user_id, expires_at) VALUES ($1, $2, now() + interval '2 days')`, hash, u.ID); err != nil {
		t.Fatal(err)
	}
	c := h.client()
	c.bearer = "" // cookie auth
	c.http.Jar.SetCookies(mustURL(h.srv.URL), []*httpCookie{{Name: SessionCookie, Value: token}})
	var me UserView
	r := c.must(200, "GET", "/api/me", nil, &me)
	if me.ID != u.ID || c.cookie(CSRFCookie) == "" {
		t.Fatalf("v1 session: %s csrf=%q", r.Body, c.cookie(CSRFCookie))
	}
	var expires time.Time
	_ = h.db.Pool.QueryRow(ctx, `SELECT expires_at FROM at_sessions WHERE token_hash = $1`, hash).Scan(&expires)
	if time.Until(expires) < 29*24*time.Hour {
		t.Fatalf("session not renewed: %v", expires)
	}
	c.must(201, "POST", "/api/projects", map[string]string{"name": "after-upgrade"}, nil)
}

func TestRegistrationClosedAndRateLimit(t *testing.T) {
	h := newHarness(t)
	admin, _ := h.signUp("a@example.com")
	var s AdminSettings
	admin.must(200, "GET", "/api/admin/settings", nil, &s)
	s.RegistrationOpen = false
	admin.must(200, "PUT", "/api/admin/settings", s, nil)
	c := h.client()
	if r := c.do("POST", "/auth/register", Credentials{"b@example.com", "password123"}); r.Status != 403 {
		t.Fatalf("closed registration = %d", r.Status)
	}
	var last response
	for i := 0; i < 12; i++ {
		last = c.do("POST", "/auth/login", Credentials{"nobody@example.com", "password123"})
	}
	if last.Status != 429 || last.Header.Get("Retry-After") == "" {
		t.Fatalf("rate limit = %d %v", last.Status, last.Header)
	}
}

func TestPasswordChangeAndSessions(t *testing.T) {
	h := newHarness(t)
	c1, _ := h.signUp("a@example.com")
	c2 := h.client()
	c2.must(200, "POST", "/auth/login", Credentials{"a@example.com", "password123"}, nil)
	var sessions []SessionView
	c1.must(200, "GET", "/api/me/sessions", nil, &sessions)
	if len(sessions) != 2 {
		t.Fatalf("sessions %d", len(sessions))
	}
	if r := c1.do("POST", "/api/me/password", map[string]string{"current": "nope", "new": "newpassword1"}); r.Status != 422 {
		t.Fatalf("wrong current = %d", r.Status)
	}
	c1.must(204, "POST", "/api/me/password", map[string]string{"current": "password123", "new": "newpassword1"}, nil)
	if r := c2.do("GET", "/api/me", nil); r.Status != 401 {
		t.Fatal("other session survived a password change")
	}
	c1.must(200, "GET", "/api/me", nil, nil)
	c1.must(200, "GET", "/api/me/sessions", nil, &sessions)
	if len(sessions) != 1 || !sessions[0].Current {
		t.Fatalf("sessions %+v", sessions)
	}
	if r := c1.do("DELETE", "/api/me/sessions/0000000000000000", nil); r.Status != 404 {
		t.Fatalf("delete unknown = %d", r.Status)
	}
	c1.must(204, "DELETE", "/api/me/sessions/"+sessions[0].ID, nil, nil)
	if r := c1.do("GET", "/api/me", nil); r.Status != 401 {
		t.Fatal("deleted session still works")
	}
}

func TestAdminPasswordResetForcesChange(t *testing.T) {
	h := newHarness(t)
	admin, _ := h.signUp("a@example.com")
	_, u := h.signUp("b@example.com")
	var out struct {
		TempPassword string `json:"temp_password"`
	}
	admin.must(200, "POST", "/api/admin/users/"+u.ID+"/password-reset", nil, &out)
	c := h.client()
	var me UserView
	c.must(200, "POST", "/auth/login", Credentials{"b@example.com", out.TempPassword}, &me)
	if !me.MustChangePassword {
		t.Fatal("must_change_password not set")
	}
	if r := c.do("GET", "/api/projects", nil); r.Status != 403 || r.apiError(t).Code != "password_change_required" {
		t.Fatalf("blocked = %d %s", r.Status, r.Body)
	}
	c.must(204, "POST", "/api/me/password", map[string]string{"current": out.TempPassword, "new": "brandnew123"}, nil)
	c.must(200, "GET", "/api/projects", nil, nil)
}
