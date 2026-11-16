package timers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	_ "time/tzdata" // schedules name IANA zones; the image may have no zoneinfo

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

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
	CreatedAt time.Time  `json:"created_at" gorm:"autoCreateTime:false;default:now()"`
	UpdatedAt time.Time  `json:"updated_at" gorm:"autoUpdateTime:false;default:now()"`
}

// TableName maps Schedule to at_schedules.
func (Schedule) TableName() string { return "at_schedules" }

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

func schedules(ctx context.Context, q core.DB) *gorm.DB { return q.Gorm(ctx).Model(&Schedule{}) }

func takeSchedule(g *gorm.DB) (Schedule, error) {
	var s Schedule
	err := g.Take(&s).Error
	return s, core.MapErr(err)
}

// Schedules lists a project's schedules, oldest first.
func Schedules(ctx context.Context, q core.DB, projectID string) ([]Schedule, error) {
	out := []Schedule{}
	err := schedules(ctx, q).Where("project_id = ?", projectID).Order("created_at, id").Find(&out).Error
	return out, err
}

// ScheduleOf loads a schedule of one of the user's projects.
func ScheduleOf(ctx context.Context, q core.DB, userID, id string) (Schedule, error) {
	return takeSchedule(q.Gorm(ctx).Table("at_schedules s").Select("s.*").Joins("JOIN at_projects p ON p.id = s.project_id").
		Where("s.id = ? AND p.user_id = ?", id, userID))
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
	var n int64
	if err := schedules(ctx, q).Where("project_id = ?", projectID).Count(&n).Error; err != nil {
		return s, err
	}
	if n >= MaxPerProject {
		return s, ErrInvalid{"name", fmt.Sprintf("一个项目最多 %d 个定时任务", MaxPerProject)}
	}
	s.ID, s.NextRunAt = core.NewID("sc"), NextRun(s, now)
	err = q.Gorm(ctx).Clauses(clause.Returning{}).Create(&s).Error
	return s, err
}

// UpdateSchedule changes a schedule; the next run is recomputed from now.
func UpdateSchedule(ctx context.Context, q core.DB, cur Schedule, in ScheduleInput, now time.Time) (Schedule, error) {
	s, err := Normalize(apply(cur, in))
	if err != nil {
		return s, err
	}
	err = core.Changed(q.Gorm(ctx).Table("at_schedules").Where("id = ?", s.ID).Updates(map[string]any{"name": s.Name,
		"cron": s.Cron, "timezone": s.Timezone, "message": s.Message, "enabled": s.Enabled, "next_run_at": NextRun(s, now),
		"updated_at": gorm.Expr("now()")}))
	if err != nil {
		return s, err
	}
	return takeSchedule(schedules(ctx, q).Where("id = ?", s.ID))
}

// DeleteSchedule removes a schedule.
func DeleteSchedule(ctx context.Context, q core.DB, id string) error {
	return core.Changed(q.Gorm(ctx).Where("id = ?", id).Delete(&Schedule{}))
}

// IsInvalid unwraps a validation error.
func IsInvalid(err error) (ErrInvalid, bool) {
	var e ErrInvalid
	ok := errors.As(err, &e)
	return e, ok
}
