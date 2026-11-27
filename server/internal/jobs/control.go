package jobs

import (
	"context"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// Control is a job's row: the administrator's controls and its last run.
type Control struct {
	Name           string     `json:"name"`
	Paused         bool       `json:"paused"`
	EverySeconds   *int       `json:"every_seconds,omitempty"`
	RunRequestedAt *time.Time `json:"run_requested_at,omitempty"`
	RunningSince   *time.Time `json:"running_since,omitempty"`
	LastStartedAt  *time.Time `json:"last_started_at,omitempty"`
	LastFinishedAt *time.Time `json:"last_finished_at,omitempty"`
	LastOK         *bool      `json:"last_ok,omitempty"`
	LastSummary    string     `json:"last_summary"`
	LastError      string     `json:"last_error"`
	LastReplica    string     `json:"last_replica"`
	Runs           int64      `json:"runs"`
	Failures       int64      `json:"failures"`
}

// Interval is j's interval with the override applied.
func (c Control) Interval(j Job) time.Duration {
	if c.EverySeconds != nil && *c.EverySeconds > 0 {
		return time.Duration(*c.EverySeconds) * time.Second
	}
	return j.Every
}

const controlCols = `name, paused, every_seconds, run_requested_at, running_since, last_started_at, last_finished_at,
	last_ok, last_summary, last_error, last_replica, runs, failures`

func scanControl(row store.Scanner) (Control, error) {
	var c Control
	err := row.Scan(&c.Name, &c.Paused, &c.EverySeconds, &c.RunRequestedAt, &c.RunningSince, &c.LastStartedAt,
		&c.LastFinishedAt, &c.LastOK, &c.LastSummary, &c.LastError, &c.LastReplica, &c.Runs, &c.Failures)
	return c, err
}

// Controls reads every job's row by name.
func (r *Registry) Controls(ctx context.Context) (map[string]Control, error) {
	rows, err := store.Query(ctx, r.DB, `SELECT `+controlCols+` FROM at_jobs`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Control{}
	for rows.Next() {
		c, err := scanControl(rows)
		if err != nil {
			return nil, err
		}
		out[c.Name] = c
	}
	return out, rows.Err()
}

// control is name's row, read at most every Poll for all the jobs of this process together; on a
// read failure the last rows are kept.
func (r *Registry) control(ctx context.Context, name string) Control {
	r.mu.Lock()
	defer r.mu.Unlock()
	if time.Since(r.at) >= orDefault(r.Poll, 2*time.Second)/2 {
		if ctl, err := r.Controls(ctx); err == nil {
			r.ctl, r.at = ctl, time.Now()
		}
	}
	if c, ok := r.ctl[name]; ok {
		return c
	}
	return Control{Name: name}
}

// Run is one recorded run.
type Run struct {
	ID         int64     `json:"id"`
	Replica    string    `json:"replica"`
	Trigger    string    `json:"trigger"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	OK         bool      `json:"ok"`
	Summary    string    `json:"summary"`
	Error      string    `json:"error"`
}

// Runs lists a job's recorded runs, newest first.
func (r *Registry) Runs(ctx context.Context, name string, limit int) ([]Run, error) {
	if _, ok := r.Job(name); !ok {
		return nil, ErrUnknown
	}
	rows, err := store.Query(ctx, r.DB, `SELECT id, replica, trigger, started_at, finished_at, ok, summary, error
		FROM at_job_runs WHERE job = $1 ORDER BY started_at DESC, id DESC LIMIT $2`, name, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		var x Run
		if err := rows.Scan(&x.ID, &x.Replica, &x.Trigger, &x.StartedAt, &x.FinishedAt, &x.OK, &x.Summary, &x.Error); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// RequestRun asks the replica running the job to run it now (within Poll), paused or not.
func (r *Registry) RequestRun(ctx context.Context, name string) error {
	return r.update(ctx, name, `run_requested_at = now()`)
}

// SetPaused pauses or resumes a job (a paused job runs only when asked).
func (r *Registry) SetPaused(ctx context.Context, name string, paused bool) error {
	return r.update(ctx, name, `paused = $2`, paused)
}

// SetEvery overrides a job's interval; 0 goes back to the default.
func (r *Registry) SetEvery(ctx context.Context, name string, seconds int) error {
	var v *int
	if seconds > 0 {
		v = &seconds
	}
	return r.update(ctx, name, `every_seconds = $2`, v)
}

func (r *Registry) update(ctx context.Context, name, set string, args ...any) error {
	if _, ok := r.Job(name); !ok {
		return ErrUnknown
	}
	_, err := store.Exec(ctx, r.DB, `INSERT INTO at_jobs (name) VALUES ($1) ON CONFLICT DO NOTHING`, name)
	if err != nil {
		return err
	}
	_, err = store.Exec(ctx, r.DB, `UPDATE at_jobs SET `+set+`, updated_at = now() WHERE name = $1`, append([]any{name}, args...)...)
	if err == nil {
		r.mu.Lock()
		r.at = time.Time{} // read again at the next look
		r.mu.Unlock()
	}
	return err
}

// Prune removes recorded runs older than keep; it returns how many went.
func (r *Registry) Prune(ctx context.Context, keep time.Duration) (int64, error) {
	res, err := store.Exec(ctx, r.DB, `DELETE FROM at_job_runs WHERE started_at < $1`, time.Now().Add(-keep))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected(), nil
}
