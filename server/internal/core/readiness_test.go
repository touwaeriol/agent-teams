package core

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// step is one change to a thread's tasks: add a task (deps by id; with status, moved there in
// the same transaction), move a task to a status (SetTaskStatus) or requeue it (RequeueTask).
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
			task, err := InsertTask(w.ctx, tx, NewTask{Thread: th, Assignee: "dev", Role: "dev", DependsOn: s.add})
			if err != nil || s.status == "" {
				return err
			}
			_, err = SetTaskStatus(w.ctx, tx, task, s.status)
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
		{"a task finished where it was added is not counted", []step{addTask(), addTaskAs("done", "t1"), addTaskAs("cancelled", "t1")}, "t1:0 t2:0 t3:0"},
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

// interleave runs each fn in its own transaction, all open at once: every fn runs to its end
// before any transaction commits; then they commit in argument order, each commit's settling
// (Store.SettleTasks) finished before the next commit. A fn that waits for a row lock the others hold
// never ends: that fails the test (status changes must not wait on related tasks' rows).
func (w *staffWorld) interleave(fns ...func(tx *Tx) error) {
	w.t.Helper()
	ran := make(chan int, len(fns))
	commit := make([]chan struct{}, len(fns))
	done := make([]chan error, len(fns))
	for i, fn := range fns {
		commit[i], done[i] = make(chan struct{}), make(chan error, 1)
		go func() {
			done[i] <- w.s.InTx(w.ctx, func(tx *Tx) error {
				err := fn(tx)
				ran <- i
				<-commit[i]
				return err
			})
		}()
	}
	release := func() {
		for _, c := range commit {
			close(c)
		}
	}
	for range fns {
		select {
		case <-ran:
		case <-time.After(10 * time.Second):
			release()
			w.t.Fatal("a transaction waited for another one's locks")
		}
	}
	for i := range fns {
		close(commit[i])
		if err := <-done[i]; err != nil {
			for _, c := range commit[i+1:] {
				close(c)
			}
			w.t.Fatalf("transaction %d: %v", i, err)
		}
	}
}

// statusOf returns a transaction step moving task id of th to status.
func (w *staffWorld) statusOf(th Thread, id, status string) func(tx *Tx) error {
	return func(tx *Tx) error {
		task, err := TaskByID(w.ctx, tx, th.ID, id)
		if err == nil {
			_, err = SetTaskStatus(w.ctx, tx, task, status)
		}
		return err
	}
}

// TestReadyConcurrentCommits: ready_at and downstream are right whatever two transactions do at
// once under READ COMMITTED, where neither sees the other's change before it commits: the two
// last dependencies of a task done at once, a task added or a blocked task retried while its
// dependency is being done; in both commit orders.
func TestReadyConcurrentCommits(t *testing.T) {
	w := newStaffWorld(t)
	w.ensure(lead(), member("dev", 1))
	cases := []struct {
		name  string
		setup []step
		a, b  func(th Thread) func(tx *Tx) error
		ready string
	}{
		{"two last dependencies done at once", []step{addTask(), addTask(), addTask("t1", "t2")},
			func(th Thread) func(tx *Tx) error { return w.statusOf(th, "t1", "done") },
			func(th Thread) func(tx *Tx) error { return w.statusOf(th, "t2", "done") },
			"t1:true t2:true t3:true"},
		{"task added on a dependency being done", []step{addTask()},
			func(th Thread) func(tx *Tx) error { return w.statusOf(th, "t1", "done") },
			func(th Thread) func(tx *Tx) error {
				return func(tx *Tx) error {
					_, err := InsertTask(w.ctx, tx, NewTask{Thread: th, Assignee: "dev", Role: "dev", DependsOn: []string{"t1"}})
					return err
				}
			},
			"t1:true t2:true"},
		{"blocked task retried while its dependency is being done", []step{addTask(), addTask("t1"), moveTask("t2", "blocked")},
			func(th Thread) func(tx *Tx) error { return w.statusOf(th, "t1", "done") },
			func(th Thread) func(tx *Tx) error {
				return func(tx *Tx) error {
					task, err := TaskByID(w.ctx, tx, th.ID, "t2")
					if err == nil {
						_, err = RequeueTask(w.ctx, tx, task, map[string]any{"claimed_by": ""})
					}
					return err
				}
			},
			"t1:true t2:true"},
	}
	n := 0
	for _, c := range cases {
		for _, order := range []string{"a first", "b first"} {
			t.Run(c.name+", "+order, func(t *testing.T) {
				n++
				th := w.thread(fmt.Sprint("C", n))
				for _, s := range c.setup {
					w.apply(th, s)
				}
				a, b := c.a(th), c.b(th)
				if order == "b first" {
					a, b = b, a
				}
				w.interleave(a, b)
				if got := w.render(th, "(ready_at IS NOT NULL)::text"); got != c.ready {
					t.Fatalf("ready\n got %s\nwant %s", got, c.ready)
				}
				if got, want := w.render(th, "downstream"), w.recount(th); got != want {
					t.Fatalf("downstream %s, recount %s", got, want)
				}
			})
		}
	}
}

// race moves two tasks of th at once, each in its own transaction that first locks its task and
// then waits until the other has locked its own: the classic setup of a deadlock when either
// change went on to lock the other's row. Both must commit.
func (w *staffWorld) race(th Thread, a, b [2]string) error {
	locked := make(chan struct{}, 2)
	start := make(chan struct{})
	errs := make(chan error, 2)
	for _, m := range [][2]string{a, b} {
		go func() {
			errs <- w.s.InTx(w.ctx, func(tx *Tx) error {
				task, err := LockTask(w.ctx, tx, th.ID, m[0])
				locked <- struct{}{}
				<-start
				if err == nil {
					_, err = SetTaskStatus(w.ctx, tx, task, m[1])
				}
				return err
			})
		}()
	}
	<-locked
	<-locked
	close(start)
	timeout := time.After(20 * time.Second)
	var err error
	for range 2 {
		select {
		case e := <-errs:
			err = errors.Join(err, e)
		case <-timeout:
			return errors.New("the transactions did not finish")
		}
	}
	return err
}

// TestStatusChangesNeverDeadlock: finishing a task while a pending task that depends on it
// (directly or through a third) is cancelled, failed or blocked, each transaction holding its own
// task first: neither change locks the other's row while waiting (MarkReady skips a dependent
// another transaction holds; downstream is recounted after the commit), so there is no deadlock,
// many times over; afterwards readiness and the downstream counts are right.
func TestStatusChangesNeverDeadlock(t *testing.T) {
	w := newStaffWorld(t)
	w.ensure(lead(), member("dev", 1))
	chain := []step{addTask(), addTask("t1"), addTask("t2"), addTask("t3")} // t1 ← t2 ← t3 ← t4
	pairs := [][2][2]string{
		{{"t2", "done"}, {"t3", "cancelled"}},
		{{"t2", "done"}, {"t3", "failed"}},
		{{"t2", "done"}, {"t3", "blocked"}},
		{{"t1", "done"}, {"t3", "cancelled"}},
		{{"t2", "cancelled"}, {"t3", "cancelled"}},
		{{"t1", "done"}, {"t2", "failed"}},
	}
	for i := range 60 {
		p := pairs[i%len(pairs)]
		th := w.thread(fmt.Sprint("W", i))
		for _, s := range chain {
			w.apply(th, s)
		}
		if err := w.race(th, p[0], p[1]); err != nil {
			t.Fatalf("round %d %v: %v", i, p, err)
		}
		if got, want := w.render(th, "downstream"), w.recount(th); got != want {
			t.Fatalf("round %d %v: downstream %s, recount %s", i, p, got, want)
		}
		var wrong int
		if err := w.s.Pool.Gorm(w.ctx).Raw(`SELECT count(*) FROM at_tasks t WHERE t.thread_id = $1 AND t.status = 'pending'
			AND (t.ready_at IS NOT NULL) <> (`+depsDone+`)`, th.ID).Scan(&wrong).Error; err != nil || wrong != 0 {
			t.Fatalf("round %d %v: pending tasks with a wrong ready_at: %d %v (%s)", i, p, wrong, err,
				w.render(th, "status || '/' || (ready_at IS NOT NULL)::text"))
		}
	}
}

// TestDoneIsFinal: a done task never leaves done (a ready task's dependencies stay done).
func TestDoneIsFinal(t *testing.T) {
	w := newStaffWorld(t)
	w.ensure(lead(), member("dev", 1))
	th := w.thread("F")
	w.apply(th, addTask())
	w.apply(th, moveTask("t1", "done"))
	for _, to := range []string{"failed", "cancelled", "blocked", "running", "requeue"} {
		err := w.s.InTx(w.ctx, func(tx *Tx) error {
			task, err := TaskByID(w.ctx, tx, th.ID, "t1")
			if err != nil {
				return err
			}
			if to == "requeue" {
				_, err = RequeueTask(w.ctx, tx, task, nil)
			} else {
				_, err = SetTaskStatus(w.ctx, tx, task, to)
			}
			return err
		})
		if !errors.Is(err, ErrTaskDone) {
			t.Fatalf("done → %s: %v", to, err)
		}
	}
	if got := w.render(th, "status"); got != "t1:done" {
		t.Fatalf("after the refusals %s", got)
	}
}
