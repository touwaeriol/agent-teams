// Package gitrelay is the control plane's Git relay for standalone repositories (git-repos.md §5):
// one bare mirror per repository, served to nodes over smart HTTP (git http-backend) with
// short-lived relay tokens; pushes go through a pre-receive hook that forwards them to the
// upstream with the credential only the console holds. The merge queue and thread branches use
// the same mirrors (Merge, UpdateRef…), so the upstream needs nothing beyond plain git.
package gitrelay

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/credvault"
	"github.com/touwaeriol/agent-teams/server/internal/gitremote"
	"github.com/touwaeriol/agent-teams/server/internal/sshdial"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// Relay serves and maintains the mirrors.
type Relay struct {
	Store     *store.Store
	Vault     *credvault.Vault
	Git       gitremote.Git
	Dir       string // mirrors root (GIT_MIRROR_DIR); one <repo_id>.git per repository
	PublicURL string // the console's public base, e.g. https://agent.example.com
	Log       *slog.Logger
	// Fresh is how long a fetched mirror is served without asking the upstream again (10 s).
	Fresh time.Duration
	Now   func() time.Time

	mu     sync.Mutex
	locks  map[string]*sync.Mutex
	synced map[string]time.Time
}

// Errors the engines map to their own outcomes.
var (
	ErrNoRef    = errors.New("分支不存在")
	ErrNotFF    = errors.New("不是快进更新")
	ErrStale    = errors.New("上游的分支已经变了，请重新获取后再推送")
	ErrDisabled = errors.New("这个控制面没有配置 Git 中转（GIT_MIRROR_DIR）")
)

func (r *Relay) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Relay) log() *slog.Logger {
	if r.Log != nil {
		return r.Log
	}
	return slog.Default()
}

// Enabled reports whether the relay can serve (a mirror directory and a vault).
func (r *Relay) Enabled() bool { return r != nil && r.Dir != "" && r.Vault.Ready() }

// URL is the relay URL nodes clone a repository from.
func (r *Relay) URL(repoID string) string {
	return strings.TrimRight(r.PublicURL, "/") + "/git/" + repoID + ".git"
}

// Host is the host (with port when not default) of the relay URLs, as git's credential
// protocol names it.
func (r *Relay) Host() string {
	u, err := url.Parse(r.PublicURL)
	if err != nil {
		return ""
	}
	return u.Host
}

var repoPath = regexp.MustCompile(`^/?git/(gr_[a-z0-9]+)(\.git)?/?$`)

// RepoOfPath is the repository id of a relay path as git's credential helper sends it
// ("git/gr_x.git"); ok=false for anything else.
func RepoOfPath(path string) (string, bool) {
	m := repoPath.FindStringSubmatch(path)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// Policy is the address policy for upstream hosts: the admin's allowed private networks.
func Policy(ctx context.Context, st *store.Store) sshdial.Policy {
	var set sshdial.AllowedSetting
	_, _ = st.Setting(ctx, sshdial.SettingKey, &set)
	allowed, err := sshdial.ParseAllowed(set)
	if err != nil {
		allowed = nil
	}
	return sshdial.Policy{Allowed: allowed}
}

// OpenRemote opens a saved repository's credential and checks its pinned host key's MAC: the
// upstream as the console uses it (test connection, relay, merge queue).
func OpenRemote(ctx context.Context, st *store.Store, v *credvault.Vault, policy sshdial.Policy, userID string, r store.GitRepo) (gitremote.Remote, error) {
	remote := gitremote.Remote{URL: r.URL, Transport: r.Transport, HostKey: r.HostKey, Policy: policy}
	if r.HostKey != "" {
		if ok, _ := v.VerifyMAC(credvault.GitHostKeyMACData(r.ID, r.Host, r.HostKey), r.HostKeyMAC); !ok {
			return remote, errors.New("已固定的主机密钥校验不通过（可能被直接改过数据库），请重置主机密钥后重新测试")
		}
	}
	if r.CredentialID == nil {
		return remote, nil
	}
	sec, err := st.GitCredentialSecret(ctx, userID, *r.CredentialID)
	if err != nil {
		return remote, err
	}
	cred, err := v.OpenCredential(credvault.GitCredAAD(userID, sec.ID, sec.Kind, sec.Host, sec.Username), sec.Sealed)
	if err != nil {
		return remote, errors.New("保存的凭据解不开（主密钥变过，或数据被改动过），请重新填写凭据")
	}
	remote.Kind, remote.Username, remote.Cred = sec.Kind, sec.Username, cred
	return remote, nil
}

func (r *Relay) remote(ctx context.Context, repo store.GitRepo) (gitremote.Remote, error) {
	if repo.Transport == "ssh" && repo.HostKey == "" {
		return gitremote.Remote{}, errors.New("SSH 仓库还没有固定主机密钥：请先在设置里测试连接")
	}
	return OpenRemote(ctx, r.Store, r.Vault, Policy(ctx, r.Store), repo.UserID, repo)
}

// lock serialises everything that changes one mirror (sync, receive-pack, console pushes).
func (r *Relay) lock(id string) func() {
	r.mu.Lock()
	if r.locks == nil {
		r.locks, r.synced = map[string]*sync.Mutex{}, map[string]time.Time{}
	}
	m, ok := r.locks[id]
	if !ok {
		m = &sync.Mutex{}
		r.locks[id] = m
	}
	r.mu.Unlock()
	m.Lock()
	return m.Unlock
}

func (r *Relay) mirror(id string) string { return filepath.Join(r.Dir, id+".git") }

// ensure creates the bare mirror when it does not exist yet.
func (r *Relay) ensure(ctx context.Context, id string) (string, error) {
	if !r.Enabled() {
		return "", ErrDisabled
	}
	dir := r.mirror(id)
	if _, err := os.Stat(filepath.Join(dir, "HEAD")); err == nil {
		return dir, nil
	}
	if err := os.MkdirAll(r.Dir, 0o700); err != nil {
		return "", err
	}
	if _, err := r.Git.Local(ctx, r.Dir, nil, "init", "--bare", "--quiet", dir); err != nil {
		return "", fmt.Errorf("init mirror: %w", err)
	}
	return dir, nil
}

// syncLocked fetches the upstream's branches into the mirror unless it was fetched within
// Fresh (force skips that). The caller holds the repository's lock.
func (r *Relay) syncLocked(ctx context.Context, repo store.GitRepo, force bool) (string, error) {
	dir, err := r.ensure(ctx, repo.ID)
	if err != nil {
		return "", err
	}
	fresh := r.Fresh
	if fresh == 0 {
		fresh = 10 * time.Second
	}
	r.mu.Lock()
	last := r.synced[repo.ID]
	r.mu.Unlock()
	if !force && r.now().Sub(last) < fresh {
		return dir, nil
	}
	rem, err := r.remote(ctx, repo)
	if err != nil {
		return "", err
	}
	if _, err := r.Git.Run(ctx, rem, dir, "fetch", "--prune", "--no-tags", "--quiet", rem.URL, "+refs/heads/*:refs/heads/*"); err != nil {
		return "", err
	}
	if b := repo.DefaultBranch; b != "" {
		_, _ = r.Git.Local(ctx, dir, nil, "symbolic-ref", "HEAD", "refs/heads/"+b)
	}
	r.mu.Lock()
	r.synced[repo.ID] = r.now()
	r.mu.Unlock()
	return dir, nil
}

// Sync brings the mirror up to date (see syncLocked) and returns its directory.
func (r *Relay) Sync(ctx context.Context, repo store.GitRepo, force bool) (string, error) {
	defer r.lock(repo.ID)()
	return r.syncLocked(ctx, repo, force)
}

// forget makes the next sync fetch again (the upstream changed under the mirror).
func (r *Relay) forget(id string) {
	r.mu.Lock()
	delete(r.synced, id)
	r.mu.Unlock()
}

// Remove deletes a repository's mirror (the repository was deleted).
func (r *Relay) Remove(id string) error {
	if r.Dir == "" {
		return nil
	}
	defer r.lock(id)()
	r.forget(id)
	return os.RemoveAll(r.mirror(id))
}
