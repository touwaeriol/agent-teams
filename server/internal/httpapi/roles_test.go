package httpapi

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/touwaeriol/agent-teams/server/internal/platform"
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

type failingRestaff struct{ platform.NoHooks }

func (failingRestaff) RolesSaved(context.Context, string) error { return errors.New("re-staff failed") }

// TestSaveRolesRestaffFailure: the roles are saved when re-staffing the projects fails
// afterwards; the save reports success.
func TestSaveRolesRestaffFailure(t *testing.T) {
	h := newHarness(t)
	h.server.hooks = failingRestaff{}
	c, _ := h.signUp("a@example.com")
	var body RolesBody
	c.must(200, "GET", "/api/roles", nil, &body)
	body.Roles[2].Title = "开发者"
	c.must(200, "PUT", "/api/roles", map[string]any{"roles": body.Roles}, &body)
	if body.Roles[2].Title != "开发者" {
		t.Fatalf("saved %+v", body.Roles[2])
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

// TestSaveRolesModelValidation: role.model is typed by hand: empty follows the project and the
// built-in default, any other id only needs a valid format, on both PUT /api/roles and PUT
// /api/roles/yaml (there is no model list to check it against).
func TestSaveRolesModelValidation(t *testing.T) {
	h := newHarness(t)
	c, _ := h.signUp("a@example.com")
	var body RolesBody
	c.must(200, "GET", "/api/roles", nil, &body)
	modelError := func(r response) FieldError {
		t.Helper()
		if r.Status != 422 {
			t.Fatalf("status %d %s", r.Status, r.Body)
		}
		for _, f := range r.apiError(t).Fields {
			if strings.HasSuffix(f.Path, ".model") {
				return f
			}
		}
		t.Fatalf("no model error: %s", r.Body)
		return FieldError{}
	}
	withModel := func(i int, model string) []store.Role {
		next := append([]store.Role{}, body.Roles...)
		next[i].Model = model
		return next
	}
	if f := modelError(c.do("PUT", "/api/roles", map[string]any{"roles": withModel(2, "bad model")})); f.Path != "roles[2].model" || !strings.Contains(f.Message, "模型 ID 只能用") {
		t.Fatalf("bad format %+v", f)
	}
	// Empty and any well-formed id save (no list; roles.TestModelValidation covers Codex ids).
	c.must(200, "PUT", "/api/roles", map[string]any{"roles": withModel(2, "")}, &body)
	c.must(200, "PUT", "/api/roles", map[string]any{"roles": withModel(2, " claude-temp ")}, &body)
	if body.Roles[2].Model != "claude-temp" {
		t.Fatalf("saved model %q", body.Roles[2].Model)
	}
	var y struct {
		YAML string `json:"yaml"`
	}
	c.must(200, "GET", "/api/roles/yaml", nil, &y)
	if !strings.Contains(y.YAML, "model: claude-temp") {
		t.Fatalf("yaml:\n%s", y.YAML)
	}
	r := c.do("PUT", "/api/roles/yaml", map[string]string{"yaml": strings.Replace(y.YAML, "model: claude-temp", "model: bad model", 1)})
	if f := modelError(r); f.Path != "roles[2].model" {
		t.Fatalf("yaml model %+v", f)
	}
}

// TestSaveRolesKeepsFixedModels: the coordinator and the lead pick their model in their role like
// members (they have no /model): the model is saved, stored and returned.
func TestSaveRolesKeepsFixedModels(t *testing.T) {
	h := newHarness(t)
	c, _ := h.signUp("a@example.com")
	var body RolesBody
	c.must(200, "GET", "/api/roles", nil, &body)
	next := append([]store.Role{}, body.Roles...)
	next[0].Model, next[1].Model, next[2].Model = "claude-coord", "claude-lead", "claude-temp"
	c.must(200, "PUT", "/api/roles", map[string]any{"roles": next}, &body)
	if body.Roles[0].Tier != "coordinator" || body.Roles[1].Tier != "lead" || body.Roles[2].Tier != "member" {
		t.Fatalf("order %+v", body.Roles)
	}
	if body.Roles[0].Model != "claude-coord" || body.Roles[1].Model != "claude-lead" || body.Roles[2].Model != "claude-temp" {
		t.Fatalf("saved models %q %q %q", body.Roles[0].Model, body.Roles[1].Model, body.Roles[2].Model)
	}
	var stored string
	if err := store.QueryRow(context.Background(), h.db.Pool, `SELECT string_agg(tier || ':' || model, ' ' ORDER BY sort) FROM at_roles`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != "coordinator:claude-coord lead:claude-lead member:claude-temp" {
		t.Fatalf("stored %q", stored)
	}
	c.must(200, "GET", "/api/roles", nil, &body)
	if body.Roles[0].Model != "claude-coord" || body.Roles[1].Model != "claude-lead" {
		t.Fatalf("GET models %q %q", body.Roles[0].Model, body.Roles[1].Model)
	}
}
