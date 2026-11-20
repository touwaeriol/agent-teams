package httpapi

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/touwaeriol/agent-teams/server/internal/gitremote/gittest"
	"github.com/touwaeriol/agent-teams/server/internal/sshdial"
)

// TestGitReposAPI: credentials and standalone repositories end to end against an HTTPS upstream:
// validation, sealing (no secret in any response), test connection (draft and saved, read vs
// write), other users see nothing, deletion guarded while in use, projects pick the repository.
func TestGitReposAPI(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if err := h.db.SaveSetting(ctx, sshdial.SettingKey, sshdial.AllowedSetting{CIDRs: []string{"127.0.0.1/32"}}); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	gittest.Bare(t, root, "acme/web")
	up := gittest.HTTPS(t, root, "oauth2", "tok-secret-1")
	h.server.git = up.Runner
	c, _ := h.signUp("a@example.com")
	other, _ := h.signUp("b@example.com")
	url := up.URL + "/acme/web.git"

	for body, code := range map[string]string{
		`{"url":"http://gitlab.com/a/b"}`:                "validation",
		`{"url":"https://u:p@gitlab.com/a/b"}`:           "validation",
		`{"url":"https://gitlab.com/onlyone"}`:           "validation",
		`{"url":"https://169.254.169.254/a/b"}`:          "address_blocked",
		`{"url":"https://gitlab.com/a/b","web_url":"x"}`: "validation",
	} {
		if r := c.do("POST", "/api/git/repos", json.RawMessage(body)); r.Status != 422 || r.apiError(t).Code != code {
			t.Errorf("%s = %d %s", body, r.Status, r.Body)
		}
	}

	var bad GitTestResult
	c.must(200, "POST", "/api/git/repos/test", map[string]any{"url": url, "credential": map[string]any{"token": "wrong"}}, &bad)
	if bad.OK || bad.Error != "auth_failed" {
		t.Fatalf("draft with a wrong token %+v", bad)
	}
	var good GitTestResult
	c.must(200, "POST", "/api/git/repos/test", map[string]any{"url": url, "credential": map[string]any{"username": "oauth2", "token": "tok-secret-1"}}, &good)
	if !good.OK || good.Access != "write" || good.DefaultBranch != "main" || good.Branches != 1 {
		t.Fatalf("draft test %+v", good)
	}

	var repo GitRepoView
	r := c.must(201, "POST", "/api/git/repos", map[string]any{"url": url, "name": "web",
		"credential": map[string]any{"name": "local", "username": "oauth2", "token": "tok-secret-1"}}, &repo)
	noSecrets(t, r, "tok-secret-1")
	if repo.CredentialID == nil || repo.Access != "write" || repo.DefaultBranch != "main" || repo.Transport != "https" || !strings.HasSuffix(repo.Slug, "/web") {
		t.Fatalf("created %+v", repo)
	}
	if r := c.do("POST", "/api/git/repos", map[string]any{"url": url}); r.Status != 409 {
		t.Fatalf("duplicate repo = %d %s", r.Status, r.Body)
	}
	var creds []GitCredentialView
	r = c.must(200, "GET", "/api/git/credentials", nil, &creds)
	noSecrets(t, r, "tok-secret-1")
	if len(creds) != 1 || creds[0].Repos != 1 || creds[0].Kind != "https_token" {
		t.Fatalf("credentials %+v", creds)
	}
	if r := other.do("GET", "/api/git/repos/"+repo.ID, nil); r.Status != 404 {
		t.Fatalf("other user = %d", r.Status)
	}
	if r := other.do("POST", "/api/git/repos", map[string]any{"url": up.URL + "/x/y.git", "credential_id": *repo.CredentialID}); r.Status != 404 {
		t.Fatalf("other user's credential = %d %s", r.Status, r.Body)
	}

	// A token that can read but not push: the upstream answers receive-pack only to tok-secret-1.
	var replaced GitCredentialView
	c.must(200, "PATCH", "/api/git/credentials/"+*repo.CredentialID, map[string]any{"secret": map[string]any{"token": "nope"}}, &replaced)
	var res GitTestResult
	c.must(200, "POST", "/api/git/repos/"+repo.ID+"/test", nil, &res)
	if res.OK || res.Error != "auth_failed" {
		t.Fatalf("test with a replaced bad token %+v", res)
	}
	c.must(200, "PATCH", "/api/git/credentials/"+*repo.CredentialID, map[string]any{"secret": map[string]any{"token": "tok-secret-1"}}, nil)
	c.must(200, "POST", "/api/git/repos/"+repo.ID+"/test", nil, &res)
	if !res.OK || res.Access != "write" {
		t.Fatalf("test %+v", res)
	}
	if r := c.do("PATCH", "/api/git/credentials/"+*repo.CredentialID, map[string]any{"username": "other"}); r.Status != 422 || r.apiError(t).Code != "credential_required" {
		t.Fatalf("username change without value = %d %s", r.Status, r.Body)
	}
	var branches []string
	c.must(200, "GET", "/api/git/repos/"+repo.ID+"/branches", nil, &branches)
	if len(branches) != 1 || branches[0] != "main" {
		t.Fatalf("branches %v", branches)
	}

	// Projects pick it; then it cannot be deleted.
	var prj ProjectView
	c.must(201, "POST", "/api/projects", map[string]any{"name": "p", "repos": []map[string]any{{"repo": repo.Slug, "git_repo_id": repo.ID}}}, &prj)
	if len(prj.Repos) != 1 || prj.Repos[0].GitRepoID == nil || *prj.Repos[0].GitRepoID != repo.ID {
		t.Fatalf("project repos %+v", prj.Repos)
	}
	if r := other.do("POST", "/api/projects", map[string]any{"name": "q", "repos": []map[string]any{{"repo": repo.Slug, "git_repo_id": repo.ID}}}); r.Status < 400 {
		t.Fatalf("other user's repo in a project = %d", r.Status)
	}
	if r := c.do("DELETE", "/api/git/repos/"+repo.ID, nil); r.Status != 409 {
		t.Fatalf("delete in use = %d %s", r.Status, r.Body)
	}
	if r := c.do("DELETE", "/api/git/credentials/"+*repo.CredentialID, nil); r.Status != 409 {
		t.Fatalf("delete credential in use = %d", r.Status)
	}
	c.must(200, "PATCH", "/api/projects/"+prj.ID, map[string]any{"repos": []any{}}, nil)
	c.must(204, "DELETE", "/api/git/repos/"+repo.ID, nil, nil)
	c.must(204, "DELETE", "/api/git/credentials/"+*repo.CredentialID, nil, nil)

	// A generated SSH key: only the public key is returned.
	var key GitCredentialView
	r = c.must(201, "POST", "/api/git/credentials", map[string]any{"name": "deploy", "host": "git.example.com:2222", "kind": "ssh_key", "generate": true}, &key)
	noSecrets(t, r)
	if !key.Generated || !strings.HasPrefix(key.PublicKey, "ssh-ed25519 ") || key.Username != "git" || !strings.HasPrefix(key.KeyFingerprint, "SHA256:") {
		t.Fatalf("generated %+v", key)
	}
	if r := c.do("POST", "/api/git/repos", map[string]any{"url": "ssh://git@gitlab.com/a/b.git", "credential_id": key.ID}); r.Status != 422 || r.apiError(t).Code != "host_mismatch" {
		t.Fatalf("credential of another host = %d %s", r.Status, r.Body)
	}
}
