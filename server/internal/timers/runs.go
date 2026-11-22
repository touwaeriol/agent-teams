package timers

import (
	"context"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/core"
)

// Run outcomes (at_schedule_runs.outcome).
const (
	RunDelivered = "delivered"
	RunSkipped   = "skipped"
	RunFailed    = "failed"
	RunMissed    = "missed"
)

// KeepRuns is how many runs a schedule keeps.
const KeepRuns = 100

// mergeWithin: a run whose previous run's message is still waiting for its target, posted less
// than this long ago, is skipped (the target gets one message, not a pile, when it comes back).
const mergeWithin = 3 * 24 * time.Hour

// Run is a row of at_schedule_runs.
type Run struct {
	ID         int64      `json:"id"`
	ScheduleID string     `json:"schedule_id"`
	PlannedAt  *time.Time `json:"planned_at" doc:"The run's planned time; null for a manual run"`
	FiredAt    time.Time  `json:"fired_at" gorm:"autoCreateTime:false;default:now()"`
	Manual     bool       `json:"manual" doc:"Started with the run-now button"`
	Outcome    string     `json:"outcome" enum:"delivered,skipped,failed,missed"`
	MessageID  *int64     `json:"message_id"`
	Error      string     `json:"error"`
}

// TableName maps Run to at_schedule_runs.
func (Run) TableName() string { return "at_schedule_runs" }

// recordRun stores a run and drops the schedule's runs past KeepRuns.
func recordRun(ctx context.Context, tx *core.Tx, run Run) error {
	if len(run.Error) > 500 {
		run.Error = run.Error[:500]
	}
	if err := tx.Gorm(ctx).Omit("ID").Create(&run).Error; err != nil {
		return err
	}
	return tx.Gorm(ctx).Exec(`DELETE FROM at_schedule_runs WHERE schedule_id = ? AND id NOT IN (
		SELECT id FROM at_schedule_runs WHERE schedule_id = ? ORDER BY id DESC LIMIT ?)`, run.ScheduleID, run.ScheduleID, KeepRuns).Error
}

// Runs lists a schedule's runs, newest first.
func Runs(ctx context.Context, q core.DB, scheduleID string) ([]Run, error) {
	out := []Run{}
	err := q.Gorm(ctx).Where("schedule_id = ?", scheduleID).Order("id DESC").Find(&out).Error
	return out, err
}

// previousPending reports whether the schedule's last delivered run posted a message its target
// has not received yet (recently enough that it is still on its way).
func previousPending(ctx context.Context, tx *core.Tx, scheduleID string, now time.Time) (bool, error) {
	var n int64
	err := tx.Gorm(ctx).Raw(`SELECT count(*) FROM (SELECT message_id FROM at_schedule_runs
		WHERE schedule_id = ? AND outcome = ? AND NOT manual ORDER BY id DESC LIMIT 1) r
		JOIN at_messages m ON m.id = r.message_id WHERE m.delivered_at IS NULL AND m.created_at > ?`,
		scheduleID, RunDelivered, now.Add(-mergeWithin)).Scan(&n).Error
	return n > 0, err
}
