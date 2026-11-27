package orchapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/cronx"
	"github.com/touwaeriol/agent-teams/server/internal/httpapi"
	"github.com/touwaeriol/agent-teams/server/internal/jobs"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// The admin console's 后台任务 page: every scheduled task in engine.Jobs (all run on River), its
// state and runs, and the controls (run now, pause / resume, interval or cron). Built-in tasks
// cannot be deleted; there are no added ones yet, so there is no delete route.

// JobView is one background job with its state.
type JobView struct {
	Name            string     `json:"name"`
	Title           string     `json:"title"`
	Description     string     `json:"description"`
	Kind            string     `json:"kind" enum:"builtin,added" doc:"builtin: registered by the code, cannot be deleted; added: created by the system, deletable"`
	DefaultSeconds  int        `json:"default_seconds" doc:"The registered interval"`
	IntervalSeconds int        `json:"interval_seconds" doc:"The interval in force (the override when set); unused while cron is set"`
	Cron            string     `json:"cron" doc:"The cron schedule replacing the interval; empty = runs on the interval"`
	Timezone        string     `json:"timezone" doc:"The zone cron is read in (IANA name; empty = UTC)"`
	Override        bool       `json:"override" doc:"An administrator changed the schedule (interval or cron)"`
	Paused          bool       `json:"paused"`
	Running         bool       `json:"running"`
	RunRequested    bool       `json:"run_requested" doc:"A run was asked for and has not started yet"`
	RunningSince    *time.Time `json:"running_since,omitempty"`
	LastStartedAt   *time.Time `json:"last_started_at,omitempty"`
	LastFinishedAt  *time.Time `json:"last_finished_at,omitempty"`
	LastOK          *bool      `json:"last_ok,omitempty"`
	LastSummary     string     `json:"last_summary"`
	LastError       string     `json:"last_error"`
	LastReplica     string     `json:"last_replica"`
	NextRunAt       *time.Time `json:"next_run_at,omitempty" doc:"About when the next scheduled run starts; absent while paused"`
	Runs            int64      `json:"runs"`
	Failures        int64      `json:"failures"`
}

type jobsOutput struct{ Body []JobView }

type jobNameInput struct {
	Name string `path:"name" maxLength:"64"`
}

type jobRunsInput struct {
	Name  string `path:"name" maxLength:"64"`
	Limit int    `query:"limit" minimum:"1" maximum:"200" default:"50"`
}

// JobRunView is one recorded run of a background job.
type JobRunView struct {
	ID         int64     `json:"id"`
	Replica    string    `json:"replica"`
	Trigger    string    `json:"trigger" enum:"scheduled,woken,manual"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	OK         bool      `json:"ok"`
	Summary    string    `json:"summary"`
	Error      string    `json:"error"`
}

type jobRunsOutput struct{ Body []JobRunView }

type jobPatchInput struct {
	Name string `path:"name" maxLength:"64"`
	Body struct {
		Paused       *bool   `json:"paused,omitempty"`
		EverySeconds *int    `json:"every_seconds,omitempty" minimum:"0" maximum:"604800" doc:"Run on this interval (a cron schedule goes); 0 = back to the registered interval"`
		Cron         *string `json:"cron,omitempty" maxLength:"200" doc:"Run on this cron schedule (five fields or a descriptor such as @daily; the interval override goes); empty = back to the registered interval"`
		Timezone     string  `json:"timezone,omitempty" maxLength:"64" doc:"IANA zone the cron schedule is read in; empty = UTC"`
	}
}

type jobOutput struct{ Body JobView }

func (a *API) registerJobs(api huma.API) {
	admin := "admin"
	huma.Register(api, op("admin-list-jobs", "GET", "/api/admin/jobs", admin, summary("Background jobs and their state")), a.listJobs)
	huma.Register(api, op("admin-job-runs", "GET", "/api/admin/jobs/{name}/runs", admin,
		summary("Recorded runs of a background job (the ones that did something, failed or were asked for)")), a.jobRuns)
	huma.Register(api, op("admin-run-job", "POST", "/api/admin/jobs/{name}/run", admin,
		summary("Run a background job now (within a second or so, on whichever replica fetches it)")), a.runJob)
	huma.Register(api, op("admin-update-job", "PATCH", "/api/admin/jobs/{name}", admin,
		summary("Pause or resume a background job, or change its interval or cron schedule")), a.updateJob)
}

func (a *API) listJobs(ctx context.Context, _ *struct{}) (*jobsOutput, error) {
	if _, err := a.user(ctx); err != nil {
		return nil, err
	}
	ctl, err := a.e.Jobs.Controls(ctx)
	if err != nil {
		return nil, err
	}
	out := []JobView{}
	for _, j := range a.e.Jobs.Jobs() {
		out = append(out, jobView(j, ctl[j.Name]))
	}
	return &jobsOutput{Body: out}, nil
}

func jobView(j jobs.Job, c jobs.Control) JobView {
	kind := c.Kind
	if kind == "" {
		kind = jobs.Builtin
	}
	v := JobView{Name: j.Name, Title: j.Title, Description: j.Description, Kind: string(kind),
		DefaultSeconds: int(j.Every / time.Second), IntervalSeconds: int(c.Interval(j) / time.Second),
		Cron: c.Cron, Timezone: c.Timezone, Override: c.EverySeconds != nil || c.Cron != "", Paused: c.Paused, Running: c.RunningSince != nil, RunningSince: c.RunningSince,
		LastStartedAt: c.LastStartedAt, LastFinishedAt: c.LastFinishedAt, LastOK: c.LastOK, LastSummary: c.LastSummary,
		LastError: c.LastError, LastReplica: c.LastReplica, Runs: c.Runs, Failures: c.Failures}
	v.RunRequested = c.RunRequestedAt != nil && (c.LastStartedAt == nil || c.RunRequestedAt.After(*c.LastStartedAt))
	var last time.Time
	if c.LastStartedAt != nil {
		last = *c.LastStartedAt
	}
	if next := c.Next(j, last, time.Now()); !c.Paused && !next.IsZero() {
		v.NextRunAt = &next
	}
	return v
}

func (a *API) jobRuns(ctx context.Context, in *jobRunsInput) (*jobRunsOutput, error) {
	if _, err := a.user(ctx); err != nil {
		return nil, err
	}
	runs, err := a.e.Jobs.Runs(ctx, in.Name, in.Limit)
	out := make([]JobRunView, len(runs))
	for i, x := range runs {
		out[i] = JobRunView(x)
	}
	return &jobRunsOutput{Body: out}, jobErr(err)
}

func (a *API) runJob(ctx context.Context, in *jobNameInput) (*jobOutput, error) {
	return a.changeJob(ctx, in.Name, "job_run", nil, func() error { return a.e.Jobs.RequestRun(ctx, in.Name) })
}

func (a *API) updateJob(ctx context.Context, in *jobPatchInput) (*jobOutput, error) {
	b := in.Body
	detail := map[string]any{}
	return a.changeJob(ctx, in.Name, "job_update", detail, func() error {
		if b.Paused != nil {
			detail["paused"] = *b.Paused
			if err := a.e.Jobs.SetPaused(ctx, in.Name, *b.Paused); err != nil {
				return err
			}
		}
		switch {
		case b.Cron != nil:
			detail["cron"], detail["timezone"] = *b.Cron, b.Timezone
			return a.e.Jobs.SetCron(ctx, in.Name, *b.Cron, b.Timezone)
		case b.EverySeconds != nil:
			detail["every_seconds"] = *b.EverySeconds
			return a.e.Jobs.SetEvery(ctx, in.Name, *b.EverySeconds)
		}
		return nil
	})
}

// changeJob applies an administrator's change to a job, audits it and returns the job's state.
func (a *API) changeJob(ctx context.Context, name, action string, detail map[string]any, apply func() error) (*jobOutput, error) {
	u, err := a.user(ctx)
	if err != nil {
		return nil, err
	}
	j, ok := a.e.Jobs.Job(name)
	if !ok {
		return nil, jobErr(jobs.ErrUnknown)
	}
	if err := apply(); err != nil {
		return nil, jobErr(err)
	}
	_ = a.e.Core.InTx(ctx, func(tx *core.Tx) error {
		return tx.Gorm(ctx).Table("at_audit_log").Create(map[string]any{"user_id": u.ID, "actor": "user:" + u.ID,
			"action": action, "target_type": "job", "target_id": name, "detail": store.Param(detail)}).Error
	})
	ctl, err := a.e.Jobs.Controls(ctx)
	if err != nil {
		return nil, err
	}
	return &jobOutput{Body: jobView(j, ctl[name])}, nil
}

func jobErr(err error) error {
	switch {
	case errors.Is(err, jobs.ErrUnknown):
		return httpapi.Fail(http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, cronx.ErrCron), errors.Is(err, jobs.ErrSchedule):
		return httpapi.Fail(http.StatusUnprocessableEntity, "bad_schedule", err.Error())
	}
	return err
}
