// Package ghops is what the orchestration engines do on GitHub for a project:
// pick the project's credential (App installation token narrowed to the repo
// and a permission set, or the sealed PAT) and call the REST endpoints for
// refs, merges, pull requests, check runs and file contents.
package ghops

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/touwaeriol/agent-teams/server/internal/auth"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/github"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// Scope is what a token is for (architecture.md §10.4).
type Scope int

// Scopes.
const (
	ScopeFull      Scope = iota // console-side merge queue, PRs, merges
	ScopeRead                   // employee sessions, prepare/status jobs
	ScopePush                   // push jobs: contents write
	ScopePushFlows              // push jobs when workflow edits are allowed
)

// ErrNoRepo means the project has no GitHub repository or connection.
var ErrNoRepo = errors.New("项目没有关联 GitHub 仓库")

// ErrPATNarrow means a PAT cannot be narrowed for employee sessions.
var ErrPATNarrow = errors.New("个人令牌无法缩小权限，员工会话不发令牌（建议改用 GitHub App 连接）")

// ErrNoChecks means the App installation lacks checks:read.
var ErrNoChecks = errors.New("GitHub App 没有 checks:read 权限")

// GitHub performs project-scoped GitHub calls.
type GitHub struct {
	Client *github.Client
	Tokens *github.Tokens
	Box    *auth.SecretBox
	Store  *store.Store
}

// Repo is a project's repository and how to reach it.
type Repo struct {
	Slug    string // owner/name
	Base    string // project base branch
	conn    store.GitConnection
	project store.Project
}

// RepoOf resolves a project's repository and connection; ErrNoRepo when unset.
func (g *GitHub) RepoOf(ctx context.Context, p store.Project) (Repo, error) {
	if p.Repo == nil || *p.Repo == "" || p.GitConnectionID == nil {
		return Repo{}, ErrNoRepo
	}
	slug, err := github.ParseRepo(*p.Repo)
	if err != nil {
		return Repo{}, err
	}
	conn, err := g.Store.Connection(ctx, p.UserID, *p.GitConnectionID)
	if err != nil {
		return Repo{}, fmt.Errorf("git connection: %w", err)
	}
	return Repo{Slug: slug, Base: p.Branch, conn: conn, project: p}, nil
}

// HasChecks reports whether the connection can read check runs.
func (r Repo) HasChecks() bool {
	if r.conn.Kind != "app" {
		return true
	}
	v := r.conn.Permissions["checks"]
	return v == "read" || v == "write"
}

// IsApp reports whether the repo is reached through the GitHub App.
func (r Repo) IsApp() bool { return r.conn.Kind == "app" }

// Token returns a credential for the repo with the given scope.
func (g *GitHub) Token(ctx context.Context, r Repo, scope Scope) (string, error) {
	if r.conn.Kind == "app" && r.conn.InstallationID != nil {
		return g.Tokens.Installation(ctx, *r.conn.InstallationID, request(r, scope))
	}
	if scope == ScopeRead {
		return "", ErrPATNarrow
	}
	sealed, err := g.Store.ConnectionSecret(ctx, r.project.UserID, r.conn.ID)
	if err != nil {
		return "", err
	}
	return g.Box.Open(sealed)
}

func request(r Repo, scope Scope) github.TokenRequest {
	name := r.Slug[strings.Index(r.Slug, "/")+1:]
	req := github.TokenRequest{Repositories: []string{name}}
	switch scope {
	case ScopeRead:
		req.Permissions = map[string]string{"contents": "read", "pull_requests": "read", "metadata": "read"}
		if r.HasChecks() {
			req.Permissions["checks"] = "read"
		}
	case ScopePush:
		req.Permissions = map[string]string{"contents": "write", "metadata": "read"}
	case ScopePushFlows:
		req.Permissions = map[string]string{"contents": "write", "workflows": "write", "metadata": "read"}
	}
	return req
}

func (g *GitHub) call(ctx context.Context, r Repo, method, path string, body, out any) (int, error) {
	tok, err := g.Token(ctx, r, ScopeFull)
	if err != nil {
		return 0, err
	}
	return g.Client.Do(ctx, method, "/repos/"+r.Slug+path, tok, body, out)
}

// Status returns the HTTP status of a GitHub error (0 for other errors).
func Status(err error) int {
	var ge *github.Error
	if errors.As(err, &ge) {
		return ge.Status
	}
	return 0
}

// RefSHA returns the head sha of a branch.
func (g *GitHub) RefSHA(ctx context.Context, r Repo, branch string) (string, error) {
	var out struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	_, err := g.call(ctx, r, http.MethodGet, "/git/ref/heads/"+escapeRef(branch), nil, &out)
	return out.Object.SHA, err
}

// CreateRef creates refs/heads/<branch> at sha; an existing ref is not an error.
func (g *GitHub) CreateRef(ctx context.Context, r Repo, branch, sha string) error {
	body := map[string]string{"ref": "refs/heads/" + branch, "sha": sha}
	_, err := g.call(ctx, r, http.MethodPost, "/git/refs", body, nil)
	if Status(err) == http.StatusUnprocessableEntity && strings.Contains(err.Error(), "already exists") {
		return nil
	}
	return err
}

// EnsureBranch creates branch from base when it does not exist; returns its sha.
func (g *GitHub) EnsureBranch(ctx context.Context, r Repo, branch, base string) (string, error) {
	if sha, err := g.RefSHA(ctx, r, branch); err == nil {
		return sha, nil
	}
	sha, err := g.RefSHA(ctx, r, base)
	if err != nil {
		return "", err
	}
	return sha, g.CreateRef(ctx, r, branch, sha)
}

// UpdateRef moves a branch to sha (force=false: fast-forward only; 422 when not).
func (g *GitHub) UpdateRef(ctx context.Context, r Repo, branch, sha string, force bool) error {
	_, err := g.call(ctx, r, http.MethodPatch, "/git/refs/heads/"+escapeRef(branch), map[string]any{"sha": sha, "force": force}, nil)
	return err
}

// DeleteRef deletes a branch; a missing branch is not an error.
func (g *GitHub) DeleteRef(ctx context.Context, r Repo, branch string) error {
	_, err := g.call(ctx, r, http.MethodDelete, "/git/refs/heads/"+escapeRef(branch), nil, nil)
	if s := Status(err); s == http.StatusNotFound || s == http.StatusUnprocessableEntity {
		return nil
	}
	return err
}

// MergeResult is the outcome of POST /merges.
type MergeResult struct {
	SHA      string
	Conflict bool
}

// Merge merges head into base on GitHub (409 → Conflict; 204 → nothing to merge, SHA = "").
func (g *GitHub) Merge(ctx context.Context, r Repo, base, head, message string) (MergeResult, error) {
	var out struct {
		SHA string `json:"sha"`
	}
	_, err := g.call(ctx, r, http.MethodPost, "/merges", map[string]string{"base": base, "head": head, "commit_message": message}, &out)
	if Status(err) == http.StatusConflict {
		return MergeResult{Conflict: true}, nil
	}
	return MergeResult{SHA: out.SHA}, err
}

// PR is the part of a pull request the engines read.
type PR struct {
	Number    int    `json:"number"`
	HTMLURL   string `json:"html_url"`
	State     string `json:"state"`
	Merged    bool   `json:"merged"`
	Mergeable *bool  `json:"mergeable"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Head      struct {
		SHA string `json:"sha"`
		Ref string `json:"ref"`
	} `json:"head"`
}

// CreatePR opens a pull request head → base.
func (g *GitHub) CreatePR(ctx context.Context, r Repo, head, base, title, body string) (PR, error) {
	var out PR
	_, err := g.call(ctx, r, http.MethodPost, "/pulls", map[string]string{"head": head, "base": base, "title": title, "body": body}, &out)
	return out, err
}

// GetPR reads a pull request.
func (g *GitHub) GetPR(ctx context.Context, r Repo, number int) (PR, error) {
	var out PR
	_, err := g.call(ctx, r, http.MethodGet, fmt.Sprintf("/pulls/%d", number), nil, &out)
	return out, err
}

// MergePR merges a pull request with the method (squash|merge|rebase).
func (g *GitHub) MergePR(ctx context.Context, r Repo, number int, method, sha string) error {
	body := map[string]string{"merge_method": method, "sha": sha}
	_, err := g.call(ctx, r, http.MethodPut, fmt.Sprintf("/pulls/%d/merge", number), body, nil)
	return err
}

// CheckRuns lists the check runs on a commit; ErrNoChecks when the App may not read them.
func (g *GitHub) CheckRuns(ctx context.Context, r Repo, sha string) ([]core.CheckView, error) {
	if !r.HasChecks() {
		return nil, ErrNoChecks
	}
	var out struct {
		CheckRuns []struct {
			Name       string  `json:"name"`
			Status     string  `json:"status"`
			Conclusion *string `json:"conclusion"`
			HTMLURL    string  `json:"html_url"`
			Output     struct {
				Summary *string `json:"summary"`
			} `json:"output"`
		} `json:"check_runs"`
	}
	_, err := g.call(ctx, r, http.MethodGet, "/commits/"+sha+"/check-runs?per_page=100", nil, &out)
	if Status(err) == http.StatusForbidden {
		return nil, ErrNoChecks
	}
	checks := make([]core.CheckView, 0, len(out.CheckRuns))
	for _, c := range out.CheckRuns {
		checks = append(checks, core.CheckView{Name: c.Name, Status: c.Status, Conclusion: deref(c.Conclusion),
			URL: c.HTMLURL, Summary: deref(c.Output.Summary)})
	}
	return checks, err
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// FileContent reads a file at ref ("" when it does not exist).
func (g *GitHub) FileContent(ctx context.Context, r Repo, path, ref string) (string, error) {
	var out struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	_, err := g.call(ctx, r, http.MethodGet, "/contents/"+path+"?ref="+url.QueryEscape(ref), nil, &out)
	if Status(err) == http.StatusNotFound {
		return "", nil
	}
	if err != nil || out.Encoding != "base64" {
		return "", err
	}
	raw, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(out.Content, "\n", ""))
	return string(raw), err
}

// Workflows lists .github/workflows files with their content (CI detection).
func (g *GitHub) Workflows(ctx context.Context, r Repo, ref string) (map[string]string, error) {
	var list []struct {
		Path string `json:"path"`
		Type string `json:"type"`
	}
	_, err := g.call(ctx, r, http.MethodGet, "/contents/.github/workflows?ref="+url.QueryEscape(ref), nil, &list)
	if Status(err) == http.StatusNotFound {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, f := range list {
		if f.Type != "file" {
			continue
		}
		text, err := g.FileContent(ctx, r, f.Path, ref)
		if err != nil {
			return nil, err
		}
		out[f.Path] = text
	}
	return out, nil
}

// PutFile creates or updates a file on a branch (CI setup PR).
func (g *GitHub) PutFile(ctx context.Context, r Repo, path, branch, message, content, sha string) error {
	body := map[string]string{"message": message, "content": base64.StdEncoding.EncodeToString([]byte(content)), "branch": branch}
	if sha != "" {
		body["sha"] = sha
	}
	_, err := g.call(ctx, r, http.MethodPut, "/contents/"+path, body, nil)
	return err
}

// FileSHA returns the blob sha of a file at ref ("" when missing).
func (g *GitHub) FileSHA(ctx context.Context, r Repo, path, ref string) (string, error) {
	var out struct {
		SHA string `json:"sha"`
	}
	_, err := g.call(ctx, r, http.MethodGet, "/contents/"+path+"?ref="+url.QueryEscape(ref), nil, &out)
	if Status(err) == http.StatusNotFound {
		return "", nil
	}
	return out.SHA, err
}

// Rulesets reports whether the repository has any ruleset.
func (g *GitHub) Rulesets(ctx context.Context, r Repo) (bool, error) {
	var out []map[string]any
	_, err := g.call(ctx, r, http.MethodGet, "/rulesets", nil, &out)
	if s := Status(err); s == http.StatusNotFound || s == http.StatusForbidden {
		return false, nil
	}
	return len(out) > 0, err
}

func escapeRef(branch string) string {
	parts := strings.Split(branch, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}
