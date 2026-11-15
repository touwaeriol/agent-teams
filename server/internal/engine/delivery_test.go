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
	if p.Refspec != "HEAD:refs/heads/"+branch || p.Repo != "o/r" {
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
	b.must(201, "POST", "/api/projects", map[string]any{"name": "web", "repo": "o/r", "branch": "main", "git_connection_id": conn.ID}, &project)
	n := w.connect(node.Token, nodetest.Options{})
	events := b.stream("/api/projects/"+project.ID+"/events?topics=merge,thread,task,deliverable,ci", "")

	var thread core.ThreadView
	b.must(201, "POST", "/api/projects/"+project.ID+"/threads", map[string]any{"title": "Login", "brief": "登录页"}, &thread)
	if thread.Branch != "thread/1-login" {
		t.Fatalf("branch %s", thread.Branch)
	}
	eventually(t, "thread branch on GitHub", func() bool { _, ok := gh.ref("thread/1-login"); return ok })

	lead, leadOpen := expectOpen(t, w, n, "lead-1")
	if leadOpen.Prepare == nil || !*leadOpen.GitCredentials {
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
	if errs := n.Errors(); len(errs) > 0 {
		t.Fatalf("protocol errors: %+v", errs)
	}
}

// assertCredentials: push jobs get a write token, employee sessions with a PAT connection get none.
func assertCredentials(t *testing.T, w *world, n *nodetest.Node, dev *fakeAgent, pushed protocol.GitPush) {
	t.Helper()
	job := pushed.JobID
	ok := credential(t, w, n, protocol.CredRequest{ReqID: "r1", JobID: &job, Host: "github.com", Path: "o/r.git", Purpose: "push"})
	if !ok.Ok || ok.Password == nil || *ok.Password != "ghp_good" {
		t.Fatalf("push job credential %+v", ok)
	}
	sid := protocol.SessionID(dev.session)
	refused := credential(t, w, n, protocol.CredRequest{ReqID: "r2", SessionID: &sid, Host: "github.com", Path: "o/r.git", Purpose: "read"})
	if refused.Ok || refused.Error == nil || *refused.Error != "pat_not_narrowable" {
		t.Fatalf("session credential with a PAT %+v", refused)
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
	gh.mu.Lock()
	gh.conflicts["thread/1/dev-1"] = true
	gh.mu.Unlock()
	lead.mustCall("assign_task", map[string]any{"assignee": "dev-1", "instruction": "加记住我", "main": "页面"})
	dev.expectSend("[Task t2")
	completeWithPush(t, w, gh, n, dev, "t2", "sha-dev-2")
	lead.expectSend("[Task t2 · dev-1 · done]")
	lead.mustCall("enqueue_merge", map[string]any{"member": "dev-1"})
	events.wait(t, "merge", `"status":"conflict"`)
	events.wait(t, "task", `"id":"t3"`, `"kind":"fix"`)
	dev.expectSend("[Task t3")
	lead.expectSend("合并冲突")

	gh.mu.Lock()
	delete(gh.conflicts, "thread/1/dev-1")
	gh.mu.Unlock()
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
