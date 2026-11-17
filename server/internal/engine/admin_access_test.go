package engine_test

import (
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var adminSweepSeq atomic.Int64

// fillAdmin puts placeholder values into a gin route template's parameters.
func fillAdmin(route string) string {
	parts := strings.Split(route, "/")
	for i, p := range parts {
		if strings.HasPrefix(p, ":") || strings.HasPrefix(p, "*") {
			parts[i] = "placeholder"
		}
	}
	return strings.Join(parts, "/")
}

// TestAdminRoutesOverHTTP: every /api/admin/ route of the fully wired server (engines'
// routes included) answers 401 to a caller without a session and 403 admin_required to a
// signed-in user, before its handler runs; the administrator gets past authorization.
func TestAdminRoutesOverHTTP(t *testing.T) {
	w := newWorld(t, nil)
	admin := w.signUp("first@example.com") // the first account is the administrator
	user := w.signUp("second@example.com")
	jar, _ := cookiejar.New(nil)
	n := adminSweepSeq.Add(1)
	anon := &browser{w: w, http: &http.Client{Jar: jar, Timeout: 20 * time.Second}, ip: fmt.Sprintf("10.78.%d.%d", n/250, n%250+1)}
	swept := 0
	for _, r := range w.h.Routes() {
		if !strings.HasPrefix(r.Path, "/api/admin/") {
			continue
		}
		swept++
		path := fillAdmin(r.Path)
		if code, raw := anon.do(r.Method, path, map[string]any{}); code != http.StatusUnauthorized || !strings.Contains(string(raw), `"code":"unauthorized"`) {
			t.Errorf("anonymous %s %s = %d %s", r.Method, path, code, raw)
		}
		if code, raw := user.do(r.Method, path, map[string]any{}); code != http.StatusForbidden || !strings.Contains(string(raw), `"code":"admin_required"`) {
			t.Errorf("user %s %s = %d %s", r.Method, path, code, raw)
		}
		if code, raw := admin.do(r.Method, path, map[string]any{}); code == http.StatusUnauthorized || code == http.StatusForbidden {
			t.Errorf("admin %s %s = %d %s", r.Method, path, code, raw)
		}
	}
	if swept < 25 {
		t.Fatalf("only %d admin routes swept", swept)
	}
	if code, raw := admin.do("GET", "/api/me", nil); code != http.StatusOK || !strings.Contains(string(raw), `"role":"admin"`) {
		t.Fatalf("first account is not admin: %d %s", code, raw)
	}
}
