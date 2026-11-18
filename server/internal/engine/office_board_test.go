package engine_test

import (
	"slices"
	"strconv"
	"testing"

	"github.com/touwaeriol/agent-teams/server/internal/orchapi"
)

// TestOfficeBoardQueue: the office board lists the project queue (pending tasks only); a task
// waiting for an unfinished dependency is locked; a rework reservation names the member only
// while it holds. Employees carry their role order and instance number for the desk rows.
func TestOfficeBoardQueue(t *testing.T) {
	tm := sharedTeam(t)
	one, lead := tm.openThread(t, "One", nil)
	lead.mustCall("assign_task", map[string]any{"assignee": "dev", "instruction": "first", "main": "做"})
	startTask(t, tm, "dev-1", "first")
	lead.mustCall("assign_task", map[string]any{"assignee": "dev", "instruction": "second", "main": "做", "depends_on": []string{"t1"}})

	office := tm.office(t)
	if len(office.Board.Queue) != 1 {
		t.Fatalf("queue %+v", office.Board.Queue)
	}
	want := orchapi.QueueCard{ThreadNo: one.No, ID: "t2", Role: "dev", Locked: true}
	if got := office.Board.Queue[0]; got != want {
		t.Fatalf("card %+v, want %+v", got, want)
	}

	db := tm.w.e.Core.Pool.Gorm(tm.w.ctx)
	if err := db.Exec(`UPDATE at_tasks SET reserved_for = 'dev-2', reserved_until = now() + interval '5 minutes' WHERE id = 't2'`).Error; err != nil {
		t.Fatal(err)
	}
	if got := tm.office(t).Board.Queue[0].ReservedFor; got != "dev-2" {
		t.Fatalf("reserved_for %q", got)
	}
	if err := db.Exec(`UPDATE at_tasks SET reserved_until = now() - interval '1 minute' WHERE id = 't2'`).Error; err != nil {
		t.Fatal(err)
	}
	if got := tm.office(t).Board.Queue[0].ReservedFor; got != "" {
		t.Fatalf("expired reservation still shown: %q", got)
	}

	dev1, dev2, lead1 := briefOf(t, office.Employees, "dev-1"), briefOf(t, office.Employees, "dev-2"), briefOf(t, office.Employees, "lead-1")
	if dev1.Instance != 1 || dev2.Instance != 2 || dev1.Sort != dev2.Sort || dev1.Sort <= lead1.Sort {
		t.Fatalf("desk order: dev-1 %d/%d dev-2 %d/%d lead-1 %d", dev1.Sort, dev1.Instance, dev2.Sort, dev2.Instance, lead1.Sort)
	}
}

func (tm *team) office(t *testing.T) orchapi.OfficeSnapshot {
	t.Helper()
	var office orchapi.OfficeSnapshot
	tm.b.must(200, "GET", "/api/projects/"+tm.projectID+"/office", nil, &office)
	return office
}

// TestOfficeBoardQueueOrder: the board lists each thread's queue in claim order (lower priority
// number first, then the longest waiting), at most 12 cards per thread so a later thread's cards
// still show; tasks of a paused thread are not claimable and leave the board; a task named to a
// retired member shows as the role's.
func TestOfficeBoardQueueOrder(t *testing.T) {
	tm := sharedTeam(t)
	one, lead1 := tm.openThread(t, "One", nil)
	lead1.mustCall("assign_task", map[string]any{"assignee": "dev", "instruction": "first", "main": "做"})
	startTask(t, tm, "dev-1", "first")
	for i := 0; i < 13; i++ {
		lead1.mustCall("assign_task", map[string]any{"assignee": "dev", "instruction": "later", "main": "做", "depends_on": []string{"t1"}})
	}
	two, lead2 := tm.openThread(t, "Two", nil)
	lead2.mustCall("assign_task", map[string]any{"assignee": "dev", "instruction": "other", "main": "做"})
	startTask(t, tm, "dev-2", "other")
	lead2.mustCall("assign_task", map[string]any{"assignee": "dev", "instruction": "next", "main": "做", "depends_on": []string{"t1"}})

	db := tm.w.e.Core.Pool.Gorm(tm.w.ctx)
	exec := func(sql string, args ...any) {
		t.Helper()
		if err := db.Exec(sql, args...).Error; err != nil {
			t.Fatal(err)
		}
	}
	exec(`UPDATE at_tasks SET priority = 500 WHERE thread_id = ? AND id = 't2'`, one.ID)
	exec(`UPDATE at_tasks SET priority = 1 WHERE thread_id = ? AND id = 't9'`, one.ID)

	board := tm.office(t).Board
	var ids []string
	perThread := map[int]int{}
	for _, c := range board.Queue {
		perThread[c.ThreadNo]++
		if c.ThreadNo == one.No {
			ids = append(ids, c.ID)
		}
	}
	if perThread[one.No] != 12 || perThread[two.No] != 1 {
		t.Fatalf("cards per thread %v", perThread)
	}
	if ids[0] != "t9" || ids[1] != "t3" || slices.Contains(ids, "t2") {
		t.Fatalf("thread #%d order %v", one.No, ids)
	}
	if board.Claimable != 14 {
		t.Fatalf("claimable %d", board.Claimable)
	}

	exec(`UPDATE at_tasks SET assignee = 'dev-9' WHERE thread_id = ? AND id = 't2'`, two.ID)
	if got := cardOf(t, tm.office(t).Board, two.No, "t2").Assignee; got != "" {
		t.Fatalf("retired member shown as assignee %q", got)
	}
	exec(`UPDATE at_tasks SET assignee = 'dev-1' WHERE thread_id = ? AND id = 't2'`, two.ID)
	if got := cardOf(t, tm.office(t).Board, two.No, "t2").Assignee; got != "dev-1" {
		t.Fatalf("named member %q", got)
	}

	exec(`UPDATE at_threads SET status = 'paused' WHERE id = ?`, two.ID)
	board = tm.office(t).Board
	for _, c := range board.Queue {
		if c.ThreadNo == two.No {
			t.Fatalf("paused thread's card on the board: %+v", c)
		}
	}
	if board.Claimable != 13 || board.ByThread[strconv.Itoa(two.No)]["pending"] != 1 {
		t.Fatalf("claimable %d, by thread %v", board.Claimable, board.ByThread)
	}
}

func cardOf(t *testing.T, b orchapi.Board, no int, id string) orchapi.QueueCard {
	t.Helper()
	for _, c := range b.Queue {
		if c.ThreadNo == no && c.ID == id {
			return c
		}
	}
	t.Fatalf("#%d %s not on the board %+v", no, id, b.Queue)
	return orchapi.QueueCard{}
}
