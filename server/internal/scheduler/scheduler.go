// Package scheduler is the task pool (architecture.md §7.2, §7.3): two-level
// tasks (plan steps and small tasks), claiming by role with dependencies and
// idle instances (FOR UPDATE SKIP LOCKED), lazy member sessions, completion
// with review/decision gates, retries, the "turn ended but task still running"
// nudge and automatic ticking of plan steps.
package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/fanout"
	"github.com/touwaeriol/agent-teams/server/internal/orchestrator"
	"github.com/touwaeriol/agent-teams/server/internal/status"
)

// MaxAttempts is how often a failed task returns to the pool before it fails.
const MaxAttempts = 2

// kickPrefix + project id is the channel that relays scheduling hints between replicas.
const kickPrefix = "sched:"

// Scheduler runs the task pool.
type Scheduler struct {
	core     *core.Store
	orch     *orchestrator.Orchestrator
	status   *status.Engine
	hub      *fanout.Hub
	log      *slog.Logger
	Interval time.Duration

	// TaskFinished runs inside the transaction that finished a task (wait_task / request_review results).
	TaskFinished func(ctx context.Context, tx *core.Tx, t core.Task) error
	// PushWork pushes a member's committed work to its member branch (delivery.Push), so it
	// survives the node going away (shared-members.md §4.3).
	PushWork func(ctx context.Context, tx *core.Tx, e core.Employee, t core.Thread) error
	// AutoPushEvery throttles PushWork per member.
	AutoPushEvery time.Duration

	mu       sync.Mutex
	pending  map[string]bool
	signal   chan struct{}
	lastPush map[string]time.Time // employee id → last automatic push
}

// New builds the scheduler.
func New(cs *core.Store, orch *orchestrator.Orchestrator, se *status.Engine, hub *fanout.Hub, log *slog.Logger) *Scheduler {
	return &Scheduler{core: cs, orch: orch, status: se, hub: hub, log: log, Interval: 10 * time.Second,
		TaskFinished:  func(context.Context, *core.Tx, core.Task) error { return nil },
		PushWork:      func(context.Context, *core.Tx, core.Employee, core.Thread) error { return nil },
		AutoPushEvery: 5 * time.Minute,
		pending:       map[string]bool{}, signal: make(chan struct{}, 1), lastPush: map[string]time.Time{}}
}

// Kick asks for a scheduling pass over a project's task queue (local and other replicas).
func (s *Scheduler) Kick(ctx context.Context, projectID string) {
	s.mark(projectID)
	s.hub.Publish(ctx, kickPrefix+projectID, projectID)
}

func (s *Scheduler) mark(projectID string) {
	s.mu.Lock()
	s.pending[projectID] = true
	s.mu.Unlock()
	select {
	case s.signal <- struct{}{}:
	default:
	}
}

func (s *Scheduler) take() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.pending))
	for id := range s.pending {
		out = append(out, id)
	}
	s.pending = map[string]bool{}
	return out
}

// ListenHints marks the projects other replicas hint at (every replica listens; the replica running
// the scheduling job schedules them when Signal fires).
func (s *Scheduler) ListenHints(ctx context.Context) {
	hints, cancel := s.hub.SubscribePrefix(kickPrefix)
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-hints:
			s.mark(id)
		}
	}
}

// Signal fires when projects were marked for scheduling.
func (s *Scheduler) Signal() <-chan struct{} { return s.signal }

// Round is one scheduling round (the background job, on the scheduler lock's holder): the marked
// projects when woken, else the sweep. It says how many projects it went through.
func (s *Scheduler) Round(ctx context.Context, woken bool) string {
	n := 0
	for _, id := range s.take() {
		s.Schedule(ctx, id)
		n++
	}
	if !woken {
		n += s.sweep(ctx)
	}
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("调度了 %d 个项目", n)
}

// sweep schedules every project with claimable work (the 10 s safety net) and moves on the tasks
// whose pre-task compaction or sync timed out. A project that has work but no members yet (created before
// members were project staff) is staffed first.
func (s *Scheduler) sweep(ctx context.Context) int {
	s.releaseStaleCompactions(ctx)
	s.releaseStaleSyncs(ctx)
	s.repairReady(ctx)
	ids, err := core.ProjectsWithWork(ctx, s.core.Pool)
	if err != nil {
		s.log.Warn("scheduler sweep", "err", err)
		return 0
	}
	for _, id := range ids {
		s.staff(ctx, id)
		s.Schedule(ctx, id)
	}
	return len(ids)
}

// ReadyRepairAfter is how long a pending task whose dependencies are all done may stay without
// ready_at before the sweep stamps it (the events stamp it at once; this only catches misses).
const ReadyRepairAfter = 2 * time.Minute

// repairReady stamps the tasks an event failed to make ready, and says so: each one is a bug.
// It also recounts the downstream of the live threads: a settling after a commit skips rows
// other transactions hold, so a count may lag until here (a tie-break; nothing to report).
func (s *Scheduler) repairReady(ctx context.Context) {
	ids, err := core.RepairReady(ctx, s.core.Pool, ReadyRepairAfter)
	if err != nil {
		s.log.Warn("repair ready", "err", err)
	}
	if len(ids) > 0 {
		s.log.Warn("tasks were ready without ready_at", "tasks", ids)
	}
	threads, err := core.LiveTaskThreads(ctx, s.core.Pool)
	if err == nil && len(threads) > 0 {
		err = core.RecountDownstream(ctx, s.core.Pool, threads)
	}
	if err != nil {
		s.log.Warn("recount downstream", "err", err)
	}
}

// staff creates the project's members when it has none (EnsureStaff otherwise runs when a
// thread opens and when the role templates are saved).
func (s *Scheduler) staff(ctx context.Context, projectID string) {
	err := s.core.InTx(ctx, func(tx *core.Tx) error {
		has, err := core.HasStaff(ctx, tx, projectID)
		if err != nil || has {
			return err
		}
		return s.orch.EnsureStaff(ctx, tx, projectID)
	})
	if err != nil {
		s.log.Warn("staff project", "project_id", projectID, "err", err)
	}
}

// Schedule claims tasks of the project's queue while idle members can take them.
func (s *Scheduler) Schedule(ctx context.Context, projectID string) {
	for i := 0; i < 50; i++ {
		claimed, err := s.claimOne(ctx, projectID)
		if err != nil {
			s.log.Warn("claim task", "project_id", projectID, "err", err)
			return
		}
		if !claimed {
			return
		}
	}
}
