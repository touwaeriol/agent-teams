// Package jobs runs the console's background jobs in one place: every periodic loop registers
// here, and the admin console's 后台任务 page shows their state and controls them (pause, resume,
// run now, change the interval). A job with a Lock runs on the one replica holding that advisory
// lock; without one it runs on every replica.
package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime/debug"
	"sync"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/scheduler"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// Trigger says why a run started.
type Trigger string

const (
	// Scheduled: the interval passed.
	Scheduled Trigger = "scheduled"
	// Woken: the job's Wake channel fired (work was hinted).
	Woken Trigger = "woken"
	// Manual: an administrator asked for a run.
	Manual Trigger = "manual"
)

// Job is one background job.
type Job struct {
	Name        string        // stable id ("shared-folders")
	Title       string        // shown on the admin page
	Description string        // what it does, shown on the admin page
	Every       time.Duration // default interval; the admin page can override it
	Lock        string        // advisory lock: one replica runs it; "" = every replica
	// Wake, when set, starts a run each time it receives (a hint that there is work).
	Wake <-chan struct{}
	// Run does one round and says what it did ("" = nothing worth keeping).
	Run func(ctx context.Context, t Trigger) (string, error)
}

// ErrUnknown is a job name nobody registered.
var ErrUnknown = errors.New("没有这个后台任务")

// Registry holds the jobs of this process.
type Registry struct {
	DB      store.DB
	Log     *slog.Logger
	Replica string        // this process, in the runs it records
	Poll    time.Duration // how often controls are read (default 2 s)

	mu   sync.Mutex
	jobs []*Job
	ctl  map[string]Control
	at   time.Time
}

// New builds an empty registry.
func New(db store.DB, log *slog.Logger) *Registry {
	host, _ := os.Hostname()
	return &Registry{DB: db, Log: log, Replica: fmt.Sprintf("%s/%d", host, os.Getpid())}
}

// Add registers a job (before Start).
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

// Start creates the jobs' rows and runs each job until ctx ends.
func (r *Registry) Start(ctx context.Context) error {
	for _, j := range r.Jobs() {
		if _, err := store.Exec(ctx, r.DB, `INSERT INTO at_jobs (name) VALUES ($1) ON CONFLICT DO NOTHING`, j.Name); err != nil {
			return err
		}
	}
	for _, j := range r.Jobs() {
		go r.run(ctx, j)
	}
	return nil
}

func (r *Registry) run(ctx context.Context, j Job) {
	if j.Lock == "" {
		r.loop(ctx, j)
		return
	}
	pool, ok := r.DB.(*store.Pool)
	if !ok {
		r.loop(ctx, j)
		return
	}
	scheduler.RunAsLeader(ctx, pool, j.Lock, 5*time.Second, func(ctx context.Context) { r.loop(ctx, j) })
}

// loop runs j at start, then whenever its interval passed, it was woken or a run was asked for;
// a paused job runs only when asked.
func (r *Registry) loop(ctx context.Context, j Job) {
	next := time.Now()
	poll := orDefault(r.Poll, 2*time.Second)
	for ctx.Err() == nil {
		c := r.control(ctx, j.Name)
		if t, ok := c.due(next); ok {
			r.RunOnce(ctx, j, t)
			next = time.Now().Add(c.Interval(j))
		}
		timer := time.NewTimer(max(min(poll, time.Until(next)), 10*time.Millisecond))
		select {
		case <-ctx.Done():
		case <-timer.C:
		case <-j.Wake:
			if !r.control(ctx, j.Name).Paused {
				r.RunOnce(ctx, j, Woken)
			}
		}
		timer.Stop()
	}
}

// due tells whether a run starts now, and why.
func (c Control) due(next time.Time) (Trigger, bool) {
	switch {
	case c.RunRequestedAt != nil && (c.LastStartedAt == nil || c.RunRequestedAt.After(*c.LastStartedAt)):
		return Manual, true
	case !c.Paused && !time.Now().Before(next):
		return Scheduled, true
	}
	return "", false
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
