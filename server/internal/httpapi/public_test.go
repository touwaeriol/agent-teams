package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStaticWebApp(t *testing.T) {
	h := newHarness(t)
	_ = os.WriteFile(filepath.Join(h.cfg.WebDir, "index.html"), []byte("<html>app</html>"), 0o644)
	_ = os.MkdirAll(filepath.Join(h.cfg.WebDir, "assets"), 0o755)
	_ = os.WriteFile(filepath.Join(h.cfg.WebDir, "assets", "app.js"), []byte("js"), 0o644)
	c := h.client()
	for _, path := range []string{"/", "/projects/abc/threads/3", "/settings/team"} {
		if r := c.do("GET", path, nil); r.Status != 200 || string(r.Body) != "<html>app</html>" {
			t.Fatalf("%s = %d %s", path, r.Status, r.Body)
		}
	}
	r := c.do("GET", "/assets/app.js", nil)
	if r.Status != 200 || !strings.Contains(r.Header.Get("Cache-Control"), "immutable") {
		t.Fatalf("asset = %d %v", r.Status, r.Header)
	}
	if r := c.do("GET", "/api/nope", nil); r.Status != 404 || r.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("api 404 = %d %s", r.Status, r.Header.Get("Content-Type"))
	}
	if r := c.do("GET", "/../../etc/passwd", nil); r.Status != 200 || string(r.Body) != "<html>app</html>" {
		t.Fatalf("traversal = %d %s", r.Status, r.Body)
	}
}

func TestInstallScriptAndDist(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	r := c.must(200, "GET", "/install-node.sh", nil, nil, "Host", "console.test")
	if !strings.Contains(string(r.Body), `release_version=""`) || !strings.Contains(string(r.Body), "http://console.test") {
		t.Fatal("script without release should have an empty version")
	}
	tgz := []byte("fake tarball")
	_ = os.WriteFile(filepath.Join(h.cfg.NodeDistDir, "agent-teams-node-2.0.3.tgz"), tgz, 0o644)
	sum := sha256.Sum256(tgz)
	r = c.must(200, "GET", "/install-node.sh", nil, nil)
	body := string(r.Body)
	if !strings.Contains(body, `release_version="2.0.3"`) || !strings.Contains(body, hex.EncodeToString(sum[:])) {
		t.Fatalf("script release not rendered")
	}
	if r := c.do("GET", "/node/dist/agent-teams-node-2.0.3.tgz", nil); r.Status != 200 || string(r.Body) != "fake tarball" {
		t.Fatalf("dist = %d", r.Status)
	}
	if r := c.do("GET", "/node/dist/..%2Fsecret", nil); r.Status != 404 {
		t.Fatalf("dist traversal = %d", r.Status)
	}
}

func TestNodeDockerGuideIsPublic(t *testing.T) {
	h := newHarness(t)
	r := h.client().must(200, "GET", "/node-docker.md", nil, nil, "Host", "console.test")
	body := string(r.Body)
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "text/markdown") || strings.Contains(body, "__") ||
		!strings.Contains(body, "AGENT_TEAMS_CONSOLE=http://console.test") || !strings.Contains(body, "image: ghcr.io/example/node:test") {
		t.Fatalf("guide = %s\n%s", r.Header.Get("Content-Type"), body)
	}
}

func TestHealthAndOpenAPI(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	c.must(200, "GET", "/healthz", nil, nil)
	var ready struct {
		OK     bool              `json:"ok"`
		Checks map[string]string `json:"checks"`
	}
	c.must(200, "GET", "/readyz", nil, &ready)
	if !ready.OK {
		t.Fatalf("ready %+v", ready)
	}
	r := c.must(200, "GET", "/api/openapi.json", nil, nil)
	if !strings.Contains(string(r.Body), `"openapi": "3.1.0"`) && !strings.Contains(string(r.Body), `"openapi":"3.1.0"`) {
		t.Fatalf("openapi %s", r.Body[:200])
	}
}

// TestAdminPagesFallBackToWebApp: the console's /admin/* pages are web app routes (the
// server serves index.html, the API decides who sees data), while unknown /api/admin/ paths
// stay JSON errors and never turn into the web page.
func TestAdminPagesFallBackToWebApp(t *testing.T) {
	h := newHarness(t)
	_ = os.WriteFile(filepath.Join(h.cfg.WebDir, "index.html"), []byte("<html>app</html>"), 0o644)
	anon := h.client()
	_, _ = h.signUp("first@example.com") // the first account becomes admin
	user, _ := h.signUp("someone@example.com")
	for _, c := range []*client{anon, user} {
		for _, path := range []string{"/admin", "/admin/", "/admin/users", "/admin/platform", "/admin/users/u1"} {
			r := c.do("GET", path, nil)
			if r.Status != 200 || string(r.Body) != "<html>app</html>" || !strings.HasPrefix(r.Header.Get("Content-Type"), "text/html") {
				t.Fatalf("%s = %d %q %s", path, r.Status, r.Header.Get("Content-Type"), r.Body)
			}
		}
	}
	for _, path := range []string{"/api/admin/nope", "/api/admin", "/api/admin/users/u1/nope"} {
		r := anon.do("GET", path, nil)
		if r.Status != 404 || r.Header.Get("Content-Type") != "application/json" || strings.Contains(string(r.Body), "<html>") {
			t.Fatalf("%s = %d %q %s", path, r.Status, r.Header.Get("Content-Type"), r.Body)
		}
	}
	for _, c := range []struct {
		cl   *client
		want int
	}{{anon, 401}, {user, 403}} {
		r := c.cl.do("GET", "/api/admin/users", nil)
		if r.Status != c.want || r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("GET /api/admin/users = %d %q, want %d JSON", r.Status, r.Header.Get("Content-Type"), c.want)
		}
	}
}
