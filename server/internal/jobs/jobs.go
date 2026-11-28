// Package jobs runs the console's scheduled tasks in one place, all on River (riverqueue/river):
// every periodic job registers here, and the admin console's 后台任务 page shows their state and
// controls them (pause, resume, run now, change the interval or set a cron schedule).
//
// A task is built in (registered by the code, kind "builtin": it cannot be deleted, only paused
// or rescheduled) or added (kind "added", created later and deletable; none exist yet). Each
// task is a River periodic job inserting job_run jobs (river.go); a wake or a manual run inserts
// one more. Whichever replica fetches a run works it, under the task's advisory lock, so a task
// never runs twice at once.
package jobs

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"runtime/debug"
	"sync"
	"time"

	"github.com/riverqueue/river"

	"github.com/touwaeriol/agent-teams/server/internal/msg"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// Trigger says why a run started.
type Trigger string

const (
	// Scheduled: the interval passed or the cron time came.
	Scheduled Trigger = "scheduled"
	// Woken: the job's Wake channel fired (work was hinted).
	Woken Trigger = "woken"
	// Manual: an administrator asked for a run.
	Manual Trigger = "manual"
)

// Kind tells built-in tasks from added ones (at_jobs.kind).
type Kind string

const (
	// Builtin: registered by the code; not deletable.
	Builtin Kind = "builtin"
	// Added: created by the system at run time; deletable.
	Added Kind = "added"
)

// DefaultFetchPoll is how often River looks for due jobs (database/sql has no LISTEN): the
// longest a wake or a "run now" waits before it starts.
const DefaultFetchPoll = 250 * time.Millisecond

// DefaultTimeout is how long a run may take when the job sets no Timeout.
const DefaultTimeout = 10 * time.Minute

// Job is one scheduled task.
type Job struct {
	Name        string        // stable id ("shared-folders")
	Title       string        // shown on the admin page
	Description string        // what it does, shown on the admin page
	Every       time.Duration // default interval; the admin page can override it or set a cron schedule
	Timeout     time.Duration // longest run (0 = DefaultTimeout)
	// Wake, when set, starts a run each time it receives (a hint that there is work).
	Wake <-chan struct{}
	// Run does one round and says what it did ("" = nothing worth keeping).
	Run func(ctx context.Context, t Trigger) (string, error)
}

// ErrUnknown is a job name nobody registered.
var ErrUnknown = msg.Err("没有这个后台任务")

// Registry holds the tasks of this process and the River client that runs them (and the other
// River jobs, whose workers are added through Workers before Build).
type Registry struct {
	DB      *store.Pool
	Log     *slog.Logger
	Replica string        // this process, in the runs it records
	Poll    time.Duration // how often schedule changes are picked up (default 2 s)
	// FetchPoll is how often River looks for due jobs (0 = DefaultFetchPoll; set before Build).
	FetchPoll time.Duration

	mu      sync.Mutex
	jobs    []*Job
	workers *river.Workers
	client  *river.Client[*sql.Tx]
	armed   map[string]string // periodic jobs added: task → schedule signature
}

// New builds an empty registry.
func New(db *store.Pool, log *slog.Logger) *Registry {
	host, _ := os.Hostname()
	r := &Registry{DB: db, Log: log, Replica: fmt.Sprintf("%s/%d", host, os.Getpid()), workers: river.NewWorkers(), armed: map[string]string{}}
	river.AddWorker(r.workers, &runWorker{r: r})
	return r
}

// Add registers a built-in job (before Start).
func (r *Registry) Add(j Job) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.jobs = append(r.jobs, &j)
}

// Jobs lists the registered jobs in registration order.
func (r *Registry) Jobs() []Job {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Job, len(r.jobs))
	for i, j := range r.jobs {
		out[i] = *j
	}
	return out
}

// Job is the registered job of that name.
func (r *Registry) Job(name string) (Job, bool) {
	for _, j := range r.Jobs() {
		if j.Name == name {
			return j, true
		}
	}
	return Job{}, false
}

// Start creates the jobs' rows, adds their periodic jobs, starts River, keeps the schedules in
// step and forwards the jobs' wakes, until ctx ends.
func (r *Registry) Start(ctx context.Context) error {
	if err := r.Build(); err != nil {
		return err
	}
	for _, j := range r.Jobs() {
		if _, err := store.Exec(ctx, r.DB, `INSERT INTO at_jobs (name, kind) VALUES ($1, $2)
			ON CONFLICT (name) DO UPDATE SET kind = EXCLUDED.kind`, j.Name, string(Builtin)); err != nil {
			return err
		}
	}
	if err := r.sync(ctx); err != nil {
		return err
	}
	if err := r.client.Start(ctx); err != nil {
		return err
	}
	go r.stopWith(ctx)
	go r.keepSynced(ctx)
	for _, j := range r.Jobs() {
		if j.Wake != nil {
			go r.forward(ctx, j)
		}
	}
	return nil
}

func (r *Registry) stopWith(ctx context.Context) {
	<-ctx.Done()
	stop, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := r.client.Stop(stop); err != nil {
		r.Log.Warn("stop river", "err", err)
	}
}

// forward turns j's wakes into woken runs.
func (r *Registry) forward(ctx context.Context, j Job) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-j.Wake:
			if err := r.wake(ctx, j.Name); err != nil && ctx.Err() == nil {
				r.Log.Warn("background job wake", "job", j.Name, "err", err)
			}
		}
	}
}

// RunOnce runs j now and records it (a panic is a failed run).
func (r *Registry) RunOnce(ctx context.Context, j Job, t Trigger) {
	start := time.Now()
	_, _ = store.Exec(ctx, r.DB, `UPDATE at_jobs SET running_since = $2, last_replica = $3, updated_at = now() WHERE name = $1`, j.Name, start, r.Replica)
	summary, err := safeRun(ctx, j, t)
	if ctx.Err() != nil && err == nil {
		err = ctx.Err()
	}
	r.record(context.WithoutCancel(ctx), j.Name, t, start, summary, err)
}

func safeRun(ctx context.Context, j Job, t Trigger) (summary string, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("panic: %v\n%s", p, debug.Stack())
		}
	}()
	return j.Run(ctx, t)
}

func (r *Registry) record(ctx context.Context, name string, t Trigger, start time.Time, summary string, err error) {
	msg, failed := "", 0
	if err != nil {
		msg, failed = err.Error(), 1
		r.Log.Warn("background job failed", "job", name, "trigger", t, "err", err)
	}
	end := time.Now()
	_, uerr := store.Exec(ctx, r.DB, `UPDATE at_jobs SET running_since = NULL, last_started_at = $2, last_finished_at = $3,
		last_ok = $4, last_summary = CASE WHEN $5 <> '' OR NOT $4 THEN $5 ELSE last_summary END, last_error = $6,
		runs = runs + 1, failures = failures + $7, updated_at = now() WHERE name = $1`,
		name, start, end, err == nil, summary, msg, failed)
	if uerr != nil {
		r.Log.Warn("background job not recorded", "job", name, "err", uerr)
	}
	if summary == "" && err == nil && t != Manual {
		return
	}
	_, _ = store.Exec(ctx, r.DB, `INSERT INTO at_job_runs (job, replica, trigger, started_at, finished_at, ok, summary, error)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, name, r.Replica, string(t), start, end, err == nil, summary, msg)
}

func orDefault(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}
