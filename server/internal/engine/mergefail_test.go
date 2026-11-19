package engine_test

import (
	"fmt"
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

// mergeCase is a thread in o/r whose first task t1 (dev-1) is merged into the thread branch.
type mergeCase struct {
	w         *world
	gh        *fakeGitHub
	n         *nodetest.Node
	b         *browser
	lead, dev *fakeAgent
	events    *sse
	thread    core.ThreadView
	projectID string
	next      int
}

func newMergeCase(t *testing.T, delivery map[string]any) *mergeCase {
	t.Helper()
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
	if delivery != nil {
		b.must(200, "PATCH", "/api/projects/"+project.ID, map[string]any{"delivery": delivery}, nil)
	}
	c := &mergeCase{w: w, gh: gh, b: b, projectID: project.ID, next: 1}
	c.n = w.connect(node.Token, nodetest.Options{})
	c.events = b.stream("/api/projects/"+project.ID+"/events?topics=merge,task", "")
	b.must(201, "POST", "/api/projects/"+project.ID+"/threads", map[string]any{"title": "Login", "brief": "登录页"}, &c.thread)
	c.lead, _ = expectOpen(t, w, c.n, "lead-1")
	answerPrepares(t, w, c.n)
	c.lead.mustCall("update_plan", map[string]any{"steps": []map[string]string{{"title": "页面", "status": "doing"}}})
	c.lead.mustCall("assign_task", map[string]any{"assignee": "dev", "instruction": "写表单", "main": "页面"})
	var devOpen protocol.SessionOpen
	c.dev, devOpen = expectOpen(t, w, c.n, "dev-1")
	answerPrepares(t, w, c.n)
	c.dev.send(protocol.TypeSessionDelivered, protocol.SessionDelivered{SessionID: protocol.SessionID(c.dev.session), MsgID: devOpen.FirstMessage.MsgID})
	completeWithPush(t, w, gh, c.n, c.dev, "t1", "sha-dev-1")
	c.lead.expectSend("[Task t1 · dev-1 · done]")
	c.lead.mustCall("enqueue_merge", map[string]any{"task_id": "t1"})
	c.events.wait(t, "task", `"id":"t1"`, `"status":"done"`)
	return c
}

// task assigns the next task to dev-1, completes it with a push and enqueues it; its id.
func (c *mergeCase) task(t *testing.T, instruction string) string {
	t.Helper()
	c.next++
	id := fmt.Sprintf("t%d", c.next)
	c.lead.mustCall("assign_task", map[string]any{"assignee": "dev-1", "instruction": instruction, "main": "页面"})
	c.dev.expectSend("[Task " + id)
	completeWithPush(t, c.w, c.gh, c.n, c.dev, id, "sha-dev-"+id)
	c.lead.expectSend("[Task " + id + " · dev-1 · done]")
	c.lead.mustCall("enqueue_merge", map[string]any{"task_id": id})
	return id
}

// TestMergeFailReleasesTask: a thread branch deleted before its PR merged is cut again from the
// base and the lead is told which merged tasks may be missing (t1); a merge entry cancelled for a
// non-merge reason (branch and base both gone) puts its task back to blocked, so the lead can
// enqueue it again.
func TestMergeFailReleasesTask(t *testing.T) {
	c := newMergeCase(t, nil)
	c.gh.dropRef("thread/1-login")
	t2 := c.task(t, "加记住我")
	note := c.lead.expectSend("已从 main 重新切出")
	if !strings.Contains(note.Text, "讨论串分支 thread/1-login 在仓库 o/r 上不存在") || !strings.Contains(note.Text, "t1（dev-1，成员分支 thread/1/dev-1）") ||
		!strings.Contains(note.Text, "不能再 enqueue_merge") {
		t.Fatalf("recreate note %q", note.Text)
	}
	c.events.wait(t, "task", `"id":"`+t2+`"`, `"status":"done"`)
	if head, ok := c.gh.ref("thread/1-login"); !ok || !strings.HasPrefix(head, "sha-merge-") {
		t.Fatalf("thread branch not cut again and merged: %q %v", head, ok)
	}

	// Branch and base both gone: the entry is cancelled, the task goes back to blocked and can be
	// enqueued again once the branch is back.
	c.gh.dropRef("thread/1-login")
	main, _ := c.gh.ref("main")
	c.gh.dropRef("main")
	t3 := c.task(t, "加忘记密码")
	c.events.wait(t, "merge", `"status":"cancelled"`)
	c.lead.expectSend("任务 " + t3 + " 回到 blocked")
	eventually(t, t3+" blocked", func() bool { return taskStatus(t, c.w, c.thread.ID, t3) == "blocked" })
	c.gh.setRef("main", main)
	res := c.lead.mustCall("enqueue_merge", map[string]any{"task_id": t3})
	if !strings.Contains(text(res), "合并队列") {
		t.Fatalf("enqueue again: %q", text(res))
	}
	c.events.wait(t, "task", `"id":"`+t3+`"`, `"status":"done"`)
}

// TestRecreateAfterMergedPR: the thread branch went with its merged PR: it is cut again, the lead
// is told, and no task is named (their changes are in the base).
func TestRecreateAfterMergedPR(t *testing.T) {
	c := newMergeCase(t, nil)
	err := c.w.e.Core.InTx(c.w.ctx, func(tx *core.Tx) error {
		if err := core.SetThreadRepoPR(c.w.ctx, tx, c.thread.ID, "o/r", 7, prURL("o/r", 7)); err != nil {
			return err
		}
		return core.SetThreadRepoPRState(c.w.ctx, tx, c.thread.ID, "o/r", "merged")
	})
	if err != nil {
		t.Fatal(err)
	}
	c.gh.dropRef("thread/1-login")
	t2 := c.task(t, "加记住我")
	note := c.lead.expectSend("已从 main 重新切出")
	if !strings.Contains(note.Text, "PR 已经合并") || strings.Contains(note.Text, "t1（") {
		t.Fatalf("recreate note %q", note.Text)
	}
	c.events.wait(t, "task", `"id":"`+t2+`"`, `"status":"done"`)
}

// TestDirectMergeRecreatesBranch: with the merge queue gate off the member branch is merged into
// the thread branch directly; a missing thread branch is cut again the same way.
func TestDirectMergeRecreatesBranch(t *testing.T) {
	c := newMergeCase(t, map[string]any{"gates": map[string]any{"merge_queue": false}})
	c.gh.dropRef("thread/1-login")
	t2 := c.task(t, "加记住我")
	note := c.lead.expectSend("已从 main 重新切出")
	if !strings.Contains(note.Text, "t1（dev-1，成员分支 thread/1/dev-1）") {
		t.Fatalf("recreate note %q", note.Text)
	}
	c.events.wait(t, "task", `"id":"`+t2+`"`, `"status":"done"`)
	if strings.Contains(strings.Join(c.gh.mergeLog(), ","), "mq/") {
		t.Fatalf("a trial branch with the gate off: %v", c.gh.mergeLog())
	}
}
