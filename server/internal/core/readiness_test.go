package core

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// step is one change to a thread's tasks: add a task (deps by step index, as t<n+1>), move a
// task to a status (SetTaskStatus) or requeue it (RequeueTask).
type step struct {
	add     []string // dependencies of a new task
	status  string   // with add: the new task's status ("" pending); else the target status of id
	id      string
	requeue bool
}

func addTask(deps ...string) step { return step{add: append([]string{}, deps...)} }
func addTaskAs(st string, deps ...string) step {
	return step{add: append([]string{}, deps...), status: st}
}
func moveTask(id, st string) step { return step{id: id, status: st} }
func requeueTask(id string) step  { return step{id: id, requeue: true} }

func (w *staffWorld) apply(th Thread, s step) {
	w.t.Helper()
	w.tx(func(tx *Tx) error {
		if s.add != nil {
			_, err := InsertTask(w.ctx, tx, NewTask{Thread: th, Assignee: "dev", Role: "dev", DependsOn: s.add, Status: s.status})
			return err
		}
		task, err := TaskByID(w.ctx, tx, th.ID, s.id)
		if err != nil {
			return err
		}
		if s.requeue {
			_, err = RequeueTask(w.ctx, tx, task, map[string]any{"claimed_by": ""})
			return err
		}
		_, err = SetTaskStatus(w.ctx, tx, task, s.status)
		return err
	})
}

// render lists a thread's tasks as "id:<column>" in id order.
func (w *staffWorld) render(th Thread, column string) string {
	w.t.Helper()
	var rows []string
	if err := w.s.Pool.Gorm(w.ctx).Raw(`SELECT id || ':' || `+column+` FROM at_tasks WHERE thread_id = $1
		ORDER BY substring(id from 2)::int`, th.ID).Scan(&rows).Error; err != nil {
		w.t.Fatal(err)
	}
	return strings.Join(rows, " ")
}

// recount computes every task's downstream from scratch (distinct unfinished transitive dependents).
func (w *staffWorld) recount(th Thread) string {
	w.t.Helper()
	var rows []string
	err := w.s.Pool.Gorm(w.ctx).Raw(`WITH RECURSIVE down(anc, id) AS (
			SELECT dep.id, t.id FROM at_tasks t, jsonb_array_elements_text(t.depends_on) dep(id) WHERE t.conversation_id = $1
			UNION
			SELECT d.anc, t.id FROM down d JOIN at_tasks t ON t.conversation_id = $1 AND jsonb_exists(t.depends_on, d.id))
		SELECT a.id || ':' || (SELECT count(*) FROM down d JOIN at_tasks t ON t.conversation_id = $1 AND t.id = d.id
			WHERE d.anc = a.id AND t.status NOT IN ('done','failed','cancelled'))
		FROM at_tasks a WHERE a.conversation_id = $1 ORDER BY substring(a.id from 2)::int`, th.ID).Scan(&rows).Error
	if err != nil {
		w.t.Fatal(err)
	}
	return strings.Join(rows, " ")
}

// TestDownstream: the critical-path count of every task follows inserts and status changes into
// and out of final states, counting each transitive dependent once, and always equals a recount.
func TestDownstream(t *testing.T) {
	diamond := []step{addTask(), addTask("t1"), addTask("t1"), addTask("t2", "t3")}
	cases := []struct {
		name  string
		steps []step
		want  string
	}{
		{"chain", []step{addTask(), addTask("t1"), addTask("t2")}, "t1:2 t2:1 t3:0"},
		{"diamond counts the join once", diamond, "t1:3 t2:1 t3:1 t4:0"},
		{"cancelled dependent drops out", append(slices.Clone(diamond), moveTask("t4", "cancelled")), "t1:2 t2:0 t3:0 t4:0"},
		{"failed then retried counts again", []step{addTask(), addTask("t1"), addTask("t2"), moveTask("t3", "failed"), requeueTask("t3")}, "t1:2 t2:1 t3:0"},
		{"blocked still counts", []step{addTask(), addTask("t1"), addTask("t2"), moveTask("t3", "blocked")}, "t1:2 t2:1 t3:0"},
		{"done dependents leave their ancestors", []step{addTask(), addTask("t1"), addTask("t2"), moveTask("t1", "done"), moveTask("t2", "done")},
			"t1:1 t2:1 t3:0"},
		{"a task inserted final is not counted", []step{addTask(), addTaskAs("done", "t1"), addTaskAs("cancelled", "t1")}, "t1:0 t2:0 t3:0"},
		{"two roots and a shared tail", []step{addTask(), addTask(), addTask("t1", "t2"), addTask("t3"), addTask("t1")}, "t1:3 t2:2 t3:1 t4:0 t5:0"},
		{"claim and back do not change it", []step{addTask(), addTask("t1"), moveTask("t2", "claimed"), moveTask("t2", "running"), requeueTask("t2")},
			"t1:1 t2:0"},
	}
	w := newStaffWorld(t)
	w.ensure(lead(), member("dev", 1))
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			th := w.thread(fmt.Sprint("D", i))
			for _, s := range c.steps {
				w.apply(th, s)
			}
			if got := w.render(th, "downstream"); got != c.want {
				t.Fatalf("downstream\n got %s\nwant %s", got, c.want)
			}
			if got := w.recount(th); got != c.want {
				t.Fatalf("recount %s differs from the kept counts %s", got, c.want)
			}
		})
	}
}

// TestReadyEvents: ready_at is stamped on insert without open dependencies and by the status
// change that makes the last dependency done (direct dependents only); a blocked task retried
// before its dependency is done waits again; the claim query lists ready tasks only, and the
// safety net stamps what an event missed once it is old enough.
func TestReadyEvents(t *testing.T) {
	w := newStaffWorld(t)
	w.ensure(lead(), member("dev", 1))
	th := w.thread("R")
	w.exec(`UPDATE at_threads SET status = 'running' WHERE id = $1`, th.ID)
	ready := func() string { return w.render(th, "(ready_at IS NOT NULL)::text") }
	claimable := func() string {
		q, err := ClaimableTasks(w.ctx, w.s.Pool, "p1", []string{"dev"})
		if err != nil {
			t.Fatal(err)
		}
		ids := []string{}
		for _, x := range q {
			ids = append(ids, x.ID)
		}
		return strings.Join(ids, " ")
	}
	for _, s := range []step{addTask(), addTask("t1"), addTask("t1", "t2")} {
		w.apply(th, s)
	}
	checks := []struct {
		name             string
		step             *step
		ready, claimable string
	}{
		{"only the root", nil, "t1:true t2:false t3:false", "t1"},
		{"root done: its dependent", ptr(moveTask("t1", "done")), "t1:true t2:true t3:false", "t2"},
		{"last dependency done", ptr(moveTask("t2", "done")), "t1:true t2:true t3:true", "t3"},
		{"new task on a failed one", ptr(addTask("t3")), "t1:true t2:true t3:true t4:false", "t3"},
		{"dependency fails", ptr(moveTask("t3", "failed")), "t1:true t2:true t3:true t4:false", ""},
		{"its dependent blocked", ptr(moveTask("t4", "blocked")), "t1:true t2:true t3:true t4:false", ""},
		{"blocked task retried too early", ptr(requeueTask("t4")), "t1:true t2:true t3:true t4:false", ""},
		{"dependency retried", ptr(requeueTask("t3")), "t1:true t2:true t3:true t4:false", "t3"},
		{"dependency done at last", ptr(moveTask("t3", "done")), "t1:true t2:true t3:true t4:true", "t4"},
	}
	for _, c := range checks {
		if c.step != nil {
			w.apply(th, *c.step)
		}
		if got := ready(); got != c.ready {
			t.Fatalf("%s: ready\n got %s\nwant %s", c.name, got, c.ready)
		}
		if got := claimable(); got != c.claimable {
			t.Fatalf("%s: claimable %q, want %q", c.name, got, c.claimable)
		}
	}

	// A missed stamp: t4 lost it long ago, t5 (no dependencies) just now.
	w.apply(th, addTask())
	w.exec(`UPDATE at_tasks SET ready_at = NULL, updated_at = now() - interval '5 minutes' WHERE thread_id = $1 AND id = 't4'`, th.ID)
	w.exec(`UPDATE at_tasks SET ready_at = NULL WHERE thread_id = $1 AND id = 't5'`, th.ID)
	if got := claimable(); got != "" {
		t.Fatalf("claimable without ready_at %q", got)
	}
	got, err := RepairReady(w.ctx, w.s.Pool, 2*time.Minute)
	if err != nil || !slices.Equal(got, []string{th.ID + "/t4"}) {
		t.Fatalf("repaired %v %v", got, err)
	}
	if got := claimable(); got != "t4" {
		t.Fatalf("claimable after the repair %q", got)
	}
}

func ptr[T any](v T) *T { return &v }
