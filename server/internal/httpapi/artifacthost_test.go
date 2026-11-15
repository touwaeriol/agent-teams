package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestArtifactHostOnly(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	h := artifactHostOnly("https://files.example.net")(ok)
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
	artifactHostOnly("")(ok).ServeHTTP(w, r)
	if w.Code != http.StatusTeapot {
		t.Fatalf("no artifact origin must not filter: %d", w.Code)
	}
}
