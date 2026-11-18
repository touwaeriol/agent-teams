package engine_test

import (
	"context"
	"strings"
	"testing"
	"time"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw/nodetest"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// setRoleCount changes how many members a role template counts (the project's staff).
func (tm *team) setRoleCount(t *testing.T, role string, n int) {
	t.Helper()
	var roles struct{ Roles []store.Role }
	tm.b.must(200, "GET", "/api/roles", nil, &roles)
	for i := range roles.Roles {
		if roles.Roles[i].Role == role {
			roles.Roles[i].Count = n
		}
	}
	tm.b.must(200, "PUT", "/api/roles", map[string]any{"roles": roles.Roles}, nil)
}

// sql runs a statement on the test database.
func (tm *team) sql(t *testing.T, stmt string, args ...any) {
	t.Helper()
	if _, err := store.Exec(tm.w.ctx, tm.w.e.Core.Pool, stmt, args...); err != nil {
		t.Fatal(err)
	}
}

// kick asks for a scheduling pass over the project's queue.
func (tm *team) kick() { tm.w.e.Sched.Kick(tm.w.ctx, tm.projectID) }

// expectClosed waits for the session.close of an agent's session and returns its reason.
func expectClosed(t *testing.T, w *world, n *nodetest.Node, a *fakeAgent) string {
	t.Helper()
	ctx, cancel := w.waitCtx()
	defer cancel()
	f, err := n.Wait(ctx, protocol.TypeSessionClose, a.mine)
	if err != nil {
		t.Fatal(err)
	}
	var c protocol.SessionClose
	_ = f.Decode(&c)
	if c.Reason == nil {
		return ""
	}
	return string(*c.Reason)
}

// noOpen asserts that no session opens for name within a moment.
func noOpen(t *testing.T, n *nodetest.Node, name string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	if _, err := n.Wait(ctx, protocol.TypeSessionOpen, func(f nodetest.Frame) bool {
		return strings.Contains(string(f.Body), `"name":"`+name+`"`)
	}); err == nil {
		t.Fatalf("%s got a session", name)
	}
}

// taskRow loads a task of a thread.
func (tm *team) taskRow(t *testing.T, threadID, id string) core.Task {
	t.Helper()
	task, err := core.TaskByID(tm.w.ctx, tm.w.e.Core.Pool, threadID, id)
	if err != nil {
		t.Fatal(err)
	}
	return task
}

// TestThreadStickiness: two threads share dev-1 and dev-2. A member that frees up takes the
// pending task of the thread it works in, before an older one of the other thread, and keeps
// its session there.
func TestThreadStickiness(t *testing.T) {
	tm := sharedTeam(t)
	one, lead1 := tm.openThread(t, "One", nil)
	lead1.mustCall("assign_task", map[string]any{"assignee": "dev", "instruction": "one a", "main": "做"})
	dev1, _ := startTask(t, tm, "dev-1", "one a")
	two, lead2 := tm.openThread(t, "Two", nil)
	lead2.mustCall("assign_task", map[string]any{"assignee": "dev", "instruction": "two a", "main": "做"})
	dev2, _ := startTask(t, tm, "dev-2", "two a")

	lead1.mustCall("assign_task", map[string]any{"assignee": "dev", "instruction": "one b", "main": "做"})
	lead2.mustCall("assign_task", map[string]any{"assignee": "dev", "instruction": "two b", "main": "做"})
	dev2.mustCall("complete_task", map[string]any{"task_id": "t1", "summary": "two a done"})
	dev2.expectSend("two b")
	if task := tm.taskRow(t, one.ID, "t2"); task.Status != "pending" {
		t.Fatalf("one b went to %s (%s)", task.ClaimedBy, task.Status)
	}
	dev1.mustCall("complete_task", map[string]any{"task_id": "t1", "summary": "one a done"})
	dev1.expectSend("one b")
	if task := tm.taskRow(t, two.ID, "t2"); task.ClaimedBy != "dev-2" {
		t.Fatalf("two b claimed by %q", task.ClaimedBy)
	}
	if errs := tm.n.Errors(); len(errs) > 0 {
		t.Fatalf("protocol errors: %+v", errs)
	}
}

// TestDependencyStickiness: the task that builds on dev-2's work goes to dev-2, although dev-1
// (first in order) is idle too.
func TestDependencyStickiness(t *testing.T) {
	tm := sharedTeam(t)
	th, lead := tm.openThread(t, "Deps", nil)
	lead.mustCall("assign_task", map[string]any{"assignee": "dev", "instruction": "base a", "main": "做"})
	dev1, _ := startTask(t, tm, "dev-1", "base a")
	lead.mustCall("assign_task", map[string]any{"assignee": "dev", "instruction": "base b", "main": "做"})
	dev2, _ := startTask(t, tm, "dev-2", "base b")
	lead.mustCall("assign_task", map[string]any{"assignee": "dev", "instruction": "on top of b", "main": "做", "depends_on": []string{"t2"}})

	dev1.mustCall("complete_task", map[string]any{"task_id": "t1", "summary": "a done"})
	lead.expectSend("[Task t1 · dev-1 · done]")
	if task := tm.taskRow(t, th.ID, "t3"); task.Status != "pending" || task.ReadyAt != nil {
		t.Fatalf("t3 before its dependency: %s ready %v", task.Status, task.ReadyAt)
	}
	dev2.mustCall("complete_task", map[string]any{"task_id": "t2", "summary": "b done", "handoff": "接口在 api.go"})
	dev2.expectSend("on top of b")
	if task := tm.taskRow(t, th.ID, "t3"); task.ClaimedBy != "dev-2" || task.ReadyAt == nil {
		t.Fatalf("t3 claimed by %q ready %v", task.ClaimedBy, task.ReadyAt)
	}
}

// TestStarvationAndThreadSwitch: with one dev, a task that waited past the starvation limit in
// thread #2 goes before the next task of dev-1's own thread. Moving to #2 closes its session
// (switch_thread) and opens a fresh one in #2's worktree with a digest; its tools act on #2.
// Back in #1 the digest lists what it did there before.
func TestStarvationAndThreadSwitch(t *testing.T) {
	tm := sharedTeam(t)
	tm.setRoleCount(t, "dev", 1)
	_, lead1 := tm.openThread(t, "One", nil)
	lead1.mustCall("assign_task", map[string]any{"assignee": "dev", "instruction": "first of one", "main": "做"})
	dev1, open1 := startTask(t, tm, "dev-1", "first of one")
	if strings.Contains(open1.FirstMessage.Text, "[换讨论串]") {
		t.Fatalf("first session has a switch digest: %s", open1.FirstMessage.Text)
	}
	two, lead2 := tm.openThread(t, "Two", nil)
	lead2.mustCall("assign_task", map[string]any{"assignee": "dev", "instruction": "waiting in two", "main": "做"})
	lead1.mustCall("assign_task", map[string]any{"assignee": "dev", "instruction": "second of one", "main": "做"})
	tm.sql(t, `UPDATE at_tasks SET ready_at = now() - interval '6 minutes' WHERE thread_id = $1 AND id = 't1'`, two.ID)

	dev1.mustCall("complete_task", map[string]any{"task_id": "t1", "summary": "one done"})
	if reason := expectClosed(t, tm.w, tm.n, dev1); reason != "switch_thread" {
		t.Fatalf("close reason %q", reason)
	}
	moved, open2 := startTask(t, tm, "dev-1", "waiting in two")
	first := open2.FirstMessage.Text
	if open2.Cwd != protocol.RelPath("projects/"+tm.projectID+"/threads/2/dev-1") || open2.Resume != nil ||
		!strings.HasPrefix(first, "[换讨论串]") || !strings.Contains(first, "#2「Two」") || !strings.Contains(first, "lead-2") ||
		!strings.Contains(first, "thread/2/dev-1") {
		t.Fatalf("dev-1 in #2: cwd %s resume %+v first %s", open2.Cwd, open2.Resume, first)
	}
	if got := text(moved.mustCall("get_task", map[string]any{"task_id": "t1"})); !strings.Contains(got, "waiting in two") {
		t.Fatalf("get_task t1 from #2: %s", got)
	}
	if team := text(moved.mustCall("list_team", map[string]any{})); !strings.Contains(team, "lead-2") || strings.Contains(team, "lead-1") {
		t.Fatalf("list_team from #2: %s", team)
	}
	moved.mustCall("complete_task", map[string]any{"task_id": "t1", "summary": "two done"})
	lead2.expectSend("[Task t1 · dev-1 · done] two done")

	if reason := expectClosed(t, tm.w, tm.n, moved); reason != "switch_thread" {
		t.Fatalf("close reason back to #1 %q", reason)
	}
	back, open3 := startTask(t, tm, "dev-1", "second of one")
	if open3.Cwd != protocol.RelPath("projects/"+tm.projectID+"/threads/1/dev-1") || open3.Resume != nil ||
		!strings.Contains(open3.FirstMessage.Text, "t1 [done] first of one → one done") {
		t.Fatalf("dev-1 back in #1: cwd %s first %s", open3.Cwd, open3.FirstMessage.Text)
	}
	back.mustCall("complete_task", map[string]any{"task_id": "t2", "summary": "second done"})
	lead1.expectSend("[Task t2 · dev-1 · done] second done")
}

// TestReworkReservedAfterRejection: a task the reviewer rejects goes back to the queue reserved
// for its member; while that member is busy another idle dev leaves it alone, and once the
// reservation runs out the other dev takes it over with the verdict and the original branch.
func TestReworkReservedAfterRejection(t *testing.T) {
	gh, ghSrv := newFakeGitHub(t)
	w := newWorld(t, ghSrv)
	tm := &team{w: w, b: w.signUp("rework@example.com")}
	var node nodeCreated
	tm.b.must(201, "POST", "/api/nodes", map[string]string{"name": "fake"}, &node)
	var roles struct{ Roles []store.Role }
	tm.b.must(200, "GET", "/api/roles", nil, &roles)
	reviewer := roles.Roles[2]
	reviewer.ID, reviewer.Role, reviewer.Title, reviewer.Count, reviewer.Gate = "", "reviewer", "审查", 1, "review"
	tm.b.must(200, "PUT", "/api/roles", map[string]any{"roles": append(roles.Roles, reviewer)}, nil)
	var conn struct{ ID string }
	tm.b.must(201, "POST", "/api/git/connections", map[string]string{"name": "gh", "token": "ghp_good"}, &conn)
	var project struct{ ID string }
	tm.b.must(201, "POST", "/api/projects", map[string]any{"name": "web",
		"repos": []map[string]any{{"repo": "o/r", "git_connection_id": conn.ID}}}, &project)
	tm.projectID = project.ID
	tm.n = w.connect(node.Token, nodetest.Options{})
	n := tm.n

	var th core.ThreadView
	tm.b.must(201, "POST", "/api/projects/"+project.ID+"/threads", map[string]any{"title": "Login", "brief": "登录页"}, &th)
	lead, _ := expectOpen(t, w, n, "lead-1")
	answerPrepares(t, w, n)
	lead.mustCall("update_plan", map[string]any{"steps": []map[string]string{{"title": "页面", "status": "doing"}}})
	lead.mustCall("assign_task", map[string]any{"assignee": "dev", "instruction": "写表单", "main": "页面"})
	dev1, open := expectOpen(t, w, n, "dev-1")
	answerPrepares(t, w, n)
	dev1.send(protocol.TypeSessionDelivered, protocol.SessionDelivered{SessionID: protocol.SessionID(dev1.session), MsgID: open.FirstMessage.MsgID})
	completeWithPush(t, w, gh, n, dev1, "t1", "sha-dev-1")

	rev, revOpen := expectOpen(t, w, n, "reviewer-1")
	answerPrepares(t, w, n)
	if !strings.Contains(revOpen.FirstMessage.Text, "审查 dev-1 的任务 t1") {
		t.Fatalf("review brief %s", revOpen.FirstMessage.Text)
	}
	rev.send(protocol.TypeSessionDelivered, protocol.SessionDelivered{SessionID: protocol.SessionID(rev.session), MsgID: revOpen.FirstMessage.MsgID})
	lead.mustCall("assign_task", map[string]any{"assignee": "dev", "instruction": "加记住我", "main": "页面"})
	answerPrepares(t, w, n)
	dev1.expectSend("加记住我")

	rev.mustCall("complete_task", map[string]any{"task_id": "t2", "status": "failed", "summary": "缺少输入校验"})
	lead.expectSend("预留给 dev-1 返工")
	if task := tm.taskRow(t, th.ID, "t1"); task.Status != "pending" || task.ReservedFor != "dev-1" || task.ClaimedBy != "" {
		t.Fatalf("rejected t1: %s reserved %q claimed %q", task.Status, task.ReservedFor, task.ClaimedBy)
	}
	tm.kick()
	noOpen(t, n, "dev-2")

	tm.sql(t, `UPDATE at_tasks SET reserved_until = now() - interval '1 second' WHERE thread_id = $1 AND id = 't1'`, th.ID)
	tm.kick()
	dev2, open2 := expectOpen(t, w, n, "dev-2")
	answerPrepares(t, w, n)
	brief := open2.FirstMessage.Text
	for _, part := range []string{"[Task t1", "缺少输入校验", "这是 dev-1 的返工", "git merge origin/thread/1/dev-1", "你的分支：thread/1/dev-2"} {
		if !strings.Contains(brief, part) {
			t.Fatalf("takeover brief lacks %q: %s", part, brief)
		}
	}
	dev2.send(protocol.TypeSessionDelivered, protocol.SessionDelivered{SessionID: protocol.SessionID(dev2.session), MsgID: open2.FirstMessage.MsgID})
	if task := tm.taskRow(t, th.ID, "t1"); task.ClaimedBy != "dev-2" || task.Branch != "thread/1/dev-2" {
		t.Fatalf("taken over t1: %q %s", task.ClaimedBy, task.Branch)
	}
	if errs := n.Errors(); len(errs) > 0 {
		t.Fatalf("protocol errors: %+v", errs)
	}
}
