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

// TestSaveRolesModelValidation: role.model must be in the user's model list with the role's
// runtime (empty follows the defaults), on both PUT /api/roles and PUT /api/roles/yaml; a value
// the role already had passes even after it left the list.
func TestSaveRolesModelValidation(t *testing.T) {
	h := newHarness(t)
	c, _ := h.signUp("a@example.com")
	mine := []map[string]any{{"runtime": "claude", "model": "claude-opus-5-5", "default": true},
		{"runtime": "claude", "model": "claude-temp"}, {"runtime": "codex", "model": "gpt-x"}}
	c.must(200, "PUT", "/api/models/mine", map[string]any{"models": mine}, nil)
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
	// Not in the list (a member), runtime mismatch (a Codex member with a Claude id).
	if f := modelError(c.do("PUT", "/api/roles", map[string]any{"roles": withModel(2, "claude-nope")})); f.Path != "roles[2].model" || f.Message != "模型不在你的模型列表里：claude-nope" {
		t.Fatalf("not in list %+v", f)
	}
	codex := append(append([]store.Role{}, body.Roles...), store.Role{Tier: "member", Role: "cx", Runtime: "codex", Model: "claude-opus-5-5"})
	if f := modelError(c.do("PUT", "/api/roles", map[string]any{"roles": codex})); f.Path != "roles[3].model" || f.Message != "Codex 岗位不能用 Claude 模型：claude-opus-5-5" {
		t.Fatalf("runtime mismatch %+v", f)
	}
	// Empty and listed values save.
	c.must(200, "PUT", "/api/roles", map[string]any{"roles": withModel(2, "")}, &body)
	c.must(200, "PUT", "/api/roles", map[string]any{"roles": withModel(2, "claude-temp")}, &body)
	if body.Roles[2].Model != "claude-temp" {
		t.Fatalf("saved model %q", body.Roles[2].Model)
	}
	// The model leaves the list: the unchanged role still saves (JSON and YAML) ...
	c.must(200, "PUT", "/api/models/mine", map[string]any{"models": mine[:1]}, nil)
	next := append([]store.Role{}, body.Roles...)
	next[2].Title = "后端"
	c.must(200, "PUT", "/api/roles", map[string]any{"roles": next}, &body)
	var y struct {
		YAML string `json:"yaml"`
	}
	c.must(200, "GET", "/api/roles/yaml", nil, &y)
	if !strings.Contains(y.YAML, "model: claude-temp") {
		t.Fatalf("yaml:\n%s", y.YAML)
	}
	c.must(200, "PUT", "/api/roles/yaml", map[string]string{"yaml": strings.Replace(y.YAML, "title: 后端", "title: 服务端", 1)}, &body)
	if body.Roles[2].Title != "服务端" || body.Roles[2].Model != "claude-temp" {
		t.Fatalf("yaml save %+v", body.Roles[2])
	}
	// ... but the YAML path checks new values like the JSON one.
	c.must(200, "GET", "/api/roles/yaml", nil, &y)
	r := c.do("PUT", "/api/roles/yaml", map[string]string{"yaml": strings.Replace(y.YAML, "model: claude-temp", "model: claude-nope", 1)})
	if f := modelError(r); f.Path != "roles[2].model" {
		t.Fatalf("yaml model %+v", f)
	}
	// Setting the stale value on another role is a change and is rejected.
	another := append(append([]store.Role{}, body.Roles...), store.Role{Tier: "member", Role: "qa", Runtime: "claude", Model: "claude-temp"})
	if f := modelError(c.do("PUT", "/api/roles", map[string]any{"roles": another})); f.Path != "roles[3].model" {
		t.Fatalf("stale on another role %+v", f)
	}
}

// TestSaveRolesDropsFixedModels: the coordinator and the lead always run the default model, so a
// model sent for them (an older client; listed or not, any runtime) is dropped: the save succeeds,
// the rows store ” and GET returns ”. Members keep theirs.
func TestSaveRolesDropsFixedModels(t *testing.T) {
	h := newHarness(t)
	c, _ := h.signUp("a@example.com")
	c.must(200, "PUT", "/api/models/mine", map[string]any{"models": []map[string]any{
		{"runtime": "claude", "model": "claude-opus-5-5", "default": true}, {"runtime": "claude", "model": "claude-temp"}}}, nil)
	var body RolesBody
	c.must(200, "GET", "/api/roles", nil, &body)
	next := append([]store.Role{}, body.Roles...)
	next[0].Model, next[1].Model, next[2].Model = "claude-temp", "gpt-not-listed", "claude-temp"
	c.must(200, "PUT", "/api/roles", map[string]any{"roles": next}, &body)
	if body.Roles[0].Tier != "coordinator" || body.Roles[1].Tier != "lead" || body.Roles[2].Tier != "member" {
		t.Fatalf("order %+v", body.Roles)
	}
	if body.Roles[0].Model != "" || body.Roles[1].Model != "" || body.Roles[2].Model != "claude-temp" {
		t.Fatalf("saved models %q %q %q", body.Roles[0].Model, body.Roles[1].Model, body.Roles[2].Model)
	}
	var stored string
	if err := store.QueryRow(context.Background(), h.db.Pool, `SELECT string_agg(tier || ':' || model, ' ' ORDER BY sort) FROM at_roles`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != "coordinator: lead: member:claude-temp" {
		t.Fatalf("stored %q", stored)
	}
	c.must(200, "GET", "/api/roles", nil, &body)
	if body.Roles[0].Model != "" || body.Roles[1].Model != "" {
		t.Fatalf("GET models %q %q", body.Roles[0].Model, body.Roles[1].Model)
	}
}
