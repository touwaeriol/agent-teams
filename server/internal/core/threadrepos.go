package core

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrBaseFixed means the thread branch already exists in the repository, cut from another base.
var ErrBaseFixed = errors.New("这个仓库的讨论串分支已经从另一个基准分支创建了，不能再改基准分支")

// ThreadRepo is a row of at_thread_repos: one repository of a thread's project as the thread
// uses it. The row appears when a base is set or the thread branch is created in the repository.
type ThreadRepo struct {
	ThreadID      string
	Repo          string
	BaseBranch    string // explicit base (lead/coordinator) or the default branch the thread branch was cut from
	BranchCreated bool
	PRNumber      *int       `gorm:"column:pr_number"`
	PRURL         *string    `gorm:"column:pr_url"`
	PRState       string     `gorm:"column:pr_state"`
	PROpenedAt    *time.Time `gorm:"column:pr_opened_at"`
	// DeliveredSHA is the thread branch head handed over in a standalone Git repository, which
	// has no pull requests: the thread's work there ends on its branch (git-repos.md §7).
	DeliveredSHA *string `gorm:"column:delivered_sha"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// TableName maps ThreadRepo to at_thread_repos.
func (ThreadRepo) TableName() string { return "at_thread_repos" }

// ThreadRepoView is a repository of a thread as the API shows it.
type ThreadRepoView struct {
	Repo          string  `json:"repo"`
	BaseBranch    string  `json:"base_branch"`
	BranchCreated bool    `json:"branch_created"`
	PRNumber      *int    `json:"pr_number"`
	PRURL         *string `json:"pr_url"`
	PRState       string  `json:"pr_state"`
}

// View renders the row.
func (t ThreadRepo) View() ThreadRepoView {
	return ThreadRepoView{Repo: t.Repo, BaseBranch: t.BaseBranch, BranchCreated: t.BranchCreated, PRNumber: t.PRNumber,
		PRURL: t.PRURL, PRState: t.PRState}
}

// HasOpenPR reports whether the repository's pull request is open (or being accepted).
func (t ThreadRepo) HasOpenPR() bool {
	return t.PRNumber != nil && t.PRState != "merged" && t.PRState != "closed"
}

func threadRepos(ctx context.Context, q DB) *gorm.DB { return q.Gorm(ctx).Model(&ThreadRepo{}) }

// StandaloneRepos are the slugs (lower case) of a project's standalone Git repositories: they
// have no pull requests, so a thread's changes there need none.
func StandaloneRepos(ctx context.Context, q DB, projectID string) (map[string]bool, error) {
	var slugs []string
	err := q.Gorm(ctx).Table("at_project_repos").Where("project_id = ? AND git_repo_id IS NOT NULL", projectID).Pluck("repo", &slugs).Error
	out := make(map[string]bool, len(slugs))
	for _, s := range slugs {
		out[strings.ToLower(s)] = true
	}
	return out, err
}

// NeedsPR reports a repository row with changes that wait for a pull request: a thread branch
// without one, outside the standalone repositories.
func (t ThreadRepo) NeedsPR(standalone map[string]bool) bool {
	return t.BranchCreated && t.PRNumber == nil && !standalone[strings.ToLower(t.Repo)]
}

// Delivered reports changes on a standalone repository's thread branch.
func (t ThreadRepo) Delivered(standalone map[string]bool) bool {
	return t.BranchCreated && standalone[strings.ToLower(t.Repo)]
}

// ThreadRepos lists a thread's repository rows (created order).
func ThreadRepos(ctx context.Context, q DB, threadID string) ([]ThreadRepo, error) {
	return find[ThreadRepo](threadRepos(ctx, q).Where("thread_id = ?", threadID).Order("created_at, repo"))
}

// ThreadRepoOf loads one repository row of a thread (ErrNotFound when the thread has not touched it).
func ThreadRepoOf(ctx context.Context, q DB, threadID, repo string) (ThreadRepo, error) {
	return take[ThreadRepo](threadRepos(ctx, q).Where("thread_id = ? AND lower(repo) = lower(?)", threadID, repo))
}

// ThreadRepoByPR finds the repository row whose pull request has this URL.
func ThreadRepoByPR(ctx context.Context, q DB, url string) (ThreadRepo, error) {
	return take[ThreadRepo](threadRepos(ctx, q).Where("pr_url = ?", url))
}

// upsertThreadRepo inserts a repository row or updates the given columns of the existing one.
func upsertThreadRepo(ctx context.Context, q DB, row map[string]any, update ...string) error {
	set := clause.AssignmentColumns(update)
	set = append(set, clause.Assignment{Column: clause.Column{Name: "updated_at"}, Value: gorm.Expr("now()")})
	return q.Gorm(ctx).Table("at_thread_repos").Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "thread_id"}, {Name: "repo"}},
		DoUpdates: set,
	}).Create(row).Error
}

// SetThreadBase records the base branch a thread uses in a repository. Once the thread branch
// exists the base is fixed: a different one is refused with ErrBaseFixed.
func SetThreadBase(ctx context.Context, q DB, threadID, repo, base string) error {
	base = strings.TrimSpace(base)
	if base == "" {
		return nil
	}
	cur, err := ThreadRepoOf(ctx, q, threadID, repo)
	if err == nil && cur.BranchCreated && cur.BaseBranch != base {
		return ErrBaseFixed
	}
	return upsertThreadRepo(ctx, q, map[string]any{"thread_id": threadID, "repo": repo, "base_branch": base},
		"base_branch")
}

// MarkThreadBranch records that the thread branch now exists in the repository, cut from base.
func MarkThreadBranch(ctx context.Context, q DB, threadID, repo, base string) error {
	return upsertThreadRepo(ctx, q, map[string]any{"thread_id": threadID, "repo": repo, "base_branch": base,
		"branch_created": true}, "base_branch", "branch_created")
}

// threadRepoRow selects one repository row of a thread for a write.
func threadRepoRow(ctx context.Context, q DB, threadID, repo string) *gorm.DB {
	return q.Gorm(ctx).Table("at_thread_repos").Where("thread_id = ? AND repo = ?", threadID, repo)
}

// SetThreadRepoPR records a repository's pull request (state open) and when it was opened.
func SetThreadRepoPR(ctx context.Context, q DB, threadID, repo string, number int, url string) error {
	return Changed(threadRepoRow(ctx, q, threadID, repo).Updates(map[string]any{"pr_number": number, "pr_url": url,
		"pr_state": "open", "pr_opened_at": gorm.Expr("now()"), "updated_at": gorm.Expr("now()")}))
}

// UpdateThreadRepo applies column changes to one repository row of a thread.
func UpdateThreadRepo(ctx context.Context, q DB, threadID, repo string, set map[string]any) error {
	set["updated_at"] = gorm.Expr("now()")
	return threadRepoRow(ctx, q, threadID, repo).Updates(set).Error
}

// SetThreadRepoPRState changes a repository's pull request state.
func SetThreadRepoPRState(ctx context.Context, q DB, threadID, repo, state string) error {
	return threadRepoRow(ctx, q, threadID, repo).Updates(map[string]any{"pr_state": state,
		"updated_at": gorm.Expr("now()")}).Error
}

// SetThreadPRStates changes the state of every pull request of the thread that is still open.
func SetThreadPRStates(ctx context.Context, q DB, threadID, state string) error {
	return q.Gorm(ctx).Table("at_thread_repos").
		Where("thread_id = ? AND pr_number IS NOT NULL AND pr_state NOT IN ('merged','closed')", threadID).
		Updates(map[string]any{"pr_state": state, "updated_at": gorm.Expr("now()")}).Error
}

// ThreadRepoViews renders a thread's repositories.
func ThreadRepoViews(ctx context.Context, q DB, threadID string) ([]ThreadRepoView, error) {
	rows, err := ThreadRepos(ctx, q, threadID)
	out := make([]ThreadRepoView, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.View())
	}
	return out, err
}
