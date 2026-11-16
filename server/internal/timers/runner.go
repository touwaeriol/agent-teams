package timers

import (
	"context"
	"log/slog"
	"time"

	"gorm.io/gorm/clause"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/orchestrator"
	"github.com/touwaeriol/agent-teams/server/internal/scheduler"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// Runner fires due schedules and resumes threads whose usage limit has reset.
type Runner struct {
	core     *core.Store
	orch     *orchestrator.Orchestrator
	log      *slog.Logger
	Interval time.Duration
	Now      func() time.Time
}

// New returns a runner that checks every 20 seconds.
func New(cs *core.Store, orch *orchestrator.Orchestrator, log *slog.Logger) *Runner {
	return &Runner{core: cs, orch: orch, log: log, Interval: 20 * time.Second, Now: time.Now}
}

// Run loops while this process holds the timers lock.
func (r *Runner) Run(ctx context.Context) {
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

// Tick does one round: usage-limit resumes first, then thread reminders, then due schedules.
func (r *Runner) Tick(ctx context.Context) {
	if err := r.resumeWaits(ctx); err != nil && ctx.Err() == nil {
		r.log.Warn("resume after usage limit", "err", err)
	}
	if err := r.FireReminders(ctx); err != nil && ctx.Err() == nil {
		r.log.Warn("thread reminders", "err", err)
	}
	for ctx.Err() == nil {
		fired, err := r.FireOne(ctx)
		if err != nil {
			r.log.Warn("scheduled task", "err", err)
		}
		if !fired {
			return
		}
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

// FireOne claims one due schedule (moving its next run on), then posts its message to the
// coordinator. It reports whether a schedule was due.
func (r *Runner) FireOne(ctx context.Context) (bool, error) {
	var s Schedule
	var owner string
	var archived bool
	err := r.core.InTx(ctx, func(tx *core.Tx) error {
		var err error
		s, owner, archived, err = claimDue(ctx, tx, r.Now())
		return err
	})
	if err != nil || s.ID == "" {
		return false, ignoreMissing(err)
	}
	if archived {
		return true, nil // an archived project's schedules move on without waking its coordinator
	}
	return true, r.post(ctx, s, owner)
}

func claimDue(ctx context.Context, tx *core.Tx, now time.Time) (Schedule, string, bool, error) {
	s, err := takeSchedule(schedules(ctx, tx).Clauses(core.SkipLocked).Where("enabled AND next_run_at <= ?", now).
		Order("next_run_at"))
	if err != nil {
		return Schedule{}, "", false, err
	}
	var owner string
	var archived bool
	if err := store.QueryRow(ctx, tx, `UPDATE at_schedules s SET last_run_at = $2, next_run_at = $3, last_error = '', updated_at = now()
		FROM at_projects p WHERE s.id = $1 AND p.id = s.project_id RETURNING p.user_id, p.archived`, s.ID, now, NextRun(s, now)).
		Scan(&owner, &archived); err != nil {
		return Schedule{}, "", false, err
	}
	return s, owner, archived, nil
}

// Fire posts a schedule's message now (the "立即运行" button); next_run_at stays.
func (r *Runner) Fire(ctx context.Context, s Schedule, owner string) error {
	err := r.core.Pool.Gorm(ctx).Table("at_schedules").Where("id = ?", s.ID).
		Updates(map[string]any{"last_run_at": r.Now(), "last_error": ""}).Error
	if err != nil {
		return err
	}
	return r.post(ctx, s, owner)
}

// ScheduledText is the message the coordinator receives.
func ScheduledText(s Schedule) string { return "【定时任务：" + s.Name + "】\n" + s.Message }

func (r *Runner) post(ctx context.Context, s Schedule, owner string) error {
	_, err := r.orch.PostToCoordinator(ctx, owner, s.ProjectID, ScheduledText(s),
		map[string]any{"schedule_id": s.ID, "schedule_name": s.Name})
	if err == nil {
		return nil
	}
	msg := err.Error()
	if len(msg) > 500 {
		msg = msg[:500]
	}
	uerr := r.core.Pool.Gorm(ctx).Table("at_schedules").Where("id = ?", s.ID).Update("last_error", msg).Error
	if uerr != nil {
		return uerr
	}
	return err
}
