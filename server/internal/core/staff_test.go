package core

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/touwaeriol/agent-teams/server/internal/store"
	"github.com/touwaeriol/agent-teams/server/internal/testdb"
)

// staffWorld is a migrated database with a user, a project and three nodes (n2 isolated).
type staffWorld struct {
	t     *testing.T
	ctx   context.Context
	s     *Store
	nodes []store.Node
}

func newStaffWorld(t *testing.T) *staffWorld {
	t.Helper()
	ctx := context.Background()
	pool, err := store.OpenPool(ctx, testdb.Migrated(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	w := &staffWorld{t: t, ctx: ctx, s: New(pool, nil),
		nodes: []store.Node{{ID: "n1"}, {ID: "n2", Isolated: true}, {ID: "n3"}}}
	w.exec(`INSERT INTO at_users (id, email, password_hash) VALUES ('u1', 'a@example.com', 'x');
INSERT INTO at_projects (id, user_id, name) VALUES ('p1', 'u1', 'A');
INSERT INTO at_nodes (id, user_id, name, token_hash) VALUES ('n1', 'u1', 'one', 'h1'), ('n2', 'u1', 'two', 'h2'), ('n3', 'u1', 'three', 'h3')`)
	return w
}

func (w *staffWorld) exec(sql string, args ...any) {
	w.t.Helper()
	if _, err := store.Exec(w.ctx, w.s.Pool, sql, args...); err != nil {
		w.t.Fatal(err)
	}
}

func (w *staffWorld) tx(fn func(tx *Tx) error) {
	w.t.Helper()
	if err := w.s.InTx(w.ctx, fn); err != nil {
		w.t.Fatal(err)
	}
}

func (w *staffWorld) ensure(roles ...store.Role) []Task {
	w.t.Helper()
	hair := func(i int) *string { h := fmt.Sprintf("hair-%d", i); return &h }
	var released []Task
	w.tx(func(tx *Tx) error {
		var err error
		released, err = EnsureStaff(w.ctx, tx, "p1", Staffing{Roles: roles, Nodes: w.nodes, Hair: hair})
		return err
	})
	return released
}

// rowVersions renders the project members' row versions (xmin): unchanged rows keep theirs.
func (w *staffWorld) rowVersions() string {
	w.t.Helper()
	var v string
	if err := w.s.Pool.Gorm(w.ctx).Raw(`SELECT string_agg(name || '@' || xmin::text, ' ' ORDER BY name) FROM at_employees
		WHERE project_id = 'p1' AND thread_id IS NULL`).Scan(&v).Error; err != nil {
		w.t.Fatal(err)
	}
	return v
}

// staff renders the project members as "name:active:node" sorted by name.
func (w *staffWorld) staff() string {
	w.t.Helper()
	list, err := find[Employee](employees(w.ctx, w.s.Pool).Where("project_id = 'p1' AND thread_id IS NULL AND tier = 'member'").Order("name"))
	if err != nil {
		w.t.Fatal(err)
	}
	out := make([]string, len(list))
	for i, e := range list {
		out[i] = fmt.Sprintf("%s:%v:%s", e.Name, e.Active, deref(e.NodeID))
	}
	return strings.Join(out, " ")
}

func (w *staffWorld) thread(title string) Thread {
	w.t.Helper()
	var t Thread
	w.tx(func(tx *Tx) error {
		var err error
		t, err = CreateThread(w.ctx, tx, NewThread{ProjectID: "p1", UserID: "u1", Title: title, Roles: []store.Role{lead()}})
		if err != nil {
			return err
		}
		_, err = InstantiateThread(w.ctx, tx, t, nil)
		return err
	})
	return t
}

func (w *staffWorld) task(t Thread, who, status string) Task {
	w.t.Helper()
	var task Task
	w.tx(func(tx *Tx) error {
		var err error
		task, err = InsertTask(w.ctx, tx, NewTask{Thread: t, Assignee: "dev", Role: "dev", ClaimedBy: who})
		if err != nil || status == "pending" {
			return err
		}
		task, err = SetTaskStatus(w.ctx, tx, task, status)
		return err
	})
	return task
}

func member(role string, count int) store.Role {
	return store.Role{Tier: "member", Role: role, Title: role, Runtime: "claude", Count: count}
}

func lead() store.Role {
	return store.Role{Tier: "lead", Role: "lead", Title: "组长", Runtime: "claude", Count: 1}
}

// TestEnsureStaff: members follow the template counts with a snapshot and palette hair, are
// spread over the nodes (a role's members on different nodes, Codex only on isolated nodes),
// and a second call changes nothing.
func TestEnsureStaff(t *testing.T) {
	w := newStaffWorld(t)
	coder := member("coder", 1)
	coder.Runtime = "codex"
	reviewer := member("reviewer", 1)
	reviewer.Gate = "review"
	w.ensure(lead(), member("dev", 3), reviewer, coder)
	want := "coder-1:true:n2 dev-1:true:n1 dev-2:true:n3 dev-3:true:n2 reviewer-1:true:n1"
	if got := w.staff(); got != want {
		t.Fatalf("staff\n got %s\nwant %s", got, want)
	}
	versions := w.rowVersions()
	w.ensure(lead(), member("dev", 3), reviewer, coder)
	if got := w.staff(); got != want {
		t.Fatalf("second call changed staff: %s", got)
	}
	if got := w.rowVersions(); got != versions {
		t.Fatalf("second call rewrote unchanged members:\n got %s\nwant %s", got, versions)
	}
	e, err := ProjectMemberByName(w.ctx, w.s.Pool, "p1", "dev-2")
	if err != nil || e.InstanceNo != 2 || e.Spec.Role != "dev" || e.Look.Hair == nil || *e.Look.Hair != "hair-1" || e.ThreadID != nil {
		t.Fatalf("dev-2 %+v %v", e, err)
	}
	if n, err := count(employees(w.ctx, w.s.Pool).Where("tier = 'lead'")); err != nil || n != 0 {
		t.Fatalf("leads %d %v", n, err)
	}
}

// TestEnsureStaffNoIsolatedNode: a Codex member without an isolated node stays without a node.
func TestEnsureStaffNoIsolatedNode(t *testing.T) {
	w := newStaffWorld(t)
	w.nodes = w.nodes[:1]
	coder := member("coder", 1)
	coder.Runtime = "codex"
	w.ensure(coder, member("dev", 1))
	if got := w.staff(); got != "coder-1:true: dev-1:true:n1" {
		t.Fatalf("staff %s", got)
	}
}

// TestEnsureStaffShrinkKeepsBusy: lowering a count or dropping a role deactivates the surplus,
// except a member with a running task, which goes on the next call after its task; raising
// the count again re-activates the same row with the template's new snapshot.
func TestEnsureStaffShrinkKeepsBusy(t *testing.T) {
	w := newStaffWorld(t)
	reviewer := member("reviewer", 1)
	w.ensure(member("dev", 3), reviewer)
	before, _ := ProjectMemberByName(w.ctx, w.s.Pool, "p1", "dev-3")
	th := w.thread("x")
	task := w.task(th, "dev-3", "running")
	w.ensure(member("dev", 1))
	if got := w.staff(); got != "dev-1:true:n1 dev-2:false:n2 dev-3:true:n3 reviewer-1:false:n1" {
		t.Fatalf("after shrink %s", got)
	}
	w.tx(func(tx *Tx) error { _, err := SetTaskStatus(w.ctx, tx, task, "done"); return err })
	w.ensure(member("dev", 1))
	if got := w.staff(); got != "dev-1:true:n1 dev-2:false:n2 dev-3:false:n3 reviewer-1:false:n1" {
		t.Fatalf("after task done %s", got)
	}
	grown := member("dev", 3)
	grown.Instructions = "new"
	w.ensure(grown)
	after, err := ProjectMemberByName(w.ctx, w.s.Pool, "p1", "dev-3")
	if err != nil || after.ID != before.ID || !after.Active || after.Spec.Instructions != "new" {
		t.Fatalf("dev-3 again %+v %v", after, err)
	}
}

// TestEnsureStaffReleasesNamedTasks: retiring a member turns its pending named tasks into tasks
// of its role (not a legacy member's of that name, not a finished thread's) and returns them.
func TestEnsureStaffReleasesNamedTasks(t *testing.T) {
	w := newStaffWorld(t)
	w.ensure(member("dev", 2))
	a, b := w.thread("a"), w.thread("b")
	var named, legacyNamed Task
	w.tx(func(tx *Tx) error {
		var err error
		if named, err = InsertTask(w.ctx, tx, NewTask{Thread: a, Assignee: "dev-2", Role: "dev"}); err != nil {
			return err
		}
		legacyNamed, err = InsertTask(w.ctx, tx, NewTask{Thread: b, Assignee: "dev-2", Role: "dev"})
		return err
	})
	w.legacy(b, "dev-2")
	released := w.ensure(member("dev", 1))
	if len(released) != 1 || released[0].ID != named.ID || released[0].ConversationID != a.ID || released[0].Assignee != "dev" {
		t.Fatalf("released %+v", released)
	}
	got, _ := TaskByID(w.ctx, w.s.Pool, b.ID, legacyNamed.ID)
	if got.Assignee != "dev-2" {
		t.Fatalf("the legacy member's task changed: %+v", got)
	}
}

// TestEnsureStaffLegacyTaskNotBusy: a running task of a legacy member named dev-2 does not keep
// the project's dev-2.
func TestEnsureStaffLegacyTaskNotBusy(t *testing.T) {
	w := newStaffWorld(t)
	w.ensure(member("dev", 2))
	th := w.thread("x")
	w.legacy(th, "dev-2")
	w.task(th, "dev-2", "running")
	w.ensure(member("dev", 1))
	if got := w.staff(); got != "dev-1:true:n1 dev-2:false:n2" {
		t.Fatalf("staff %s", got)
	}
}

func (w *staffWorld) legacy(th Thread, name string) Employee {
	w.t.Helper()
	var e Employee
	w.tx(func(tx *Tx) error {
		var err error
		e, err = InsertEmployee(w.ctx, tx, NewEmployee{ProjectID: "p1", ThreadID: &th.ID, Name: name, Role: member("dev", 1), InstanceNo: 2})
		return err
	})
	return e
}

// TestInstantiateThreadLeadOnly: a thread gets only its lead, whatever the snapshot lists.
func TestInstantiateThreadLeadOnly(t *testing.T) {
	w := newStaffWorld(t)
	var made []Employee
	w.tx(func(tx *Tx) error {
		th, err := CreateThread(w.ctx, tx, NewThread{ProjectID: "p1", UserID: "u1", Title: "x",
			Roles: []store.Role{lead(), member("dev", 2)}, OnlyRoles: []string{"dev"}})
		if err != nil {
			return err
		}
		made, err = InstantiateThread(w.ctx, tx, th, nil)
		return err
	})
	if len(made) != 1 || made[0].Name != "lead-1" || made[0].Tier != "lead" {
		t.Fatalf("made %+v", made)
	}
	th, err := ThreadByNo(w.ctx, w.s.Pool, "p1", 1)
	if err != nil || !slices.Equal(th.OnlyRoles, []string{"dev"}) {
		t.Fatalf("only_roles %v %v", th.OnlyRoles, err)
	}
	all := w.thread("y")
	if all.OnlyRoles == nil || len(all.OnlyRoles) != 0 {
		t.Fatalf("no only_roles: %#v", all.OnlyRoles)
	}
	var raw *string
	if err := w.s.Pool.Gorm(w.ctx).Raw(`SELECT only_roles::text FROM at_threads WHERE id = ?`, all.ID).Scan(&raw).Error; err != nil || raw != nil {
		t.Fatalf("only_roles stored %v %v", raw, err)
	}
}

// TestEmployeeByNamePrefersThreadOwn: inside a thread with a legacy dev-1 the name means that
// member; in another thread (or a thread without one) it means the project's dev-1.
func TestEmployeeByNamePrefersThreadOwn(t *testing.T) {
	w := newStaffWorld(t)
	w.ensure(member("dev", 1))
	a, b := w.thread("a"), w.thread("b")
	old := w.legacy(a, "dev-1")
	got, err := EmployeeByName(w.ctx, w.s.Pool, "p1", &a.ID, "dev-1")
	if err != nil || got.ID != old.ID {
		t.Fatalf("in a: %+v %v", got, err)
	}
	got, err = EmployeeByName(w.ctx, w.s.Pool, "p1", &b.ID, "dev-1")
	if err != nil || got.ThreadID != nil || got.Name != "dev-1" {
		t.Fatalf("in b: %+v %v", got, err)
	}
	if lead, err := EmployeeByName(w.ctx, w.s.Pool, "p1", &b.ID, "lead-2"); err != nil || lead.Tier != "lead" {
		t.Fatalf("lead: %+v %v", lead, err)
	}
	if _, err := EmployeeByName(w.ctx, w.s.Pool, "p1", &b.ID, "lead-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another thread's lead: %v", err)
	}
	if _, err := EmployeeByName(w.ctx, w.s.Pool, "p1", nil, "dev-1"); err != nil {
		t.Fatalf("project scope: %v", err)
	}
}

// TestThreadParticipants: the lead, legacy members and the project members that had a task
// there, work there now or opened a session there; a project member hidden by a legacy member
// of its name is not one of them.
func TestThreadParticipants(t *testing.T) {
	w := newStaffWorld(t)
	w.ensure(member("dev", 4), member("reviewer", 1))
	a, b := w.thread("a"), w.thread("b")
	w.task(b, "dev-1", "done")
	dev2, _ := ProjectMemberByName(w.ctx, w.s.Pool, "p1", "dev-2")
	rev, _ := ProjectMemberByName(w.ctx, w.s.Pool, "p1", "reviewer-1")
	w.tx(func(tx *Tx) error {
		if err := SetCurrentThread(w.ctx, tx, dev2.ID, &b.ID); err != nil {
			return err
		}
		_, err := InsertSession(w.ctx, tx, NewSession{EmployeeID: rev.ID, NodeID: "n1", Runtime: "claude", Cwd: "x", ThreadID: &b.ID})
		return err
	})
	w.legacy(a, "dev-3")
	w.task(a, "dev-3", "running")
	if got := names(t, w, b.ID); got != "lead-2 dev-1 dev-2 reviewer-1" {
		t.Fatalf("participants of b: %s", got)
	}
	if got := names(t, w, a.ID); got != "lead-1 dev-3" {
		t.Fatalf("participants of a: %s", got)
	}
	team, err := ThreadTeam(w.ctx, w.s.Pool, Thread{ID: a.ID, ProjectID: "p1", OnlyRoles: []string{"dev"}})
	if err != nil || len(team) != 5 { // lead-1, legacy dev-3, project dev-1, dev-2, dev-4
		t.Fatalf("team of a: %v %v", team, err)
	}
	w.tx(func(tx *Tx) error { return LeaveThread(w.ctx, tx, b.ID) })
	if e, _ := EmployeeByID(w.ctx, w.s.Pool, dev2.ID); e.CurrentThreadID != nil {
		t.Fatalf("current thread after leave: %v", *e.CurrentThreadID)
	}
}

// TestThreadWorkers: cleanup covers the members that worked in a thread, retired ones too
// (claimed a task, opened a session there), and its legacy members, but not its lead nor a
// project member hidden by a legacy member of its name.
func TestThreadWorkers(t *testing.T) {
	w := newStaffWorld(t)
	w.ensure(member("dev", 4))
	a, b := w.thread("a"), w.thread("b")
	w.task(a, "dev-1", "done")
	w.task(b, "dev-2", "running")
	dev4, _ := ProjectMemberByName(w.ctx, w.s.Pool, "p1", "dev-4")
	w.tx(func(tx *Tx) error {
		_, err := InsertSession(w.ctx, tx, NewSession{EmployeeID: dev4.ID, NodeID: "n1", Runtime: "claude", Cwd: "x", ThreadID: &a.ID})
		return err
	})
	w.legacy(a, "dev-3")
	w.task(a, "dev-3", "done")
	w.ensure(member("dev", 1)) // dev-4 retires; it still worked in a
	list, err := ThreadWorkers(w.ctx, w.s.Pool, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, len(list))
	for i, e := range list {
		got[i] = fmt.Sprintf("%s:%v:%v", e.Name, e.ThreadID != nil, e.Active)
	}
	sort.Strings(got)
	if strings.Join(got, " ") != "dev-1:false:true dev-3:true:true dev-4:false:false" {
		t.Fatalf("workers of a: %v", got)
	}
}

func names(t *testing.T, w *staffWorld, threadID string) string {
	t.Helper()
	list, err := ThreadParticipants(w.ctx, w.s.Pool, threadID)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, len(list))
	for i, e := range list {
		out[i] = e.Name
	}
	sort.SliceStable(out[1:], func(i, j int) bool { return out[1+i] < out[1+j] })
	return strings.Join(out, " ")
}

// TestEmployeeBriefsProjectWide: the office list leaves out the employees of ended threads (their
// lead and legacy per-thread members) and marks only project members as staff (headcount).
func TestEmployeeBriefsProjectWide(t *testing.T) {
	w := newStaffWorld(t)
	w.ensure(member("dev", 2))
	a, b := w.thread("a"), w.thread("b")
	w.legacy(a, "dev-1")
	w.legacy(b, "dev-1")
	if _, err := store.Exec(w.ctx, w.s.Pool, `UPDATE at_threads SET status = 'done' WHERE id = $1`, b.ID); err != nil {
		t.Fatal(err)
	}
	briefs, err := w.s.EmployeeBriefs(w.ctx, w.s.Pool, "p1", nil)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range briefs {
		got = append(got, fmt.Sprintf("%s:%v", e.Name, e.Staff))
	}
	if strings.Join(got, " ") != "lead-1:false dev-1:false dev-1:true dev-2:true" {
		t.Fatalf("project-wide briefs: %v", got)
	}
}
