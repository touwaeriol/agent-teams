package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/touwaeriol/agent-teams/server/internal/config"
)

func TestArtifactHostOnly(t *testing.T) {
	h := hostOnlyEngine("https://files.example.net")
	cases := []struct {
		host, path string
		want       int
	}{
		{"files.example.net", "/files/tok/index.html", http.StatusTeapot},
		{"FILES.example.net", "/files/tok/", http.StatusTeapot},
		{"files.example.net", "/api/me", http.StatusNotFound},
		{"files.example.net", "/", http.StatusNotFound},
		{"files.example.net", "/login", http.StatusNotFound},
		{"agent.example.net", "/api/me", http.StatusTeapot},
		{"agent.example.net", "/", http.StatusTeapot},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", c.path, nil)
		r.Host = c.host
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != c.want {
			t.Errorf("%s%s: %d, want %d", c.host, c.path, w.Code, c.want)
		}
	}
	r := httptest.NewRequest("GET", "/api/me", nil)
	w := httptest.NewRecorder()
	hostOnlyEngine("").ServeHTTP(w, r)
	if w.Code != http.StatusTeapot {
		t.Fatalf("no artifact origin must not filter: %d", w.Code)
	}
}

// hostOnlyEngine answers 418 on every path that gets past artifactHostOnly.
func hostOnlyEngine(origin string) http.Handler {
	e := newEngine()
	e.Use(artifactHostOnly(origin))
	e.NoRoute(func(c *gin.Context) { c.Status(http.StatusTeapot) })
	return e
}

// TestFileOriginOnFullServer: on the file origin, every route of the real server but /files/
// is 404 before session, CSRF or authorization run; the main origin is unaffected.
func TestFileOriginOnFullServer(t *testing.T) {
	s := New(Deps{Config: config.Config{PublicURL: "https://agent.example.net", ArtifactOrigin: "https://files.example.net"}})
	for _, r := range s.Routes() {
		if r.Path == "/files/*path" {
			continue
		}
		req := httptest.NewRequest(r.Method, r.Path, nil)
		req.Host = "files.example.net"
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Errorf("file origin %s %s = %d", r.Method, r.Path, w.Code)
		}
	}
	for _, path := range []string{"/", "/login", "/projects/p1"} {
		req := httptest.NewRequest("GET", path, nil)
		req.Host = "files.example.net"
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Errorf("file origin web app %s = %d", path, w.Code)
		}
	}
	req := httptest.NewRequest("GET", "/api/openapi.json", nil)
	req.Host = "agent.example.net"
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("main origin openapi = %d", w.Code)
	}
}
