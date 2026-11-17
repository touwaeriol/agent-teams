package engine_test

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	protocol "github.com/touwaeriol/agent-teams/protocol"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw/nodetest"
)

// prepares collects the git.prepare jobs queued for one employee checkout (one per repository),
// answers them and returns them by repository.
func prepares(t *testing.T, w *world, n *nodetest.Node, want int) map[string]protocol.GitPrepare {
	t.Helper()
	out := map[string]protocol.GitPrepare{}
	for len(out) < want {
		ctx, cancel := w.waitCtx()
		f, err := n.Wait(ctx, protocol.TypeGitPrepare, nil)
		cancel()
		if err != nil {
			t.Fatalf("waiting for %d prepares, got %d: %v", want, len(out), err)
		}
		var p protocol.GitPrepare
		_ = f.Decode(&p)
		out[string(p.Repo)] = p
		head := "sha-prep"
		ctx, cancel = w.waitCtx()
		_, err = n.Send(ctx, protocol.TypeGitResult, protocol.GitResult{JobID: p.JobID, Ok: true, HeadSHA: &head})
		cancel()
		if err != nil {
			t.Fatal(err)
		}
	}
	return out
}

// pushRepos answers the git.push jobs of one call: a sha pushes that repository (the fake GitHub
// gets the branch), "" reports it unchanged.
func pushRepos(t *testing.T, w *world, gh *fakeGitHub, n *nodetest.Node, shas map[string]string) map[string]protocol.GitPush {
	t.Helper()
	out := map[string]protocol.GitPush{}
	for len(out) < len(shas) {
		ctx, cancel := w.waitCtx()
		f, err := n.Wait(ctx, protocol.TypeGitPush, nil)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		var p protocol.GitPush
		_ = f.Decode(&p)
		out[string(p.Repo)] = p
		res := protocol.GitResult{JobID: p.JobID, Ok: true}
		if sha := shas[string(p.Repo)]; sha != "" {
			gh.setRefIn(string(p.Repo), strings.TrimPrefix(p.Refspec, "HEAD:refs/heads/"), sha)
			res.HeadSHA = &sha
		} else {
			unchanged, head := true, "sha-base"
			res.Unchanged, res.HeadSHA = &unchanged, &head
		}
		_, err = n.Send(ctx, protocol.TypeGitResult, res)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func completeTask(t *testing.T, w *world, gh *fakeGitHub, n *nodetest.Node, dev *fakeAgent, task string, shas map[string]string) (protocol.ToolResult, map[string]protocol.GitPush) {
	t.Helper()
	callID := "tc_complete_" + task
	gen := protocol.OpenGen(dev.gen)
	dev.send(protocol.TypeToolCall, protocol.ToolCall{CallID: protocol.CallID(callID), SessionID: protocol.SessionID(dev.session), OpenGen: &gen,
		Tool: "complete_task", Args: []byte(`{"task_id":"` + task + `","summary":"done ` + task + `"}`)})
	pushes := pushRepos(t, w, gh, n, shas)
	return dev.result(callID), pushes
}

// TestDeliveryTwoRepos: a project with two repositories. Checkouts per repository, a base branch
// given by the lead for one of them, thread branches created only where something merges, a merge
// queue entry per changed repository, one PR per repository (each to its own base), acceptance
// merging both, credentials scoped to the job's repository and cleanup per shared clone. A PR that
// merges while the other is still open emits a thread event (repos[].pr_state), both when the
// platform merges it on acceptance and when a pull_request webhook reports it.
func TestDeliveryTwoRepos(t *testing.T) {
	t.Run("accept", func(t *testing.T) { deliveryTwoRepos(t, false) })
	t.Run("webhook", func(t *testing.T) { deliveryTwoRepos(t, true) })
}

func deliveryTwoRepos(t *testing.T, webhook bool) {
	gh, ghSrv := newFakeGitHub(t)
	w := newWorld(t, ghSrv)
	b := w.signUp("multi@example.com")
	var node nodeCreated
	b.must(201, "POST", "/api/nodes", map[string]string{"name": "fake"}, &node)
	b.must(200, "GET", "/api/roles", nil, nil)
	var conn struct{ ID string }
	b.must(201, "POST", "/api/git/connections", map[string]string{"name": "gh", "token": "ghp_good"}, &conn)
	var project struct {
		ID    string
		Repos []struct{ Repo string }
	}
	b.must(201, "POST", "/api/projects", map[string]any{"name": "shop", "context": "仓库 o/web 的基准分支：dev",
		"repos": []map[string]any{{"repo": "o/r", "git_connection_id": conn.ID}, {"repo": "o/web", "git_connection_id": conn.ID}}}, &project)
	if len(project.Repos) != 2 || project.Repos[1].Repo != "o/web" {
		t.Fatalf("project repos %+v", project.Repos)
	}
	n := w.connect(node.Token, nodetest.Options{})
	events := b.stream("/api/projects/"+project.ID+"/events?topics=merge,thread,task,deliverable", "")

	var thread core.ThreadView
	b.must(201, "POST", "/api/projects/"+project.ID+"/threads", map[string]any{"title": "Cart", "brief": "购物车"}, &thread)
	lead, leadOpen := expectOpen(t, w, n, "lead-1")
	leadPreps := prepares(t, w, n, 2)
	assertLeadCheckouts(t, leadOpen, leadPreps)
	if !strings.Contains(*leadOpen.Claude.SystemAppend, "o/web：目录 ./o__web") || !strings.Contains(*leadOpen.Claude.SystemAppend, "仓库 o/web 的基准分支：dev") {
		t.Fatalf("lead prompt lacks the repositories / project context:\n%s", *leadOpen.Claude.SystemAppend)
	}

	lead.mustCall("update_plan", map[string]any{"steps": []map[string]string{{"title": "前端", "status": "doing"}}})
	if res := lead.call("assign_task", map[string]any{"assignee": "dev", "instruction": "x", "main": "前端", "repo": "o/nope"}); res.Ok {
		t.Fatal("assign_task accepted a repository the project does not bind")
	}
	lead.mustCall("assign_task", map[string]any{"assignee": "dev", "instruction": "改购物车页面", "main": "前端", "repo": "o/web", "base": "dev"})
	dev, devOpen := expectOpen(t, w, n, "dev-1")
	devPreps := prepares(t, w, n, 2)
	if *devPreps["o/web"].Base != "dev" || *devPreps["o/r"].Base != "main" || devPreps["o/web"].Branch != "thread/1/dev-1" ||
		string(*devPreps["o/web"].Shared) != "projects/"+project.ID+"/repos/o__web" {
		t.Fatalf("member checkouts start from each repository's base: %+v", devPreps)
	}
	if !strings.Contains(devOpen.FirstMessage.Text, "[仓库 o/web · 基准分支 dev]") {
		t.Fatalf("task text names the repository: %s", devOpen.FirstMessage.Text)
	}
	dev.send(protocol.TypeSessionDelivered, protocol.SessionDelivered{SessionID: protocol.SessionID(dev.session), MsgID: devOpen.FirstMessage.MsgID})

	res, pushes := completeTask(t, w, gh, n, dev, "t1", map[string]string{"o/r": "", "o/web": "sha-web-1"})
	if !res.Ok || !strings.Contains(string(*res.Structured), `"pushed":[{"repo":"o/web","head_sha":"sha-web-1"}]`) {
		t.Fatalf("complete_task pushed only the changed repository: %+v %s", res, deref(res.Structured))
	}
	if pushes["o/web"].UnchangedAgainst[1] != "dev" || pushes["o/r"].UnchangedAgainst[1] != "main" {
		t.Fatalf("push jobs compare with each repository's base: %+v", pushes)
	}
	assertJobCredentials(t, w, n, pushes["o/web"])
	lead.expectSend("改动的仓库：o/web")

	out := lead.mustCall("enqueue_merge", map[string]any{"task_id": "t1"})
	if !strings.Contains(string(*out.Structured), `"repo":"o/web"`) || strings.Contains(string(*out.Structured), `"repo":"o/r"`) {
		t.Fatalf("one queue entry for the changed repository: %s", *out.Structured)
	}
	events.wait(t, "task", `"id":"t1"`, `"status":"done"`)
	if sha, ok := gh.refIn("o/web", "thread/1-cart"); !ok || !strings.HasPrefix(sha, "sha-merge-") {
		t.Fatalf("thread branch in o/web: %q %v", sha, ok)
	}
	if !strings.Contains(strings.Join(gh.mergeLog(), ","), "o/web:thread/1/dev-1→mq/1/") {
		t.Fatalf("trial merge in o/web: %v", gh.mergeLog())
	}
	if _, ok := gh.refIn("o/r", "thread/1-cart"); ok {
		t.Fatal("no thread branch in a repository the thread did not change")
	}

	bothRepos(t, w, gh, n, lead, dev, events)

	pr := lead.call("open_pr", map[string]any{"title": "购物车", "body": "完成"})
	if !pr.Ok || !strings.Contains(string(*pr.Structured), "o/web/pull/") || !strings.Contains(string(*pr.Structured), "o/r/pull/") {
		t.Fatalf("open_pr opens one PR per changed repository: %+v %s", pr, deref(pr.Structured))
	}
	if web, _ := gh.prOf("o/web", "thread/1-cart"); web.base != "dev" {
		t.Fatalf("o/web PR base %q", web.base)
	}
	if api, _ := gh.prOf("o/r", "thread/1-cart"); api.base != "main" {
		t.Fatalf("o/r PR base %q", api.base)
	}
	events.wait(t, "thread", `"status":"review"`)
	var view core.ThreadView
	b.must(200, "GET", "/api/threads/"+thread.ID, nil, &view)
	if len(view.Repos) != 2 || countPRs(view.Repos) != 2 {
		t.Fatalf("thread tracks both PRs: %+v", view.Repos)
	}

	if webhook {
		webPR, _ := gh.prOf("o/web", "thread/1-cart")
		payload, _ := json.Marshal(map[string]any{"action": "closed", "pull_request": map[string]any{
			"html_url": prURL("o/web", webPR.number), "merged": true}})
		gh.mu.Lock()
		gh.pulls["o/web#"+strconv.Itoa(webPR.number)].merged = true
		gh.mu.Unlock()
		if err := w.e.Delivery.GitHubEvent(w.ctx, "pull_request", payload); err != nil {
			t.Fatal(err)
		}
		// Thread-level pr_state stays open: "merged" can only be o/web's repos[] entry.
		events.wait(t, "thread", `"status":"review"`, `"pr_state":"open"`, `"pr_state":"merged"`)
	}
	b.must(200, "POST", "/api/threads/"+thread.ID+"/accept", nil, nil)
	if !webhook {
		// After the first mergeOne: one repository merged, the other still being accepted.
		events.wait(t, "thread", `"status":"review"`, `"pr_state":"accepting"`, `"pr_state":"merged"`)
	}
	events.wait(t, "thread", `"status":"done"`, `"pr_state":"merged"`)
	eventually(t, "thread done", func() bool { // the needles above also match a partial-merge frame
		var v core.ThreadView
		b.must(200, "GET", "/api/threads/"+thread.ID, nil, &v)
		return v.Status == "done"
	})
	if web, _ := gh.prOf("o/web", "thread/1-cart"); !web.merged {
		t.Fatal("o/web PR not merged")
	}
	if api, _ := gh.prOf("o/r", "thread/1-cart"); !api.merged {
		t.Fatal("o/r PR not merged")
	}
	assertCleanupPerRepo(t, w, n, project.ID)
}

func deref(p *json.RawMessage) string {
	if p == nil {
		return ""
	}
	return string(*p)
}

func countPRs(repos []core.ThreadRepoView) int {
	n := 0
	for _, r := range repos {
		if r.PRNumber != nil {
			n++
		}
	}
	return n
}

func assertLeadCheckouts(t *testing.T, open protocol.SessionOpen, preps map[string]protocol.GitPrepare) {
	t.Helper()
	if len(open.PrepareJobs) != 2 || open.Prepare == nil || *open.Prepare != open.PrepareJobs[0] {
		t.Fatalf("session.open waits for both checkouts: %v %v", open.Prepare, open.PrepareJobs)
	}
	if !strings.HasSuffix(string(preps["o/r"].Cwd), "/threads/1/lead/o__r") || !strings.HasSuffix(string(preps["o/web"].Cwd), "/threads/1/lead/o__web") ||
		preps["o/r"].Branch != "thread/1-cart" || *preps["o/web"].Base != "main" {
		t.Fatalf("lead checkouts %+v", preps)
	}
}

// bothRepos: a task that changes both repositories gets a queue entry in each and is done only
// when both merged; o/r's thread branch is cut from its default branch.
func bothRepos(t *testing.T, w *world, gh *fakeGitHub, n *nodetest.Node, lead, dev *fakeAgent, events *sse) {
	t.Helper()
	lead.mustCall("assign_task", map[string]any{"assignee": "dev-1", "instruction": "接口和页面一起改", "main": "前端"})
	dev.expectSend("[Task t2")
	if res, _ := completeTask(t, w, gh, n, dev, "t2", map[string]string{"o/r": "sha-r-2", "o/web": "sha-web-2"}); !res.Ok {
		t.Fatalf("complete t2 %+v", res)
	}
	out := lead.mustCall("enqueue_merge", map[string]any{"member": "dev-1"})
	if !strings.Contains(string(*out.Structured), `"repo":"o/web"`) || !strings.Contains(string(*out.Structured), `"repo":"o/r"`) {
		t.Fatalf("entries for both repositories: %s", *out.Structured)
	}
	events.wait(t, "task", `"id":"t2"`, `"status":"done"`)
	if _, ok := gh.refIn("o/r", "thread/1-cart"); !ok {
		t.Fatal("thread branch in o/r after its first merge")
	}
	if !strings.Contains(strings.Join(gh.mergeLog(), ","), "o/r:thread/1/dev-1→mq/1/") {
		t.Fatalf("trial merge in o/r: %v", gh.mergeLog())
	}
}

// assertJobCredentials: a push job gets a token for its own repository only.
func assertJobCredentials(t *testing.T, w *world, n *nodetest.Node, job protocol.GitPush) {
	t.Helper()
	id := job.JobID
	ok := credential(t, w, n, protocol.CredRequest{ReqID: "m1", JobID: &id, Host: "github.com", Path: "o/web.git", Purpose: "push"})
	if !ok.Ok {
		t.Fatalf("push job credential for its repository %+v", ok)
	}
	other := credential(t, w, n, protocol.CredRequest{ReqID: "m2", JobID: &id, Host: "github.com", Path: "o/r.git", Purpose: "push"})
	if other.Ok || *other.Error != "repo_not_in_project" {
		t.Fatalf("push job of o/web got a token for o/r: %+v", other)
	}
}

// assertCleanupPerRepo: worktrees are removed from each repository's shared clone.
func assertCleanupPerRepo(t *testing.T, w *world, n *nodetest.Node, projectID string) {
	t.Helper()
	shared := map[string]bool{}
	for len(shared) < 2 {
		ctx, cancel := w.waitCtx()
		f, err := n.Wait(ctx, protocol.TypeGitCleanup, nil)
		cancel()
		if err != nil {
			t.Fatalf("cleanup jobs %v: %v", shared, err)
		}
		var c protocol.GitCleanup
		_ = f.Decode(&c)
		shared[string(c.Shared)] = true
		for _, p := range c.Paths {
			if !strings.HasSuffix(string(p), "/"+strings.TrimPrefix(string(c.Shared), "projects/"+projectID+"/repos/")) {
				t.Fatalf("path %s cleaned against %s", p, c.Shared)
			}
		}
	}
	if !shared["projects/"+projectID+"/repos/o__r"] || !shared["projects/"+projectID+"/repos/o__web"] {
		t.Fatalf("cleanup per shared clone %v", shared)
	}
}
