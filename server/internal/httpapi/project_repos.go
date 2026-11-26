package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/touwaeriol/agent-teams/server/internal/github"
	"github.com/touwaeriol/agent-teams/server/internal/gitremote"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// RepoRef is one repository bound to a project, with the branch its threads start from.
type RepoRef struct {
	Repo            string  `json:"repo" maxLength:"300" doc:"GitHub URL, git@ URL or owner/repo"`
	GitConnectionID *string `json:"git_connection_id" nullable:"true" required:"false"`
	GitRepoID       *string `json:"git_repo_id" nullable:"true" required:"false" doc:"A standalone repository (git-repos.md); then repo is its slug and git_connection_id is null"`
	BaseBranch      string  `json:"base_branch,omitempty" required:"false" maxLength:"255" doc:"Branch threads start from and open pull requests against; '' on input = the repository's default branch (looked up and stored)"`
}

// RepoRefs renders a project's repositories.
func RepoRefs(repos []store.ProjectRepo) []RepoRef {
	out := make([]RepoRef, len(repos))
	for i, r := range repos {
		out[i] = RepoRef{Repo: r.Repo, GitConnectionID: r.GitConnectionID, GitRepoID: r.GitRepoID, BaseBranch: r.BaseBranch}
	}
	return out
}

// checkRepos normalizes the repositories of a project form: each needs a connection that can
// reach it, and none may appear twice. Repositories already bound with the same connection
// (known) are not looked up again.
func (s *Server) checkRepos(ctx context.Context, userID string, in []RepoRef, known []store.ProjectRepo) ([]store.ProjectRepo, error) {
	out := make([]store.ProjectRepo, 0, len(in))
	seen := map[string]bool{}
	for i, r := range in {
		repo, err := s.checkOneRepo(ctx, userID, r, known)
		if err != nil {
			return nil, err
		}
		key := strings.ToLower(repo.Repo)
		if seen[key] {
			msg := "仓库 " + repo.Repo + " 重复了"
			return nil, invalid([]FieldError{{fmt.Sprintf("repos[%d].repo", i), msg}}, msg)
		}
		seen[key] = true
		out = append(out, repo)
	}
	return out, nil
}

func (s *Server) checkOneRepo(ctx context.Context, userID string, r RepoRef, known []store.ProjectRepo) (store.ProjectRepo, error) {
	base := strings.TrimSpace(r.BaseBranch)
	if id := deref(r.GitRepoID); id != "" {
		if k, ok := knownStandalone(known, id); ok && (base == "" || base == k.BaseBranch) && k.BaseBranch != "" {
			return k, nil
		}
		return s.checkStandaloneRepo(ctx, userID, id, base)
	}
	if deref(r.GitConnectionID) == "" {
		return store.ProjectRepo{}, invalid([]FieldError{{"git_connection_id", "关联仓库需要先选择一个 Git 连接"}}, "关联仓库需要先选择一个 Git 连接")
	}
	slug, err := github.ParseRepo(r.Repo)
	if err != nil {
		return store.ProjectRepo{}, githubErr(err)
	}
	if k, ok := knownRepo(known, slug, *r.GitConnectionID); ok && (base == "" || base == k.BaseBranch) && k.BaseBranch != "" {
		return k, nil
	}
	token, _, err := s.connectionToken(ctx, userID, *r.GitConnectionID)
	if err != nil {
		return store.ProjectRepo{}, err
	}
	found, err := s.gh.Repo(ctx, token, slug)
	if err != nil {
		return store.ProjectRepo{}, githubErr(err)
	}
	if found.FullName != "" {
		slug = found.FullName
	}
	if base, err = s.githubBase(ctx, token, slug, base, found.DefaultBranch); err != nil {
		return store.ProjectRepo{}, err
	}
	return store.ProjectRepo{Repo: slug, GitConnectionID: r.GitConnectionID, BaseBranch: base}, nil
}

// githubBase checks that the branch threads will start from exists: the one asked for, else the
// repository's default branch. An empty repository has no branch to start from.
func (s *Server) githubBase(ctx context.Context, token, slug, want, def string) (string, error) {
	base := want
	if base == "" {
		base = def
	}
	if base == "" {
		return "", baseErr(emptyRepoMsg(slug))
	}
	_, err := s.gh.Do(ctx, http.MethodGet, "/repos/"+slug+"/git/ref/heads/"+escapeBranch(base), token, nil, nil)
	var ge *github.Error
	switch {
	case err == nil:
		return base, nil
	case errors.As(err, &ge) && ge.Status == http.StatusConflict: // "Git Repository is empty."
		return "", baseErr(emptyRepoMsg(slug))
	case errors.As(err, &ge) && ge.Status == http.StatusNotFound && want == "":
		return "", baseErr(emptyRepoMsg(slug))
	case errors.As(err, &ge) && ge.Status == http.StatusNotFound:
		return "", baseErr(missingBranchMsg(slug, base))
	}
	return "", githubErr(err)
}

// standaloneBase is githubBase for a standalone repository, from its ls-remote.
func standaloneBase(slug, want string, refs gitremote.Refs) (string, error) {
	base := want
	if base == "" {
		base = refs.Head
	}
	switch {
	case len(refs.Branches) == 0 || base == "":
		return "", baseErr(emptyRepoMsg(slug))
	case refs.Branches[base] == "":
		return "", baseErr(missingBranchMsg(slug, base))
	}
	return base, nil
}

func emptyRepoMsg(slug string) string {
	return "仓库 " + slug + " 是空的，还没有任何分支，讨论串没有可以开始的分支。请先推送一个分支，再添加这个仓库"
}

func missingBranchMsg(slug, branch string) string {
	return "仓库 " + slug + " 里没有分支 " + branch + "，请填一个已经存在的分支作为起点分支"
}

func baseErr(msg string) error {
	return invalid([]FieldError{{"repos.base_branch", msg}}, msg)
}

// escapeBranch escapes each segment of a branch name for a URL path (feature/x stays two segments).
func escapeBranch(b string) string {
	parts := strings.Split(b, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

func knownRepo(known []store.ProjectRepo, slug, connID string) (store.ProjectRepo, bool) {
	for _, k := range known {
		if strings.EqualFold(k.Repo, slug) && deref(k.GitConnectionID) == connID {
			return k, true
		}
	}
	return store.ProjectRepo{}, false
}

func knownStandalone(known []store.ProjectRepo, id string) (store.ProjectRepo, bool) {
	for _, k := range known {
		if deref(k.GitRepoID) == id {
			return k, true
		}
	}
	return store.ProjectRepo{}, false
}

// patchRepos validates the repositories of a PATCH (nil when it does not change them).
func (s *Server) patchRepos(ctx context.Context, userID string, cur store.Project, in *[]RepoRef) ([]store.ProjectRepo, error) {
	if in == nil {
		return nil, nil
	}
	return s.checkRepos(ctx, userID, *in, cur.Repos)
}

// checkStandaloneRepo: one of the user's standalone repositories, with the base branch checked
// against the upstream's branches.
func (s *Server) checkStandaloneRepo(ctx context.Context, userID, id, want string) (store.ProjectRepo, error) {
	g, err := s.store.GitRepo(ctx, userID, id)
	if err != nil {
		return store.ProjectRepo{}, gitStoreErr(err, "仓库")
	}
	remote, err := s.GitRemote(ctx, userID, g)
	if err != nil {
		return store.ProjectRepo{}, fail(http.StatusConflict, "credential_unreadable", err.Error())
	}
	ctx, cancel := context.WithTimeout(ctx, gitTestTimeout)
	defer cancel()
	refs, err := s.gitRunner().LsRemote(ctx, remote)
	if err != nil {
		_, msg := gitTestError(err)
		return store.ProjectRepo{}, fail(http.StatusBadGateway, "git_failed", "读取仓库 "+g.Slug+" 的分支失败："+msg)
	}
	base, err := standaloneBase(g.Slug, want, refs)
	if err != nil {
		return store.ProjectRepo{}, err
	}
	return store.ProjectRepo{Repo: g.Slug, GitRepoID: &g.ID, BaseBranch: base}, nil
}
