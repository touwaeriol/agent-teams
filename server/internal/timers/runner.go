package timers

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/riverqueue/river"
	"gorm.io/gorm/clause"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/orchestrator"
	"github.com/touwaeriol/agent-teams/server/internal/scheduler"
)

// Runner runs scheduled tasks (on River, river.go) and, in a 20 s loop, resumes threads whose
// usage limit has reset and fires thread reminders.
type Runner struct {
	core     *core.Store
	orch     *orchestrator.Orchestrator
	river    *river.Client[*sql.Tx]
	log      *slog.Logger
	Interval time.Duration
	Now      func() time.Time
}

// New returns a runner whose loop checks every 20 seconds.
func New(cs *core.Store, orch *orchestrator.Orchestrator, log *slog.Logger) (*Runner, error) {
	r := &Runner{core: cs, orch: orch, log: log, Interval: 20 * time.Second, Now: time.Now}
	rc, err := newRiver(cs.Pool, r, log)
	if err != nil {
		return nil, err
	}
	r.river = rc
	return r, nil
}

// Run starts River (every replica works jobs; River elects its own leader for maintenance and
// the periodic reconcile) and loops while this process holds the timers lock.
func (r *Runner) Run(ctx context.Context) {
	go r.startRiver(ctx)
	scheduler.RunAsLeader(ctx, r.core.Pool, "agent-teams:timers", 5*time.Second, r.loop)
}

func (r *Runner) loop(ctx context.Context) {
	t := time.NewTicker(r.Interval)
	defer t.Stop()
	for {
		r.Tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Tick does one round: usage-limit resumes first, then thread reminders, then stale session moves.
func (r *Runner) Tick(ctx context.Context) {
	if err := r.resumeWaits(ctx); err != nil && ctx.Err() == nil {
		r.log.Warn("resume after usage limit", "err", err)
	}
	if err := r.FireReminders(ctx); err != nil && ctx.Err() == nil {
		r.log.Warn("thread reminders", "err", err)
	}
	if err := r.orch.SweepMoves(ctx, r.Now()); err != nil && ctx.Err() == nil {
		r.log.Warn("session moves", "err", err)
	}
}

// resumeWaits ends the waits whose limit has reset. A thread resumes only once every
// waiting employee in it is past its reset, so nobody works into a limit still in force.
func (r *Runner) resumeWaits(ctx context.Context) error {
	return r.core.InTx(ctx, func(tx *core.Tx) error {
		var due []dueWait
		now := r.Now()
		err := tx.Gorm(ctx).Table("at_rate_limit_waits w").Select("w.employee_id, w.thread_id, p.auto_resume").
			Joins("JOIN at_projects p ON p.id = w.project_id").
			Where(`w.reset_at <= ? AND NOT EXISTS (SELECT 1 FROM at_rate_limit_waits o
				WHERE o.thread_id = w.thread_id AND o.reset_at > ?)`, now, now).
			Order("w.reset_at").Limit(50).
			Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate, Table: clause.Table{Name: "w"},
				Options: clause.LockingOptionsSkipLocked}).
			Find(&due).Error
		if err != nil {
			return err
		}
		for _, w := range due {
			if err := resumeDue(ctx, tx, r.orch, w); err != nil {
				return err
			}
		}
		return nil
	})
}

// FireReminders fires every thread reminder that is due: remind_at moves to reminded_at in one
// statement (so a reminder fires once), and each thread's new view goes out to the browsers,
// which list it under 等你处理 and notify the user.
func (r *Runner) FireReminders(ctx context.Context) error {
	return r.core.InTx(ctx, func(tx *core.Tx) error {
		ids, err := core.FireDueReminders(ctx, tx, r.Now())
		if err != nil {
			return err
		}
		for _, id := range ids {
			if err := tx.EmitThread(ctx, id); err != nil {
				return err
			}
		}
		return nil
	})
}
