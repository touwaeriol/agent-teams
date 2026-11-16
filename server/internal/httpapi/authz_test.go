package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/touwaeriol/agent-teams/server/internal/config"
)

// TestPolicyCoversFoundationRoutes: every route of the foundation has its own policy line
// (cmd/atctl/authz_test.go checks the full table with the engines wired, and each subject).
func TestPolicyCoversFoundationRoutes(t *testing.T) {
	s := New(Deps{Config: config.Config{PublicURL: "http://localhost:3001"}})
	lines, err := s.Policy()
	if err != nil {
		t.Fatal(err)
	}
	explicit := map[string]bool{}
	for _, l := range lines {
		explicit[l[2]+" "+l[1]] = true
	}
	routes := s.Routes()
	if len(routes) < 60 {
		t.Fatalf("only %d routes registered", len(routes))
	}
	for _, r := range routes {
		if !explicit[r.Method+" "+r.Path] {
			t.Errorf("%s %s has no policy line", r.Method, r.Path)
		}
	}
}

// TestPolicyIsExact: no prefixes or patterns, a route template matches only itself.
func TestPolicyIsExact(t *testing.T) {
	a := mustAuthorizer(nil)
	for _, c := range [][3]string{{roleUser, "/api/projects/:pid/x", "GET"}, {roleUser, "/api/projects", "PUT"},
		{roleAdmin, "/api/admin/other", "GET"}, {roleUser, "/unknown", "GET"}, {roleNode, "/api/projects", "GET"},
		{roleAnonymous, "/nodes/connect", "GET"}, {roleUser, "/nodes/connect", "GET"}, {roleNode, "/files/*path", "POST"}} {
		if ok, err := a.allowed(c[0], c[1], c[2]); err != nil || ok {
			t.Errorf("%s %s as %s allowed=%v err=%v", c[2], c[1], c[0], ok, err)
		}
	}
	if ok, _ := a.allowed(roleNode, "/nodes/connect", "GET"); !ok {
		t.Error("node denied its WebSocket")
	}
}

func TestParsePolicyRejectsBadLines(t *testing.T) {
	for _, bad := range []string{"p, user, /x", "x, a, b", "g, a"} {
		if _, err := parsePolicy(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// TestRouteRulesAreReadOnly: the embedded route policy cannot be changed at run time, and
// without a database there is nowhere to keep role assignments.
func TestRouteRulesAreReadOnly(t *testing.T) {
	a := mustAuthorizer(nil)
	if ok, err := a.e.AddPolicy("user", "/api/admin/settings", "PUT"); err == nil || ok {
		t.Fatalf("p rule written: %v %v", ok, err)
	}
	if err := a.setRole("u1", roleAdmin); err == nil {
		t.Fatal("role assigned without a database")
	}
	if ok, _ := a.allowed("u1", "/api/admin/settings", "PUT"); ok {
		t.Fatal("failed assignment still grants admin")
	}
}

func TestSubjectOf(t *testing.T) {
	plain := httptest.NewRequest("GET", "/", nil)
	bearer := httptest.NewRequest("GET", "/", nil)
	bearer.Header.Set("Authorization", "Bearer node-token")
	user := &principal{Role: roleAdmin}
	user.User.ID = "u1"
	cases := []struct {
		p    *principal
		r    *http.Request
		want string
	}{{nil, plain, roleAnonymous}, {nil, bearer, roleNode}, {user, plain, "u1"}, {user, bearer, "u1"}}
	for _, c := range cases {
		if got := subjectOf(c.p, c.r); got != c.want {
			t.Errorf("subjectOf = %s, want %s", got, c.want)
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
	r = anon.do("GET", "/api/projects", nil, "Authorization", "Bearer not-a-session")
	if r.Status != http.StatusUnauthorized || r.apiError(t).Code != "unauthorized" {
		t.Fatalf("node token on a user route = %d %s", r.Status, string(r.Body))
	}
	if r := anon.do("GET", "/healthz", nil, "Authorization", "Bearer not-a-session"); r.Status != http.StatusOK {
		t.Fatalf("healthz with a node token = %d", r.Status)
	}
	if r := anon.do("GET", "/healthz", nil); r.Status != http.StatusOK {
		t.Fatalf("healthz = %d", r.Status)
	}
	if r := anon.do("GET", "/api/openapi.json", nil); r.Status != http.StatusOK {
		t.Fatalf("openapi = %d", r.Status)
	}
}

// TestRouterEdges keeps the routing of the chi era: 405 on a wrong method, no trailing-slash redirect.
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
