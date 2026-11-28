package engine_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/engine"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw/nodetest"
	"github.com/touwaeriol/agent-teams/server/internal/orchapi"
)

// ghTeam is sharedTeam with a GitHub repository (o/r) on the fake GitHub.
func ghTeam(t *testing.T) (*team, *fakeGitHub) {
	t.Helper()
	gh, ghSrv := newFakeGitHub(t)
	w := newWorld(t, ghSrv)
	tm := &team{w: w, b: w.signUp("shared-gh@example.com")}
	var node nodeCreated
	tm.b.must(201, "POST", "/api/nodes", map[string]string{"name": "fake"}, &node)
	tm.token = node.Token
	tm.b.must(200, "GET", "/api/roles", nil, nil)
	var conn struct{ ID string }
	tm.b.must(201, "POST", "/api/git/connections", map[string]string{"name": "gh", "token": "ghp_good"}, &conn)
	var project struct{ ID string }
	tm.b.must(201, "POST", "/api/projects", map[string]any{"name": "web", "merge_policy": "manual",
		"repos": []map[string]any{{"repo": "o/r", "git_connection_id": conn.ID}}}, &project)
	tm.projectID = project.ID
	tm.n = w.connect(node.Token, nodetest.Options{})
	return tm, gh
}

// ghThread opens a thread whose lead assigns one dev task; returns the thread and the member
// that took the task.
func ghThread(t *testing.T, tm *team, title, member string) (core.ThreadView, *fakeAgent, *fakeAgent) {
	t.Helper()
	var th core.ThreadView
	tm.b.must(201, "POST", "/api/projects/"+tm.projectID+"/threads", map[string]any{"title": title, "brief": "do " + title}, &th)
	lead, _ := expectOpen(t, tm.w, tm.n, fmt.Sprintf("lead-%d", th.No))
	answerPrepares(t, tm.w, tm.n)
	lead.mustCall("update_plan", map[string]any{"steps": []map[string]string{{"title": "做", "status": "todo"}}})
	lead.mustCall("assign_task", map[string]any{"assignee": "dev", "instruction": "work of " + title, "main": "做"})
	dev, _ := startTask(t, tm, member, "work of "+title)
	answerPrepares(t, tm.w, tm.n)
	return th, lead, dev
}

// TestThreadEndCleansProjectMembers: archiving thread #1 closes the sessions opened there (its
// lead and dev-1), clears dev-1's current thread, cleans dev-1's checkout of #1 and deletes its
// #1 branch, while dev-2's work in #2 is untouched; deleting #1 purges its directory and keeps
// the project members.
func TestThreadEndCleansProjectMembers(t *testing.T) {
	tm, gh := ghTeam(t)
	one, lead1, dev1 := ghThread(t, tm, "One", "dev-1")
	two, _, dev2 := ghThread(t, tm, "Two", "dev-2")
	gh.setRef("thread/1/dev-1", "sha-1")
	gh.setRef("thread/2/dev-2", "sha-2")

	tm.b.must(200, "POST", "/api/threads/"+one.ID+"/archive", nil, nil)
	ctx, cancel := tm.w.waitCtx()
	defer cancel()
	for _, a := range []*fakeAgent{lead1, dev1} {
		if _, err := tm.n.Wait(ctx, protocol.TypeSessionClose, a.mine); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tm.n.Wait(ctx, protocol.TypeGitCleanup, func(f nodetest.Frame) bool {
		return strings.Contains(string(f.Body), "threads/1/dev-1")
	}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "dev-1's #1 branch deleted", func() bool { _, ok := gh.ref("thread/1/dev-1"); return !ok })
	if _, ok := gh.ref("thread/2/dev-2"); !ok {
		t.Fatal("dev-2's #2 branch deleted with #1")
	}
	if s, err := core.SessionByID(tm.w.ctx, tm.w.e.Core.Pool, dev2.session); err != nil || s.State == "closed" {
		t.Fatalf("dev-2's session in #2: %+v %v", s, err)
	}
	dev, err := core.ProjectMemberByName(tm.w.ctx, tm.w.e.Core.Pool, tm.projectID, "dev-1")
	if err != nil || dev.CurrentThreadID != nil {
		t.Fatalf("dev-1 after #1 ended: %+v %v", dev, err)
	}

	tm.b.must(204, "DELETE", "/api/threads/"+one.ID, nil, nil)
	if _, err := tm.n.Wait(ctx, protocol.TypeGitPurge, func(f nodetest.Frame) bool {
		return strings.Contains(string(f.Body), "projects/"+tm.projectID+"/threads/1")
	}); err != nil {
		t.Fatal(err)
	}
	var staff int
	if err := tm.w.e.Core.Pool.Gorm(tm.w.ctx).Raw(`SELECT count(*) FROM at_employees WHERE project_id = ? AND thread_id IS NULL
		AND tier = 'member' AND active`, tm.projectID).Scan(&staff).Error; err != nil || staff != 2 {
		t.Fatalf("project members after delete %d %v", staff, err)
	}
	tm.b.must(200, "GET", "/api/threads/"+two.ID, nil, &two)
	if memberNames(two) != "lead-2,dev-2" {
		t.Fatalf("#2 participants %s", memberNames(two))
	}
}

func briefOf(t *testing.T, list []core.EmployeeBrief, name string) core.EmployeeBrief {
	t.Helper()
	for _, b := range list {
		if b.Name == name {
			return b
		}
	}
	t.Fatalf("%s not in %+v", name, list)
	return core.EmployeeBrief{}
}

func threadNo(b core.EmployeeBrief) int {
	if b.ThreadNo == nil {
		return 0
	}
	return *b.ThreadNo
}

// TestBusyElsewhereAndMailAcrossThreads: dev-2 did #2's t1, then took #1's t2. In #2's member
// list it is busy elsewhere (thread_no 1); the office shows it at #1. Mail from lead-2 reaches
// it in its #1 session labelled with #2, and read_messages in #1 mentions it. Once #1 ends the
// members have no thread and still appear in the office.
func TestBusyElsewhereAndMailAcrossThreads(t *testing.T) {
	tm := sharedTeam(t)
	one, lead1 := tm.openThread(t, "One", nil)
	lead1.mustCall("assign_task", map[string]any{"assignee": "dev", "instruction": "task of one", "main": "做"})
	dev1, _ := startTask(t, tm, "dev-1", "task of one")
	two, lead2 := tm.openThread(t, "Two", nil)
	lead2.mustCall("assign_task", map[string]any{"assignee": "dev", "instruction": "first of two", "main": "做"})
	dev2, _ := startTask(t, tm, "dev-2", "first of two")
	dev2.mustCall("complete_task", map[string]any{"task_id": "t1", "summary": "two done"})
	lead2.expectSend("[Task t1 · dev-2 · done] two done")
	lead1.mustCall("assign_task", map[string]any{"assignee": "dev", "instruction": "second of one", "main": "做"})
	moved, _ := startTask(t, tm, "dev-2", "second of one")

	tm.b.must(200, "GET", "/api/threads/"+two.ID, nil, &two)
	if b := briefOf(t, two.Members, "dev-2"); !b.BusyElsewhere || threadNo(b) != 1 {
		t.Fatalf("dev-2 in #2's list: %+v", b)
	}
	tm.b.must(200, "GET", "/api/threads/"+one.ID, nil, &one)
	for _, name := range []string{"dev-1", "dev-2"} {
		if b := briefOf(t, one.Members, name); b.BusyElsewhere || threadNo(b) != 1 {
			t.Fatalf("%s in #1's list: %+v", name, b)
		}
	}
	var office orchapi.OfficeSnapshot
	tm.b.must(200, "GET", "/api/projects/"+tm.projectID+"/office", nil, &office)
	if b := briefOf(t, office.Employees, "dev-2"); threadNo(b) != 1 || b.BusyElsewhere {
		t.Fatalf("dev-2 in the office: %+v", b)
	}

	lead2.mustCall("send_message", map[string]any{"to": "dev-2", "text": "look at the login page too"})
	if got := moved.expectSend("[#2 · 来自 lead-2 的留言]"); got.Text != "[#2 · 来自 lead-2 的留言]\nlook at the login page too" {
		t.Fatalf("mail across threads: %q", got.Text)
	}
	if got := text(moved.mustCall("read_messages", map[string]any{})); !strings.Contains(got, "no unread messages") ||
		!strings.Contains(got, "Unread messages to you in other threads: #2: 1") {
		t.Fatalf("read_messages in #1: %s", got)
	}
	lead1.mustCall("send_message", map[string]any{"to": "dev-1", "text": "same thread"})
	dev1.expectSend("[Message from lead-1]")
	if got := text(dev1.mustCall("read_messages", map[string]any{})); !strings.Contains(got, "lead-1 → dev-1：same thread") ||
		strings.Contains(got, "其他讨论串") {
		t.Fatalf("dev-1 read_messages: %s", got)
	}

	tm.b.must(200, "POST", "/api/threads/"+one.ID+"/archive", nil, nil)
	eventually(t, "members leave #1", func() bool {
		tm.b.must(200, "GET", "/api/projects/"+tm.projectID+"/office", nil, &office)
		return threadNo(briefOf(t, office.Employees, "dev-1")) == 0 && threadNo(briefOf(t, office.Employees, "dev-2")) == 0
	})
}

// TestNodeOnlineKicksScheduling: a task queued without a scheduling pass (the periodic sweep is
// off) is claimed as soon as one of the owner's nodes comes online.
func TestNodeOnlineKicksScheduling(t *testing.T) {
	tm := sharedTeamWith(t, func(e *engine.Engine) { e.Sched.Interval = time.Hour })
	th, _ := tm.openThread(t, "Queued", nil)
	err := tm.w.e.Core.InTx(tm.w.ctx, func(tx *core.Tx) error {
		thread, err := core.ThreadByID(tm.w.ctx, tx, th.ID)
		if err != nil {
			return err
		}
		_, err = core.InsertTask(tm.w.ctx, tx, core.NewTask{Thread: thread, Role: "dev", Instruction: "queued work", CreatedBy: "lead-1"})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := takeNow(tm.n, protocol.TypeSessionOpen); ok {
		t.Fatal("claimed before any scheduling pass")
	}
	var other nodeCreated
	tm.b.must(201, "POST", "/api/nodes", map[string]string{"name": "second"}, &other)
	second := tm.w.connect(other.Token, nodetest.Options{})
	// The first node already runs lead-1's session: by load score (pickRank) the idle new node
	// takes dev-1's.
	_, open := expectOpen(t, tm.w, second, "dev-1")
	if open.FirstMessage == nil || !strings.Contains(open.FirstMessage.Text, "queued work") {
		t.Fatalf("dev-1 first message %+v", open.FirstMessage)
	}
}
