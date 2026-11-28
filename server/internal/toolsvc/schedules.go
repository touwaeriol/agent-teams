package toolsvc

import (
	"context"
	"fmt"
	"strings"
	"time"

	protocol "github.com/touwaeriol/agent-teams/protocol"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/timers"
)

// The schedule_* tools let the coordinator, a lead and a member set wake-ups for themselves (the
// coordinator also for a thread's lead; a member only one-off ones within a day, bound to its
// current task). They change only schedules an agent set
// (the user's own stay read-only to agents); the user sees and manages all of them on the page.

// ScheduleRunner is the timers runner as the schedule tools need it.
type ScheduleRunner interface {
	Create(ctx context.Context, tx *core.Tx, o timers.Owner, in timers.ScheduleInput) (timers.Schedule, error)
	Update(ctx context.Context, tx *core.Tx, cur timers.Schedule, in timers.ScheduleInput, byAgent bool) (timers.Schedule, error)
	Cancel(ctx context.Context, tx *core.Tx, s timers.Schedule) error
}

// scheduleWhen turns at / in_minutes / cron into the kind and time fields of an input; set
// reports whether any was given (an update may leave them out).
func scheduleWhen(at *time.Time, inMinutes *int, cron *string, now time.Time) (timers.ScheduleInput, bool, error) {
	var in timers.ScheduleInput
	n := 0
	if at != nil {
		n++
		in.Kind, in.RunAt = ptr(timers.KindOnce), at
	}
	if inMinutes != nil {
		n++
		t := now.Add(time.Duration(*inMinutes) * time.Minute)
		in.Kind, in.RunAt = ptr(timers.KindOnce), &t
	}
	if cron != nil {
		n++
		in.Kind, in.Cron = ptr(timers.KindCron), cron
	}
	if n > 1 {
		return in, true, refuse("give only one of at, in_minutes and cron")
	}
	return in, n == 1, nil
}

func ptr[T any](v T) *T { return &v }

func scheduleErr(err error) error {
	if bad, ok := timers.IsInvalid(err); ok {
		return refuse("%s", bad.Text.String())
	}
	return err
}

// scheduleOwner is who the caller is as a schedule creator.
func scheduleOwner(ctx context.Context, tx *core.Tx, c *call) (timers.Owner, error) {
	e := c.emp()
	switch {
	case e.Tier == "member":
		task, err := core.CurrentTaskOf(ctx, tx, e)
		if err != nil {
			return timers.Owner{}, refuse("you have no task; a member's wake-up is bound to the current task and cancelled when it ends")
		}
		return timers.Owner{ProjectID: e.ProjectID, Target: timers.TargetMember, ThreadID: &task.ConversationID, EmployeeID: &e.ID,
			TaskID: &task.ID, CreatedBy: timers.ByMember}, nil
	case e.Tier == "coordinator":
		return timers.Owner{ProjectID: e.ProjectID, Target: timers.TargetCoordinator, CreatedBy: timers.ByCoordinator}, nil
	case e.Tier == "lead" && c.thread != nil:
		id := c.thread.ID
		return timers.Owner{ProjectID: e.ProjectID, Target: timers.TargetLead, ThreadID: &id, CreatedBy: timers.ByLead}, nil
	}
	return timers.Owner{}, refuse("you cannot set schedules")
}

// mine reports whether the caller may change s: the coordinator any schedule an agent set in its
// project, a lead the ones it set in its thread, a member its own.
func mine(c *call, s timers.Schedule) bool {
	e := c.emp()
	if s.ProjectID != e.ProjectID || s.CreatedBy == timers.ByUser {
		return false
	}
	switch e.Tier {
	case "coordinator":
		return true
	case "member":
		return s.CreatedBy == timers.ByMember && s.EmployeeID != nil && *s.EmployeeID == e.ID
	}
	return s.CreatedBy == timers.ByLead && c.thread != nil && s.ThreadID != nil && *s.ThreadID == c.thread.ID
}

func (s *Service) scheduleCreate(ctx context.Context, tx *core.Tx, c *call) (Result, error) {
	var in protocol.ScheduleCreateInput
	if err := c.decode(&in); err != nil {
		return Result{}, err
	}
	o, err := scheduleOwner(ctx, tx, c)
	if err != nil {
		return Result{}, err
	}
	si, set, err := scheduleWhen(in.At, in.InMinutes, in.Cron, time.Now())
	if err != nil {
		return Result{}, err
	}
	if !set {
		return Result{}, refuse("give one of at, in_minutes or cron")
	}
	if in.Thread != nil {
		if o.CreatedBy != timers.ByCoordinator {
			return Result{}, refuse("only the coordinator can set schedules for other threads; yours wake only you")
		}
		t, err := core.ThreadByNo(ctx, tx, o.ProjectID, *in.Thread)
		if err != nil {
			return Result{}, refuse("no thread #%d", *in.Thread)
		}
		if t.Status == "archived" {
			return Result{}, refuse("thread #%d is archived", t.No)
		}
		o.Target, o.ThreadID = timers.TargetLead, &t.ID
	}
	si.Name, si.Message, si.Timezone, si.EndsAt, si.MaxRuns = &in.Name, &in.Message, in.Timezone, in.EndsAt, in.MaxRuns
	sc, err := s.Schedules.Create(ctx, tx, o, si)
	if err != nil {
		return Result{}, scheduleErr(err)
	}
	if err := s.scheduleNote(ctx, tx, c, sc.ID, true, fmt.Sprintf("%s设置了定时任务「%s」：%s，%s。", speaker(c), sc.Name, when(sc), wakes(sc))); err != nil {
		return Result{}, err
	}
	return okText(fmt.Sprintf("Created schedule %s %q: %s, next %s, %s. The platform sends a message when it fires; you can end the turn now.",
		sc.ID, sc.Name, whenEn(sc), nextOr(sc, "none"), wakesEn(sc))), nil
}

func (s *Service) scheduleList(ctx context.Context, tx *core.Tx, c *call) (Result, error) {
	var in protocol.ScheduleListInput
	if err := c.decode(&in); err != nil {
		return Result{}, err
	}
	list, err := timers.Schedules(ctx, tx, c.emp().ProjectID)
	if err != nil {
		return Result{}, err
	}
	all := in.Scope != nil && *in.Scope == protocol.ScheduleListInputScopeProject
	var lines []string
	for _, sc := range list {
		if all || (mine(c, sc) && (c.emp().Tier != "coordinator" || sc.CreatedBy == timers.ByCoordinator)) {
			lines = append(lines, scheduleLine(sc))
		}
	}
	if len(lines) == 0 {
		return okText("no schedules"), nil
	}
	return okText(strings.Join(lines, "\n")), nil
}

func (s *Service) scheduleUpdate(ctx context.Context, tx *core.Tx, c *call) (Result, error) {
	var in protocol.ScheduleUpdateInput
	if err := c.decode(&in); err != nil {
		return Result{}, err
	}
	cur, err := s.ownSchedule(ctx, tx, c, in.ScheduleID)
	if err != nil {
		return Result{}, err
	}
	si, _, err := scheduleWhen(in.At, in.InMinutes, in.Cron, time.Now())
	if err != nil {
		return Result{}, err
	}
	si.Name, si.Message, si.Timezone, si.EndsAt, si.MaxRuns, si.Enabled = in.Name, in.Message, in.Timezone, in.EndsAt, in.MaxRuns, in.Enabled
	sc, err := s.Schedules.Update(ctx, tx, cur, si, true)
	if err != nil {
		return Result{}, scheduleErr(err)
	}
	state := "next " + nextOr(sc, "none")
	if !sc.Enabled {
		state = "paused"
	}
	return okText(fmt.Sprintf("Updated schedule %s %q: %s, %s.", sc.ID, sc.Name, whenEn(sc), state)), nil
}

func (s *Service) scheduleCancel(ctx context.Context, tx *core.Tx, c *call) (Result, error) {
	var in protocol.ScheduleCancelInput
	if err := c.decode(&in); err != nil {
		return Result{}, err
	}
	cur, err := s.ownSchedule(ctx, tx, c, in.ScheduleID)
	if err != nil {
		return Result{}, err
	}
	if err := s.Schedules.Cancel(ctx, tx, cur); err != nil {
		return Result{}, scheduleErr(err)
	}
	if err := s.scheduleNote(ctx, tx, c, cur.ID, false, fmt.Sprintf("%s取消了定时任务「%s」。", speaker(c), cur.Name)); err != nil {
		return Result{}, err
	}
	return okText(fmt.Sprintf("cancelled schedule %s %q", cur.ID, cur.Name)), nil
}

func (s *Service) ownSchedule(ctx context.Context, tx *core.Tx, c *call, id string) (timers.Schedule, error) {
	sc, err := timers.ScheduleByID(ctx, tx, id)
	if err != nil || sc.ProjectID != c.emp().ProjectID {
		return sc, refuse("no schedule %s", id)
	}
	if !mine(c, sc) {
		if sc.CreatedBy == timers.ByUser {
			return sc, refuse("schedule %s was set by the user, who alone can change it on the page", id)
		}
		return sc, refuse("you did not set schedule %s", id)
	}
	return sc, nil
}

// scheduleNote records a schedule change as a platform line in the caller's conversation, so the
// user sees it where the agent talks; a created one carries schedule_id, so the user can undo it
// from the line (meta.schedule_undo).
func (s *Service) scheduleNote(ctx context.Context, tx *core.Tx, c *call, id string, created bool, text string) error {
	conv, err := s.orch.ConversationOf(ctx, tx, c.emp())
	if err != nil {
		return err
	}
	m, err := core.InsertMessage(ctx, tx, core.NewMessage{ConversationID: conv, Role: "system", Author: "platform", Kind: "note",
		Content: text, Meta: map[string]any{"schedule_note": true, "schedule_id": id, "schedule_undo": created}})
	if err != nil {
		return err
	}
	var thread *string
	if c.thread != nil {
		thread = &c.thread.ID
	}
	return tx.EmitMessage(ctx, c.emp().ProjectID, thread, m)
}

func speaker(c *call) string {
	switch c.emp().Tier {
	case "coordinator":
		return "统筹"
	case "member":
		return c.emp().Name
	}
	return "组长"
}

func when(sc timers.Schedule) string {
	if sc.Kind == timers.KindOnce {
		return "一次性，" + localTime(sc.RunAt, sc.Timezone)
	}
	return fmt.Sprintf("周期 %s（%s）", sc.Cron, sc.Timezone)
}

func wakes(sc timers.Schedule) string {
	switch {
	case sc.Target == timers.TargetMember && sc.Employee != nil && sc.TaskID != nil:
		return fmt.Sprintf("唤醒 %s（任务 %s，任务结束即取消）", *sc.Employee, *sc.TaskID)
	case sc.Target == timers.TargetLead && sc.ThreadNo != nil:
		return fmt.Sprintf("唤醒 #%d 的组长", *sc.ThreadNo)
	}
	return "唤醒统筹"
}

// whenEn and wakesEn are when and wakes for tool results (when and wakes write the notes the
// user sees in the chat).
func whenEn(sc timers.Schedule) string {
	if sc.Kind == timers.KindOnce {
		return "once, " + localTime(sc.RunAt, sc.Timezone)
	}
	return fmt.Sprintf("recurring %s (%s)", sc.Cron, sc.Timezone)
}

func wakesEn(sc timers.Schedule) string {
	switch {
	case sc.Target == timers.TargetMember && sc.Employee != nil && sc.TaskID != nil:
		return fmt.Sprintf("wakes %s (task %s, cancelled when it ends)", *sc.Employee, *sc.TaskID)
	case sc.Target == timers.TargetLead && sc.ThreadNo != nil:
		return fmt.Sprintf("wakes the lead of #%d", *sc.ThreadNo)
	}
	return "wakes the coordinator"
}

// nextOr is the next run in the schedule's zone, or none.
func nextOr(sc timers.Schedule, none string) string {
	if sc.NextRunAt == nil {
		return none
	}
	return localTime(sc.NextRunAt, sc.Timezone)
}

func localTime(t *time.Time, zone string) string {
	if t == nil {
		return "-"
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		loc = time.UTC
	}
	return t.In(loc).Format("2006-01-02 15:04 MST")
}

var scheduleStatusText = map[string]string{timers.StatusActive: "active", timers.StatusDone: "done", timers.StatusCancelled: "cancelled",
	timers.StatusMissed: "missed"}

var creatorText = map[string]string{timers.ByUser: "the user", timers.ByCoordinator: "the coordinator", timers.ByLead: "a lead", timers.ByMember: "a member"}

func scheduleLine(sc timers.Schedule) string {
	state := scheduleStatusText[sc.Status]
	if sc.Status == timers.StatusActive {
		state = "next " + nextOr(sc, "none")
		if !sc.Enabled {
			state = "paused"
		}
	}
	line := fmt.Sprintf("%s %q: %s, %s, set by %s, %s, fired %d times", sc.ID, sc.Name, whenEn(sc), wakesEn(sc), creatorText[sc.CreatedBy], state, sc.RunCount)
	if sc.LastError != "" {
		line += ", last error: " + sc.LastError
	}
	return line
}
