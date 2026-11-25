package main

import (
	"strings"
	"testing"

	"github.com/touwaeriol/agent-teams/server/internal/config"
	"github.com/touwaeriol/agent-teams/server/internal/httpapi"
)

// The route groups, written down independently of authz/policy.csv.
var (
	publicRoutes = map[string]bool{
		"GET /healthz": true, "GET /readyz": true, "GET /api/docs": true,
		"POST /auth/login": true, "POST /auth/register": true, "POST /auth/logout": true,
		"GET /install-node.sh": true, "GET /node-docker.md": true, "GET /node/dist/:file": true, "GET /office/*path": true,
		"GET /files/*path": true, "HEAD /files/*path": true,
		"GET /api/git/github-app/install": true, "GET /api/git/github-app/created": true,
		"GET /api/git/github-app/installed": true, "POST /api/git/github-app/webhook": true,
	}
	nodeRoutes = map[string]bool{
		"GET /nodes/connect": true, "GET /nodes/v2/connect": true, "GET /nodes/attachments/:id": true, "GET /nodes/moves/:id": true, "PUT /nodes/moves/:id": true, "GET /nodes/ssh": true,
		"GET /nodes/attachments/:id/url": true, "POST /nodes/uploads": true,
	}
	subjects = []string{httpapi.SubjectAnonymous, httpapi.SubjectNode, httpapi.SubjectUser, httpapi.SubjectAdmin}
)

// expected is who may call a route.
func expected(method, route string) map[string]bool {
	key := method + " " + route
	switch {
	case publicRoutes[key] || strings.HasPrefix(route, "/api/openapi"):
		return map[string]bool{httpapi.SubjectAnonymous: true, httpapi.SubjectNode: true, httpapi.SubjectUser: true, httpapi.SubjectAdmin: true}
	case nodeRoutes[key]:
		return map[string]bool{httpapi.SubjectNode: true}
	case strings.HasPrefix(route, "/api/admin/"):
		return map[string]bool{httpapi.SubjectAdmin: true}
	}
	return map[string]bool{httpapi.SubjectUser: true, httpapi.SubjectAdmin: true}
}

// fullServer is the server exactly as serve wires it (engines' routes included), without I/O.
func fullServer(t *testing.T) *httpapi.Server {
	t.Helper()
	d, _, err := wire(httpapi.Deps{Config: config.Config{PublicURL: "http://localhost:3001"}})
	if err != nil {
		t.Fatal(err)
	}
	return httpapi.New(d)
}

// policyLines maps "METHOD route" to the subjects that hold a line for it.
func policyLines(t *testing.T, s *httpapi.Server) map[string][]string {
	t.Helper()
	lines, err := s.Policy()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	for _, l := range lines {
		out[l[2]+" "+l[1]] = append(out[l[2]+" "+l[1]], l[0])
	}
	return out
}

// TestEveryRouteHasExplicitPolicy: each registered route has exactly one policy line of its
// own, and no policy line names a route that does not exist.
func TestEveryRouteHasExplicitPolicy(t *testing.T) {
	s := fullServer(t)
	lines := policyLines(t, s)
	routes := s.Routes()
	if len(routes) < 140 {
		t.Fatalf("only %d routes registered", len(routes))
	}
	seen := map[string]bool{}
	for _, r := range routes {
		key := r.Method + " " + r.Path
		seen[key] = true
		if len(lines[key]) != 1 {
			t.Errorf("%s: policy lines for %v, want exactly one", key, lines[key])
		}
	}
	for key := range lines {
		if !seen[key] {
			t.Errorf("policy line for unregistered route %s", key)
		}
	}
}

// TestRouteAccessMatrix: anonymous, node, user and admin get exactly the routes of their groups.
func TestRouteAccessMatrix(t *testing.T) {
	s := fullServer(t)
	for _, r := range s.Routes() {
		want := expected(r.Method, r.Path)
		for _, sub := range subjects {
			ok, err := s.Allows(sub, r.Method, r.Path)
			if err != nil {
				t.Fatalf("%s %s as %s: %v", r.Method, r.Path, sub, err)
			}
			if ok != want[sub] {
				t.Errorf("%s %s as %s: allowed=%v, want %v", r.Method, r.Path, sub, ok, want[sub])
			}
		}
	}
}

// TestRouteGroupsExist keeps the hand-written groups honest: each listed route is registered.
func TestRouteGroupsExist(t *testing.T) {
	registered := map[string]bool{}
	for _, r := range fullServer(t).Routes() {
		registered[r.Method+" "+r.Path] = true
	}
	for _, group := range []map[string]bool{publicRoutes, nodeRoutes} {
		for key := range group {
			if !registered[key] {
				t.Errorf("%s is not registered", key)
			}
		}
	}
}
