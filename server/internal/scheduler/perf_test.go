package scheduler

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/store"
	"github.com/touwaeriol/agent-teams/server/internal/testdb"
)

// Scale of the scheduling performance check (shared-members-plan.md P4-3).
const (
	perfThreads  = 20
	perfChains   = 5                      // dependency chains per thread
	perfChainLen = 5                      // tasks per chain: perfThreads × perfChains × perfChainLen = 500 tasks
	perfBudget   = 250 * time.Millisecond // generous: a loaded test machine, not the target (~20 ms)
)

// perfWorld is one project with perfThreads running threads, 10 members (7 dev, 3 qa) and
// 500 tasks in dependency chains: in every thread the first chain has its first two tasks done,
// so each thread has a mix of claimable, waiting and finished tasks.
func perfWorld(t *testing.T) (*core.Store, string) {
	t.Helper()
	ctx := context.Background()
	pool, err := store.OpenPool(ctx, testdb.Migrated(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	cs := core.New(pool, nil)
	if _, err := store.Exec(ctx, pool, `INSERT INTO at_users (id, email, password_hash) VALUES ('u1', 'a@example.com', 'x');
INSERT INTO at_projects (id, user_id, name) VALUES ('p1', 'u1', 'A');
INSERT INTO at_nodes (id, user_id, name, token_hash) VALUES ('n1', 'u1', 'one', 'h1'), ('n2', 'u1', 'two', 'h2')`); err != nil {
		t.Fatal(err)
	}
	lead := store.Role{Tier: "lead", Role: "lead", Title: "组长", Runtime: "claude", Count: 1}
	roles := []store.Role{lead, {Tier: "member", Role: "dev", Title: "dev", Runtime: "claude", Count: 7},
		{Tier: "member", Role: "qa", Title: "qa", Runtime: "claude", Count: 3}}
	err = cs.InTx(ctx, func(tx *core.Tx) error {
		if _, err := core.EnsureStaff(ctx, tx, "p1", core.Staffing{Roles: roles, Nodes: []store.Node{{ID: "n1"}, {ID: "n2"}},
			Hair: func(int) *string { return nil }}); err != nil {
			return err
		}
		for i := range perfThreads {
			th, err := core.CreateThread(ctx, tx, core.NewThread{ProjectID: "p1", UserID: "u1", Title: fmt.Sprint("T", i), Roles: []store.Role{lead}})
			if err != nil {
				return err
			}
			if _, err := core.InstantiateThread(ctx, tx, th, nil); err != nil {
				return err
			}
			if err := perfTasks(ctx, tx, th); err != nil {
				return err
			}
		}
		_, err := store.Exec(ctx, tx, `UPDATE at_threads SET status = 'running' WHERE project_id = 'p1'`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	// Planner statistics, as autovacuum keeps them on a live database.
	if _, err := store.Exec(ctx, pool, `ANALYZE`); err != nil {
		t.Fatal(err)
	}
	return cs, "p1"
}

// perfTasks adds a thread's chains: each task depends on the one before it in its chain, the
// last task of a chain also on the second task of the previous chain (a cross edge).
func perfTasks(ctx context.Context, tx *core.Tx, th core.Thread) error {
	var prevChain []string
	for c := range perfChains {
		var chain []string
		for k := range perfChainLen {
			n := core.NewTask{Thread: th, Assignee: "dev", Role: "dev", Instruction: fmt.Sprintf("c%d k%d", c, k), Priority: 100}
			if k%3 == 2 {
				n.Assignee, n.Role = "qa", "qa"
			}
			if k > 0 {
				n.DependsOn = []string{chain[k-1]}
			}
			if k == perfChainLen-1 && prevChain != nil {
				n.DependsOn = append(n.DependsOn, prevChain[1])
			}
			task, err := core.InsertTask(ctx, tx, n)
			if err != nil {
				return err
			}
			if c == 0 && k < 2 {
				if task, err = core.SetTaskStatus(ctx, tx, task, "done"); err != nil {
					return err
				}
			}
			chain = append(chain, task.ID)
		}
		prevChain = chain
	}
	return nil
}

var errRollback = errors.New("rollback")

// perfRound is one scheduling round of claimOne without the brief (which does not depend on the
// queue): lock the project, load idle members and claimable tasks, pick, lock and claim the pair.
func perfRound(ctx context.Context, cs *core.Store, projectID string, commit bool) (bool, time.Duration, error) {
	claimed := false
	start := time.Now()
	err := cs.InTx(ctx, func(tx *core.Tx) error {
		if err := core.LockProject(ctx, tx, projectID); err != nil {
			return err
		}
		m, task, ok, err := (&Scheduler{}).choose(ctx, tx, projectID)
		if err != nil || !ok {
			return err
		}
		if _, err := core.UpdateTask(ctx, tx, task, map[string]any{"status": "claimed", "claimed_by": m.Name,
			"claimed_at": time.Now().UTC()}); err != nil {
			return err
		}
		claimed = true
		if !commit {
			return errRollback
		}
		return nil
	})
	if errors.Is(err, errRollback) {
		err = nil
	}
	return claimed, time.Since(start), err
}

// TestScheduleRoundPerf: with 500 tasks, 20 threads and 10 members a scheduling round takes
// less than perfBudget (median of repeated rounds with every member idle, then the rounds that
// hand out work until every member is busy).
func TestScheduleRoundPerf(t *testing.T) {
	if testing.Short() {
		t.Skip("timing check")
	}
	cs, projectID := perfWorld(t)
	ctx := context.Background()
	var idle []time.Duration
	for range 30 {
		ok, d, err := perfRound(ctx, cs, projectID, false)
		if err != nil || !ok {
			t.Fatalf("round: claimed %v err %v", ok, err)
		}
		idle = append(idle, d)
	}
	var busy []time.Duration
	for {
		ok, d, err := perfRound(ctx, cs, projectID, true)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
		busy = append(busy, d)
	}
	if len(busy) != 10 {
		t.Fatalf("claimed %d tasks, want one per member", len(busy))
	}
	all := append(slices.Clone(idle), busy...)
	slices.Sort(all)
	slices.Sort(idle)
	t.Logf("round (all idle, rolled back) p50 %v max %v; claiming rounds %v; overall p50 %v max %v",
		idle[len(idle)/2], idle[len(idle)-1], busy, all[len(all)/2], all[len(all)-1])
	if p50 := all[len(all)/2]; p50 > perfBudget {
		t.Fatalf("scheduling round p50 %v > %v", p50, perfBudget)
	}
}
