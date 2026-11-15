package httpapi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestOfficeLayouts(t *testing.T) {
	h := newHarness(t)
	admin, _ := h.signUp("a@example.com")
	user, _ := h.signUp("b@example.com")
	r := user.must(200, "GET", "/api/office/layouts/default", nil, nil)
	etag := r.Header.Get("ETag")
	var doc map[string]any
	r.json(t, &doc)
	if etag == "" || doc["format"] != float64(1) {
		t.Fatalf("layout etag=%q", etag)
	}
	if r := user.do("GET", "/api/office/layouts/default", nil, "If-None-Match", etag); r.Status != 304 || len(r.Body) != 0 {
		t.Fatalf("conditional = %d %q", r.Status, r.Body)
	}
	if r := user.do("GET", "/api/office/layouts/nope", nil); r.Status != 404 {
		t.Fatalf("unknown = %d", r.Status)
	}
	if r := user.do("PUT", "/api/admin/office/layouts/compact", map[string]any{"doc": doc}); r.Status != 403 {
		t.Fatalf("non-admin put = %d", r.Status)
	}
	r = admin.do("PUT", "/api/admin/office/layouts/compact", map[string]any{"doc": map[string]any{"format": 1}})
	if r.Status != 422 || len(r.apiError(t).Fields) == 0 {
		t.Fatalf("invalid layout = %d %s", r.Status, r.Body)
	}
	doc["id"], doc["name"] = "compact", "Compact"
	admin.must(200, "PUT", "/api/admin/office/layouts/compact", map[string]any{"doc": doc}, nil)
	var p ProjectView
	user.must(201, "POST", "/api/projects", map[string]any{"name": "P"}, &p)
	user.must(200, "PATCH", "/api/projects/"+p.ID, map[string]any{"office_layout": "compact"}, nil)
	r = user.must(200, "GET", "/api/office/layout?project="+p.ID, nil, nil)
	if r.Header.Get("X-Layout-Id") != "compact" {
		t.Fatalf("current layout %s", r.Header.Get("X-Layout-Id"))
	}
	doc["name"] = "Compact 2"
	admin.must(200, "PUT", "/api/admin/office/layouts/compact", map[string]any{"doc": doc}, nil)
	if !h.events.has("project:" + p.ID + ":layout") {
		t.Fatal("layout event not sent to the project using it")
	}
	var list []map[string]any
	admin.must(200, "GET", "/api/admin/office/layouts", nil, &list)
	if len(list) != 2 {
		t.Fatalf("layouts %v", list)
	}
	admin.must(204, "DELETE", "/api/admin/office/layouts/compact", nil, nil)
	if r := admin.do("DELETE", "/api/admin/office/layouts/default", nil); r.Status != 404 {
		t.Fatalf("delete file layout = %d", r.Status)
	}
}

func TestOfficeFilesFromDirectory(t *testing.T) {
	h := newHarness(t)
	raw, _ := os.ReadFile("../office/default.json")
	var doc map[string]any
	_ = json.Unmarshal(raw, &doc)
	doc["name"] = "From disk"
	edited, _ := json.Marshal(doc)
	_ = os.WriteFile(filepath.Join(h.cfg.OfficeDir, "default.json"), edited, 0o644)
	_ = os.MkdirAll(filepath.Join(h.cfg.OfficeDir, "assets"), 0o755)
	_ = os.WriteFile(filepath.Join(h.cfg.OfficeDir, "assets", "tiles.png"), []byte("png"), 0o644)
	h.server.office.ReloadFiles()
	c := h.client()
	r := c.must(200, "GET", "/office/default.json", nil, nil)
	var got map[string]any
	r.json(t, &got)
	if got["name"] != "From disk" {
		t.Fatalf("name %v", got["name"])
	}
	if r := c.do("GET", "/office/assets/tiles.png", nil); r.Status != 200 || string(r.Body) != "png" {
		t.Fatalf("asset = %d", r.Status)
	}
	if r := c.do("GET", "/office/assets/../../etc/passwd", nil); r.Status != 404 {
		t.Fatalf("traversal = %d", r.Status)
	}
}
