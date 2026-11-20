package gitrelay_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/touwaeriol/agent-teams/server/internal/credvault"
	"github.com/touwaeriol/agent-teams/server/internal/gitrelay"
	"github.com/touwaeriol/agent-teams/server/internal/gitremote/gittest"
	"github.com/touwaeriol/agent-teams/server/internal/sshdial"
	"github.com/touwaeriol/agent-teams/server/internal/store"
	"github.com/touwaeriol/agent-teams/server/internal/testdb"
)

// The test binary doubles as "atctl git-hook".
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "git-hook" {
		os.Exit(gitrelay.RunHook(os.Args[2:]))
	}
	os.Exit(m.Run())
}

type fixture struct {
	relay    *gitrelay.Relay
	repo     store.GitRepo
	upstream string // bare repository on disk
	base     string // relay base URL (http://127.0.0.1:port)
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, testdb.Migrated(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := s.SaveSetting(ctx, sshdial.SettingKey, sshdial.AllowedSetting{CIDRs: []string{"127.0.0.1/32"}}); err != nil {
		t.Fatal(err)
	}
	v, err := credvault.New("test-key")
	if err != nil {
		t.Fatal(err)
	}
	u, err := s.CreateUser(ctx, "relay@example.com", "h")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	up := gittest.Bare(t, filepath.Join(root), "web")
	if err := os.MkdirAll(filepath.Join(root, "acme"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(up, filepath.Join(root, "acme", "web.git")); err != nil {
		t.Fatal(err)
	}
	up = filepath.Join(root, "acme", "web.git")
	srv := gittest.HTTPS(t, root, "oauth2", "tok")
	host := strings.TrimPrefix(srv.URL, "https://")

	cid := store.NewID("gc")
	sealed, _, err := v.SealCredential(credvault.GitCredAAD(u.ID, cid, credvault.GitHTTPSToken, host, "oauth2"), credvault.AuthPassword, credvault.Credential{Password: "tok"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateGitCredential(ctx, store.GitCredential{ID: cid, UserID: u.ID, Name: "c", Host: host, Kind: credvault.GitHTTPSToken,
		Username: "oauth2"}, store.GitSealed{Sealed: sealed}); err != nil {
		t.Fatal(err)
	}
	repo, err := s.CreateGitRepo(ctx, store.GitRepo{ID: store.NewID("gr"), UserID: u.ID, Slug: host + "/acme/web", Name: "web",
		URL: srv.URL + "/acme/web.git", Transport: "https", Host: host, CredentialID: &cid, DefaultBranch: "main", Provider: "generic"})
	if err != nil {
		t.Fatal(err)
	}
	r := &gitrelay.Relay{Store: s, Vault: v, Git: srv.Runner, Dir: filepath.Join(t.TempDir(), "mirrors")}
	exe, _ := os.Executable()
	r.Git.Exe = exe
	hs := httptest.NewServer(r)
	t.Cleanup(hs.Close)
	r.PublicURL = hs.URL
	return &fixture{relay: r, repo: repo, upstream: up, base: hs.URL}
}

func (f *fixture) url(t *testing.T, c gitrelay.Claims) string {
	t.Helper()
	c.Repo, c.User = f.repo.ID, f.repo.UserID
	tok, _, err := f.relay.Sign(c)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Replace(f.relay.URL(f.repo.ID), "http://", "http://x:"+tok+"@", 1)
}

func gitTry(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func commit(t *testing.T, dir, file, text string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Git(t, dir, "add", ".")
	gittest.Git(t, dir, "commit", "-qm", "edit "+file)
}

// TestRelayCloneAndPush: a node clones through the relay with a read token; an employee token
// pushes its branch to the upstream through the pre-receive hook but not a platform branch; a
// read token cannot push; a push job token only its ref.
func TestRelayCloneAndPush(t *testing.T) {
	f := newFixture(t)
	work := filepath.Join(t.TempDir(), "w")
	if out, err := gitTry(".", "clone", "-q", f.url(t, gitrelay.Claims{Mode: gitrelay.ModeRead}), work); err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}
	gittest.Git(t, work, "checkout", "-qb", "feature")
	commit(t, work, "b.txt", "b\n")
	if out, err := gitTry(work, "push", f.url(t, gitrelay.Claims{Mode: gitrelay.ModeRead}), "feature"); err == nil {
		t.Fatalf("read token pushed:\n%s", out)
	}
	emp := f.url(t, gitrelay.Claims{Mode: gitrelay.ModeEmployee, Deny: []string{"refs/heads/main"}})
	if out, err := gitTry(work, "push", emp, "feature"); err != nil {
		t.Fatalf("employee push: %v\n%s", err, out)
	}
	want := gittest.Git(t, work, "rev-parse", "HEAD")
	if got := gittest.Git(t, f.upstream, "rev-parse", "refs/heads/feature"); got != want {
		t.Fatalf("upstream feature = %s, want %s", got, want)
	}
	for _, ref := range []string{"main", "thread/1-x", "mq/1/x"} {
		out, err := gitTry(work, "push", emp, "+feature:refs/heads/"+ref) // forced: the client would refuse main itself
		if err == nil || !strings.Contains(out, "不允许推送") {
			t.Fatalf("pushed %s: %v\n%s", ref, err, out)
		}
		if _, err := gitTry(f.upstream, "rev-parse", "--verify", "refs/heads/"+ref); err == nil && ref != "main" {
			t.Fatalf("upstream got %s", ref)
		}
	}
	job := f.url(t, gitrelay.Claims{Mode: gitrelay.ModePush, Ref: "refs/heads/thread/1/dev"})
	if out, err := gitTry(work, "push", job, "feature:refs/heads/other"); err == nil {
		t.Fatalf("job token pushed another ref:\n%s", out)
	}
	if out, err := gitTry(work, "push", job, "feature:refs/heads/thread/1/dev"); err != nil {
		t.Fatalf("job push: %v\n%s", err, out)
	}
	// Another repository's token is refused.
	c := gitrelay.Claims{Repo: "gr_other", User: f.repo.UserID, Mode: gitrelay.ModeRead}
	tok, _, _ := f.relay.Sign(c)
	if out, err := gitTry(".", "ls-remote", strings.Replace(f.relay.URL(f.repo.ID), "http://", "http://x:"+tok+"@", 1)); err == nil {
		t.Fatalf("foreign token listed:\n%s", out)
	}
}

// TestRelayOps: the console-side operations the merge queue uses work on the upstream.
func TestRelayOps(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	r := f.relay
	main, err := r.RefSHA(ctx, f.repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.RefSHA(ctx, f.repo, "nope"); !errors.Is(err, gitrelay.ErrNoRef) {
		t.Fatalf("missing ref: %v", err)
	}
	if err := r.CreateRef(ctx, f.repo, "thread/1-x", main); err != nil {
		t.Fatal(err)
	}
	// A member pushes work to the upstream directly (as through the relay).
	work := t.TempDir()
	gittest.Git(t, work, "clone", "-q", f.upstream, ".")
	gittest.Git(t, work, "checkout", "-qb", "thread/1/dev")
	commit(t, work, "b.txt", "b\n")
	gittest.Git(t, work, "push", "-q", "origin", "thread/1/dev")
	res, err := r.Merge(ctx, f.repo, "thread/1-x", "thread/1/dev", "Merge dev")
	if err != nil || res.Conflict || res.SHA == "" {
		t.Fatalf("merge: %+v %v", res, err)
	}
	if got := gittest.Git(t, f.upstream, "rev-parse", "refs/heads/thread/1-x"); got != res.SHA {
		t.Fatalf("upstream thread/1-x = %s, want %s", got, res.SHA)
	}
	again, err := r.Merge(ctx, f.repo, "thread/1-x", "thread/1/dev", "again")
	if err != nil || again.SHA != "" {
		t.Fatalf("second merge: %+v %v", again, err)
	}
	files, err := r.Compare(ctx, f.repo, "main", "thread/1-x", 10)
	if err != nil || len(files) != 1 || files[0] != "b.txt" {
		t.Fatalf("compare: %v %v", files, err)
	}
	// Conflict: two branches edit a.txt differently.
	gittest.Git(t, work, "checkout", "-qb", "x", "main")
	commit(t, work, "a.txt", "x\n")
	gittest.Git(t, work, "checkout", "-qb", "y", "main")
	commit(t, work, "a.txt", "y\n")
	gittest.Git(t, work, "push", "-q", "origin", "x", "y")
	if res, err := r.Merge(ctx, f.repo, "x", "y", "c"); err != nil || !res.Conflict {
		t.Fatalf("conflict: %+v %v", res, err)
	}
	if err := r.UpdateRef(ctx, f.repo, "main", res.SHA, false); err != nil {
		t.Fatalf("fast-forward: %v", err)
	}
	if err := r.UpdateRef(ctx, f.repo, "x", main, false); !errors.Is(err, gitrelay.ErrNotFF) {
		t.Fatalf("non-ff update: %v", err)
	}
	if err := r.DeleteRef(ctx, f.repo, "thread/1/dev"); err != nil {
		t.Fatal(err)
	}
	if _, err := gitTry(f.upstream, "rev-parse", "--verify", "refs/heads/thread/1/dev"); err == nil {
		t.Fatal("branch still on upstream")
	}
	if err := r.DeleteRef(ctx, f.repo, "thread/1/dev"); err != nil {
		t.Fatalf("deleting a missing branch: %v", err)
	}
	text, err := r.FileContent(ctx, f.repo, "b.txt", "main")
	if err != nil || text != "b\n" {
		t.Fatalf("file: %q %v", text, err)
	}
}

// TestRelayToken: tampered and expired tokens are refused.
func TestRelayToken(t *testing.T) {
	v, _ := credvault.New("k")
	r := &gitrelay.Relay{Vault: v}
	tok, _, err := r.Sign(gitrelay.Claims{Repo: "gr_a", User: "u", Mode: gitrelay.ModeRead})
	if err != nil {
		t.Fatal(err)
	}
	if c, err := r.Verify(tok); err != nil || c.Repo != "gr_a" {
		t.Fatalf("verify: %+v %v", c, err)
	}
	if _, err := r.Verify(tok[:len(tok)-2] + "AA"); err == nil {
		t.Fatal("tampered token accepted")
	}
	old, _, _ := r.Sign(gitrelay.Claims{Repo: "gr_a", User: "u", Mode: gitrelay.ModeRead, Exp: 1})
	if _, err := r.Verify(old); err == nil {
		t.Fatal("expired token accepted")
	}
	other, _ := credvault.New("other")
	if _, err := (&gitrelay.Relay{Vault: other}).Verify(tok); err == nil {
		t.Fatal("token of another key accepted")
	}
	if id, ok := gitrelay.RepoOfPath("git/gr_abc.git"); !ok || id != "gr_abc" {
		t.Fatalf("path: %q %v", id, ok)
	}
}
