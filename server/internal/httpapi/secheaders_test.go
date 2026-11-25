package httpapi

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/touwaeriol/agent-teams/server/internal/config"
)

// TestSecurityHeaders: every response of the full server (page, API, error, 404) carries the
// baseline headers; HSTS follows the https public URL like the Secure cookie flag.
func TestSecurityHeaders(t *testing.T) {
	paths := []string{"/", "/healthz", "/api/openapi.json", "/api/me", "/office/", "/nodes/ws", "/no/such/page"}
	for _, tc := range []struct {
		name, publicURL string
		hsts            bool
	}{
		{"https", "https://agent.example.net", true},
		{"http", "http://localhost:8080", false},
	} {
		s := New(Deps{Config: config.Config{PublicURL: tc.publicURL}})
		for _, p := range paths {
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, httptest.NewRequest("GET", p, nil))
			h := w.Result().Header
			if h.Get("X-Content-Type-Options") != "nosniff" {
				t.Errorf("%s %s: X-Content-Type-Options %q", tc.name, p, h.Get("X-Content-Type-Options"))
			}
			if h.Get("Referrer-Policy") != "strict-origin-when-cross-origin" {
				t.Errorf("%s %s: Referrer-Policy %q", tc.name, p, h.Get("Referrer-Policy"))
			}
			if h.Get("X-Frame-Options") != "SAMEORIGIN" {
				t.Errorf("%s %s: X-Frame-Options %q", tc.name, p, h.Get("X-Frame-Options"))
			}
			if got := h.Get("Strict-Transport-Security"); (got == hstsValue) != tc.hsts || (!tc.hsts && got != "") {
				t.Errorf("%s %s (status %d): Strict-Transport-Security %q, want present=%v", tc.name, p, w.Code, got, tc.hsts)
			}
		}
	}
}

// TestSecurityHeadersTLSRequest: a request that itself arrived over TLS gets HSTS even when the
// public URL is not https (direct TLS without a proxy).
func TestSecurityHeadersTLSRequest(t *testing.T) {
	s := New(Deps{Config: config.Config{PublicURL: "http://localhost:8080"}})
	r := httptest.NewRequest("GET", "/healthz", nil)
	r.TLS = &tls.ConnectionState{}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if got := w.Result().Header.Get("Strict-Transport-Security"); got != hstsValue {
		t.Fatalf("TLS request: Strict-Transport-Security %q", got)
	}
}

// TestSecurityHeadersPublishedFiles: /files/ is framed by the console from the file origin, so it
// gets no X-Frame-Options (its own CSP frame-ancestors governs framing), on either host.
func TestSecurityHeadersPublishedFiles(t *testing.T) {
	s := New(Deps{Config: config.Config{PublicURL: "https://agent.example.net", ArtifactOrigin: "https://files.example.net"}})
	for _, host := range []string{"files.example.net", "agent.example.net"} {
		r := httptest.NewRequest("GET", "/files/tok/index.html", nil)
		r.Host = host
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		h := w.Result().Header
		if h.Get("X-Frame-Options") != "" {
			t.Errorf("%s /files/: X-Frame-Options %q must be absent", host, h.Get("X-Frame-Options"))
		}
		if h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Strict-Transport-Security") != hstsValue {
			t.Errorf("%s /files/: baseline headers missing: %v", host, h)
		}
	}
	// Off /files/ on the file origin the 404 still carries X-Frame-Options.
	r := httptest.NewRequest("GET", "/", nil)
	r.Host = "files.example.net"
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusNotFound || w.Result().Header.Get("X-Frame-Options") != "SAMEORIGIN" {
		t.Fatalf("file origin /: %d %v", w.Code, w.Result().Header)
	}
}
