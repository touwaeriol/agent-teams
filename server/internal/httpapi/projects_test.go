package httpapi

import (
	"context"
	"strings"
	"testing"

	"github.com/touwaeriol/agent-teams/server/internal/auth"
)

func TestProjectsCRUD(t *testing.T) {
	h := newHarness(t)
	c, _ := h.signUp("a@example.com")
	other, _ := h.signUp("b@example.com")
	var p ProjectView
	c.must(201, "POST", "/api/projects", map[string]any{"name": " Alpha ", "description": "first"}, &p)
	if p.Name != "Alpha" || p.WorkMode != "worktree" || p.MergePolicy != "pr" || len(p.Repos) != 0 {
		t.Fatalf("created %+v", p)
	}
	if !p.Delivery.Gates.Review || p.Delivery.MergeMethod != "squash" || p.Delivery.CI.TimeoutS != 3600 || !p.Delivery.NotifyOnAsk {
		t.Fatalf("delivery defaults %+v", p.Delivery)
	}
	if r := c.do("POST", "/api/projects", map[string]any{"name": "Alpha"}); r.Status != 409 {
		t.Fatalf("duplicate = %d", r.Status)
	}
	if r := c.do("POST", "/api/projects", map[string]any{"name": "  "}); r.Status != 422 {
		t.Fatalf("empty name = %d", r.Status)
	}
	c.must(201, "POST", "/api/projects", map[string]any{"name": "Beta"}, nil)
	var updated ProjectView
	c.must(200, "PATCH", "/api/projects/"+p.ID, map[string]any{"pinned": true, "name": "Alpha 2", "work_mode": "clone",
		"delivery": map[string]any{"merge_method": "rebase", "gates": map[string]any{"decision": true}}}, &updated)
	if !updated.Pinned || updated.Name != "Alpha 2" || updated.WorkMode != "clone" {
		t.Fatalf("updated %+v", updated)
	}
	if updated.Delivery.MergeMethod != "rebase" || !updated.Delivery.Gates.Decision || !updated.Delivery.Gates.Review {
		t.Fatalf("delivery patch %+v", updated.Delivery)
	}
	var list []ProjectView
	c.must(200, "GET", "/api/projects", nil, &list)
	if len(list) != 2 || list[0].ID != p.ID {
		t.Fatalf("pinned first: %+v", list)
	}
	c.must(200, "GET", "/api/projects?q=bet", nil, &list)
	if len(list) != 1 || list[0].Name != "Beta" {
		t.Fatalf("search %+v", list)
	}
	if r := other.do("GET", "/api/projects/"+p.ID, nil); r.Status != 404 {
		t.Fatalf("other user's project = %d", r.Status)
	}
	if r := other.do("DELETE", "/api/projects/"+p.ID, nil); r.Status != 404 {
		t.Fatalf("other user delete = %d", r.Status)
	}
	if r := c.do("PATCH", "/api/projects/"+p.ID, map[string]any{"office_layout": "nope"}); r.Status != 422 {
		t.Fatalf("unknown layout = %d", r.Status)
	}
	c.must(204, "DELETE", "/api/projects/"+p.ID, nil, nil)
	if r := c.do("GET", "/api/projects/"+p.ID, nil); r.Status != 404 {
		t.Fatalf("deleted = %d", r.Status)
	}
	if !h.events.has("user:") {
		t.Fatal("no project events published")
	}
}

func TestProjectWithRepoAndConnection(t *testing.T) {
	h := newHarness(t)
	c, _ := h.signUp("a@example.com")
	var conn GitConnectionView
	c.must(201, "POST", "/api/git/connections", map[string]string{"token": "ghp_good"}, &conn)
	if conn.Login != "octo" || conn.Name != "GitHub · octo" || conn.Kind != "token" {
		t.Fatalf("connection %+v", conn)
	}
	if r := c.do("POST", "/api/projects", map[string]any{"name": "R", "repos": []map[string]any{{"repo": "o/r"}}}); r.Status != 422 {
		t.Fatalf("repo without connection = %d", r.Status)
	}
	var p ProjectView
	c.must(201, "POST", "/api/projects", map[string]any{"name": "R", "repos": []map[string]any{
		{"repo": "https://github.com/o/r.git", "git_connection_id": conn.ID}}}, &p)
	if len(p.Repos) != 1 || p.Repos[0].Repo != "o/r" || *p.Repos[0].GitConnectionID != conn.ID {
		t.Fatalf("repos %+v", p.Repos)
	}
	if r := c.do("PATCH", "/api/projects/"+p.ID, map[string]any{"repos": []map[string]any{{"repo": "o/missing", "git_connection_id": conn.ID}}}); r.Status != 422 {
		t.Fatalf("missing repo = %d %s", r.Status, r.Body)
	}
	if r := c.do("PATCH", "/api/projects/"+p.ID, map[string]any{"repos": []map[string]any{
		{"repo": "o/r", "git_connection_id": conn.ID}, {"repo": "O/R", "git_connection_id": conn.ID}}}); r.Status != 422 {
		t.Fatalf("duplicate repo = %d %s", r.Status, r.Body)
	}
	c.must(200, "PATCH", "/api/projects/"+p.ID, map[string]any{"name": "R2", "repos": []map[string]any{
		{"repo": "o/web", "git_connection_id": conn.ID}, {"repo": "o/r", "git_connection_id": conn.ID}}}, &p)
	if p.Name != "R2" || len(p.Repos) != 2 || p.Repos[0].Repo != "o/web" || p.Repos[1].Repo != "o/r" {
		t.Fatalf("reordered repos %+v", p)
	}
	c.must(200, "PATCH", "/api/projects/"+p.ID, map[string]any{"description": "kept"}, &p)
	if len(p.Repos) != 2 {
		t.Fatalf("PATCH without repos changed them: %+v", p.Repos)
	}
	c.must(200, "PATCH", "/api/projects/"+p.ID, map[string]any{"repos": []map[string]any{}}, &p)
	if len(p.Repos) != 0 {
		t.Fatalf("unbind all %+v", p.Repos)
	}
}

// TestAutoApproveAnyNode: "全部放行" no longer needs an isolated node.
func TestAutoApproveAnyNode(t *testing.T) {
	h := newHarness(t)
	c, _ := h.signUp("a@example.com")
	var p ProjectView
	c.must(201, "POST", "/api/projects", map[string]any{"name": "A"}, &p)
	c.must(201, "POST", "/api/nodes", map[string]string{"name": "box"}, nil)
	c.must(200, "PATCH", "/api/projects/"+p.ID, map[string]any{"auto_approve": true}, &p)
	if !p.AutoApprove {
		t.Fatal("auto_approve not saved")
	}
}

// TestMemory: memory files are the owner's only; a topic file keeps its line in MEMORY.md,
// deleting it drops the line; MEMORY.md cannot be deleted; bad names and missing descriptions are 422.
func TestMemory(t *testing.T) {
	h := newHarness(t)
	c, _ := h.signUp("a@example.com")
	other, _ := h.signUp("b@example.com")
	var p ProjectView
	c.must(201, "POST", "/api/projects", map[string]any{"name": "A"}, &p)
	base := "/api/projects/" + p.ID + "/memory"
	var f struct {
		Name      string `json:"name"`
		Content   string `json:"content"`
		UpdatedBy string `json:"updated_by"`
	}
	c.must(200, "PUT", base+"/testing", map[string]string{"description": "怎么跑测试", "content": "make test"}, &f)
	if f.Name != "testing.md" || f.Content != "make test" {
		t.Fatalf("file %+v", f)
	}
	c.must(200, "GET", base+"/MEMORY.md", nil, &f)
	if !strings.Contains(f.Content, "- [testing.md](testing.md) — 怎么跑测试") {
		t.Fatalf("index %q", f.Content)
	}
	c.must(200, "PUT", base+"/MEMORY.md", map[string]string{"content": f.Content + "- 用 pnpm\n"}, &f)
	for _, path := range []string{base, base + "/MEMORY.md"} {
		if r := other.do("GET", path, nil); r.Status != 404 {
			t.Fatalf("other user %s = %d", path, r.Status)
		}
	}
	if r := other.do("PUT", base+"/x.md", map[string]string{"description": "x", "content": "x"}); r.Status != 404 {
		t.Fatalf("other user put = %d", r.Status)
	}
	var list []struct {
		Name string
		Size int
	}
	c.must(200, "GET", base, nil, &list)
	if len(list) != 2 || list[0].Name != "MEMORY.md" || list[1].Name != "testing.md" || list[1].Size != 9 {
		t.Fatalf("list %+v", list)
	}
	for _, bad := range []struct{ name, desc string }{{"bad$name", "d"}, {"notes", ""}} {
		if r := c.do("PUT", base+"/"+bad.name, map[string]string{"description": bad.desc, "content": "x"}); r.Status != 422 {
			t.Fatalf("put %s = %d", bad.name, r.Status)
		}
	}
	if r := c.do("DELETE", base+"/MEMORY.md", nil); r.Status != 422 {
		t.Fatalf("delete MEMORY.md = %d", r.Status)
	}
	c.must(204, "DELETE", base+"/testing.md", nil, nil)
	c.must(200, "GET", base+"/MEMORY.md", nil, &f)
	if strings.Contains(f.Content, "testing.md") || !strings.Contains(f.Content, "- 用 pnpm") || f.UpdatedBy != "user" {
		t.Fatalf("index after delete %+v", f)
	}
	if r := c.do("GET", base+"/testing.md", nil); r.Status != 404 {
		t.Fatalf("deleted file = %d", r.Status)
	}
	if !h.events.has("project:" + p.ID + ":memory") {
		t.Fatal("memory event missing")
	}
}

func TestPATIsSealedWithV1Format(t *testing.T) {
	h := newHarness(t)
	c, u := h.signUp("a@example.com")
	var conn GitConnectionView
	c.must(201, "POST", "/api/git/connections", map[string]string{"token": "ghp_good", "name": "mine"}, &conn)
	sealed, err := h.db.ConnectionSecret(context.Background(), u.ID, conn.ID)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := auth.NewSecretBox(testSecret).Open(sealed)
	if err != nil || plain != "ghp_good" {
		t.Fatalf("sealed value %q %v", plain, err)
	}
	if r := c.do("POST", "/api/git/connections", map[string]string{"token": "bad"}); r.Status != 422 {
		t.Fatalf("bad token = %d", r.Status)
	}
	if r := c.do("POST", "/api/git/connections", map[string]string{"token": "ghp_good", "name": "mine"}); r.Status != 409 {
		t.Fatalf("duplicate name = %d", r.Status)
	}
}
