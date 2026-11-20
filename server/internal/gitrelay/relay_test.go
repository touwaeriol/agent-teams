package gitrelay_test

import (
	"context"
	"errors"
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

type fixture struct {
	relay    *gitrelay.Relay
	repo     store.GitRepo
	upstream string // bare repository on disk
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
	return &fixture{relay: r, repo: repo, upstream: up}
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
	// A member pushes work to the upstream directly (as employees do with the handed-out credential).
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
