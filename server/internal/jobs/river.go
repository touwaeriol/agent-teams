package jobs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverdatabasesql"

	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// How the tasks run on River. Each task not paused is a River periodic job (id "job:<name>") on
// its interval or cron schedule: River's elected leader inserts a scheduled job_run when it is
// due, and whichever replica fetches the run works it, under the task's advisory lock. Every
// replica keeps its periodic jobs in step with at_jobs (sync, every Poll): a changed schedule is
// removed and added again, a paused task removed. A wake or "run now" inserts one job_run.

// Queue is the River queue of the tasks' runs (user schedules and other jobs use the default one).
const Queue = "jobs"

// RunArgs is a job_run: run task Job, for Trigger, planned at At.
type RunArgs struct {
	Job     string    `json:"job"`
	Trigger Trigger   `json:"trigger"`
	At      time.Time `json:"at"`
}

// Kind implements river.JobArgs.
func (RunArgs) Kind() string { return "job_run" }

// InsertOpts implements river.JobArgsWithInsertOpts: a failed run is recorded, not retried.
func (RunArgs) InsertOpts() river.InsertOpts { return river.InsertOpts{Queue: Queue, MaxAttempts: 1} }

// busyRetry is how soon a woken or manual run that found its task running tries again.
const busyRetry = 100 * time.Millisecond

type runWorker struct {
	river.WorkerDefaults[RunArgs]
	r *Registry
}

func (w *runWorker) Timeout(job *river.Job[RunArgs]) time.Duration {
	if j, ok := w.r.Job(job.Args.Job); ok && j.Timeout > 0 {
		return j.Timeout
	}
	return DefaultTimeout
}

func (w *runWorker) Work(ctx context.Context, job *river.Job[RunArgs]) error {
	return w.r.work(ctx, job.Args)
}

// Workers is where other packages add their River workers (before Build).
func (r *Registry) Workers() *river.Workers { return r.workers }

// Build creates the River client with every worker added so far (once; Start calls it too).
func (r *Registry) Build() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.client != nil {
		return nil
	}
	poll := orDefault(r.FetchPoll, DefaultFetchPoll)
	c, err := river.NewClient(riverdatabasesql.New(r.DB.SQL()), &river.Config{
		Queues: map[string]river.QueueConfig{
			river.QueueDefault: {MaxWorkers: 4},
			Queue:              {MaxWorkers: 16},
		},
		Workers:    r.workers,
		JobTimeout: 2 * time.Minute,
		// database/sql has no LISTEN: workers poll for due jobs.
		PollOnly:                    true,
		FetchPollInterval:           poll,
		FetchCooldown:               min(poll, 100*time.Millisecond),
		CompletedJobRetentionPeriod: 2 * time.Hour,
		Logger:                      r.Log,
	})
	r.client = c
	return err
}

// ErrNotBuilt is an insert before Build.
var ErrNotBuilt = errors.New("river client not built")

// InsertTx inserts a River job in tx (other packages' jobs share the client).
func (r *Registry) InsertTx(ctx context.Context, tx *sql.Tx, args river.JobArgs, opts *river.InsertOpts) error {
	r.mu.Lock()
	c := r.client
	r.mu.Unlock()
	if c == nil {
		return ErrNotBuilt
	}
	_, err := c.InsertTx(ctx, tx, args, opts)
	return err
}

// work runs one job_run: a task this binary does not have (a rolling deploy, a feature turned
// off) or a paused one (unless asked for) is skipped; a task already running skips a scheduled
// run and has a woken or manual one try again shortly.
func (r *Registry) work(ctx context.Context, a RunArgs) error {
	j, ok := r.Job(a.Job)
	if !ok {
		return nil
	}
	c, err := r.readControl(ctx, j.Name)
	if err != nil {
		return err
	}
	if c.Paused && a.Trigger != Manual {
		return nil
	}
	release, held := r.tryLock(ctx, j.Name)
	switch {
	case !held && a.Trigger == Scheduled:
		return nil
	case !held:
		return river.JobSnooze(busyRetry)
	}
	defer release()
	r.RunOnce(ctx, j, a.Trigger)
	return nil
}

// tryLock takes the task's session-level advisory lock on a dedicated connection, held while it runs.
func (r *Registry) tryLock(ctx context.Context, name string) (func(), bool) {
	conn, err := r.DB.SQL().Conn(ctx)
	if err != nil {
		return nil, false
	}
	key := "agent-teams:job:" + name
	var ok bool
	if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock(hashtext($1))`, key).Scan(&ok); err != nil || !ok {
		_ = conn.Close()
		return nil, false
	}
	return func() {
		_, _ = conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock(hashtext($1))`, key)
		_ = conn.Close()
	}, true
}

// periodicID is task name's River periodic job.
func periodicID(name string) string { return "job:" + name }

// schedule is a task's River schedule: its cron schedule, else its interval.
type schedule struct {
	j Job
	c Control
}

// Next implements river.PeriodicSchedule.
func (s schedule) Next(now time.Time) time.Time {
	if cr, loc, ok := s.c.schedule(); ok {
		return cr.Next(now, loc)
	}
	return now.Add(s.c.Interval(s.j))
}

// signature is what a task's periodic job depends on; a change re-adds it.
func (c Control) signature(j Job) string {
	if c.Paused {
		return "paused"
	}
	return fmt.Sprintf("%s|%s|%s", c.Interval(j), c.Cron, c.Timezone)
}

// sync brings this replica's periodic jobs in step with the tasks' rows. An interval task runs
// once when first added (at start, like a fresh loop); a re-added one waits for its next time.
func (r *Registry) sync(ctx context.Context) error {
	ctl, err := r.Controls(ctx)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	bundle := r.client.PeriodicJobs()
	for _, jp := range r.jobs {
		j := *jp
		c := ctl[j.Name]
		sig := c.signature(j)
		old, seen := r.armed[j.Name]
		if seen && old == sig {
			continue
		}
		r.armed[j.Name] = sig
		bundle.RemoveByID(periodicID(j.Name))
		if c.Paused {
			continue
		}
		_, _, cronOK := c.schedule()
		name := j.Name
		_, err := bundle.AddSafely(river.NewPeriodicJob(schedule{j: j, c: c}, func() (river.JobArgs, *river.InsertOpts) {
			return RunArgs{Job: name, Trigger: Scheduled, At: time.Now().UTC()}, nil
		}, &river.PeriodicJobOpts{ID: periodicID(j.Name), RunOnStart: !seen && !cronOK}))
		if err != nil {
			return err
		}
	}
	return nil
}

// keepSynced runs sync every Poll until ctx ends (per-replica plumbing: each replica's periodic
// jobs count once it is elected leader).
func (r *Registry) keepSynced(ctx context.Context) {
	t := time.NewTicker(orDefault(r.Poll, 2*time.Second))
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := r.sync(ctx); err != nil && ctx.Err() == nil {
				r.Log.Warn("background jobs: sync schedules", "err", err)
			}
		}
	}
}

// insert inserts a job_run of task name for t.
func (r *Registry) insert(ctx context.Context, name string, t Trigger) error {
	r.mu.Lock()
	c := r.client
	r.mu.Unlock()
	if c == nil {
		return ErrNotBuilt
	}
	_, err := c.Insert(ctx, RunArgs{Job: name, Trigger: t, At: time.Now().UTC()}, nil)
	return err
}

// wake inserts a woken run of task name, unless it is paused or one is already waiting.
func (r *Registry) wake(ctx context.Context, name string) error {
	var skip bool
	err := store.QueryRow(ctx, r.DB, `SELECT EXISTS (SELECT 1 FROM at_jobs WHERE name = $1 AND paused)
		OR EXISTS (SELECT 1 FROM river_job WHERE kind = 'job_run' AND args->>'job' = $1 AND args->>'trigger' = $2
			AND state IN ('available', 'pending', 'retryable', 'scheduled'))`, name, string(Woken)).Scan(&skip)
	if err != nil || skip {
		return err
	}
	return r.insert(ctx, name, Woken)
}
