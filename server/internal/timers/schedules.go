package timers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	_ "time/tzdata" // schedules name IANA zones; the image may have no zoneinfo

	"github.com/jackc/pgx/v5"

	"github.com/touwaeriol/agent-teams/server/internal/core"
)

// Schedule is a row of at_schedules.
type Schedule struct {
	ID        string     `json:"id"`
	ProjectID string     `json:"project_id"`
	Name      string     `json:"name"`
	Cron      string     `json:"cron"`
	Timezone  string     `json:"timezone"`
	Message   string     `json:"message"`
	Enabled   bool       `json:"enabled"`
	NextRunAt *time.Time `json:"next_run_at"`
	LastRunAt *time.Time `json:"last_run_at"`
	LastError string     `json:"last_error"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// ScheduleInput is what a create or update sets (nil = keep on update).
type ScheduleInput struct {
	Name     *string
	Cron     *string
	Timezone *string
	Message  *string
	Enabled  *bool
}

// ErrInvalid is a schedule the user has to fix; its message is shown as is.
type ErrInvalid struct{ Field, Message string }

func (e ErrInvalid) Error() string { return e.Message }

// MaxPerProject caps how many schedules one project may have.
const MaxPerProject = 20

const scheduleCols = `id, project_id, name, cron, timezone, message, enabled, next_run_at, last_run_at, last_error, created_at, updated_at`

func scanSchedule(r interface{ Scan(...any) error }) (Schedule, error) {
	var s Schedule
	err := r.Scan(&s.ID, &s.ProjectID, &s.Name, &s.Cron, &s.Timezone, &s.Message, &s.Enabled, &s.NextRunAt, &s.LastRunAt,
		&s.LastError, &s.CreatedAt, &s.UpdatedAt)
	return s, core.MapErr(err)
}

func collectSchedules(rows pgx.Rows, err error) ([]Schedule, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Schedule{}
	for rows.Next() {
		s, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Schedules lists a project's schedules, oldest first.
func Schedules(ctx context.Context, q core.DB, projectID string) ([]Schedule, error) {
	return collectSchedules(q.Query(ctx, `SELECT `+scheduleCols+` FROM at_schedules WHERE project_id = $1 ORDER BY created_at, id`, projectID))
}

// ScheduleOf loads a schedule of one of the user's projects.
func ScheduleOf(ctx context.Context, q core.DB, userID, id string) (Schedule, error) {
	return scanSchedule(q.QueryRow(ctx, `SELECT `+prefixed("s.")+` FROM at_schedules s JOIN at_projects p ON p.id = s.project_id
		WHERE s.id = $1 AND p.user_id = $2`, id, userID))
}

func prefixed(p string) string {
	cols := strings.Split(scheduleCols, ", ")
	for i, c := range cols {
		cols[i] = p + c
	}
	return strings.Join(cols, ", ")
}

// Normalize trims and validates a complete schedule (after applying an update).
func Normalize(s Schedule) (Schedule, error) {
	s.Name, s.Message, s.Cron, s.Timezone = strings.TrimSpace(s.Name), strings.TrimSpace(s.Message),
		strings.Join(strings.Fields(s.Cron), " "), strings.TrimSpace(s.Timezone)
	if s.Timezone == "" {
		s.Timezone = "UTC"
	}
	switch {
	case s.Name == "":
		return s, ErrInvalid{"name", "请填写名称"}
	case s.Message == "":
		return s, ErrInvalid{"message", "请填写要发给统筹的消息"}
	}
	if _, err := ParseCron(s.Cron); err != nil {
		return s, ErrInvalid{"cron", "时间表达式不对（分 时 日 月 周，例如 0 9 * * *）"}
	}
	if _, err := time.LoadLocation(s.Timezone); err != nil {
		return s, ErrInvalid{"timezone", "没有这个时区：" + s.Timezone}
	}
	return s, nil
}

// NextRun is when an enabled schedule fires next after now; nil when disabled or never.
func NextRun(s Schedule, now time.Time) *time.Time {
	if !s.Enabled {
		return nil
	}
	c, err := ParseCron(s.Cron)
	loc, lerr := time.LoadLocation(s.Timezone)
	if err != nil || lerr != nil {
		return nil
	}
	next := c.Next(now, loc)
	if next.IsZero() {
		return nil
	}
	return &next
}

// apply copies the set fields of an input onto a schedule.
func apply(s Schedule, in ScheduleInput) Schedule {
	set := func(dst *string, v *string) {
		if v != nil {
			*dst = *v
		}
	}
	set(&s.Name, in.Name)
	set(&s.Cron, in.Cron)
	set(&s.Timezone, in.Timezone)
	set(&s.Message, in.Message)
	if in.Enabled != nil {
		s.Enabled = *in.Enabled
	}
	return s
}

// CreateSchedule validates and stores a new schedule.
func CreateSchedule(ctx context.Context, q core.DB, projectID string, in ScheduleInput, now time.Time) (Schedule, error) {
	s, err := Normalize(apply(Schedule{ProjectID: projectID, Enabled: true}, in))
	if err != nil {
		return s, err
	}
	var n int
	if err := q.QueryRow(ctx, `SELECT count(*) FROM at_schedules WHERE project_id = $1`, projectID).Scan(&n); err != nil {
		return s, err
	}
	if n >= MaxPerProject {
		return s, ErrInvalid{"name", fmt.Sprintf("一个项目最多 %d 个定时任务", MaxPerProject)}
	}
	return scanSchedule(q.QueryRow(ctx, `INSERT INTO at_schedules (id, project_id, name, cron, timezone, message, enabled, next_run_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING `+scheduleCols, core.NewID("sc"), projectID, s.Name, s.Cron, s.Timezone,
		s.Message, s.Enabled, NextRun(s, now)))
}

// UpdateSchedule changes a schedule; the next run is recomputed from now.
func UpdateSchedule(ctx context.Context, q core.DB, cur Schedule, in ScheduleInput, now time.Time) (Schedule, error) {
	s, err := Normalize(apply(cur, in))
	if err != nil {
		return s, err
	}
	return scanSchedule(q.QueryRow(ctx, `UPDATE at_schedules SET name = $2, cron = $3, timezone = $4, message = $5, enabled = $6,
		next_run_at = $7, updated_at = now() WHERE id = $1 RETURNING `+scheduleCols, s.ID, s.Name, s.Cron, s.Timezone, s.Message,
		s.Enabled, NextRun(s, now)))
}

// DeleteSchedule removes a schedule.
func DeleteSchedule(ctx context.Context, q core.DB, id string) error {
	return core.Affected(q.Exec(ctx, `DELETE FROM at_schedules WHERE id = $1`, id))
}

// IsInvalid unwraps a validation error.
func IsInvalid(err error) (ErrInvalid, bool) {
	var e ErrInvalid
	ok := errors.As(err, &e)
	return e, ok
}
