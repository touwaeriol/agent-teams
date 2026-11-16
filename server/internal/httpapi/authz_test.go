package httpapi

import (
	"net/http"
	"strings"
	"testing"

	"github.com/touwaeriol/agent-teams/server/internal/config"
)

// publicRoutes may be called without signing in ("METHOD route").
var publicRoutes = map[string]bool{
	"GET /healthz": true, "GET /readyz": true, "POST /auth/login": true, "POST /auth/register": true,
	"POST /auth/logout": true, "GET /install-node.sh": true, "GET /node/dist/:file": true, "GET /office/*path": true,
	"GET /api/git/github-app/install": true, "GET /api/git/github-app/created": true,
	"GET /api/git/github-app/installed": true, "POST /api/git/github-app/webhook": true,
}

// wantRoles is who may call a route: public, admin-only or signed-in users.
func wantRoles(method, route string) map[string]bool {
	switch {
	case publicRoutes[method+" "+route] || strings.HasPrefix(route, "/api/openapi") || route == "/api/docs":
		return map[string]bool{roleAnonymous: true, roleUser: true, roleAdmin: true}
	case strings.HasPrefix(route, "/api/admin/"):
		return map[string]bool{roleAdmin: true}
	}
	return map[string]bool{roleUser: true, roleAdmin: true}
}

// TestPolicyCoversEveryRoute: each registered route is reachable by exactly the roles of its group.
func TestPolicyCoversEveryRoute(t *testing.T) {
	s := New(Deps{Config: config.Config{PublicURL: "http://localhost:3001"}})
	routes := s.router.Routes()
	if len(routes) < 60 {
		t.Fatalf("only %d routes registered", len(routes))
	}
	for _, r := range routes {
		checkRoute(t, s.authz, r.Method, r.Path, wantRoles(r.Method, r.Path))
	}
}

// TestPolicyRawRoutes covers routes only mounted with a running engine.
func TestPolicyRawRoutes(t *testing.T) {
	a := mustAuthorizer()
	everyone := map[string]bool{roleAnonymous: true, roleUser: true, roleAdmin: true}
	for _, route := range []string{"/nodes/connect", "/nodes/v2/connect", "/nodes/attachments/:id", "/files/*path"} {
		checkRoute(t, a, "GET", route, everyone)
	}
	checkRoute(t, a, "HEAD", "/files/*path", everyone)
	checkRoute(t, a, "POST", "/files/*path", map[string]bool{})
	checkRoute(t, a, "GET", "/unknown", map[string]bool{})
}

func checkRoute(t *testing.T, a *authorizer, method, route string, want map[string]bool) {
	t.Helper()
	for _, role := range []string{roleAnonymous, roleUser, roleAdmin} {
		ok, err := a.allowed(role, route, method)
		if err != nil {
			t.Fatalf("%s %s as %s: %v", method, route, role, err)
		}
		if ok != want[role] {
			t.Errorf("%s %s as %s: allowed=%v, want %v", method, route, role, ok, want[role])
		}
	}
}

func TestLoadPolicyRejectsBadLines(t *testing.T) {
	a := mustAuthorizer()
	for _, bad := range []string{"p, user, /x", "x, a, b", "g, a"} {
		if err := loadPolicy(a.e, bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestRoleOf(t *testing.T) {
	cases := map[string]*principal{roleAnonymous: nil, roleUser: {}, roleAdmin: {}}
	cases[roleAdmin].User.Role = "admin"
	cases[roleUser].User.Role = "user"
	for want, p := range cases {
		if got := roleOf(p); got != want {
			t.Errorf("roleOf = %s, want %s", got, want)
		}
	}
}

// TestAuthorizeResponses: anonymous callers get 401, users 403 on admin routes, before any handler runs.
func TestAuthorizeResponses(t *testing.T) {
	h := newHarness(t)
	_, _ = h.signUp("a@example.com")
	user, _ := h.signUp("b@example.com")
	anon := h.client()
	r := anon.do("POST", "/api/projects", map[string]any{"bad": 1})
	if r.Status != http.StatusUnauthorized || r.apiError(t).Code != "unauthorized" {
		t.Fatalf("anonymous write = %d %s", r.Status, string(r.Body))
	}
	r = user.do("PUT", "/api/admin/settings", map[string]any{"bad": 1})
	if r.Status != http.StatusForbidden || r.apiError(t).Code != "admin_required" {
		t.Fatalf("user on admin route = %d %s", r.Status, string(r.Body))
	}
	if r := anon.do("GET", "/healthz", nil); r.Status != http.StatusOK {
		t.Fatalf("healthz = %d", r.Status)
	}
	if r := anon.do("GET", "/api/openapi.json", nil); r.Status != http.StatusOK {
		t.Fatalf("openapi = %d", r.Status)
	}
}

// TestRouterEdges keeps the chi-era routing: 405 on a wrong method, no trailing-slash redirect.
func TestRouterEdges(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	if r := c.do("GET", "/auth/login", nil); r.Status != http.StatusMethodNotAllowed {
		t.Fatalf("GET /auth/login = %d", r.Status)
	}
	if r := c.do("GET", "/api/me/", nil); r.Status != http.StatusNotFound {
		t.Fatalf("GET /api/me/ = %d", r.Status)
	}
}
