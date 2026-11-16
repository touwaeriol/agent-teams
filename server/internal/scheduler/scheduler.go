// Package scheduler is the task pool (architecture.md §7.2, §7.3): two-level
// tasks (plan steps and small tasks), claiming by role with dependencies and
// idle instances (FOR UPDATE SKIP LOCKED), lazy member sessions, completion
// with review/decision gates, retries, the "turn ended but task still running"
// nudge and automatic ticking of plan steps.
package scheduler

import (
	"context"
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

// kickChannel relays scheduling hints between replicas.
const kickChannel = "sched:all"

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

	mu      sync.Mutex
	pending map[string]bool
	signal  chan struct{}
}

// New builds the scheduler.
func New(cs *core.Store, orch *orchestrator.Orchestrator, se *status.Engine, hub *fanout.Hub, log *slog.Logger) *Scheduler {
	return &Scheduler{core: cs, orch: orch, status: se, hub: hub, log: log, Interval: 10 * time.Second,
		TaskFinished: func(context.Context, *core.Tx, core.Task) error { return nil },
		pending:      map[string]bool{}, signal: make(chan struct{}, 1)}
}

// Kick asks for a scheduling pass over a thread (local and other replicas).
func (s *Scheduler) Kick(ctx context.Context, threadID string) {
	s.mark(threadID)
	s.hub.Publish(ctx, kickChannel, threadID)
}

func (s *Scheduler) mark(threadID string) {
	s.mu.Lock()
	s.pending[threadID] = true
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

// Run is the scheduling loop; only the advisory-lock holder schedules.
func (s *Scheduler) Run(ctx context.Context) {
	hints, cancel := s.hub.Subscribe(kickChannel)
	defer cancel()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case id := <-hints:
				s.mark(id)
			}
		}
	}()
	RunAsLeader(ctx, s.core.Pool, "agent-teams:scheduler", 5*time.Second, s.loop)
}

func (s *Scheduler) loop(ctx context.Context) {
	t := time.NewTicker(s.Interval)
	defer t.Stop()
	s.sweep(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.signal:
			for _, id := range s.take() {
				s.Schedule(ctx, id)
			}
		case <-t.C:
			s.sweep(ctx)
		}
	}
}

// sweep schedules every active thread with claimable work (the 10 s safety net).
func (s *Scheduler) sweep(ctx context.Context) {
	var ids []string
	err := s.core.Pool.Gorm(ctx).Table("at_tasks t").Distinct("t.thread_id").Joins("JOIN at_threads th ON th.id = t.thread_id").
		Where("t.status = 'pending' AND th.status IN ('running','waiting')").Pluck("t.thread_id", &ids).Error
	if err != nil {
		s.log.Warn("scheduler sweep", "err", err)
		return
	}
	for _, id := range ids {
		s.Schedule(ctx, id)
	}
}

// Schedule claims as many pending tasks of the thread as there are idle instances.
func (s *Scheduler) Schedule(ctx context.Context, threadID string) {
	for i := 0; i < 50; i++ {
		claimed, err := s.claimOne(ctx, threadID)
		if err != nil {
			s.log.Warn("claim task", "thread_id", threadID, "err", err)
			return
		}
		if !claimed {
			return
		}
	}
}
