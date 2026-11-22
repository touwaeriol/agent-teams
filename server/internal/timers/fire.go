package timers

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/orchestrator"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// Create stores a new schedule and enqueues its first run, in tx.
func (r *Runner) Create(ctx context.Context, tx *core.Tx, o Owner, in ScheduleInput) (Schedule, error) {
	s, err := createSchedule(ctx, tx, o, in, r.Now())
	if err != nil {
		return s, err
	}
	return s, r.enqueue(ctx, tx, s)
}

// Update changes a schedule and enqueues its (new) next run, in tx. byAgent applies the limits
// agents work within. The job of the old next run, if any, finds the row moved on and does nothing.
func (r *Runner) Update(ctx context.Context, tx *core.Tx, cur Schedule, in ScheduleInput, byAgent bool) (Schedule, error) {
	s, err := updateSchedule(ctx, tx, cur, in, r.Now(), byAgent)
	if err != nil {
		return s, err
	}
	return s, r.enqueue(ctx, tx, s)
}

// Cancel ends an active schedule (an agent's schedule_cancel); its pending job does nothing.
func (r *Runner) Cancel(ctx context.Context, tx *core.Tx, s Schedule) error {
	if s.Status != StatusActive {
		return ErrInvalid{"status", "这个定时任务已经结束了"}
	}
	return endSchedule(ctx, tx, s.ID, StatusCancelled, r.Now())
}

// Fire sends a schedule's message now (the "立即运行" button); next_run_at stays.
func (r *Runner) Fire(ctx context.Context, s Schedule, owner string) error {
	return r.core.InTx(ctx, func(tx *core.Tx) error {
		cur, err := lockSchedule(ctx, tx, s.ID)
		if err != nil {
			return err
		}
		if _, err := r.deliver(ctx, tx, cur, owner, r.Now(), false); err != nil {
			return err
		}
		return tx.Gorm(ctx).Table("at_schedules").Where("id = ?", s.ID).Updates(map[string]any{"last_run_at": r.Now(), "last_error": ""}).Error
	})
}

// FireDue fires every schedule whose next run is due, as their River jobs would (tests).
func (r *Runner) FireDue(ctx context.Context) error {
	var due []Schedule
	err := r.core.Pool.Gorm(ctx).Model(&Schedule{}).Where("enabled AND status = ? AND next_run_at <= ?", StatusActive, r.Now()).
		Order("next_run_at").Find(&due).Error
	if err != nil {
		return err
	}
	for _, s := range due {
		if err := r.fire(ctx, FireArgs{ScheduleID: s.ID, RunAt: *s.NextRunAt}, true); err != nil {
			return err
		}
	}
	return nil
}

// fire is a schedule_fire job: when the schedule still plans this run, deliver its message, move
// it on and enqueue the next run, in one transaction. A failed delivery returns the error (River
// tries again later) and is kept in last_error; on the last try the schedule moves on anyway.
func (r *Runner) fire(ctx context.Context, args FireArgs, lastTry bool) error {
	var deliverErr error
	err := r.core.InTx(ctx, func(tx *core.Tx) error {
		s, err := lockSchedule(ctx, tx, args.ScheduleID)
		if errors.Is(err, core.ErrNotFound) {
			return nil // deleted
		}
		if err != nil {
			return err
		}
		if s.Status != StatusActive || !s.Enabled || s.NextRunAt == nil || !s.NextRunAt.Equal(args.RunAt) {
			return nil // paused, ended or moved on since this job was enqueued
		}
		now := r.Now()
		if s.Kind == KindOnce && now.Sub(args.RunAt) > MissedAfter {
			return endSchedule(ctx, tx, s.ID, StatusMissed, now)
		}
		var owner string
		var archived bool
		if err := store.QueryRow(ctx, tx, `SELECT user_id, archived FROM at_projects WHERE id = $1`, s.ProjectID).Scan(&owner, &archived); err != nil {
			return err
		}
		ended := false
		if !archived { // an archived project's schedules move on without waking anyone
			err = tx.Savepoint(ctx, func(sub *core.Tx) error {
				var derr error
				ended, derr = r.deliver(ctx, sub, s, owner, args.RunAt, true)
				return derr
			})
		}
		var skip skipped
		if err != nil && !lastTry && !errors.As(err, &skip) {
			deliverErr = err
			return r.noteError(ctx, tx, s.ID, err)
		}
		if ended {
			return endSchedule(ctx, tx, s.ID, StatusCancelled, now)
		}
		return r.advance(ctx, tx, s, now, args.RunAt, err)
	})
	if err != nil {
		return err
	}
	return deliverErr
}

// advance records a run and moves the schedule to its next one (from now, so runs missed while
// the control plane was down collapse into the one just made; never before the planned time, so
// an early run cannot plan itself again), enqueueing it, or ends it.
func (r *Runner) advance(ctx context.Context, tx *core.Tx, s Schedule, now, planned time.Time, deliverErr error) error {
	s.RunCount++
	next := NextRun(s, maxTime(now, planned))
	set := map[string]any{"run_count": s.RunCount, "last_run_at": now, "last_error": errText(deliverErr), "next_run_at": next,
		"updated_at": now}
	if next == nil {
		set["status"], set["ended_at"] = StatusDone, now
	}
	if err := tx.Gorm(ctx).Table("at_schedules").Where("id = ?", s.ID).Updates(set).Error; err != nil {
		return err
	}
	s.NextRunAt = next
	return r.enqueue(ctx, tx, s)
}

// skipped is a run that could not reach its target for a reason trying again will not fix.
type skipped string

func (s skipped) Error() string { return string(s) }

func (r *Runner) noteError(ctx context.Context, tx *core.Tx, id string, err error) error {
	return tx.Gorm(ctx).Table("at_schedules").Where("id = ?", id).Update("last_error", errText(err)).Error
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if len(msg) > 500 {
		msg = msg[:500]
	}
	return msg
}

// deliver posts the schedule's message to its target. It reports ended when the target is gone
// for good: the thread of a lead's own schedule finished. planned is the run's planned time
// (a late run says so).
func (r *Runner) deliver(ctx context.Context, tx *core.Tx, s Schedule, owner string, planned time.Time, scheduled bool) (bool, error) {
	text := ScheduledText(s, r.Now(), planned, scheduled)
	meta := map[string]any{"schedule_id": s.ID, "schedule_name": s.Name}
	priority := "later"
	if s.CreatedBy == ByUser {
		priority = "next"
	}
	if s.Target != TargetLead {
		_, err := r.orch.PostToCoordinatorTx(ctx, tx, owner, s.ProjectID, text, priority, meta)
		return false, err
	}
	t, err := core.LockThread(ctx, tx, *s.ThreadID)
	if err != nil {
		return false, err
	}
	switch {
	case t.Status == "archived":
		return s.CreatedBy == ByLead, skipped("讨论串已归档，没有唤醒组长")
	case (t.Status == "done" || t.Status == "failed") && s.CreatedBy == ByLead:
		return true, nil
	}
	if err := r.orch.Reopen(ctx, tx, t, orchestrator.ReopenBySchedule); err != nil {
		return false, err
	}
	if t, err = core.ThreadByID(ctx, tx, t.ID); err != nil {
		return false, err
	}
	_, err = r.orch.PostToLead(ctx, tx, t, text, priority, meta)
	return false, err
}

// ScheduledText is the message the target receives: the user's schedules read as before
// (【定时任务：name】), an agent's own wake-up says it set it, and how late a late run is.
func ScheduledText(s Schedule, now, planned time.Time, scheduled bool) string {
	head := "【定时任务：" + s.Name + "】"
	switch {
	case s.CreatedBy == ByCoordinator && s.Target == TargetLead:
		head += "（统筹设置的定时任务）"
	case s.CreatedBy != ByUser:
		head = "【定时唤醒：" + s.Name + "】（你用 schedule_create 设置的，id " + s.ID + "）"
	}
	if late := now.Sub(planned); scheduled && late >= 2*time.Minute {
		head += fmt.Sprintf("（原定 %s，晚了 %d 分钟）", planned.In(zone(s.Timezone)).Format("01-02 15:04"), int(late/time.Minute))
	}
	return head + "\n" + s.Message
}

func zone(name string) *time.Location {
	if loc, err := time.LoadLocation(name); err == nil {
		return loc
	}
	return time.UTC
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
