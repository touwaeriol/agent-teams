package engine_test

import (
	"strings"
	"testing"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw/nodetest"
)

func (f *fakeGitHub) dropRef(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.refs, key(defaultRepo, name))
}

func taskStatus(t *testing.T, w *world, threadID, id string) string {
	t.Helper()
	var st string
	if err := w.e.Core.Pool.Gorm(w.ctx).Table("at_tasks").Select("status").Where("thread_id = ? AND id = ?", threadID, id).
		Scan(&st).Error; err != nil {
		t.Fatal(err)
	}
	return st
}

// TestMergeFailReleasesTask: a merge entry cancelled for a non-merge reason (the thread branch
// is gone and cannot be cut again) puts its task back to blocked, so the lead can enqueue it
// again; a thread branch that only went missing is cut again from the base and the merge goes on.
func TestMergeFailReleasesTask(t *testing.T) {
	gh, ghSrv := newFakeGitHub(t)
	w := newWorld(t, ghSrv)
	b := w.signUp("mq@example.com")
	var node nodeCreated
	b.must(201, "POST", "/api/nodes", map[string]string{"name": "fake"}, &node)
	b.must(200, "GET", "/api/roles", nil, nil)
	var conn struct{ ID string }
	b.must(201, "POST", "/api/git/connections", map[string]string{"name": "gh", "token": "ghp_good"}, &conn)
	var project struct{ ID string }
	b.must(201, "POST", "/api/projects", map[string]any{"name": "web", "repos": []map[string]any{{"repo": "o/r", "git_connection_id": conn.ID}}}, &project)
	n := w.connect(node.Token, nodetest.Options{})
	events := b.stream("/api/projects/"+project.ID+"/events?topics=merge,task", "")
	var thread core.ThreadView
	b.must(201, "POST", "/api/projects/"+project.ID+"/threads", map[string]any{"title": "Login", "brief": "登录页"}, &thread)
	lead, _ := expectOpen(t, w, n, "lead-1")
	answerPrepares(t, w, n)
	lead.mustCall("update_plan", map[string]any{"steps": []map[string]string{{"title": "页面", "status": "doing"}}})
	lead.mustCall("assign_task", map[string]any{"assignee": "dev", "instruction": "写表单", "main": "页面"})
	dev, devOpen := expectOpen(t, w, n, "dev-1")
	answerPrepares(t, w, n)
	dev.send(protocol.TypeSessionDelivered, protocol.SessionDelivered{SessionID: protocol.SessionID(dev.session), MsgID: devOpen.FirstMessage.MsgID})
	completeWithPush(t, w, gh, n, dev, "t1", "sha-dev-1")
	lead.expectSend("[Task t1 · dev-1 · done]")
	lead.mustCall("enqueue_merge", map[string]any{"task_id": "t1"})
	events.wait(t, "task", `"id":"t1"`, `"status":"done"`)

	// The thread branch was deleted on GitHub: it is cut again from main and t2 merges.
	gh.dropRef("thread/1-login")
	lead.mustCall("assign_task", map[string]any{"assignee": "dev-1", "instruction": "加记住我", "main": "页面"})
	dev.expectSend("[Task t2")
	completeWithPush(t, w, gh, n, dev, "t2", "sha-dev-2")
	lead.expectSend("[Task t2 · dev-1 · done]")
	lead.mustCall("enqueue_merge", map[string]any{"task_id": "t2"})
	events.wait(t, "task", `"id":"t2"`, `"status":"done"`)
	if head, ok := gh.ref("thread/1-login"); !ok || !strings.HasPrefix(head, "sha-merge-") {
		t.Fatalf("thread branch not cut again and merged: %q %v", head, ok)
	}

	// Branch and base both gone: the entry is cancelled, t3 goes back to blocked and can be
	// enqueued again once the branch is back.
	gh.dropRef("thread/1-login")
	main, _ := gh.ref("main")
	gh.dropRef("main")
	lead.mustCall("assign_task", map[string]any{"assignee": "dev-1", "instruction": "加忘记密码", "main": "页面"})
	dev.expectSend("[Task t3")
	completeWithPush(t, w, gh, n, dev, "t3", "sha-dev-3")
	lead.expectSend("[Task t3 · dev-1 · done]")
	lead.mustCall("enqueue_merge", map[string]any{"task_id": "t3"})
	events.wait(t, "merge", `"status":"cancelled"`)
	lead.expectSend("任务 t3 回到 blocked")
	eventually(t, "t3 blocked", func() bool { return taskStatus(t, w, thread.ID, "t3") == "blocked" })
	gh.setRef("main", main)
	res := lead.mustCall("enqueue_merge", map[string]any{"task_id": "t3"})
	if !strings.Contains(text(res), "合并队列") {
		t.Fatalf("enqueue again: %q", text(res))
	}
	events.wait(t, "task", `"id":"t3"`, `"status":"done"`)
}
