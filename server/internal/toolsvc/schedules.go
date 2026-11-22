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

// The schedule_* tools let the coordinator and a lead set wake-ups for themselves (a lead only
// for itself; the coordinator also for a thread's lead). They change only schedules an agent set
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
		return in, true, refuse("at、in_minutes、cron 只能给一个")
	}
	return in, n == 1, nil
}

func ptr[T any](v T) *T { return &v }

func scheduleErr(err error) error {
	if bad, ok := timers.IsInvalid(err); ok {
		return refuse("%s", bad.Message)
	}
	return err
}

// scheduleOwner is who the caller is as a schedule creator.
func scheduleOwner(c *call) (timers.Owner, error) {
	e := c.emp()
	switch {
	case e.Tier == "coordinator":
		return timers.Owner{ProjectID: e.ProjectID, Target: timers.TargetCoordinator, CreatedBy: timers.ByCoordinator}, nil
	case e.Tier == "lead" && c.thread != nil:
		id := c.thread.ID
		return timers.Owner{ProjectID: e.ProjectID, Target: timers.TargetLead, ThreadID: &id, CreatedBy: timers.ByLead}, nil
	}
	return timers.Owner{}, refuse("只有统筹和组长能设置定时任务")
}

// mine reports whether the caller may change s: the coordinator any schedule an agent set in its
// project, a lead the ones it set in its thread.
func mine(c *call, s timers.Schedule) bool {
	e := c.emp()
	if s.ProjectID != e.ProjectID || s.CreatedBy == timers.ByUser {
		return false
	}
	if e.Tier == "coordinator" {
		return true
	}
	return s.CreatedBy == timers.ByLead && c.thread != nil && s.ThreadID != nil && *s.ThreadID == c.thread.ID
}

func (s *Service) scheduleCreate(ctx context.Context, tx *core.Tx, c *call) (Result, error) {
	var in protocol.ScheduleCreateInput
	if err := c.decode(&in); err != nil {
		return Result{}, err
	}
	o, err := scheduleOwner(c)
	if err != nil {
		return Result{}, err
	}
	si, set, err := scheduleWhen(in.At, in.InMinutes, in.Cron, time.Now())
	if err != nil {
		return Result{}, err
	}
	if !set {
		return Result{}, refuse("要给 at、in_minutes 或 cron 其中一个")
	}
	if in.Thread != nil {
		if o.CreatedBy != timers.ByCoordinator {
			return Result{}, refuse("只有统筹能给别的讨论串设置定时任务；组长的定时任务只唤醒自己")
		}
		t, err := core.ThreadByNo(ctx, tx, o.ProjectID, *in.Thread)
		if err != nil {
			return Result{}, refuse("没有讨论串 #%d", *in.Thread)
		}
		if t.Status == "archived" {
			return Result{}, refuse("讨论串 #%d 已归档", t.No)
		}
		o.Target, o.ThreadID = timers.TargetLead, &t.ID
	}
	si.Name, si.Message, si.Timezone, si.EndsAt, si.MaxRuns = &in.Name, &in.Message, in.Timezone, in.EndsAt, in.MaxRuns
	sc, err := s.Schedules.Create(ctx, tx, o, si)
	if err != nil {
		return Result{}, scheduleErr(err)
	}
	if err := s.scheduleNote(ctx, tx, c, fmt.Sprintf("%s设置了定时任务「%s」：%s，%s。", speaker(c), sc.Name, when(sc), wakes(sc))); err != nil {
		return Result{}, err
	}
	return okText(fmt.Sprintf("已创建定时任务 %s「%s」：%s，下次 %s，%s。到点平台会发消息过来，现在可以结束本轮。",
		sc.ID, sc.Name, when(sc), nextText(sc), wakes(sc))), nil
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
		if all || (mine(c, sc) && (c.emp().Tier == "lead" || sc.CreatedBy == timers.ByCoordinator)) {
			lines = append(lines, scheduleLine(sc))
		}
	}
	if len(lines) == 0 {
		return okText("没有定时任务"), nil
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
	state := "下次 " + nextText(sc)
	if !sc.Enabled {
		state = "已暂停"
	}
	return okText(fmt.Sprintf("已更新定时任务 %s「%s」：%s，%s。", sc.ID, sc.Name, when(sc), state)), nil
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
	if err := s.scheduleNote(ctx, tx, c, fmt.Sprintf("%s取消了定时任务「%s」。", speaker(c), cur.Name)); err != nil {
		return Result{}, err
	}
	return okText(fmt.Sprintf("已取消定时任务 %s「%s」", cur.ID, cur.Name)), nil
}

func (s *Service) ownSchedule(ctx context.Context, tx *core.Tx, c *call, id string) (timers.Schedule, error) {
	sc, err := timers.ScheduleByID(ctx, tx, id)
	if err != nil || sc.ProjectID != c.emp().ProjectID {
		return sc, refuse("没有定时任务 %s", id)
	}
	if !mine(c, sc) {
		if sc.CreatedBy == timers.ByUser {
			return sc, refuse("定时任务 %s 是用户设置的，只能由用户在页面上修改", id)
		}
		return sc, refuse("定时任务 %s 不是你设置的", id)
	}
	return sc, nil
}

// scheduleNote records a schedule change as a platform line in the caller's conversation, so the
// user sees it where the agent talks.
func (s *Service) scheduleNote(ctx context.Context, tx *core.Tx, c *call, text string) error {
	conv, err := s.orch.ConversationOf(ctx, tx, c.emp())
	if err != nil {
		return err
	}
	m, err := core.InsertMessage(ctx, tx, core.NewMessage{ConversationID: conv, Role: "system", Author: "platform", Kind: "note",
		Content: text, Meta: map[string]any{"schedule_note": true}})
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
	if c.emp().Tier == "coordinator" {
		return "统筹"
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
	if sc.Target == timers.TargetLead && sc.ThreadNo != nil {
		return fmt.Sprintf("唤醒 #%d 的组长", *sc.ThreadNo)
	}
	return "唤醒统筹"
}

func nextText(sc timers.Schedule) string {
	if sc.NextRunAt == nil {
		return "无"
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

var scheduleStatusText = map[string]string{timers.StatusActive: "进行中", timers.StatusDone: "已完成", timers.StatusCancelled: "已取消",
	timers.StatusMissed: "已错过"}

var creatorText = map[string]string{timers.ByUser: "用户", timers.ByCoordinator: "统筹", timers.ByLead: "组长"}

func scheduleLine(sc timers.Schedule) string {
	state := scheduleStatusText[sc.Status]
	if sc.Status == timers.StatusActive {
		state = "下次 " + nextText(sc)
		if !sc.Enabled {
			state = "已暂停"
		}
	}
	line := fmt.Sprintf("%s「%s」%s，%s，%s设置，%s，已触发 %d 次", sc.ID, sc.Name, when(sc), wakes(sc), creatorText[sc.CreatedBy], state, sc.RunCount)
	if sc.LastError != "" {
		line += "，上次出错：" + sc.LastError
	}
	return line
}
