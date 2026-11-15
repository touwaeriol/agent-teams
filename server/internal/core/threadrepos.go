package core

import (
	"context"
	"errors"
	"strings"
	"time"
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
	PRNumber      *int
	PRURL         *string
	PRState       string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// ThreadRepoView is a repository of a thread as the API shows it.
type ThreadRepoView struct {
	Repo          string  `json:"repo"`
	BaseBranch    string  `json:"base_branch"`
	BranchCreated bool    `json:"branch_created"`
	PRNumber      *int    `json:"pr_number"`
	PRURL         *string `json:"pr_url"`
	PRState       string  `json:"pr_state"`
}

const threadRepoCols = `thread_id, repo, base_branch, branch_created, pr_number, pr_url, pr_state, created_at, updated_at`

func scanThreadRepo(r Scanner) (ThreadRepo, error) {
	var t ThreadRepo
	err := r.Scan(&t.ThreadID, &t.Repo, &t.BaseBranch, &t.BranchCreated, &t.PRNumber, &t.PRURL, &t.PRState,
		&t.CreatedAt, &t.UpdatedAt)
	return t, MapErr(err)
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

// ThreadRepos lists a thread's repository rows (created order).
func ThreadRepos(ctx context.Context, q DB, threadID string) ([]ThreadRepo, error) {
	rows, err := q.Query(ctx, `SELECT `+threadRepoCols+` FROM at_thread_repos WHERE thread_id = $1 ORDER BY created_at, repo`, threadID)
	return collect(rows, err, scanThreadRepo)
}

// ThreadRepoOf loads one repository row of a thread (ErrNotFound when the thread has not touched it).
func ThreadRepoOf(ctx context.Context, q DB, threadID, repo string) (ThreadRepo, error) {
	return scanThreadRepo(q.QueryRow(ctx, `SELECT `+threadRepoCols+` FROM at_thread_repos
		WHERE thread_id = $1 AND lower(repo) = lower($2)`, threadID, repo))
}

// ThreadRepoByPR finds the repository row whose pull request has this URL.
func ThreadRepoByPR(ctx context.Context, q DB, url string) (ThreadRepo, error) {
	return scanThreadRepo(q.QueryRow(ctx, `SELECT `+threadRepoCols+` FROM at_thread_repos WHERE pr_url = $1`, url))
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
	_, err = q.Exec(ctx, `INSERT INTO at_thread_repos (thread_id, repo, base_branch) VALUES ($1, $2, $3)
		ON CONFLICT (thread_id, repo) DO UPDATE SET base_branch = EXCLUDED.base_branch, updated_at = now()`,
		threadID, repo, base)
	return err
}

// MarkThreadBranch records that the thread branch now exists in the repository, cut from base.
func MarkThreadBranch(ctx context.Context, q DB, threadID, repo, base string) error {
	_, err := q.Exec(ctx, `INSERT INTO at_thread_repos (thread_id, repo, base_branch, branch_created) VALUES ($1, $2, $3, TRUE)
		ON CONFLICT (thread_id, repo) DO UPDATE SET base_branch = EXCLUDED.base_branch, branch_created = TRUE, updated_at = now()`,
		threadID, repo, base)
	return err
}

// SetThreadRepoPR records a repository's pull request (state open).
func SetThreadRepoPR(ctx context.Context, q DB, threadID, repo string, number int, url string) error {
	return Affected(q.Exec(ctx, `UPDATE at_thread_repos SET pr_number = $3, pr_url = $4, pr_state = 'open', updated_at = now()
		WHERE thread_id = $1 AND repo = $2`, threadID, repo, number, url))
}

// SetThreadRepoPRState changes a repository's pull request state.
func SetThreadRepoPRState(ctx context.Context, q DB, threadID, repo, state string) error {
	_, err := q.Exec(ctx, `UPDATE at_thread_repos SET pr_state = $3, updated_at = now() WHERE thread_id = $1 AND repo = $2`,
		threadID, repo, state)
	return err
}

// SetThreadPRStates changes the state of every pull request of the thread that is still open.
func SetThreadPRStates(ctx context.Context, q DB, threadID, state string) error {
	_, err := q.Exec(ctx, `UPDATE at_thread_repos SET pr_state = $2, updated_at = now()
		WHERE thread_id = $1 AND pr_number IS NOT NULL AND pr_state NOT IN ('merged','closed')`, threadID, state)
	return err
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
