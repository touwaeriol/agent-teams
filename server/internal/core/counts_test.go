package core

import "testing"

// TasksOpen counts the small tasks still going (queued, claimed, in review, ...): the web tells
// "the lead stopped and waits on the user" from "members still have work" by it.
func TestThreadCountsTasksOpen(t *testing.T) {
	w := newStaffWorld(t)
	th := w.thread("counts")
	counts := func() ThreadCounts {
		t.Helper()
		c, err := ThreadCountsOf(w.ctx, w.s.Pool, th)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	if c := counts(); c.TasksOpen != 0 || c.TasksTotal != 0 {
		t.Fatalf("empty thread counts %+v", c)
	}
	for _, s := range []string{"pending", "running", "review", "done", "failed", "cancelled"} {
		w.task(th, "", s)
	}
	c := counts()
	if c.TasksOpen != 3 || c.TasksTotal != 5 || c.TasksDone != 1 {
		t.Fatalf("counts %+v, want open 3 (pending, running, review), total 5, done 1", c)
	}
}
