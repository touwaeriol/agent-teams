package engine_test

import (
	"testing"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw/nodetest"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// TestThreadStartsFromRepoBase: a thread's checkouts start from each repository's base branch
// recorded in the project (o/web: dev, chosen when it was added; o/r: its default branch), and
// a repository bound before base branches were stored gets its default branch recorded.
func TestThreadStartsFromRepoBase(t *testing.T) {
	_, ghSrv := newFakeGitHub(t)
	w := newWorld(t, ghSrv)
	b := w.signUp("base@example.com")
	var node nodeCreated
	b.must(201, "POST", "/api/nodes", map[string]string{"name": "fake"}, &node)
	b.must(200, "GET", "/api/roles", nil, nil)
	var conn struct{ ID string }
	b.must(201, "POST", "/api/git/connections", map[string]string{"name": "gh", "token": "ghp_good"}, &conn)
	n := w.connect(node.Token, nodetest.Options{})
	var project struct {
		ID    string
		Repos []struct {
			Repo       string
			BaseBranch string `json:"base_branch"`
		}
	}
	b.must(201, "POST", "/api/projects", map[string]any{"name": "bases", "repos": []map[string]any{
		{"repo": "o/r", "git_connection_id": conn.ID}, {"repo": "o/web", "git_connection_id": conn.ID, "base_branch": "dev"}}}, &project)
	if project.Repos[0].BaseBranch != "main" || project.Repos[1].BaseBranch != "dev" {
		t.Fatalf("bases %+v", project.Repos)
	}

	var thread core.ThreadView
	b.must(201, "POST", "/api/projects/"+project.ID+"/threads", map[string]any{"title": "Login", "brief": "登录页"}, &thread)
	expectOpen(t, w, n, "lead-1")
	preps := prepares(t, w, n, 2)
	if *preps["o/r"].Base != "main" || *preps["o/web"].Base != "dev" {
		t.Fatalf("starts o/r=%s o/web=%s", *preps["o/r"].Base, *preps["o/web"].Base)
	}

	if _, err := store.Exec(w.ctx, w.e.Core.Pool, `UPDATE at_project_repos SET base_branch = '' WHERE project_id = $1 AND repo = 'o/r'`, project.ID); err != nil {
		t.Fatal(err)
	}
	w.e.Orch.WarmProject(w.ctx, project.ID)
	var base string
	if err := store.QueryRow(w.ctx, w.e.Core.Pool, `SELECT base_branch FROM at_project_repos WHERE project_id = $1 AND repo = 'o/r'`, project.ID).Scan(&base); err != nil || base != "main" {
		t.Fatalf("backfilled %q %v", base, err)
	}
}
