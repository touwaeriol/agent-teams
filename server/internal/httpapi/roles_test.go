package httpapi

import (
	"context"
	"strings"
	"testing"

	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// TestRolesSeededWithDefaults: the first read creates the default team once; the retired
// /api/team endpoints are gone.
func TestRolesSeededWithDefaults(t *testing.T) {
	h := newHarness(t)
	c, _ := h.signUp("a@example.com")
	var body RolesBody
	c.must(200, "GET", "/api/roles", nil, &body)
	if len(body.Roles) != 3 || body.Roles[0].Tier != "coordinator" || body.Roles[0].Look.Outfit == "" || len(body.Report) != 0 {
		t.Fatalf("defaults %+v", body)
	}
	first := body.Roles[2].ID
	body = RolesBody{}
	c.must(200, "GET", "/api/roles", nil, &body)
	if len(body.Roles) != 3 || body.Roles[2].ID != first {
		t.Fatalf("second read %+v", body.Roles)
	}
	if r := c.do("GET", "/api/team", nil); r.Status != 404 {
		t.Fatalf("GET /api/team = %d", r.Status)
	}
}

func TestSaveRolesValidationAndYAML(t *testing.T) {
	h := newHarness(t)
	c, _ := h.signUp("a@example.com")
	var body RolesBody
	c.must(200, "GET", "/api/roles", nil, &body)
	if len(body.Roles) != 3 || body.Warnings == nil {
		t.Fatalf("defaults %+v", body)
	}
	bad := append([]store.Role{}, body.Roles...)
	bad = append(bad, store.Role{Tier: "member", Role: "QA!", Runtime: "claude", Count: 12, Look: store.Look{Outfit: "kimono"}})
	r := c.do("PUT", "/api/roles", map[string]any{"roles": bad})
	e := r.apiError(t)
	if r.Status != 422 || len(e.Fields) < 3 {
		t.Fatalf("invalid roles = %d %s", r.Status, r.Body)
	}
	devID := body.Roles[2].ID
	next := append([]store.Role{}, body.Roles...)
	next[2].Role, next[2].Title = "backend", "后端"
	next = append(next, store.Role{Tier: "member", Role: "review", Runtime: "claude", Gate: "review", Title: "审查"})
	c.must(200, "PUT", "/api/roles", map[string]any{"roles": next}, &body)
	if len(body.Roles) != 4 || body.Roles[2].ID != devID || body.Roles[2].Role != "backend" {
		t.Fatalf("saved %+v", body.Roles)
	}
	review := body.Roles[3]
	if review.ReadOnly == nil || !*review.ReadOnly || review.Look.Outfit == "" {
		t.Fatalf("review defaults %+v", review)
	}
	var y struct {
		YAML string `json:"yaml"`
	}
	c.must(200, "GET", "/api/roles/yaml", nil, &y)
	if !strings.Contains(y.YAML, "role: backend") || !strings.Contains(y.YAML, "gate: review") {
		t.Fatalf("yaml:\n%s", y.YAML)
	}
	edited := strings.Replace(y.YAML, "title: 后端", "title: 服务端", 1)
	c.must(200, "PUT", "/api/roles/yaml", map[string]string{"yaml": edited}, &body)
	if body.Roles[2].ID != devID || body.Roles[2].Title != "服务端" {
		t.Fatalf("yaml save lost id: %+v", body.Roles[2])
	}
	if r := c.do("PUT", "/api/roles/yaml", map[string]string{"yaml": "members: [}"}); r.Status != 422 {
		t.Fatalf("bad yaml = %d", r.Status)
	}
	var look store.Look
	c.must(200, "POST", "/api/roles/look/random", map[string]string{"role": "dev"}, &look)
	if look.Color == "" || look.Outfit == "" {
		t.Fatalf("look %+v", look)
	}
}

// TestBypassDefaultNoWarnings: roles default to bypass and save on a plain node without
// warnings; switching a saved role from ask back to bypass is audited once.
func TestBypassDefaultNoWarnings(t *testing.T) {
	h := newHarness(t)
	c, _ := h.signUp("a@example.com")
	var n createdNode
	c.must(201, "POST", "/api/nodes", map[string]string{"name": "box"}, &n)
	var body RolesBody
	c.must(200, "GET", "/api/roles", nil, &body)
	if body.Roles[2].PermissionMode != store.PermBypass {
		t.Fatalf("default mode %q", body.Roles[2].PermissionMode)
	}
	body.Roles[2].NodeIDs = []string{n.Node.ID}
	c.must(200, "PUT", "/api/roles", map[string]any{"roles": body.Roles}, &body)
	if len(body.Warnings) != 0 {
		t.Fatalf("warnings %+v", body.Warnings)
	}
	body.Roles[2].PermissionMode = store.PermAsk
	c.must(200, "PUT", "/api/roles", map[string]any{"roles": body.Roles}, &body)
	body.Roles[2].PermissionMode = store.PermBypass
	c.must(200, "PUT", "/api/roles", map[string]any{"roles": body.Roles}, &body)
	body.Roles[2].PermissionMode = ""
	c.must(200, "PUT", "/api/roles", map[string]any{"roles": body.Roles}, &body)
	if body.Roles[2].PermissionMode != store.PermBypass || len(body.Warnings) != 0 {
		t.Fatalf("empty mode %q %+v", body.Roles[2].PermissionMode, body.Warnings)
	}
	body.Roles[2].PermissionMode = "auto"
	if r := c.do("PUT", "/api/roles", map[string]any{"roles": body.Roles}); r.Status != 422 {
		t.Fatalf("auto accepted: %d", r.Status)
	}
	var count int
	_ = store.QueryRow(context.Background(), h.db.Pool, `SELECT count(*) FROM at_audit_log WHERE action = 'skip_permissions_on'`).Scan(&count)
	if count != 1 {
		t.Fatalf("audit rows %d", count)
	}
	c.must(204, "DELETE", "/api/nodes/"+n.Node.ID, nil, nil)
	c.must(200, "GET", "/api/roles", nil, &body)
	if len(body.Warnings) == 0 {
		t.Fatal("no warning for deleted node")
	}
}
