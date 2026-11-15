package httpapi

import (
	"context"
	"strings"
	"testing"

	"github.com/touwaeriol/agent-teams/server/internal/roles"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

func TestRolesSeededFromV1Team(t *testing.T) {
	h := newHarness(t)
	c, u := h.signUp("a@example.com")
	var n createdNode
	c.must(201, "POST", "/api/nodes", map[string]string{"name": "rc-erio"}, &n)
	team := "leader: lead\nemployees:\n  - {name: lead, type: claude_code, node: rc-erio, instructions: 带队}\n  - {name: engineer, type: claude_code, node: rc-erio, workdir: /x}\n"
	if _, err := h.db.Pool.Exec(context.Background(), `INSERT INTO at_teams (user_id, config) VALUES ($1, $2)`, u.ID, team); err != nil {
		t.Fatal(err)
	}
	var body RolesBody
	c.must(200, "GET", "/api/roles", nil, &body)
	if len(body.Roles) != 3 || body.Roles[0].Tier != "coordinator" || body.Roles[2].Role != "engineer" {
		t.Fatalf("roles %+v", body.Roles)
	}
	if body.Roles[1].NodeIDs[0] != n.Node.ID || body.Roles[0].Instructions != "带队" || body.Roles[0].Look.Outfit != "hoodie" {
		t.Fatalf("converted %+v", body.Roles[:2])
	}
	if len(body.Report) == 0 || !strings.Contains(body.Report[0], "workdir") {
		t.Fatalf("report %v", body.Report)
	}
	// Second read does not convert again.
	body = RolesBody{}
	c.must(200, "GET", "/api/roles", nil, &body)
	if len(body.Report) != 0 || len(body.Roles) != 3 {
		t.Fatalf("second read %+v", body)
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
	var team TeamV1
	c.must(200, "GET", "/api/team", nil, &team)
	if !team.Saved || team.Team["coordinator"] == nil {
		t.Fatalf("team %+v", team)
	}
	var look store.Look
	c.must(200, "POST", "/api/roles/look/random", map[string]string{"role": "dev"}, &look)
	if look.Color == "" || look.Outfit == "" {
		t.Fatalf("look %+v", look)
	}
}

// TestBypassOnPlainNodeWarns: a bypass role may be saved for a plain node (it runs in auto
// there) and the page gets the explanation as a warning; switching it on is audited once.
func TestBypassOnPlainNodeWarns(t *testing.T) {
	h := newHarness(t)
	c, _ := h.signUp("a@example.com")
	var n createdNode
	c.must(201, "POST", "/api/nodes", map[string]string{"name": "box"}, &n)
	var body RolesBody
	c.must(200, "GET", "/api/roles", nil, &body)
	if body.Roles[2].PermissionMode != store.PermAuto {
		t.Fatalf("default mode %q", body.Roles[2].PermissionMode)
	}
	body.Roles[2].PermissionMode = store.PermBypass
	body.Roles[2].NodeIDs = []string{n.Node.ID}
	c.must(200, "PUT", "/api/roles", map[string]any{"roles": body.Roles}, &body)
	if len(body.Warnings) != 1 || body.Warnings[0].Message != roles.BypassFallback {
		t.Fatalf("warnings %+v", body.Warnings)
	}
	c.must(200, "PATCH", "/api/nodes/"+n.Node.ID, map[string]any{"isolated": true}, nil)
	c.must(200, "PUT", "/api/roles", map[string]any{"roles": body.Roles}, &body)
	if len(body.Warnings) != 0 {
		t.Fatalf("warnings on isolated %+v", body.Warnings)
	}
	var count int
	_ = h.db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM at_audit_log WHERE action = 'skip_permissions_on'`).Scan(&count)
	if count != 1 {
		t.Fatalf("audit rows %d", count)
	}
	c.must(204, "DELETE", "/api/nodes/"+n.Node.ID, nil, nil)
	c.must(200, "GET", "/api/roles", nil, &body)
	if len(body.Warnings) == 0 {
		t.Fatal("no warning for deleted node")
	}
}
