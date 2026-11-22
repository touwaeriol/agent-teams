package timers

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverdatabasesql"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// Scheduled tasks run on River (riverqueue/river, tables from migration 00360). at_schedules is
// the source of truth; every active, enabled schedule has one pending schedule_fire job for its
// next_run_at, inserted in the same transaction that sets next_run_at. The job locks the row,
// fires only when next_run_at still equals the job's run_at (so a job left over from an edit, a
// retry after a commit, or a second replica does nothing), delivers the message, moves
// next_run_at on and inserts the next job, all in one transaction. A periodic reconcile job
// inserts any missing job (rows from before River, a lost insert) and drops long-ended rows.

// FireArgs is a schedule_fire job: fire schedule ScheduleID's run planned at RunAt.
type FireArgs struct {
	ScheduleID string    `json:"schedule_id"`
	RunAt      time.Time `json:"run_at"`
}

// Kind implements river.JobArgs.
func (FireArgs) Kind() string { return "schedule_fire" }

// fireAttempts is how often a failed delivery is tried (River backs off between tries); the last
// try moves the schedule on anyway, keeping the error in last_error.
const fireAttempts = 5

// InsertOpts implements river.JobArgsWithInsertOpts: one job per schedule and run.
func (FireArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: fireAttempts, UniqueOpts: river.UniqueOpts{ByArgs: true}}
}

// ReconcileArgs is the periodic schedule_reconcile job.
type ReconcileArgs struct{}

// Kind implements river.JobArgs.
func (ReconcileArgs) Kind() string { return "schedule_reconcile" }

// ReconcileEvery is how often missing schedule_fire jobs are put back.
const ReconcileEvery = 5 * time.Minute

type fireWorker struct {
	river.WorkerDefaults[FireArgs]
	r *Runner
}

func (w *fireWorker) Work(ctx context.Context, job *river.Job[FireArgs]) error {
	return w.r.fire(ctx, job.Args, job.Attempt >= job.MaxAttempts)
}

type reconcileWorker struct {
	river.WorkerDefaults[ReconcileArgs]
	r *Runner
}

func (w *reconcileWorker) Work(ctx context.Context, _ *river.Job[ReconcileArgs]) error {
	return w.r.Reconcile(ctx)
}

func newRiver(pool *store.Pool, r *Runner, log *slog.Logger) (*river.Client[*sql.Tx], error) {
	workers := river.NewWorkers()
	river.AddWorker(workers, &fireWorker{r: r})
	river.AddWorker(workers, &reconcileWorker{r: r})
	return river.NewClient(riverdatabasesql.New(pool.SQL()), &river.Config{
		Queues:     map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: 4}},
		Workers:    workers,
		JobTimeout: 2 * time.Minute,
		// database/sql has no LISTEN: workers poll (every second by default) for due jobs.
		PollOnly: true,
		PeriodicJobs: []*river.PeriodicJob{river.NewPeriodicJob(river.PeriodicInterval(ReconcileEvery),
			func() (river.JobArgs, *river.InsertOpts) { return ReconcileArgs{}, nil }, &river.PeriodicJobOpts{RunOnStart: true})},
		Logger: log,
	})
}

// enqueue inserts the schedule_fire job for the schedule's next run, in tx.
func (r *Runner) enqueue(ctx context.Context, tx *core.Tx, s Schedule) error {
	if s.NextRunAt == nil || s.Status != StatusActive || !s.Enabled {
		return nil
	}
	sqlTx, err := tx.SQLTx()
	if err != nil {
		return err
	}
	at := s.NextRunAt.UTC()
	_, err = r.river.InsertTx(ctx, sqlTx, FireArgs{ScheduleID: s.ID, RunAt: at},
		&river.InsertOpts{ScheduledAt: at, MaxAttempts: fireAttempts, UniqueOpts: river.UniqueOpts{ByArgs: true}})
	return err
}

// Reconcile inserts the schedule_fire job of every active schedule that has none pending for its
// next run, and deletes schedules that ended more than KeepEnded ago.
func (r *Runner) Reconcile(ctx context.Context) error {
	return r.core.InTx(ctx, func(tx *core.Tx) error {
		var missing []Schedule
		err := tx.Gorm(ctx).Model(&Schedule{}).Where(`enabled AND status = ? AND next_run_at IS NOT NULL AND NOT EXISTS (
			SELECT 1 FROM river_job j WHERE j.kind = ? AND j.state IN ('available', 'pending', 'retryable', 'running', 'scheduled')
			AND j.args->>'schedule_id' = at_schedules.id AND (j.args->>'run_at')::timestamptz = at_schedules.next_run_at)`,
			StatusActive, FireArgs{}.Kind()).Limit(500).Find(&missing).Error
		if err != nil {
			return err
		}
		for _, s := range missing {
			if err := r.enqueue(ctx, tx, s); err != nil {
				return err
			}
		}
		return tx.Gorm(ctx).Where("status <> ? AND ended_at < ?", StatusActive, r.Now().Add(-KeepEnded)).Delete(&Schedule{}).Error
	})
}

// startRiver runs the River client until ctx ends.
func (r *Runner) startRiver(ctx context.Context) {
	if err := r.river.Start(ctx); err != nil {
		r.log.Error("start river", "err", err)
		return
	}
	<-ctx.Done()
	stop, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := r.river.Stop(stop); err != nil {
		r.log.Warn("stop river", "err", err)
	}
}
