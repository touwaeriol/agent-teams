package jobs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/cronx"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// Control is a job's row: the administrator's controls and its last run.
type Control struct {
	Name           string     `json:"name"`
	Kind           Kind       `json:"kind"`
	Paused         bool       `json:"paused"`
	EverySeconds   *int       `json:"every_seconds,omitempty"`
	Cron           string     `json:"cron"`
	Timezone       string     `json:"timezone"`
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

// Interval is j's interval with the override applied (a cron schedule, when set, wins).
func (c Control) Interval(j Job) time.Duration {
	if c.EverySeconds != nil && *c.EverySeconds > 0 {
		return time.Duration(*c.EverySeconds) * time.Second
	}
	return j.Every
}

// Next is when j next runs after a run that started at last (zero: never ran, which
// for an interval means now and for a cron schedule its next time after from).
func (c Control) Next(j Job, last, from time.Time) time.Time {
	if s, loc, ok := c.schedule(); ok {
		if last.After(from) {
			from = last
		}
		return s.Next(from, loc)
	}
	if last.IsZero() {
		return from
	}
	return last.Add(c.Interval(j))
}

// schedule is the cron override, when one is set and still parses.
func (c Control) schedule() (cronx.Cron, *time.Location, bool) {
	if c.Cron == "" {
		return cronx.Cron{}, nil, false
	}
	s, err := cronx.Parse(c.Cron)
	if err != nil {
		return cronx.Cron{}, nil, false
	}
	loc, err := time.LoadLocation(c.Timezone)
	if err != nil {
		loc = time.UTC
	}
	return s, loc, true
}

const controlCols = `name, kind, paused, every_seconds, cron, timezone, run_requested_at, running_since, last_started_at, last_finished_at,
	last_ok, last_summary, last_error, last_replica, runs, failures`

func scanControl(row store.Scanner) (Control, error) {
	var c Control
	err := row.Scan(&c.Name, &c.Kind, &c.Paused, &c.EverySeconds, &c.Cron, &c.Timezone, &c.RunRequestedAt, &c.RunningSince, &c.LastStartedAt,
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

// readControl is name's row (a job without one yet: defaults).
func (r *Registry) readControl(ctx context.Context, name string) (Control, error) {
	c, err := scanControl(store.QueryRow(ctx, r.DB, `SELECT `+controlCols+` FROM at_jobs WHERE name = $1`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return Control{Name: name, Kind: Builtin}, nil
	}
	return c, err
}

// ErrSchedule is a schedule the registry refuses (cronx.ErrCron for a malformed expression).
var ErrSchedule = errors.New("bad schedule")

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

// RequestRun has the job run now (on whichever replica fetches it first), paused or not.
func (r *Registry) RequestRun(ctx context.Context, name string) error {
	if _, ok := r.Job(name); !ok {
		return ErrUnknown
	}
	if _, err := store.Exec(ctx, r.DB, `UPDATE at_jobs SET run_requested_at = now(), updated_at = now() WHERE name = $1`, name); err != nil {
		return err
	}
	return r.insert(ctx, name, Manual)
}

// SetPaused pauses or resumes a job (a paused job runs only when asked).
func (r *Registry) SetPaused(ctx context.Context, name string, paused bool) error {
	return r.update(ctx, name, `paused = $2`, paused)
}

// SetEvery runs a job every seconds instead of its registered interval (a cron schedule goes);
// 0 goes back to the registered interval.
func (r *Registry) SetEvery(ctx context.Context, name string, seconds int) error {
	var v *int
	if seconds > 0 {
		v = &seconds
	}
	return r.update(ctx, name, `every_seconds = $2, cron = '', timezone = ''`, v)
}

// MinCronGap is the shortest gap a job's cron schedule may leave between two runs.
const MinCronGap = 10 * time.Second

// SetCron runs a job on a cron schedule read in timezone (an IANA name; "" = UTC) instead of an
// interval; "" goes back to the registered interval.
func (r *Registry) SetCron(ctx context.Context, name, expr, timezone string) error {
	if expr == "" {
		return r.update(ctx, name, `cron = '', timezone = '', every_seconds = NULL`)
	}
	s, err := cronx.Parse(expr)
	if err != nil {
		return err
	}
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return fmt.Errorf("%w: unknown timezone %q", ErrSchedule, timezone)
	}
	if s.Next(time.Now(), loc).IsZero() {
		return fmt.Errorf("%w: it never runs", ErrSchedule)
	}
	if s.MinGap(time.Now(), loc, 20) < MinCronGap {
		return fmt.Errorf("%w: runs closer than %s apart", ErrSchedule, MinCronGap)
	}
	return r.update(ctx, name, `cron = $2, timezone = $3, every_seconds = NULL`, strings.Join(strings.Fields(expr), " "), timezone)
}

// update changes job name's controls; this replica re-adds its periodic job at once, the others
// within Poll.
func (r *Registry) update(ctx context.Context, name, set string, args ...any) error {
	if _, ok := r.Job(name); !ok {
		return ErrUnknown
	}
	_, err := store.Exec(ctx, r.DB, `INSERT INTO at_jobs (name) VALUES ($1) ON CONFLICT DO NOTHING`, name)
	if err != nil {
		return err
	}
	if _, err = store.Exec(ctx, r.DB, `UPDATE at_jobs SET `+set+`, updated_at = now() WHERE name = $1`, append([]any{name}, args...)...); err != nil {
		return err
	}
	r.mu.Lock()
	built := r.client != nil
	r.mu.Unlock()
	if !built {
		return nil
	}
	return r.sync(ctx)
}

// Prune removes recorded runs older than keep; it returns how many went.
func (r *Registry) Prune(ctx context.Context, keep time.Duration) (int64, error) {
	res, err := store.Exec(ctx, r.DB, `DELETE FROM at_job_runs WHERE started_at < $1`, time.Now().Add(-keep))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected(), nil
}
