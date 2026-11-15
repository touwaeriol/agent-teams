package httpapi

import (
	"context"
	"testing"

	"github.com/touwaeriol/agent-teams/server/internal/auth"
)

func TestProjectsCRUD(t *testing.T) {
	h := newHarness(t)
	c, _ := h.signUp("a@example.com")
	other, _ := h.signUp("b@example.com")
	var p ProjectView
	c.must(201, "POST", "/api/projects", map[string]any{"name": " Alpha ", "description": "first"}, &p)
	if p.Name != "Alpha" || p.WorkMode != "worktree" || !p.RequireAcceptance || len(p.Repos) != 0 {
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

func TestAutoApproveNeedsIsolation(t *testing.T) {
	h := newHarness(t)
	c, _ := h.signUp("a@example.com")
	var p ProjectView
	c.must(201, "POST", "/api/projects", map[string]any{"name": "A"}, &p)
	if r := c.do("PATCH", "/api/projects/"+p.ID, map[string]any{"auto_approve": true}); r.Status != 409 || r.apiError(t).Code != "isolation_required" {
		t.Fatalf("no nodes = %d %s", r.Status, r.Body)
	}
	var created struct {
		Node NodeView `json:"node"`
	}
	c.must(201, "POST", "/api/nodes", map[string]string{"name": "box"}, &created)
	c.must(200, "PATCH", "/api/nodes/"+created.Node.ID, map[string]any{"isolated": true}, nil)
	c.must(200, "PATCH", "/api/projects/"+p.ID, map[string]any{"auto_approve": true}, &p)
	if !p.AutoApprove {
		t.Fatal("auto_approve not saved")
	}
}

func TestMemory(t *testing.T) {
	h := newHarness(t)
	c, _ := h.signUp("a@example.com")
	other, _ := h.signUp("b@example.com")
	var p ProjectView
	c.must(201, "POST", "/api/projects", map[string]any{"name": "A"}, &p)
	var item struct {
		ID     string `json:"id"`
		Source string `json:"source"`
		Pinned bool   `json:"pinned"`
	}
	c.must(201, "POST", "/api/projects/"+p.ID+"/memory", map[string]string{"text": "use pnpm"}, &item)
	if item.Source != "user" {
		t.Fatalf("source %s", item.Source)
	}
	c.must(200, "PATCH", "/api/projects/"+p.ID+"/memory/"+item.ID, map[string]any{"pinned": true}, &item)
	if !item.Pinned {
		t.Fatal("not pinned")
	}
	if r := other.do("GET", "/api/projects/"+p.ID+"/memory", nil); r.Status != 404 {
		t.Fatalf("other user = %d", r.Status)
	}
	var items []map[string]any
	c.must(200, "GET", "/api/projects/"+p.ID+"/memory", nil, &items)
	if len(items) != 1 {
		t.Fatalf("items %v", items)
	}
	c.must(204, "DELETE", "/api/projects/"+p.ID+"/memory/"+item.ID, nil, nil)
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
