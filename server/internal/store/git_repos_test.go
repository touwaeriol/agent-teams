package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/touwaeriol/agent-teams/server/internal/credvault"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

func gitCred(t *testing.T, f *sshFixture, user, host, name string) store.GitCredential {
	t.Helper()
	id := store.NewID("gc")
	sealed, err := f.vault.Seal(credvault.GitCredAAD(user, id, credvault.GitHTTPSToken, host, "oauth2"), []byte(`{"password":"tok"}`))
	if err != nil {
		t.Fatal(err)
	}
	c, err := f.s.CreateGitCredential(context.Background(), store.GitCredential{ID: id, UserID: user, Name: name, Host: host,
		Kind: credvault.GitHTTPSToken, Username: "oauth2"}, store.GitSealed{Sealed: sealed})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestGitRepos: credentials and repositories are per user; a repository may only use a credential
// of its own host; neither can be deleted while in use; the sealed value round-trips; a username
// change needs a new value; projects name standalone repositories.
func TestGitRepos(t *testing.T) {
	ctx := context.Background()
	f := newSSHFixture(t)
	c := gitCred(t, f, f.user, "gitlab.com", "GitLab")
	if _, err := f.s.GitCredential(ctx, f.other, c.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("other user sees credential: %v", err)
	}
	sec, err := f.s.GitCredentialSecret(ctx, f.user, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := f.vault.Open(credvault.GitCredAAD(f.user, c.ID, c.Kind, c.Host, c.Username), sec.Sealed)
	if err != nil || string(raw) != `{"password":"tok"}` {
		t.Fatalf("open: %q %v", raw, err)
	}
	other := "x"
	if _, err := f.s.UpdateGitCredential(ctx, f.user, c.ID, store.GitCredentialPatch{Username: &other}, nil); !errors.Is(err, store.ErrCredentialRequired) {
		t.Fatalf("username change without value: %v", err)
	}

	r := store.GitRepo{ID: store.NewID("gr"), UserID: f.user, Slug: "gitlab.com/acme/web", URL: "https://gitlab.com/acme/web.git",
		Transport: "https", Host: "gitlab.com", CredentialID: &c.ID, Provider: "gitlab"}
	if _, err := f.s.CreateGitRepo(ctx, r); err != nil {
		t.Fatal(err)
	}
	dup := r
	dup.ID = store.NewID("gr")
	if _, err := f.s.CreateGitRepo(ctx, dup); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate slug: %v", err)
	}
	wrong := store.GitRepo{ID: store.NewID("gr"), UserID: f.user, Slug: "git.example.com/a/b", URL: "https://git.example.com/a/b.git",
		Transport: "https", Host: "git.example.com", CredentialID: &c.ID, Provider: "generic"}
	if _, err := f.s.CreateGitRepo(ctx, wrong); !errors.Is(err, store.ErrHostMismatch) {
		t.Fatalf("credential of another host: %v", err)
	}
	theirs := wrong
	theirs.UserID, theirs.Host, theirs.Slug = f.other, "gitlab.com", "gitlab.com/b/c"
	if _, err := f.s.CreateGitRepo(ctx, theirs); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("other user's credential: %v", err)
	}
	if err := f.s.DeleteGitCredential(ctx, f.user, c.ID); !errors.Is(err, store.ErrInUse) {
		t.Fatalf("delete credential in use: %v", err)
	}

	if _, err := f.s.SetProjectRepos(ctx, f.user, f.project, []store.ProjectRepo{{Repo: r.Slug, GitRepoID: &r.ID}}); err != nil {
		t.Fatal(err)
	}
	p, err := f.s.ProjectOf(ctx, f.user, f.project)
	if err != nil || len(p.Repos) != 1 || !p.Repos[0].Standalone() || *p.Repos[0].GitRepoID != r.ID {
		t.Fatalf("project repos %+v %v", p.Repos, err)
	}
	if ok, _ := f.s.ProjectUsesGitRepo(ctx, f.project, r.ID); !ok {
		t.Fatal("ProjectUsesGitRepo")
	}
	used, _ := f.s.GitRepoProjects(ctx, f.user)
	if len(used[r.ID]) != 1 {
		t.Fatalf("used %+v", used)
	}
	if err := f.s.DeleteGitRepo(ctx, f.user, r.ID); !errors.Is(err, store.ErrInUse) {
		t.Fatalf("delete repo in use: %v", err)
	}
	if _, err := f.s.SetProjectRepos(ctx, f.user, f.project, nil); err != nil {
		t.Fatal(err)
	}
	if err := f.s.RecordGitRepoCheck(ctx, r.ID, store.GitRepoCheck{Access: "write", DefaultBranch: "main"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.s.GitRepo(ctx, f.user, r.ID); got.Access != "write" || got.DefaultBranch != "main" || got.LastCheckedAt == nil {
		t.Fatalf("check %+v", got)
	}
	if pinned, err := f.s.PinGitRepoHostKey(ctx, f.user, r.ID, "ssh-ed25519 AAAA", "SHA256:x", []byte{1}); err != nil || !pinned {
		t.Fatalf("pin %v %v", pinned, err)
	}
	if pinned, _ := f.s.PinGitRepoHostKey(ctx, f.user, r.ID, "ssh-ed25519 BBBB", "SHA256:y", []byte{1}); pinned {
		t.Fatal("second pin replaced the first")
	}
	if err := f.s.DeleteGitRepo(ctx, f.user, r.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.s.DeleteGitCredential(ctx, f.user, c.ID); err != nil {
		t.Fatal(err)
	}
}
