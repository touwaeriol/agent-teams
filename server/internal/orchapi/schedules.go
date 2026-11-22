package orchapi

import (
	"context"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/httpapi"
	"github.com/touwaeriol/agent-teams/server/internal/timers"
)

// ScheduleBody is PATCH /api/schedules/{id} (and the shared fields of a create).
type ScheduleBody struct {
	Name     *string    `json:"name,omitempty" maxLength:"100"`
	Kind     *string    `json:"kind,omitempty" enum:"cron,once" doc:"cron: recurring (cron + timezone); once: fires at run_at (default cron)"`
	Cron     *string    `json:"cron,omitempty" maxLength:"100" doc:"minute hour day-of-month month day-of-week, or a descriptor such as @daily"`
	RunAt    *time.Time `json:"run_at,omitempty" doc:"Once schedules: when it fires"`
	Timezone *string    `json:"timezone,omitempty" maxLength:"64" doc:"IANA zone the cron is read in (default UTC)"`
	Message  *string    `json:"message,omitempty" maxLength:"20000" doc:"Sent to the target when it fires"`
	Enabled  *bool      `json:"enabled,omitempty"`
	EndsAt   *time.Time `json:"ends_at,omitempty" doc:"Recurring schedules end after this time"`
	MaxRuns  *int       `json:"max_runs,omitempty" minimum:"0" doc:"Recurring schedules end after this many runs (0 clears)"`
}

func (b ScheduleBody) input() timers.ScheduleInput {
	return timers.ScheduleInput{Name: b.Name, Kind: b.Kind, Cron: b.Cron, RunAt: b.RunAt, Timezone: b.Timezone, Message: b.Message,
		Enabled: b.Enabled, EndsAt: b.EndsAt, MaxRuns: b.MaxRuns}
}

// NewScheduleBody is POST /api/projects/{pid}/schedules.
type NewScheduleBody struct {
	ScheduleBody
	ThreadNo *int `json:"thread_no,omitempty" minimum:"1" doc:"Wake this thread's lead instead of the project coordinator"`
}

type schedulesOutput struct{ Body []timers.Schedule }

type scheduleOutput struct{ Body timers.Schedule }

type createScheduleInput struct {
	PID  string `path:"pid" maxLength:"64"`
	Body NewScheduleBody
}

type scheduleIDInput struct {
	ID string `path:"id" maxLength:"64"`
}

type patchScheduleInput struct {
	ID   string `path:"id" maxLength:"64"`
	Body ScheduleBody
}

func (a *API) registerSchedules(api huma.API) {
	t := "schedules"
	huma.Register(api, op("list-schedules", "GET", "/api/projects/{pid}/schedules", t), a.listSchedules)
	huma.Register(api, op("create-schedule", "POST", "/api/projects/{pid}/schedules", t, status(201)), a.createSchedule)
	huma.Register(api, op("update-schedule", "PATCH", "/api/schedules/{id}", t), a.updateSchedule)
	huma.Register(api, op("delete-schedule", "DELETE", "/api/schedules/{id}", t, status(204)), a.deleteSchedule)
	huma.Register(api, op("run-schedule", "POST", "/api/schedules/{id}/run", t, status(202),
		summary("Send the schedule's message to its target now")), a.runSchedule)
}

// scheduleErr shows a validation error on its field.
func scheduleErr(err error) error {
	if bad, ok := timers.IsInvalid(err); ok {
		return httpapi.Invalid(bad.Field, bad.Message)
	}
	return fail(err, "没有这个定时任务")
}

func (a *API) listSchedules(ctx context.Context, in *pidInput) (*schedulesOutput, error) {
	_, p, err := a.project(ctx, in.PID)
	if err != nil {
		return nil, err
	}
	list, err := timers.Schedules(ctx, a.e.Core.Pool, p.ID)
	return &schedulesOutput{Body: list}, err
}

func (a *API) createSchedule(ctx context.Context, in *createScheduleInput) (*scheduleOutput, error) {
	_, p, err := a.project(ctx, in.PID)
	if err != nil {
		return nil, err
	}
	var s timers.Schedule
	err = a.e.Core.InTx(ctx, func(tx *core.Tx) error {
		o := timers.Owner{ProjectID: p.ID, Target: timers.TargetCoordinator, CreatedBy: timers.ByUser}
		if in.Body.ThreadNo != nil {
			t, err := core.ThreadByNo(ctx, tx, p.ID, *in.Body.ThreadNo)
			if err != nil {
				return timers.ErrInvalid{Field: "thread_no", Message: "没有这个讨论串"}
			}
			o.Target, o.ThreadID = timers.TargetLead, &t.ID
		}
		var err error
		s, err = a.e.Timers.Create(ctx, tx, o, in.Body.input())
		return err
	})
	if err != nil {
		return nil, scheduleErr(err)
	}
	return &scheduleOutput{Body: s}, nil
}

func (a *API) ownSchedule(ctx context.Context, id string) (timers.Schedule, string, error) {
	u, err := a.user(ctx)
	if err != nil {
		return timers.Schedule{}, "", err
	}
	s, err := timers.ScheduleOf(ctx, a.e.Core.Pool, u.ID, id)
	return s, u.ID, fail(err, "没有这个定时任务")
}

func (a *API) updateSchedule(ctx context.Context, in *patchScheduleInput) (*scheduleOutput, error) {
	cur, _, err := a.ownSchedule(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	var s timers.Schedule
	err = a.e.Core.InTx(ctx, func(tx *core.Tx) error {
		var err error
		s, err = a.e.Timers.Update(ctx, tx, cur, in.Body.input(), false)
		return err
	})
	if err != nil {
		return nil, scheduleErr(err)
	}
	return &scheduleOutput{Body: s}, nil
}

func (a *API) deleteSchedule(ctx context.Context, in *scheduleIDInput) (*struct{}, error) {
	s, _, err := a.ownSchedule(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	return nil, scheduleErr(timers.DeleteSchedule(ctx, a.e.Core.Pool, s.ID))
}

func (a *API) runSchedule(ctx context.Context, in *scheduleIDInput) (*struct{}, error) {
	s, owner, err := a.ownSchedule(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	return nil, fail(a.e.Timers.Fire(ctx, s, owner), "没有这个项目")
}
