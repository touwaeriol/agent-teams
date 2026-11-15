package httpapi

import (
	"context"
	"fmt"
	"strings"

	"github.com/touwaeriol/agent-teams/server/internal/github"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// RepoRef is one repository bound to a project. Base branches are not stored: they belong in the
// project context, which every employee reads.
type RepoRef struct {
	Repo            string  `json:"repo" maxLength:"300" doc:"GitHub URL, git@ URL or owner/repo"`
	GitConnectionID *string `json:"git_connection_id" nullable:"true"`
}

// RepoRefs renders a project's repositories.
func RepoRefs(repos []store.ProjectRepo) []RepoRef {
	out := make([]RepoRef, len(repos))
	for i, r := range repos {
		out[i] = RepoRef{Repo: r.Repo, GitConnectionID: r.GitConnectionID}
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
	if deref(r.GitConnectionID) == "" {
		return store.ProjectRepo{}, invalid([]FieldError{{"git_connection_id", "关联仓库需要先选择一个 Git 连接"}}, "关联仓库需要先选择一个 Git 连接")
	}
	slug, err := github.ParseRepo(r.Repo)
	if err != nil {
		return store.ProjectRepo{}, githubErr(err)
	}
	if k, ok := knownRepo(known, slug, *r.GitConnectionID); ok {
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
	return store.ProjectRepo{Repo: slug, GitConnectionID: r.GitConnectionID}, nil
}

func knownRepo(known []store.ProjectRepo, slug, connID string) (store.ProjectRepo, bool) {
	for _, k := range known {
		if strings.EqualFold(k.Repo, slug) && deref(k.GitConnectionID) == connID {
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
