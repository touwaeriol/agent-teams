package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
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
