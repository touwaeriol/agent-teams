package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/ghops"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// RepoFocus is a repository a thread should work in, with an optional explicit base branch
// (open_thread repos, assign_task repo/base).
type RepoFocus struct {
	Repo string
	Base string
}

// Repos returns the project's reachable repositories (nil without GitHub or repositories).
func (o *Orchestrator) Repos(ctx context.Context, p store.Project) []ghops.Repo {
	return o.ReposIn(ctx, nil, p)
}

// ReposIn is Repos reading through q: inside a transaction pass it, so the lookup uses the
// transaction's connection instead of waiting on the pool while holding the transaction's locks.
func (o *Orchestrator) ReposIn(ctx context.Context, q core.DB, p store.Project) []ghops.Repo {
	if o.gh == nil || !p.HasRepos() {
		return nil
	}
	repos, err := o.gh.In(q).ReposOf(ctx, p)
	if err != nil {
		o.log.Debug("project repositories", "project_id", p.ID, "err", err)
		return nil
	}
	return repos
}

// DefaultBranch is the base branch recorded in the project's repository settings. A repository
// bound before base branches were stored falls back to the upstream's default branch, read from
// the cache (WarmRepos fills it, and records it as the base) so that session opening, which runs
// inside a transaction, does not wait on GitHub. It is "" when neither is known: there is no
// guessed "main" any more, the node then reports that the start branch is missing.
func (o *Orchestrator) DefaultBranch(ctx context.Context, r ghops.Repo) string {
	b, err := o.gh.DefaultBranch(ctx, r)
	if err != nil {
		o.log.Warn("base branch unknown", "repo", r.Slug, "err", err)
		return ""
	}
	return b
}

// upstreamDefault is the upstream's own default branch ("" when unknown), cached like DefaultBranch.
func (o *Orchestrator) upstreamDefault(ctx context.Context, r ghops.Repo) string {
	b, err := o.gh.UpstreamDefault(ctx, r)
	if err != nil {
		return ""
	}
	return b
}

// BaseFor is the branch a thread's work in a repository starts from and its pull request
// targets: the base recorded for the thread (given by the lead or the coordinator, or the one
// the thread branch was cut from), else the repository's default branch.
func (o *Orchestrator) BaseFor(ctx context.Context, q core.DB, t core.Thread, r ghops.Repo) string {
	if tr, err := core.ThreadRepoOf(ctx, q, t.ID, r.Slug); err == nil && tr.BaseBranch != "" {
		return tr.BaseBranch
	}
	return o.DefaultBranch(ctx, r)
}

// startPoint is where a member checkout of a repository branches from: the thread branch once
// it exists there, else the thread's base in that repository.
func (o *Orchestrator) startPoint(ctx context.Context, q core.DB, t core.Thread, r ghops.Repo) string {
	if tr, err := core.ThreadRepoOf(ctx, q, t.ID, r.Slug); err == nil && tr.BranchCreated {
		return t.Branch
	}
	return o.BaseFor(ctx, q, t, r)
}

// EnsureThreadBranch creates the thread branch in a repository from the thread's base there,
// the first time the thread delivers something to it (branches are created lazily).
func (o *Orchestrator) EnsureThreadBranch(ctx context.Context, t core.Thread, r ghops.Repo) error {
	if tr, err := core.ThreadRepoOf(ctx, o.core.Pool, t.ID, r.Slug); err == nil && tr.BranchCreated {
		return nil
	}
	base := o.BaseFor(ctx, o.core.Pool, t, r)
	if _, err := o.gh.EnsureBranch(ctx, r, t.Branch, base); err != nil {
		return fmt.Errorf("在 %s 上从 %s 创建讨论串分支 %s 失败：%w", r.Slug, base, t.Branch, err)
	}
	return core.MarkThreadBranch(ctx, o.core.Pool, t.ID, r.Slug, base)
}

// SetBase records an explicit base branch for a thread in one of the project's repositories.
func (o *Orchestrator) SetBase(ctx context.Context, q core.DB, t core.Thread, repo, base string) (string, error) {
	slug, err := o.ProjectRepo(ctx, t.ProjectID, repo)
	if err != nil {
		return "", err
	}
	return slug, core.SetThreadBase(ctx, q, t.ID, slug, base)
}

// ProjectRepo resolves a repository name given by a tool to the project's spelling of it.
func (o *Orchestrator) ProjectRepo(ctx context.Context, projectID, repo string) (string, error) {
	p, err := o.Project(ctx, projectID)
	if err != nil {
		return "", err
	}
	return matchRepo(p, repo)
}

// ErrUnknownRepo means a tool named a repository the project does not bind.
var ErrUnknownRepo = errors.New("项目没有绑定这个仓库")

func matchRepo(p store.Project, repo string) (string, error) {
	want := strings.TrimSuffix(strings.Trim(strings.TrimSpace(repo), "/"), ".git")
	for _, r := range p.Repos {
		if strings.EqualFold(r.Repo, want) {
			return r.Repo, nil
		}
	}
	return "", fmt.Errorf("%w：%s（项目的仓库：%s）", ErrUnknownRepo, repo, strings.Join(repoNames(p), "、"))
}

func repoNames(p store.Project) []string {
	out := make([]string, len(p.Repos))
	for i, r := range p.Repos {
		out[i] = r.Repo
	}
	return out
}

// applyFocus validates open_thread repos and records their explicit bases; it returns the
// brief line naming them ("" without focus).
func applyFocus(ctx context.Context, tx *core.Tx, p store.Project, threadID string, focus []RepoFocus) (string, error) {
	var parts []string
	for _, f := range focus {
		slug, err := matchRepo(p, f.Repo)
		if err != nil {
			return "", err
		}
		if err := core.SetThreadBase(ctx, tx, threadID, slug, f.Base); err != nil {
			return "", err
		}
		parts = append(parts, focusText(slug, f.Base))
	}
	if len(parts) == 0 {
		return "", nil
	}
	return "涉及的仓库：" + strings.Join(parts, "、"), nil
}

func focusText(slug, base string) string {
	if strings.TrimSpace(base) == "" {
		return slug
	}
	return fmt.Sprintf("%s（基准分支 %s）", slug, strings.TrimSpace(base))
}

// reposSection tells an employee which repositories the project has, where each one is checked
// out and which base the thread uses there ("" for projects without repositories).
func (o *Orchestrator) reposSection(ctx context.Context, q core.DB, e core.Employee, t *core.Thread, p store.Project) string {
	if !p.HasRepos() {
		if e.Spec.Solo && t != nil {
			return "## Working directory\nYour working directory is the local folder " + PathsOf(e, t).Cwd + " on the node (relative to the node's data root), with no git repository; the files exist only on this machine."
		}
		return ""
	}
	var b strings.Builder
	b.WriteString("## Repositories\n")
	if t == nil || e.Tier == "coordinator" {
		for _, r := range p.Repos {
			if r.BaseBranch != "" {
				fmt.Fprintf(&b, "- %s (starting branch %s)\n", r.Repo, r.BaseBranch)
			} else {
				fmt.Fprintf(&b, "- %s\n", r.Repo)
			}
		}
		b.WriteString("Threads start from each repository's starting branch by default (set in the project settings); for a thread to start from another branch, pass {repo, base} in open_thread's repos.")
		return b.String()
	}
	paths := PathsOf(e, t)
	for _, r := range o.ReposIn(ctx, q, p) {
		fmt.Fprintf(&b, "- %s: directory ./%s, base branch %s\n", r.Slug, ghops.Dir(r.Slug), o.BaseFor(ctx, q, *t, r))
	}
	fmt.Fprintf(&b, "Your working directory %s has one subdirectory per repository; your branch is called %s in every repository. Commit only in the repositories that need changes.", paths.Cwd, paths.Branch)
	return b.String()
}

// CheckoutCwd is the employee's checkout of a repository. Threads started before projects had
// several repositories put the single worktree directly in the employee's directory; such a
// checkout keeps being used while the project still has one repository.
func (o *Orchestrator) CheckoutCwd(ctx context.Context, q core.DB, e core.Employee, t core.Thread, slug string, nRepos int) string {
	p := PathsOf(e, &t)
	if nRepos == 1 && legacyCheckout(ctx, q, t.ID, p.Cwd) {
		return p.Cwd
	}
	return p.RepoCwd(slug)
}

// legacyCheckout reports whether cwd itself was recorded as a checkout of the thread.
func legacyCheckout(ctx context.Context, q core.DB, threadID, cwd string) bool {
	var n int64
	err := q.Gorm(ctx).Table("at_thread_nodes").Where("thread_id = ? AND ? = ANY(paths)", threadID, cwd).Limit(1).Count(&n).Error
	return err == nil && n > 0
}

// WarmRepos reads from GitHub what opening a session needs (each repository's default branch
// and CLAUDE.md) when it is missing; a stale entry is renewed in the background (see the caches
// in ghops and prompt.go). Call it before a transaction that may open a session: inside the
// transaction those values come from the cache only, so the transaction's connection and row
// locks are never held across a GitHub round trip.
func (o *Orchestrator) WarmRepos(ctx context.Context, p store.Project) {
	for _, r := range o.Repos(ctx, p) {
		o.backfillBase(ctx, p, r)
		o.repoDoc(ctx, p, r)
	}
}

// backfillBase warms the upstream default branch cache (protectedRefs reads it) and records it as
// the base of a repository bound before base branches were stored (00480), so that later thread
// starts read it from the project.
func (o *Orchestrator) backfillBase(ctx context.Context, p store.Project, r ghops.Repo) {
	b, err := o.gh.UpstreamDefault(ctx, r)
	if err != nil {
		o.log.Warn("look up default branch", "project_id", p.ID, "repo", r.Slug, "err", err)
		return
	}
	if r.Base() != "" {
		return
	}
	if ok, err := store.SetProjectRepoBase(ctx, o.core.Pool.Gorm(ctx), p.ID, r.Slug, b); err != nil {
		o.log.Warn("record base branch", "project_id", p.ID, "repo", r.Slug, "err", err)
	} else if ok {
		o.log.Info("recorded base branch", "project_id", p.ID, "repo", r.Slug, "base", b)
	}
}

// WarmProject is WarmRepos by project id (errors are only logged: warming is an optimisation).
func (o *Orchestrator) WarmProject(ctx context.Context, projectID string) {
	if o.gh == nil || projectID == "" {
		return
	}
	p, err := o.Project(ctx, projectID)
	if err != nil {
		o.log.Debug("warm project", "project_id", projectID, "err", err)
		return
	}
	o.WarmRepos(ctx, p)
}

// WarmAll warms every project that has repositories (server start: the cache is empty).
func (o *Orchestrator) WarmAll(ctx context.Context) {
	if o.gh == nil {
		return
	}
	var ids []string
	err := o.core.Pool.Gorm(ctx).Table("at_project_repos").Distinct("project_id").Pluck("project_id", &ids).Error
	if err != nil {
		o.log.Warn("list projects to warm", "err", err)
		return
	}
	for _, id := range ids {
		o.WarmProject(ctx, id)
	}
}
