package core

import (
	"slices"
	"testing"
	"time"
)

// TestProjectQueue: idle members (project members busy anywhere in the project are not, a legacy
// member only counts its own thread), current tasks project-wide, and the claimable tasks with
// what scoring needs (dependency workers, downstream count, legacy names, readiness).
func TestProjectQueue(t *testing.T) {
	w := newStaffWorld(t)
	w.ensure(lead(), member("dev", 2))
	a, b := w.thread("A"), w.thread("B")
	w.exec(`UPDATE at_threads SET status = 'running' WHERE project_id = 'p1'`)
	w.exec(`INSERT INTO at_employees (id, project_id, thread_id, name, tier, role, runtime, spec)
		SELECT 'em_legacy', project_id, $1, name, tier, role, runtime, spec FROM at_employees
		WHERE project_id = 'p1' AND thread_id IS NULL AND name = 'dev-1'`, b.ID)
	busy := w.task(a, "dev-1", "running")
	w.task(b, "dev-1", "running") // the legacy dev-1's

	idle, err := IdleMembers(w.ctx, w.s.Pool, "p1")
	if err != nil {
		t.Fatal(err)
	}
	if len(idle) != 1 || idle[0].Name != "dev-2" || idle[0].ThreadID != nil {
		t.Fatalf("idle %+v", idle)
	}
	dev1, _ := ProjectMemberByName(w.ctx, w.s.Pool, "p1", "dev-1")
	if cur, err := CurrentTaskOf(w.ctx, w.s.Pool, dev1); err != nil || cur.ConversationID != a.ID || cur.ID != busy.ID {
		t.Fatalf("dev-1 current %+v %v", cur, err)
	}
	if th, err := WorkThreadOf(w.ctx, w.s.Pool, dev1); err != nil || th == nil || *th != a.ID {
		t.Fatalf("dev-1 works in %v %v", th, err)
	}
	if got, _ := BusyMembers(w.ctx, w.s.Pool, "p1"); !got["dev-1"] || got["dev-2"] || len(got) != 1 {
		t.Fatalf("busy %v", got)
	}

	w.exec(`UPDATE at_tasks SET status = 'done' WHERE thread_id = $1`, b.ID)
	idle, _ = IdleMembers(w.ctx, w.s.Pool, "p1")
	if len(idle) != 2 || idle[1].ID != "em_legacy" || idle[1].Thread != b.ID || idle[1].ThreadID == nil || *idle[1].ThreadID != b.ID {
		t.Fatalf("idle with the legacy member free %+v", idle)
	}
	w.exec(`UPDATE at_threads SET status = 'done' WHERE id = $1`, a.ID)
	if _, err := CurrentTaskOf(w.ctx, w.s.Pool, dev1); err != ErrNotFound {
		t.Fatalf("a task of an ended thread still counts: %v", err)
	}
}

// TestClaimableTasks: pending tasks of going threads whose dependencies are done, with the
// scoring inputs; ready_at is stamped on insert and when the last dependency finishes.
func TestClaimableTasks(t *testing.T) {
	w := newStaffWorld(t)
	w.ensure(lead(), member("dev", 2))
	a := w.thread("A")
	w.exec(`UPDATE at_threads SET status = 'running' WHERE id = $1`, a.ID)
	d1, d2 := w.task(a, "dev-2", "running"), w.task(a, "dev-1", "running")
	var top Task
	w.tx(func(tx *Tx) error {
		var err error
		top, err = InsertTask(w.ctx, tx, NewTask{Thread: a, Assignee: "dev", Role: "dev", DependsOn: []string{d1.ID, d2.ID}})
		return err
	})
	free := w.task(a, "", "pending")
	if top.ReadyAt != nil || free.ReadyAt == nil {
		t.Fatalf("ready_at on insert: deps %v none %v", top.ReadyAt, free.ReadyAt)
	}
	ids := func() []string {
		q, err := ClaimableTasks(w.ctx, w.s.Pool, "p1", []string{"dev"})
		if err != nil {
			t.Fatal(err)
		}
		out := []string{}
		for _, x := range q {
			out = append(out, x.ID)
		}
		return out
	}
	if got := ids(); !slices.Equal(got, []string{free.ID}) {
		t.Fatalf("claimable with open deps %v", got)
	}
	w.exec(`UPDATE at_tasks SET status = 'done' WHERE thread_id = $1 AND id IN ($2, $3)`, a.ID, d1.ID, d2.ID)
	w.tx(func(tx *Tx) error { return MarkReady(w.ctx, tx, a.ID, d2.ID) })
	q, err := ClaimableTasks(w.ctx, w.s.Pool, "p1", []string{"dev"})
	if err != nil || len(q) != 2 {
		t.Fatalf("claimable %+v %v", q, err)
	}
	got := q[slices.IndexFunc(q, func(x QueuedTask) bool { return x.ID == top.ID })]
	if got.ReadyAt == nil || !slices.Equal(got.DepWorkers, []string{"dev-2", "dev-1"}) && !slices.Equal(got.DepWorkers, []string{"dev-1", "dev-2"}) ||
		!slices.Equal(got.Legacy, []string{"lead-1"}) || got.Role != "dev" {
		t.Fatalf("queued %+v", got)
	}
	w.exec(`UPDATE at_threads SET status = 'paused' WHERE id = $1`, a.ID)
	if got := ids(); len(got) != 0 {
		t.Fatalf("claimable in a paused thread %v", got)
	}
}

// TestQueueInputs: the claimable tasks are those of the asked roles; a task named to a retired
// member is not named any more; an idle member mid-turn names the thread of that turn; a task
// put back into the queue is claimable (and starves) from then on.
func TestQueueInputs(t *testing.T) {
	w := newStaffWorld(t)
	w.ensure(lead(), member("dev", 2), member("qa", 1))
	a := w.thread("A")
	w.exec(`UPDATE at_threads SET status = 'running' WHERE id = $1`, a.ID)
	var toDev1, toDev2, check Task
	w.tx(func(tx *Tx) error {
		var err error
		if toDev1, err = InsertTask(w.ctx, tx, NewTask{Thread: a, Assignee: "dev-1", Role: "dev"}); err != nil {
			return err
		}
		if toDev2, err = InsertTask(w.ctx, tx, NewTask{Thread: a, Assignee: "dev-2", Role: "dev"}); err != nil {
			return err
		}
		check, err = InsertTask(w.ctx, tx, NewTask{Thread: a, Assignee: "qa", Role: "qa"})
		return err
	})
	w.exec(`UPDATE at_employees SET active = false WHERE project_id = 'p1' AND name = 'dev-2'`)
	q, err := ClaimableTasks(w.ctx, w.s.Pool, "p1", []string{"qa"})
	if err != nil || len(q) != 1 || q[0].ID != check.ID {
		t.Fatalf("qa queue %+v %v", q, err)
	}
	q, err = ClaimableTasks(w.ctx, w.s.Pool, "p1", []string{"dev"})
	if err != nil || len(q) != 2 {
		t.Fatalf("dev queue %+v %v", q, err)
	}
	named := map[string]bool{}
	for _, x := range q {
		named[x.ID] = x.NamedActive
	}
	if !named[toDev1.ID] || named[toDev2.ID] {
		t.Fatalf("named active %v", named)
	}

	dev1, _ := ProjectMemberByName(w.ctx, w.s.Pool, "p1", "dev-1")
	var s AgentSession
	w.tx(func(tx *Tx) error {
		var err error
		s, err = InsertSession(w.ctx, tx, NewSession{EmployeeID: dev1.ID, NodeID: "n1", Runtime: "claude", Cwd: "x", ThreadID: &a.ID})
		if err != nil {
			return err
		}
		if err := SetSessionState(w.ctx, tx, s.ID, "active"); err != nil {
			return err
		}
		return SetInTurn(w.ctx, tx, s.ID, true)
	})
	turnOf := func() string {
		idle, err := IdleMembers(w.ctx, w.s.Pool, "p1")
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range idle {
			if m.ID == dev1.ID {
				return m.TurnThread
			}
		}
		t.Fatalf("dev-1 not idle: %+v", idle)
		return ""
	}
	if got := turnOf(); got != a.ID {
		t.Fatalf("dev-1 mid-turn in %q", got)
	}
	w.tx(func(tx *Tx) error { return SetSessionState(w.ctx, tx, s.ID, "parked") })
	if got := turnOf(); got != "" {
		t.Fatalf("a parked session still runs a turn in %q", got)
	}

	w.exec(`UPDATE at_tasks SET status = 'failed', ready_at = now() - interval '1 hour' WHERE thread_id = $1 AND id = $2`, a.ID, check.ID)
	var back Task
	w.tx(func(tx *Tx) error {
		var err error
		back, err = RequeueTask(w.ctx, tx, check, map[string]any{"claimed_by": ""})
		return err
	})
	if back.Status != "pending" || back.ReadyAt == nil || time.Since(*back.ReadyAt) > time.Minute {
		t.Fatalf("requeued %+v", back)
	}
}
