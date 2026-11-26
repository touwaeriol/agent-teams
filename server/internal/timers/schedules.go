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

// Schedule kinds, targets, creators and statuses (00390).
const (
	KindCron = "cron"
	KindOnce = "once"

	TargetCoordinator = "coordinator"
	TargetLead        = "lead"
	TargetMember      = "member"

	ByUser        = "user"
	ByCoordinator = "coordinator"
	ByLead        = "lead"
	ByMember      = "member"

	StatusActive    = "active"
	StatusDone      = "done"
	StatusCancelled = "cancelled"
	StatusMissed    = "missed"
)

// Schedule is a row of at_schedules (with its thread's number and title when it targets a lead or
// a member, and the member's name).
type Schedule struct {
	ID          string     `json:"id"`
	ProjectID   string     `json:"project_id"`
	Name        string     `json:"name"`
	Kind        string     `json:"kind" enum:"cron,once"`
	Cron        string     `json:"cron" doc:"Recurring schedules: five fields or a descriptor (@daily); empty for once"`
	RunAt       *time.Time `json:"run_at" doc:"Once schedules: when it fires"`
	Timezone    string     `json:"timezone"`
	Message     string     `json:"message"`
	Target      string     `json:"target" enum:"coordinator,lead,member" doc:"Who is woken: the project coordinator, a thread's lead or a member (bound to its task)"`
	ThreadID    *string    `json:"thread_id"`
	ThreadNo    *int       `json:"thread_no" gorm:"->"`
	ThreadTitle *string    `json:"thread_title" gorm:"->"`
	EmployeeID  *string    `json:"employee_id" doc:"Member schedules: the member woken"`
	Employee    *string    `json:"employee" gorm:"->" doc:"Member schedules: the member's name"`
	TaskID      *string    `json:"task_id" doc:"Member schedules: the task it is bound to (it ends with the task)"`
	CreatedBy   string     `json:"created_by" enum:"user,coordinator,lead,member" doc:"Who made it: the user on the page or an agent with schedule_create"`
	EndsAt      *time.Time `json:"ends_at" doc:"Recurring schedules end after this time"`
	MaxRuns     *int       `json:"max_runs" doc:"Recurring schedules end after this many runs"`
	RunCount    int        `json:"run_count"`
	Enabled     bool       `json:"enabled"`
	Status      string     `json:"status" enum:"active,done,cancelled,missed"`
	NextRunAt   *time.Time `json:"next_run_at"`
	LastRunAt   *time.Time `json:"last_run_at"`
	LastError   string     `json:"last_error"`
	EndedAt     *time.Time `json:"ended_at"`
	CreatedAt   time.Time  `json:"created_at" gorm:"autoCreateTime:false;default:now()"`
	UpdatedAt   time.Time  `json:"updated_at" gorm:"autoUpdateTime:false;default:now()"`
}

// TableName maps Schedule to at_schedules.
func (Schedule) TableName() string { return "at_schedules" }

// ScheduleInput is what a create or update sets (nil = keep on update). Kind switches between
// a recurring (Cron) and a one-off (RunAt) schedule; a zero EndsAt or MaxRuns clears it.
type ScheduleInput struct {
	Name     *string
	Kind     *string
	Cron     *string
	RunAt    *time.Time
	Timezone *string
	Message  *string
	Enabled  *bool
	EndsAt   *time.Time
	MaxRuns  *int
}

// Owner says who creates a schedule and whom it wakes.
type Owner struct {
	ProjectID  string
	Target     string  // TargetCoordinator | TargetLead | TargetMember
	ThreadID   *string // the lead's thread, the member's task's thread
	EmployeeID *string // the member
	TaskID     *string // the member's task
	CreatedBy  string  // ByUser | ByCoordinator | ByLead | ByMember
}

// ErrInvalid is a schedule the user (or agent) has to fix; its message is shown as is.
type ErrInvalid struct{ Field, Message string }

func (e ErrInvalid) Error() string { return e.Message }

// Limits.
const (
	// MaxPerProject caps a project's active recurring schedules.
	MaxPerProject = 20
	// MaxOncePerProject caps a project's pending one-off schedules.
	MaxOncePerProject = 50
	// MinAgentInterval is the shortest gap between two runs of a recurring schedule an agent sets.
	MinAgentInterval = 5 * time.Minute
	// MinAgentDelay and MaxAgentDelay bound when an agent's one-off schedule may fire.
	MinAgentDelay = time.Minute
	MaxAgentDelay = 30 * 24 * time.Hour
	// MaxMemberDelay bounds a member's wake-up; MaxPerMember caps a member's pending ones.
	MaxMemberDelay = 24 * time.Hour
	MaxPerMember   = 3
	// MissedAfter: a one-off schedule this late (the control plane was down) is marked missed.
	MissedAfter = 24 * time.Hour
	// KeepEnded is how long ended schedules stay in the list.
	KeepEnded = 7 * 24 * time.Hour
)

// withThread selects schedules with their thread's number and title.
func withThread(ctx context.Context, q core.DB) *gorm.DB {
	return q.Gorm(ctx).Table("at_schedules s").Select("s.*, t.no AS thread_no, t.title AS thread_title, e.name AS employee").
		Joins("LEFT JOIN at_threads t ON t.id = s.thread_id").Joins("LEFT JOIN at_employees e ON e.id = s.employee_id")
}

func takeSchedule(g *gorm.DB) (Schedule, error) {
	var s Schedule
	err := g.Take(&s).Error
	return s, core.MapErr(err)
}

// ScheduleByID loads a schedule.
func ScheduleByID(ctx context.Context, q core.DB, id string) (Schedule, error) {
	return takeSchedule(withThread(ctx, q).Where("s.id = ?", id))
}

// Schedules lists a project's schedules: active first (by creation), then ended ones, newest first.
func Schedules(ctx context.Context, q core.DB, projectID string) ([]Schedule, error) {
	out := []Schedule{}
	err := withThread(ctx, q).Where("s.project_id = ?", projectID).
		Order("s.status <> 'active', CASE WHEN s.status = 'active' THEN s.created_at END, s.ended_at DESC, s.id").Find(&out).Error
	return out, err
}

// ThreadSchedules lists the schedules that wake a thread's lead or a member on one of its tasks.
func ThreadSchedules(ctx context.Context, q core.DB, threadID string) ([]Schedule, error) {
	out := []Schedule{}
	err := withThread(ctx, q).Where("s.thread_id = ?", threadID).Order("s.status <> 'active', s.created_at, s.id").Find(&out).Error
	return out, err
}

// ScheduleOf loads a schedule of one of the user's projects.
func ScheduleOf(ctx context.Context, q core.DB, userID, id string) (Schedule, error) {
	return takeSchedule(withThread(ctx, q).Joins("JOIN at_projects p ON p.id = s.project_id").
		Where("s.id = ? AND p.user_id = ?", id, userID))
}

// lockSchedule loads a schedule FOR UPDATE.
func lockSchedule(ctx context.Context, tx *core.Tx, id string) (Schedule, error) {
	return takeSchedule(tx.Gorm(ctx).Model(&Schedule{}).Clauses(core.ForUpdate).Where("id = ?", id))
}

// Normalize trims and validates a complete schedule (after applying an update). byAgent adds the
// limits agents work within; runAtChanged checks a one-off time only when it was just set.
func Normalize(s Schedule, now time.Time, byAgent, runAtChanged bool) (Schedule, error) {
	s.Name, s.Message, s.Cron, s.Timezone = strings.TrimSpace(s.Name), strings.TrimSpace(s.Message),
		strings.Join(strings.Fields(s.Cron), " "), strings.TrimSpace(s.Timezone)
	if s.Timezone == "" {
		s.Timezone = "UTC"
	}
	if s.Kind == "" {
		s.Kind = KindCron
	}
	switch {
	case s.Name == "":
		return s, ErrInvalid{"name", "请填写名称"}
	case s.Message == "":
		return s, ErrInvalid{"message", "请填写到点要发的消息"}
	}
	loc, err := time.LoadLocation(s.Timezone)
	if err != nil {
		return s, ErrInvalid{"timezone", "没有这个时区：" + s.Timezone}
	}
	switch s.Kind {
	case KindCron:
		if s.CreatedBy == ByMember {
			return s, ErrInvalid{"cron", "员工只能设置一次性的唤醒（at 或 in_minutes）"}
		}
		return normalizeCron(s, now, loc, byAgent)
	case KindOnce:
		return normalizeOnce(s, now, byAgent, runAtChanged)
	}
	return s, ErrInvalid{"kind", "kind 只能是 cron 或 once"}
}

func normalizeCron(s Schedule, now time.Time, loc *time.Location, byAgent bool) (Schedule, error) {
	s.RunAt = nil
	c, err := ParseCron(s.Cron)
	if err != nil {
		return s, ErrInvalid{"cron", "时间表达式不对（分 时 日 月 周，例如 0 9 * * *，或 @daily）"}
	}
	if c.Next(now, loc).IsZero() {
		return s, ErrInvalid{"cron", "这个时间表达式不会触发"}
	}
	if byAgent && c.MinGap(now, loc, 6) < MinAgentInterval {
		return s, ErrInvalid{"cron", fmt.Sprintf("定时任务两次之间至少隔 %d 分钟", int(MinAgentInterval/time.Minute))}
	}
	if s.EndsAt != nil && !s.EndsAt.After(now) {
		return s, ErrInvalid{"ends_at", "结束时间要在现在之后"}
	}
	return s, nil
}

func normalizeOnce(s Schedule, now time.Time, byAgent, runAtChanged bool) (Schedule, error) {
	s.Cron, s.EndsAt, s.MaxRuns = "", nil, nil
	if s.RunAt == nil {
		return s, ErrInvalid{"run_at", "请填写触发时间"}
	}
	at := s.RunAt.UTC().Truncate(time.Second)
	s.RunAt = &at
	if !runAtChanged {
		return s, nil
	}
	switch {
	case byAgent && at.Before(now.Add(MinAgentDelay-time.Second)):
		return s, ErrInvalid{"at", "触发时间至少在 1 分钟之后"}
	case s.CreatedBy == ByMember && at.After(now.Add(MaxMemberDelay+time.Second)):
		return s, ErrInvalid{"at", "员工的唤醒最多在 24 小时之内"}
	case byAgent && at.After(now.Add(MaxAgentDelay)):
		return s, ErrInvalid{"at", "触发时间最多在 30 天之内"}
	case !at.After(now):
		return s, ErrInvalid{"run_at", "触发时间要在现在之后"}
	}
	return s, nil
}

// NextRun is when an active, enabled schedule fires next after now (whole seconds, UTC); nil
// when it is paused, ended or will not fire again.
func NextRun(s Schedule, now time.Time) *time.Time {
	if !s.Enabled || (s.Status != "" && s.Status != StatusActive) {
		return nil
	}
	if s.Kind == KindOnce {
		if s.RunCount > 0 {
			return nil
		}
		return s.RunAt
	}
	if s.MaxRuns != nil && s.RunCount >= *s.MaxRuns {
		return nil
	}
	c, err := ParseCron(s.Cron)
	loc, lerr := time.LoadLocation(s.Timezone)
	if err != nil || lerr != nil {
		return nil
	}
	next := c.Next(now, loc)
	if next.IsZero() || (s.EndsAt != nil && next.After(*s.EndsAt)) {
		return nil
	}
	next = next.UTC().Truncate(time.Second)
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
	set(&s.Kind, in.Kind)
	set(&s.Cron, in.Cron)
	set(&s.Timezone, in.Timezone)
	set(&s.Message, in.Message)
	if in.RunAt != nil {
		s.RunAt = in.RunAt
	}
	if in.Enabled != nil {
		s.Enabled = *in.Enabled
	}
	if in.EndsAt != nil {
		s.EndsAt = nonZeroTime(*in.EndsAt)
	}
	if in.MaxRuns != nil {
		s.MaxRuns = nil
		if *in.MaxRuns > 0 {
			s.MaxRuns = in.MaxRuns
		}
	}
	return s
}

func nonZeroTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// createSchedule validates and stores a new schedule (the caller enqueues its first run).
func createSchedule(ctx context.Context, tx *core.Tx, o Owner, in ScheduleInput, now time.Time) (Schedule, error) {
	byAgent := o.CreatedBy != ByUser
	s, err := Normalize(apply(Schedule{ProjectID: o.ProjectID, Target: o.Target, ThreadID: o.ThreadID, EmployeeID: o.EmployeeID,
		TaskID: o.TaskID, CreatedBy: o.CreatedBy, Enabled: true, Status: StatusActive}, in), now, byAgent, true)
	if err != nil {
		return s, err
	}
	if err := checkLimit(ctx, tx, o.ProjectID, s.Kind); err != nil {
		return s, err
	}
	if err := checkMemberLimit(ctx, tx, o); err != nil {
		return s, err
	}
	s.ID, s.NextRunAt = core.NewID("sc"), NextRun(s, now)
	if err := tx.Gorm(ctx).Omit("ThreadNo", "ThreadTitle", "Employee").Clauses(clause.Returning{}).Create(&s).Error; err != nil {
		return s, err
	}
	if err := changed(ctx, tx, s); err != nil {
		return s, err
	}
	return ScheduleByID(ctx, tx, s.ID)
}

// checkLimit refuses a schedule past the project's caps (locking the project so two creates
// cannot both slip under).
func checkLimit(ctx context.Context, tx *core.Tx, projectID, kind string) error {
	if err := core.LockProject(ctx, tx, projectID); err != nil {
		return err
	}
	var n int64
	err := tx.Gorm(ctx).Model(&Schedule{}).Where("project_id = ? AND kind = ? AND status = ?", projectID, kind, StatusActive).Count(&n).Error
	switch {
	case err != nil:
		return err
	case kind == KindCron && n >= MaxPerProject:
		return ErrInvalid{"name", fmt.Sprintf("一个项目最多 %d 个周期定时任务", MaxPerProject)}
	case kind == KindOnce && n >= MaxOncePerProject:
		return ErrInvalid{"name", fmt.Sprintf("一个项目最多 %d 个待触发的一次性定时任务", MaxOncePerProject)}
	}
	return nil
}

// checkMemberLimit refuses a member's wake-up past MaxPerMember pending ones (checkLimit holds
// the project lock).
func checkMemberLimit(ctx context.Context, tx *core.Tx, o Owner) error {
	if o.EmployeeID == nil {
		return nil
	}
	var n int64
	err := tx.Gorm(ctx).Model(&Schedule{}).Where("employee_id = ? AND status = ?", *o.EmployeeID, StatusActive).Count(&n).Error
	if err == nil && n >= MaxPerMember {
		return ErrInvalid{"name", fmt.Sprintf("每个员工最多 %d 个待触发的唤醒", MaxPerMember)}
	}
	return err
}

// updateSchedule changes a schedule; the next run is recomputed from now (the caller enqueues it).
func updateSchedule(ctx context.Context, tx *core.Tx, cur Schedule, in ScheduleInput, now time.Time, byAgent bool) (Schedule, error) {
	if cur.Status != StatusActive {
		return cur, ErrInvalid{"status", "这个定时任务已经结束，不能再改"}
	}
	runAtChanged := in.RunAt != nil || (in.Kind != nil && *in.Kind != cur.Kind)
	s, err := Normalize(apply(cur, in), now, byAgent, runAtChanged)
	if err != nil {
		return s, err
	}
	if s.Kind != cur.Kind {
		if err := checkLimit(ctx, tx, s.ProjectID, s.Kind); err != nil {
			return s, err
		}
	}
	next := NextRun(s, now)
	if next == nil && s.Enabled {
		return s, ErrInvalid{"cron", "按这个设置定时任务不会再触发"}
	}
	err = core.Changed(tx.Gorm(ctx).Table("at_schedules").Where("id = ?", s.ID).Updates(map[string]any{"name": s.Name,
		"kind": s.Kind, "cron": s.Cron, "run_at": s.RunAt, "timezone": s.Timezone, "message": s.Message, "enabled": s.Enabled,
		"ends_at": s.EndsAt, "max_runs": s.MaxRuns, "next_run_at": next, "updated_at": gorm.Expr("now()")}))
	if err == nil {
		err = changed(ctx, tx, s)
	}
	if err != nil {
		return s, err
	}
	return ScheduleByID(ctx, tx, s.ID)
}

// changed tells the project's pages that s changed (they refetch the schedule list, so the
// thread panel drops a cancelled wake-up without a reload).
func changed(ctx context.Context, tx *core.Tx, s Schedule) error {
	return core.EmitSchedule(ctx, tx, s.ProjectID, s.ThreadID, s.ID)
}

// endSchedule ends an active schedule with status (done / cancelled / missed).
func endSchedule(ctx context.Context, tx *core.Tx, s Schedule, status string, now time.Time) error {
	err := tx.Gorm(ctx).Table("at_schedules").Where("id = ? AND status = ?", s.ID, StatusActive).
		Updates(map[string]any{"status": status, "next_run_at": nil, "ended_at": now, "updated_at": gorm.Expr("now()")}).Error
	if err != nil {
		return err
	}
	return changed(ctx, tx, s)
}

// DeleteSchedule removes a schedule, in tx.
func DeleteSchedule(ctx context.Context, tx *core.Tx, s Schedule) error {
	if err := core.Changed(tx.Gorm(ctx).Where("id = ?", s.ID).Delete(&Schedule{})); err != nil {
		return err
	}
	return changed(ctx, tx, s)
}

// IsInvalid unwraps a validation error.
func IsInvalid(err error) (ErrInvalid, bool) {
	var e ErrInvalid
	ok := errors.As(err, &e)
	return e, ok
}
