package engine_test

import (
	"strings"
	"testing"

	protocol "github.com/touwaeriol/agent-teams/protocol"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw/nodetest"
)

// answerGit replies to every pending git.prepare with success (the fake checkout).
func answerPrepares(t *testing.T, w *world, n *nodetest.Node) {
	t.Helper()
	for {
		f, ok := takeNow(n, protocol.TypeGitPrepare)
		if !ok {
			return
		}
		var p protocol.GitPrepare
		_ = f.Decode(&p)
		head := "sha-prep"
		ctx, cancel := w.waitCtx()
		_, err := n.Send(ctx, protocol.TypeGitResult, protocol.GitResult{JobID: p.JobID, Ok: true, HeadSHA: &head})
		cancel()
		if err != nil {
			t.Fatal(err)
		}
	}
}

func takeNow(n *nodetest.Node, typ string) (nodetest.Frame, bool) {
	ctx, cancel := contextShort()
	defer cancel()
	f, err := n.Wait(ctx, typ, nil)
	return f, err == nil
}

// push answers the git.push of a member: the fake GitHub gets the branch, the console gets git.result.
func push(t *testing.T, w *world, gh *fakeGitHub, n *nodetest.Node, branch, sha string) protocol.GitPush {
	t.Helper()
	ctx, cancel := w.waitCtx()
	defer cancel()
	f, err := n.Wait(ctx, protocol.TypeGitPush, nil)
	if err != nil {
		t.Fatal(err)
	}
	var p protocol.GitPush
	_ = f.Decode(&p)
	if p.Refspec != "HEAD:refs/heads/"+branch || p.Repo != "o/r" || !strings.HasSuffix(string(p.Cwd), "/o__r") ||
		len(p.UnchangedAgainst) != 2 || p.UnchangedAgainst[1] != "main" {
		t.Fatalf("push %+v", p)
	}
	gh.setRef(branch, sha)
	if _, err := n.Send(ctx, protocol.TypeGitResult, protocol.GitResult{JobID: p.JobID, Ok: true, HeadSHA: &sha}); err != nil {
		t.Fatal(err)
	}
	return p
}

func credential(t *testing.T, w *world, n *nodetest.Node, req protocol.CredRequest) protocol.CredResponse {
	t.Helper()
	ctx, cancel := w.waitCtx()
	defer cancel()
	if err := n.SendUnreliable(ctx, protocol.TypeCredRequest, req); err != nil {
		t.Fatal(err)
	}
	f, err := n.Wait(ctx, protocol.TypeCredResponse, func(f nodetest.Frame) bool { return strings.Contains(string(f.Body), req.ReqID) })
	if err != nil {
		t.Fatal(err)
	}
	var r protocol.CredResponse
	_ = f.Decode(&r)
	return r
}

// TestDeliveryEndToEnd: push jobs, the merge queue with trial branches and CI, a conflict that
// becomes a fix task, the pull request, acceptance with automatic merge, and cleanup.
func TestDeliveryEndToEnd(t *testing.T) {
	gh, ghSrv := newFakeGitHub(t)
	w := newWorld(t, ghSrv)
	b := w.signUp("dev@example.com")
	var node nodeCreated
	b.must(201, "POST", "/api/nodes", map[string]string{"name": "fake"}, &node)
	b.must(200, "GET", "/api/roles", nil, nil)
	var conn struct{ ID string }
	b.must(201, "POST", "/api/git/connections", map[string]string{"name": "gh", "token": "ghp_good"}, &conn)
	var project struct{ ID string }
	b.must(201, "POST", "/api/projects", map[string]any{"name": "web", "repos": []map[string]any{{"repo": "o/r", "git_connection_id": conn.ID}}}, &project)
	n := w.connect(node.Token, nodetest.Options{})
	events := b.stream("/api/projects/"+project.ID+"/events?topics=merge,thread,task,deliverable,ci", "")

	var thread core.ThreadView
	b.must(201, "POST", "/api/projects/"+project.ID+"/threads", map[string]any{"title": "Login", "brief": "登录页"}, &thread)
	if thread.Branch != "thread/1-login" {
		t.Fatalf("branch %s", thread.Branch)
	}
	lead, leadOpen := expectOpen(t, w, n, "lead-1")
	if _, ok := gh.ref("thread/1-login"); ok {
		t.Fatal("the thread branch is created lazily, when something is merged into it")
	}
	if leadOpen.Prepare == nil || !*leadOpen.GitCredentials || leadOpen.Env["AT_PROTECTED_REFS"] != "thread/1-login main" {
		t.Fatalf("lead session needs its worktree prepared: %+v", leadOpen)
	}
	answerPrepares(t, w, n)
	lead.mustCall("update_plan", map[string]any{"steps": []map[string]string{{"title": "页面", "status": "doing"}}})
	lead.mustCall("assign_task", map[string]any{"assignee": "dev", "instruction": "写表单", "main": "页面"})
	dev, devOpen := expectOpen(t, w, n, "dev-1")
	answerPrepares(t, w, n)
	dev.send(protocol.TypeSessionDelivered, protocol.SessionDelivered{SessionID: protocol.SessionID(dev.session), MsgID: devOpen.FirstMessage.MsgID})

	callID := "tc_complete_1"
	gen := protocol.OpenGen(dev.gen)
	dev.send(protocol.TypeToolCall, protocol.ToolCall{CallID: protocol.CallID(callID), SessionID: protocol.SessionID(dev.session), OpenGen: &gen,
		Tool: "complete_task", Args: []byte(`{"task_id":"t1","summary":"表单写好了"}`)})
	pushed := push(t, w, gh, n, "thread/1/dev-1", "sha-dev-1")
	if res := dev.result(callID); !res.Ok || !strings.Contains(string(*res.Structured), `"status":"review"`) {
		t.Fatalf("complete_task after push: %+v", res)
	}
	assertCredentials(t, w, n, dev, pushed)
	lead.expectSend("[Task t1 · dev-1 · done]")

	res := lead.mustCall("enqueue_merge", map[string]any{"task_id": "t1"})
	if !strings.Contains(string(*res.Structured), `"position":1`) {
		t.Fatalf("enqueue %s", *res.Structured)
	}
	events.wait(t, "merge", `"status":"merged"`)
	events.wait(t, "task", `"id":"t1"`, `"status":"done"`)
	if head, _ := gh.ref("thread/1-login"); !strings.HasPrefix(head, "sha-merge-") || !strings.Contains(strings.Join(gh.mergeLog(), ","), "thread/1/dev-1→mq/1/") {
		t.Fatalf("trial merge not fast-forwarded: head %s log %v", head, gh.mergeLog())
	}
	lead.expectSend("[Merge · t1 · dev-1 · merged]")

	conflictAndFix(t, w, gh, n, lead, dev, events)

	// An employee already opened the thread PR with gh: open_pr adopts it instead of failing.
	if n := gh.openPRByHand("o/r", "thread/1-login", "main"); n != 7 {
		t.Fatalf("hand-made PR #%d", n)
	}
	res = lead.call("open_pr", map[string]any{"title": "登录页", "body": "完成"})
	if !res.Ok || !strings.Contains(string(*res.Structured), "pull/7") {
		t.Fatalf("open_pr %+v", res)
	}
	events.wait(t, "thread", `"status":"review"`, `"pr_number":7`)
	events.wait(t, "deliverable", `"kind":"pr"`)

	b.must(200, "POST", "/api/threads/"+thread.ID+"/accept", nil, nil)
	events.wait(t, "thread", `"status":"done"`, `"pr_state":"merged"`)
	ctx, cancel := w.waitCtx()
	defer cancel()
	if _, err := n.Wait(ctx, protocol.TypeGitCleanup, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := n.Wait(ctx, protocol.TypeSessionClose, dev.mine); err != nil {
		t.Fatal(err)
	}
	eventually(t, "member branch deleted", func() bool { _, ok := gh.ref("thread/1/dev-1"); return !ok })
	reopenAfterMerge(t, w, gh, n, b, thread.ID, lead, dev, events)
	if errs := n.Errors(); len(errs) > 0 {
		t.Fatalf("protocol errors: %+v", errs)
	}
}

// reopenAfterMerge: a thread whose PR merged is reopened. The merged PR becomes history (the
// thread has no PR, the merged thread branch is deleted on the host), the lead resumes its old
// conversation with the user's next message, new work cuts the thread branch again from the
// base and open_pr opens a second pull request.
func reopenAfterMerge(t *testing.T, w *world, gh *fakeGitHub, n *nodetest.Node, b *browser, tid string, lead, dev *fakeAgent, events *sse) {
	t.Helper()
	var got core.ThreadView
	b.must(200, "POST", "/api/threads/"+tid+"/reopen", nil, &got)
	if got.Status != "running" || got.PRNumber != nil || got.PRState != "" {
		t.Fatalf("reopened %s pr=%v %q", got.Status, got.PRNumber, got.PRState)
	}
	if len(got.Repos) != 1 || got.Repos[0].BranchCreated || got.Repos[0].PRNumber != nil {
		t.Fatalf("reopened repos %+v", got.Repos)
	}
	eventually(t, "merged thread branch deleted", func() bool { _, ok := gh.ref("thread/1-login"); return !ok })

	b.must(202, "POST", "/api/conversations/"+tid+"/messages", map[string]any{"content": "再加一个忘记密码链接"}, nil)
	lead2, open := expectOpen(t, w, n, "lead-1")
	if open.Resume == nil || open.Resume.SDKSessionID == nil || *open.Resume.SDKSessionID != "sdk-"+lead.session ||
		!strings.Contains(open.FirstMessage.Text, "重新打开") || !strings.Contains(open.FirstMessage.Text, "忘记密码") {
		t.Fatalf("lead resume %+v %q", open.Resume, open.FirstMessage.Text)
	}
	answerPrepares(t, w, n)
	lead2.send(protocol.TypeSessionDelivered, protocol.SessionDelivered{SessionID: protocol.SessionID(lead2.session), MsgID: open.FirstMessage.MsgID})
	lead2.mustCall("assign_task", map[string]any{"assignee": "dev-1", "instruction": "忘记密码链接", "main": "页面"})
	dev2, devOpen := expectOpen(t, w, n, "dev-1")
	if devOpen.Resume == nil || devOpen.Resume.SDKSessionID == nil || *devOpen.Resume.SDKSessionID != "sdk-"+dev.session {
		t.Fatalf("member resume %+v", devOpen.Resume)
	}
	answerPrepares(t, w, n)
	dev2.send(protocol.TypeSessionDelivered, protocol.SessionDelivered{SessionID: protocol.SessionID(dev2.session), MsgID: devOpen.FirstMessage.MsgID})
	completeWithPush(t, w, gh, n, dev2, "t4", "sha-dev-4")
	lead2.expectSend("[Task t4 · dev-1 · done]")
	lead2.mustCall("enqueue_merge", map[string]any{"task_id": "t4"})
	events.wait(t, "task", `"id":"t4"`, `"status":"done"`)
	if _, ok := gh.ref("thread/1-login"); !ok {
		t.Fatal("thread branch not cut again")
	}
	res := lead2.call("open_pr", map[string]any{"title": "忘记密码", "body": "补充"})
	if !res.Ok || !strings.Contains(string(*res.Structured), "pull/8") {
		t.Fatalf("second open_pr %+v", res)
	}
	events.wait(t, "thread", `"status":"review"`, `"pr_number":8`)
}

// assertCredentials: push jobs get a write token; employee sessions get the connection's token
// too (a PAT cannot be narrowed; the user's own nodes, architecture.md §10.4), only for project repositories.
func assertCredentials(t *testing.T, w *world, n *nodetest.Node, dev *fakeAgent, pushed protocol.GitPush) {
	t.Helper()
	job := pushed.JobID
	ok := credential(t, w, n, protocol.CredRequest{ReqID: "r1", JobID: &job, Host: "github.com", Path: "o/r.git", Purpose: "push"})
	if !ok.Ok || ok.Password == nil || *ok.Password != "ghp_good" {
		t.Fatalf("push job credential %+v", ok)
	}
	sid := protocol.SessionID(dev.session)
	sess := credential(t, w, n, protocol.CredRequest{ReqID: "r2", SessionID: &sid, Host: "github.com", Path: "o/r.git", Purpose: "read"})
	if !sess.Ok || sess.Password == nil || *sess.Password != "ghp_good" {
		t.Fatalf("session credential with a PAT %+v", sess)
	}
	foreign := credential(t, w, n, protocol.CredRequest{ReqID: "r4", SessionID: &sid, Host: "github.com", Path: "evil/repo.git", Purpose: "push"})
	if foreign.Ok || foreign.Error == nil || *foreign.Error != "repo_not_in_project" {
		t.Fatalf("session credential for a foreign repository %+v", foreign)
	}
	other := credential(t, w, n, protocol.CredRequest{ReqID: "r3", JobID: &job, Host: "github.com", Path: "evil/repo.git", Purpose: "push"})
	if other.Ok {
		t.Fatal("token for a foreign repository")
	}
}

// conflictAndFix: a conflicting merge makes a fix task for the member; once fixed it merges
// and completes the original task too.
func conflictAndFix(t *testing.T, w *world, gh *fakeGitHub, n *nodetest.Node, lead, dev *fakeAgent, events *sse) {
	t.Helper()
	gh.setConflict("o/r", "thread/1/dev-1", true)
	lead.mustCall("assign_task", map[string]any{"assignee": "dev-1", "instruction": "加记住我", "main": "页面"})
	dev.expectSend("[Task t2")
	completeWithPush(t, w, gh, n, dev, "t2", "sha-dev-2")
	lead.expectSend("[Task t2 · dev-1 · done]")
	lead.mustCall("enqueue_merge", map[string]any{"member": "dev-1"})
	events.wait(t, "merge", `"status":"conflict"`)
	events.wait(t, "task", `"id":"t3"`, `"kind":"fix"`)
	dev.expectSend("[Task t3")
	lead.expectSend("合并冲突")

	gh.setConflict("o/r", "thread/1/dev-1", false)
	completeWithPush(t, w, gh, n, dev, "t3", "sha-dev-3")
	lead.expectSend("[Task t3 · dev-1 · done]")
	lead.mustCall("enqueue_merge", map[string]any{"task_id": "t3"})
	events.wait(t, "task", `"id":"t3"`, `"status":"done"`)
	events.wait(t, "task", `"id":"t2"`, `"status":"done"`)
}

func completeWithPush(t *testing.T, w *world, gh *fakeGitHub, n *nodetest.Node, dev *fakeAgent, task, sha string) {
	t.Helper()
	callID := "tc_complete_" + task
	gen := protocol.OpenGen(dev.gen)
	dev.send(protocol.TypeToolCall, protocol.ToolCall{CallID: protocol.CallID(callID), SessionID: protocol.SessionID(dev.session), OpenGen: &gen,
		Tool: "complete_task", Args: []byte(`{"task_id":"` + task + `","summary":"done ` + task + `"}`)})
	push(t, w, gh, n, "thread/1/dev-1", sha)
	if res := dev.result(callID); !res.Ok {
		t.Fatalf("complete %s: %+v", task, res)
	}
}
