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
	r := c.must(200, "GET", "/install-node.sh", nil, nil)
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
