package engine_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	protocol "github.com/touwaeriol/agent-teams/protocol"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/credvault"
	"github.com/touwaeriol/agent-teams/server/internal/engine"
	"github.com/touwaeriol/agent-teams/server/internal/gitrelay"
	"github.com/touwaeriol/agent-teams/server/internal/gitremote/gittest"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw/nodetest"
	"github.com/touwaeriol/agent-teams/server/internal/sshdial"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// TestStandaloneRepoEndToEnd: a project on a standalone Git repository (no GitHub). Nodes clone
// the upstream itself and get its credential for the employee's git (sessions and git jobs of
// the project only, by host and path; the session lists the host in git_hosts); the merge queue
// merges the member's branch into the thread branch on the real upstream through the console's
// mirror; CI degrades to off; open_pr says there are no PRs and finish ends the thread on its
// branch (delivered_sha).
func TestStandaloneRepoEndToEnd(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "acme"), 0o755); err != nil {
		t.Fatal(err)
	}
	bare := gittest.Bare(t, filepath.Join(root, "acme"), "web")
	up := gittest.HTTPS(t, root, "oauth2", "upstream-secret")
	relay := &gitrelay.Relay{Git: up.Runner, Dir: filepath.Join(t.TempDir(), "mirrors")}
	w := newWorldWith(t, nil, func(e *engine.Engine) {
		relay.Store, relay.Vault = e.Store, testVault(t)
		e.GH.Relay = relay
	})
	ctx := w.ctx
	if err := w.db.SaveSetting(ctx, sshdial.SettingKey, sshdial.AllowedSetting{CIDRs: []string{"127.0.0.1/32"}}); err != nil {
		t.Fatal(err)
	}
	b := w.signUp("git@example.com")
	var me struct{ ID string }
	b.must(200, "GET", "/api/me", nil, &me)
	repo := standaloneRepo(t, w, me.ID, up.URL)

	var node nodeCreated
	b.must(201, "POST", "/api/nodes", map[string]string{"name": "fake"}, &node)
	b.must(200, "GET", "/api/roles", nil, nil)
	var project struct{ ID string }
	b.must(201, "POST", "/api/projects", map[string]any{"name": "web", "merge_policy": "manual",
		"repos": []map[string]any{{"repo": repo.Slug, "git_repo_id": repo.ID}}}, &project)
	n := w.connect(node.Token, nodetest.Options{})
	events := b.stream("/api/projects/"+project.ID+"/events?topics=merge,thread,task", "")

	var thread core.ThreadView
	b.must(201, "POST", "/api/projects/"+project.ID+"/threads", map[string]any{"title": "Login", "brief": "登录页"}, &thread)
	lead, leadOpen := expectOpen(t, w, n, "lead-1")
	host := strings.TrimPrefix(up.URL, "https://")
	if len(leadOpen.GitHosts) != 1 || leadOpen.GitHosts[0].Host != host || leadOpen.GitHosts[0].Transport != "https" {
		t.Fatalf("session git_hosts %+v", leadOpen.GitHosts)
	}
	upstreamURL := up.URL + "/acme/web.git"
	prep := takePrepare(t, w, n)
	if string(prep.Repo) != "git/"+repo.ID || prep.RemoteURL == nil || *prep.RemoteURL != upstreamURL {
		t.Fatalf("prepare names the upstream: repo %s remote %v", prep.Repo, prep.RemoteURL)
	}
	answerPrepares(t, w, n)
	lead.mustCall("update_plan", map[string]any{"steps": []map[string]string{{"title": "页面", "status": "doing"}}})
	lead.mustCall("assign_task", map[string]any{"assignee": "dev", "instruction": "写表单", "main": "页面"})
	dev, devOpen := expectOpen(t, w, n, "dev-1")
	answerPrepares(t, w, n)
	dev.send(protocol.TypeSessionDelivered, protocol.SessionDelivered{SessionID: protocol.SessionID(dev.session), MsgID: devOpen.FirstMessage.MsgID})

	// The member's session gets the upstream credential for this repository only.
	sid := protocol.SessionID(dev.session)
	sess := credential(t, w, n, protocol.CredRequest{ReqID: "r1", SessionID: &sid, Host: host, Path: "acme/web.git", Purpose: "read"})
	if !sess.Ok || strOf(sess.Password) != "upstream-secret" || strOf(sess.Username) != "oauth2" || sess.ExpiresAt == nil {
		t.Fatalf("session credential %+v", sess)
	}
	if wrong := credential(t, w, n, protocol.CredRequest{ReqID: "r2", SessionID: &sid, Host: "evil.test", Path: "acme/web.git", Purpose: "read"}); wrong.Ok {
		t.Fatalf("credential for another host %+v", wrong)
	}
	if other := credential(t, w, n, protocol.CredRequest{ReqID: "r2b", SessionID: &sid, Host: host, Path: "acme/other.git", Purpose: "read"}); other.Ok {
		t.Fatalf("credential for another repository %+v", other)
	}

	// complete_task → git.push: the node's push is simulated by pushing to the upstream directly.
	callID := "tc_complete_1"
	gen := protocol.OpenGen(dev.gen)
	dev.send(protocol.TypeToolCall, protocol.ToolCall{CallID: protocol.CallID(callID), SessionID: sid, OpenGen: &gen,
		Tool: "complete_task", Args: []byte(`{"task_id":"t1","summary":"表单写好了"}`)})
	pushed := takePush(t, w, n)
	if string(pushed.Repo) != "git/"+repo.ID || pushed.RemoteURL == nil || *pushed.RemoteURL != upstreamURL {
		t.Fatalf("push names the upstream: %+v", pushed)
	}
	job := protocol.JobID(pushed.JobID)
	if jobCred := credential(t, w, n, protocol.CredRequest{ReqID: "r3", JobID: &job, Host: host, Path: "acme/web.git", Purpose: "push"}); strOf(jobCred.Password) != "upstream-secret" {
		t.Fatalf("push job credential %+v", jobCred)
	}
	sha := memberCommit(t, bare, "thread/1/dev-1")
	ctxW, cancel := w.waitCtx()
	if _, err := n.Send(ctxW, protocol.TypeGitResult, protocol.GitResult{JobID: pushed.JobID, Ok: true, HeadSHA: &sha}); err != nil {
		t.Fatal(err)
	}
	cancel()
	if res := dev.result(callID); !res.Ok {
		t.Fatalf("complete_task: %+v", res)
	}
	lead.expectSend("[Task t1 · dev-1 · done]")

	lead.mustCall("enqueue_merge", map[string]any{"task_id": "t1"})
	events.wait(t, "merge", `"status":"merged"`)
	head := gittest.Git(t, bare, "rev-parse", "refs/heads/"+thread.Branch)
	if parents := gittest.Git(t, bare, "rev-list", "--parents", "-n1", head); !strings.Contains(parents, sha) {
		t.Fatalf("thread branch %s does not merge %s: %s", thread.Branch, sha, parents)
	}

	if res := lead.call("open_pr", map[string]any{"title": "登录页", "body": "完成"}); res.Ok || !strings.Contains(resultText(res), "独立 Git 仓库") {
		t.Fatalf("open_pr on a standalone repository: %+v", res)
	}
	if res := lead.call("finish", map[string]any{"summary": "登录页完成"}); !res.Ok || !strings.Contains(resultText(res), thread.Branch) {
		t.Fatalf("finish %+v", res)
	}
	events.wait(t, "thread", `"status":"done"`)
	tr, err := core.ThreadRepoOf(ctx, w.e.Core.Pool, thread.ID, repo.Slug)
	if err != nil || tr.DeliveredSHA == nil || *tr.DeliveredSHA != head {
		t.Fatalf("delivered sha %+v %v (head %s)", tr.DeliveredSHA, err, head)
	}
	if errs := n.Errors(); len(errs) > 0 {
		t.Fatalf("protocol errors: %+v", errs)
	}
}

// standaloneRepo saves a token credential and the upstream acme/web as the user's repository.
func standaloneRepo(t *testing.T, w *world, userID, upstream string) store.GitRepo {
	t.Helper()
	host := strings.TrimPrefix(upstream, "https://")
	v := testVault(t)
	cid := store.NewID("gc")
	sealed, _, err := v.SealCredential(credvault.GitCredAAD(userID, cid, credvault.GitHTTPSToken, host, "oauth2"), credvault.AuthPassword,
		credvault.Credential{Password: "upstream-secret"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.db.CreateGitCredential(w.ctx, store.GitCredential{ID: cid, UserID: userID, Name: "up", Host: host,
		Kind: credvault.GitHTTPSToken, Username: "oauth2"}, store.GitSealed{Sealed: sealed}); err != nil {
		t.Fatal(err)
	}
	r, err := w.db.CreateGitRepo(w.ctx, store.GitRepo{ID: store.NewID("gr"), UserID: userID, Slug: host + "/acme/web", Name: "web",
		URL: upstream + "/acme/web.git", Transport: "https", Host: host, CredentialID: &cid, DefaultBranch: "main", Provider: "generic"})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// takePrepare waits for a git.prepare and answers it.
func takePrepare(t *testing.T, w *world, n *nodetest.Node) protocol.GitPrepare {
	t.Helper()
	ctx, cancel := w.waitCtx()
	defer cancel()
	f, err := n.Wait(ctx, protocol.TypeGitPrepare, nil)
	if err != nil {
		t.Fatal(err)
	}
	var p protocol.GitPrepare
	_ = f.Decode(&p)
	head := "sha-prep"
	if _, err := n.Send(ctx, protocol.TypeGitResult, protocol.GitResult{JobID: p.JobID, Ok: true, HeadSHA: &head}); err != nil {
		t.Fatal(err)
	}
	return p
}

func takePush(t *testing.T, w *world, n *nodetest.Node) protocol.GitPush {
	t.Helper()
	ctx, cancel := w.waitCtx()
	defer cancel()
	f, err := n.Wait(ctx, protocol.TypeGitPush, nil)
	if err != nil {
		t.Fatal(err)
	}
	var p protocol.GitPush
	_ = f.Decode(&p)
	return p
}

// memberCommit puts one commit on branch in the upstream (what the node's push would have done).
func memberCommit(t *testing.T, bare, branch string) string {
	t.Helper()
	work := t.TempDir()
	gittest.Git(t, work, "clone", "-q", bare, ".")
	gittest.Git(t, work, "checkout", "-qb", branch)
	if err := os.WriteFile(filepath.Join(work, "login.html"), []byte("<form></form>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Git(t, work, "add", ".")
	gittest.Git(t, work, "commit", "-qm", "login form")
	gittest.Git(t, work, "push", "-q", "origin", branch)
	return gittest.Git(t, work, "rev-parse", "HEAD")
}

func resultText(r protocol.ToolResult) string {
	var b strings.Builder
	for _, c := range r.Content {
		b.WriteString(c.Text)
	}
	return b.String()
}

func strOf(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
