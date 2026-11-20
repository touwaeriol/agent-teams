package gitrelay

import (
	"context"
	"errors"
	"strings"

	"github.com/touwaeriol/agent-teams/server/internal/gitremote"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// Update is one ref change pushed to the upstream: Old "" = the ref must not exist yet,
// New "" = delete it.
type Update struct {
	Ref, Old, New string
}

const zeroSHA = "0000000000000000000000000000000000000000"

// identity is the author and committer of the merge commits the platform makes.
var identity = []string{"GIT_AUTHOR_NAME=Agent Teams", "GIT_AUTHOR_EMAIL=platform@agent-teams.local",
	"GIT_COMMITTER_NAME=Agent Teams", "GIT_COMMITTER_EMAIL=platform@agent-teams.local"}

func head(branch string) string { return "refs/heads/" + branch }

// refSHA reads a branch in the mirror ("" when missing).
func (r *Relay) refSHA(ctx context.Context, dir, branch string) string {
	out, err := r.Git.Local(ctx, dir, nil, "rev-parse", "--verify", "--quiet", head(branch)+"^{commit}")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// RefSHA returns a branch's head (ErrNoRef when it does not exist).
func (r *Relay) RefSHA(ctx context.Context, repo store.GitRepo, branch string) (string, error) {
	dir, err := r.Sync(ctx, repo, false)
	if err != nil {
		return "", err
	}
	if sha := r.refSHA(ctx, dir, branch); sha != "" {
		return sha, nil
	}
	return "", ErrNoRef
}

// DefaultBranch is the upstream's default branch: the one saved by the last test connection,
// else asked from the upstream now.
func (r *Relay) DefaultBranch(ctx context.Context, repo store.GitRepo) (string, error) {
	if repo.DefaultBranch != "" {
		return repo.DefaultBranch, nil
	}
	rem, err := r.remote(ctx, repo)
	if err != nil {
		return "", err
	}
	refs, err := r.Git.LsRemote(ctx, rem)
	if err != nil {
		return "", err
	}
	if refs.Head == "" {
		return "main", nil
	}
	return refs.Head, nil
}

// Compare lists the files changed on head since it left base (base...head), at most max.
func (r *Relay) Compare(ctx context.Context, repo store.GitRepo, base, head string, max int) ([]string, error) {
	dir, err := r.Sync(ctx, repo, false)
	if err != nil {
		return nil, err
	}
	out, err := r.Git.Local(ctx, dir, nil, "diff", "--name-only", "--no-renames", refArg(base)+"..."+refArg(head))
	if err != nil {
		return nil, err
	}
	files := []string{}
	for _, f := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if f != "" && len(files) < max {
			files = append(files, f)
		}
	}
	return files, nil
}

// refArg names a branch, or passes a full sha through.
func refArg(ref string) string {
	if len(ref) == 40 && strings.Trim(ref, "0123456789abcdef") == "" {
		return ref
	}
	return head(ref)
}

// FileContent reads a file at a branch ("" when the branch or the file does not exist).
func (r *Relay) FileContent(ctx context.Context, repo store.GitRepo, path, ref string) (string, error) {
	dir, err := r.Sync(ctx, repo, false)
	if err != nil {
		return "", err
	}
	out, err := r.Git.Local(ctx, dir, nil, "cat-file", "blob", refArg(ref)+":"+path)
	if err != nil {
		return "", nil
	}
	return string(out), nil
}

// push sends updates to the upstream (atomic when several) with a lease on each ref's old value,
// The caller holds the repository's lock.
func (r *Relay) push(ctx context.Context, repo store.GitRepo, dir string, updates []Update) error {
	rem, err := r.remote(ctx, repo)
	if err != nil {
		return err
	}
	args := []string{"push", "--porcelain", "--no-verify"}
	if len(updates) > 1 {
		args = append(args, "--atomic")
	}
	args = append(args, rem.URL)
	for _, u := range updates {
		args = append(args, "--force-with-lease="+u.Ref+":"+u.Old)
	}
	for _, u := range updates {
		args = append(args, u.New+":"+u.Ref)
	}
	cmd, cleanup, err := r.Git.Command(ctx, rem, dir, args...)
	if err != nil {
		return err
	}
	defer cleanup()
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.forget(repo.ID)
		text := string(out)
		if strings.Contains(text, "stale info") || strings.Contains(text, "fetch first") || strings.Contains(text, "non-fast-forward") {
			return ErrStale
		}
		return &gitremote.RunErr{Stderr: text, Code: gitremote.ExitCode(err)}
	}
	return nil
}

// apply pushes updates and records them in the mirror (console-made changes: the mirror has
// every object already).
func (r *Relay) apply(ctx context.Context, repo store.GitRepo, dir string, updates []Update) error {
	if err := r.push(ctx, repo, dir, updates); err != nil {
		return err
	}
	for _, u := range updates {
		args := []string{"update-ref", u.Ref, u.New}
		if u.New == "" {
			args = []string{"update-ref", "-d", u.Ref}
		}
		if _, err := r.Git.Local(ctx, dir, nil, args...); err != nil {
			r.forget(repo.ID)
		}
	}
	return nil
}

// CreateRef creates a branch at sha; an existing branch is left as it is.
func (r *Relay) CreateRef(ctx context.Context, repo store.GitRepo, branch, sha string) error {
	defer r.lock(repo.ID)()
	dir, err := r.syncLocked(ctx, repo, false)
	if err != nil {
		return err
	}
	if r.refSHA(ctx, dir, branch) != "" {
		return nil
	}
	return r.apply(ctx, repo, dir, []Update{{Ref: head(branch), New: sha}})
}

// UpdateRef moves a branch to sha; force=false only fast-forwards (ErrNotFF otherwise).
func (r *Relay) UpdateRef(ctx context.Context, repo store.GitRepo, branch, sha string, force bool) error {
	defer r.lock(repo.ID)()
	dir, err := r.syncLocked(ctx, repo, true)
	if err != nil {
		return err
	}
	old := r.refSHA(ctx, dir, branch)
	if old == "" {
		return ErrNoRef
	}
	if !force && !r.isAncestor(ctx, dir, old, sha) {
		return ErrNotFF
	}
	return r.apply(ctx, repo, dir, []Update{{Ref: head(branch), Old: old, New: sha}})
}

// DeleteRef deletes a branch; a missing branch is not an error.
func (r *Relay) DeleteRef(ctx context.Context, repo store.GitRepo, branch string) error {
	defer r.lock(repo.ID)()
	dir, err := r.syncLocked(ctx, repo, true)
	if err != nil {
		return err
	}
	old := r.refSHA(ctx, dir, branch)
	if old == "" {
		return nil
	}
	return r.apply(ctx, repo, dir, []Update{{Ref: head(branch), Old: old}})
}

func (r *Relay) isAncestor(ctx context.Context, dir, a, b string) bool {
	_, err := r.Git.Local(ctx, dir, nil, "merge-base", "--is-ancestor", a, b)
	return err == nil
}

// MergeResult is the outcome of Merge (like GitHub's POST /merges).
type MergeResult struct {
	SHA      string // the merge commit; "" when head was already in base
	Conflict bool
}

// Merge merges branch head into branch base with a merge commit made in the mirror
// (git merge-tree) and pushes it to the upstream with a lease on base.
func (r *Relay) Merge(ctx context.Context, repo store.GitRepo, base, headBranch, message string) (MergeResult, error) {
	defer r.lock(repo.ID)()
	dir, err := r.syncLocked(ctx, repo, true)
	if err != nil {
		return MergeResult{}, err
	}
	b, h := r.refSHA(ctx, dir, base), r.refSHA(ctx, dir, headBranch)
	if b == "" || h == "" {
		return MergeResult{}, ErrNoRef
	}
	if r.isAncestor(ctx, dir, h, b) {
		return MergeResult{}, nil
	}
	out, err := r.Git.Local(ctx, dir, nil, "merge-tree", "--write-tree", "--no-messages", b, h)
	if gitremote.ExitCode(err) == 1 {
		return MergeResult{Conflict: true}, nil
	}
	if err != nil {
		return MergeResult{}, err
	}
	tree := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	commit, err := r.Git.Local(ctx, dir, identity, "commit-tree", tree, "-p", b, "-p", h, "-m", message)
	if err != nil {
		return MergeResult{}, err
	}
	sha := strings.TrimSpace(string(commit))
	if err := r.apply(ctx, repo, dir, []Update{{Ref: head(base), Old: b, New: sha}}); err != nil {
		return MergeResult{}, err
	}
	return MergeResult{SHA: sha}, nil
}

// IsNoRef reports a missing branch.
func IsNoRef(err error) bool { return errors.Is(err, ErrNoRef) }
