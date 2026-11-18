package engine_test

import (
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
