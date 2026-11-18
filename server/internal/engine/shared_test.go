package engine_test

import (
	"fmt"
	"strings"
	"testing"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw/nodetest"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// sharedTeam is a project with one node and the default roles (dev × 2), no thread yet.
func sharedTeam(t *testing.T) *team {
	t.Helper()
	w := newWorld(t, nil)
	tm := &team{w: w, b: w.signUp("shared@example.com")}
	var node nodeCreated
	tm.b.must(201, "POST", "/api/nodes", map[string]string{"name": "fake"}, &node)
	tm.token = node.Token
	tm.b.must(200, "GET", "/api/roles", nil, nil)
	var project struct{ ID string }
	tm.b.must(201, "POST", "/api/projects", map[string]any{"name": "p"}, &project)
	tm.projectID = project.ID
	tm.n = w.connect(node.Token, nodetest.Options{})
	return tm
}

// openThread opens a thread through the API and returns it with its lead's session.
func (tm *team) openThread(t *testing.T, title string, body map[string]any) (core.ThreadView, *fakeAgent) {
	t.Helper()
	if body == nil {
		body = map[string]any{}
	}
	body["title"], body["brief"] = title, "do "+title
	var th core.ThreadView
	tm.b.must(201, "POST", "/api/projects/"+tm.projectID+"/threads", body, &th)
	lead, _ := expectOpen(t, tm.w, tm.n, fmt.Sprintf("lead-%d", th.No))
	lead.mustCall("update_plan", map[string]any{"steps": []map[string]string{{"title": "做", "status": "todo"}}})
	return th, lead
}

// startTask waits for the member's session.open carrying a task brief and confirms its delivery.
func startTask(t *testing.T, tm *team, name, needle string) (*fakeAgent, protocol.SessionOpen) {
	t.Helper()
	a, open := expectOpen(t, tm.w, tm.n, name)
	if open.FirstMessage == nil || !strings.Contains(open.FirstMessage.Text, needle) {
		t.Fatalf("%s first message %+v", name, open.FirstMessage)
	}
	a.send(protocol.TypeSessionDelivered, protocol.SessionDelivered{SessionID: protocol.SessionID(a.session), MsgID: open.FirstMessage.MsgID})
	return a, open
}

func memberNames(v core.ThreadView) string {
	names := make([]string, len(v.Members))
	for i, m := range v.Members {
		names[i] = m.Name
	}
	return strings.Join(names, ",")
}

// TestSharedMembersAcrossThreads: threads get only a lead; the project's dev-1 and dev-2 work
// for both threads (a busy dev-1 leaves the second thread's task to dev-2); dev-1 then takes a
// task of thread #2, its thread #1 session is closed and a new one opens in #2's worktree.
func TestSharedMembersAcrossThreads(t *testing.T) {
	tm := sharedTeam(t)
	one, lead1 := tm.openThread(t, "One", nil)
	if memberNames(one) != "lead-1" {
		t.Fatalf("new thread members %s", memberNames(one))
	}
	lead1.mustCall("assign_task", map[string]any{"assignee": "dev", "instruction": "task of one", "main": "做"})
	dev1, open1 := startTask(t, tm, "dev-1", "task of one")
	if open1.Cwd != protocol.RelPath("projects/"+tm.projectID+"/threads/1/dev-1") {
		t.Fatalf("dev-1 cwd %s", open1.Cwd)
	}

	two, lead2 := tm.openThread(t, "Two", nil)
	lead2.mustCall("assign_task", map[string]any{"assignee": "dev", "instruction": "first of two", "main": "做"})
	dev2, _ := startTask(t, tm, "dev-2", "first of two")
	dev1.mustCall("complete_task", map[string]any{"task_id": "t1", "summary": "one done"})
	lead1.expectSend("[Task t1 · dev-1 · done] one done")
	dev2.mustCall("complete_task", map[string]any{"task_id": "t1", "summary": "two done"})
	lead2.expectSend("[Task t1 · dev-2 · done] two done")

	lead2.mustCall("assign_task", map[string]any{"assignee": "dev-1", "instruction": "second of two", "main": "做"})
	ctx, cancel := tm.w.waitCtx()
	defer cancel()
	if _, err := tm.n.Wait(ctx, protocol.TypeSessionClose, dev1.mine); err != nil {
		t.Fatal(err)
	}
	moved, open2 := startTask(t, tm, "dev-1", "second of two")
	if open2.Cwd != protocol.RelPath("projects/"+tm.projectID+"/threads/2/dev-1") || open2.Resume != nil {
		t.Fatalf("dev-1 in #2: cwd %s resume %+v", open2.Cwd, open2.Resume)
	}
	moved.mustCall("complete_task", map[string]any{"task_id": "t2", "summary": "second done"})
	lead2.expectSend("[Task t2 · dev-1 · done] second done")

	tm.b.must(200, "GET", "/api/threads/"+one.ID, nil, &one)
	tm.b.must(200, "GET", "/api/threads/"+two.ID, nil, &two)
	if memberNames(one) != "lead-1,dev-1" || memberNames(two) != "lead-2,dev-1,dev-2" {
		t.Fatalf("participants #1 %s #2 %s", memberNames(one), memberNames(two))
	}
	var n int
	if err := tm.w.e.Core.Pool.Gorm(tm.w.ctx).Raw(`SELECT count(*) FROM at_employees WHERE project_id = ? AND tier = 'member'`,
		tm.projectID).Scan(&n).Error; err != nil || n != 2 {
		t.Fatalf("members %d %v", n, err)
	}
	if errs := tm.n.Errors(); len(errs) > 0 {
		t.Fatalf("protocol errors: %+v", errs)
	}
}

// TestOnlyRolesAndRolesSaved: a thread opened for some roles refuses tasks for the others;
// saving the role templates re-staffs the project (a third dev appears).
func TestOnlyRolesAndRolesSaved(t *testing.T) {
	tm := sharedTeam(t)
	var roles struct{ Roles []store.Role }
	tm.b.must(200, "GET", "/api/roles", nil, &roles)
	qa := roles.Roles[2]
	qa.ID, qa.Role, qa.Title, qa.Count = "", "qa", "测试", 1
	tm.b.must(200, "PUT", "/api/roles", map[string]any{"roles": append(roles.Roles, qa)}, &roles)
	_, lead := tm.openThread(t, "Only", map[string]any{"roles": []map[string]any{{"role": "dev", "count": 5}}})
	res := lead.call("assign_task", map[string]any{"assignee": "qa", "instruction": "test it", "main": "做"})
	if res.Ok || !strings.Contains(text(res), "这个讨论串不用这个岗位") {
		t.Fatalf("qa task in a dev thread: %+v", res)
	}
	lead.mustCall("assign_task", map[string]any{"assignee": "dev", "instruction": "build it", "main": "做"})
	dev, _ := startTask(t, tm, "dev-1", "build it")
	if team := text(dev.mustCall("list_team", map[string]any{})); strings.Contains(team, "qa-1") || !strings.Contains(team, "dev-2") {
		t.Fatalf("list_team %s", team)
	}

	roles.Roles[2].Count = 3
	tm.b.must(200, "PUT", "/api/roles", map[string]any{"roles": roles.Roles}, nil)
	var staff string
	if err := tm.w.e.Core.Pool.Gorm(tm.w.ctx).Raw(`SELECT string_agg(name || ':' || active, ' ' ORDER BY name) FROM at_employees
		WHERE project_id = ? AND thread_id IS NULL AND tier = 'member'`, tm.projectID).Scan(&staff).Error; err != nil {
		t.Fatal(err)
	}
	if staff != "dev-1:true dev-2:true dev-3:true qa-1:true" {
		t.Fatalf("staff after save %s", staff)
	}
}

// TestLegacyThreadMembers: a thread with a member created before members were project staff
// keeps it: "dev-1" there means the legacy member (named tasks go to it and it completes them),
// while pooled tasks go to the project's members.
func TestLegacyThreadMembers(t *testing.T) {
	tm := sharedTeam(t)
	th, lead := tm.openThread(t, "Old", nil)
	if _, err := store.Exec(tm.w.ctx, tm.w.e.Core.Pool, `INSERT INTO at_employees (id, project_id, thread_id, name, tier, role, runtime, spec)
		SELECT 'em_legacy', project_id, $1, name, tier, role, runtime, spec FROM at_employees
		WHERE project_id = $2 AND thread_id IS NULL AND name = 'dev-1'`, th.ID, tm.projectID); err != nil {
		t.Fatal(err)
	}
	lead.mustCall("assign_task", map[string]any{"assignee": "dev", "instruction": "pooled work", "main": "做"})
	pooled, _ := startTask(t, tm, "dev-2", "pooled work")
	lead.mustCall("assign_task", map[string]any{"assignee": "dev-1", "instruction": "legacy work", "main": "做"})
	legacy, _ := startTask(t, tm, "dev-1", "legacy work")
	var owner string
	if err := tm.w.e.Core.Pool.Gorm(tm.w.ctx).Raw(`SELECT employee_id FROM at_agent_sessions WHERE id = ?`, legacy.session).
		Scan(&owner).Error; err != nil || owner != "em_legacy" {
		t.Fatalf("named task went to %s %v", owner, err)
	}
	legacy.mustCall("complete_task", map[string]any{"task_id": "t2", "summary": "legacy done"})
	lead.expectSend("[Task t2 · dev-1 · done] legacy done")
	pooled.mustCall("complete_task", map[string]any{"task_id": "t1", "summary": "pooled done"})
	lead.expectSend("[Task t1 · dev-2 · done] pooled done")
	tm.b.must(200, "GET", "/api/threads/"+th.ID, nil, &th)
	if memberNames(th) != "lead-1,dev-1,dev-2" || th.Members[1].ID != "em_legacy" {
		t.Fatalf("participants %+v", th.Members)
	}
}
