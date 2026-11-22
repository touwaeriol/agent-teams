package engine_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/store"
	"github.com/touwaeriol/agent-teams/server/internal/timers"
)

var scheduleID = regexp.MustCompile(`sc_[a-z0-9]+`)

func scheduleOf(t *testing.T, tm *team, id string) timers.Schedule {
	t.Helper()
	s, err := timers.ScheduleByID(tm.w.ctx, tm.w.e.Core.Pool, id)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// dueNow makes a schedule's next run (and a one-off's time) a minute ago.
func dueNow(t *testing.T, tm *team, id string) {
	t.Helper()
	if _, err := store.Exec(tm.w.ctx, tm.w.e.Core.Pool, `UPDATE at_schedules SET next_run_at = date_trunc('second', now()) - interval '1 minute',
		run_at = CASE WHEN kind = 'once' THEN date_trunc('second', now()) - interval '1 minute' END WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
}

// TestLeadSchedulesWakeItself: a lead sets a one-off wake-up with schedule_create (refused
// when it breaks the rules), it is queued as a River job, fires once into the lead's session and
// ends; the lead's recurring schedule is cancelled when the thread finishes.
func TestLeadSchedulesWakeItself(t *testing.T) {
	tm := startTeam(t)
	for _, bad := range []map[string]any{
		{"name": "x", "message": "y"},
		{"name": "x", "message": "y", "in_minutes": 5, "cron": "0 9 * * *"},
		{"name": "x", "message": "y", "cron": "* * * * *"},
		{"name": "x", "message": "y", "in_minutes": 5, "thread": 1},
		{"name": "x", "message": "y", "at": "2020-01-01T00:00:00Z"},
	} {
		if r := tm.lead.call("schedule_create", bad); r.Ok {
			t.Fatalf("accepted %v: %s", bad, text(r))
		}
	}
	res := tm.lead.mustCall("schedule_create", map[string]any{"name": "检查 CI", "in_minutes": 45, "message": "去看 PR 的 CI"})
	id := scheduleID.FindString(text(res))
	s := scheduleOf(t, tm, id)
	if s.Kind != timers.KindOnce || s.Target != timers.TargetLead || s.CreatedBy != timers.ByLead || s.ThreadNo == nil || s.NextRunAt == nil {
		t.Fatalf("schedule %+v", s)
	}
	var jobs int
	_ = store.QueryRow(tm.w.ctx, tm.w.e.Core.Pool, `SELECT count(*) FROM river_job WHERE kind = 'schedule_fire' AND args->>'schedule_id' = $1`, id).Scan(&jobs)
	if err := tm.w.e.Timers.Reconcile(tm.w.ctx); err != nil {
		t.Fatal(err)
	}
	_ = store.QueryRow(tm.w.ctx, tm.w.e.Core.Pool, `SELECT count(*) FROM river_job WHERE kind = 'schedule_fire' AND args->>'schedule_id' = $1`, id).Scan(&jobs)
	if jobs != 1 {
		t.Fatalf("river jobs %d", jobs)
	}
	var state string
	_ = store.QueryRow(tm.w.ctx, tm.w.e.Core.Pool, `SELECT state FROM river_job WHERE kind = 'schedule_fire' AND args->>'schedule_id' = $1`, id).Scan(&state)
	if state != "scheduled" {
		t.Fatalf("job state %s", state)
	}

	dueNow(t, tm, id)
	fireDue(t, tm)
	send := tm.lead.expectSend("【定时唤醒：检查 CI】")
	if !strings.Contains(send.Text, "去看 PR 的 CI") || !strings.Contains(send.Text, id) {
		t.Fatalf("wake-up %q", send.Text)
	}
	fireDue(t, tm)
	if s = scheduleOf(t, tm, id); s.Status != timers.StatusDone || s.RunCount != 1 || s.NextRunAt != nil {
		t.Fatalf("after firing %+v", s)
	}
	if r := tm.lead.call("schedule_cancel", map[string]any{"schedule_id": id}); r.Ok {
		t.Fatal("cancelled an ended schedule")
	}
	if list := text(tm.lead.mustCall("schedule_list", map[string]any{})); !strings.Contains(list, id) || !strings.Contains(list, "已完成") {
		t.Fatalf("list %q", list)
	}

	res = tm.lead.mustCall("schedule_create", map[string]any{"name": "日报", "cron": "0 9 * * *", "timezone": "Asia/Shanghai", "message": "汇报"})
	daily := scheduleID.FindString(text(res))
	tm.lead.mustCall("schedule_update", map[string]any{"schedule_id": daily, "enabled": false})
	if s = scheduleOf(t, tm, daily); s.Enabled || s.NextRunAt != nil {
		t.Fatalf("paused %+v", s)
	}
	tm.lead.mustCall("schedule_update", map[string]any{"schedule_id": daily, "enabled": true, "cron": "30 9 * * *"})
	if s = scheduleOf(t, tm, daily); !s.Enabled || s.NextRunAt == nil || s.Cron != "30 9 * * *" {
		t.Fatalf("resumed %+v", s)
	}
	err := tm.w.e.Core.InTx(tm.w.ctx, func(tx *core.Tx) error {
		th, err := core.LockThread(tm.w.ctx, tx, tm.thread.ID)
		if err != nil {
			return err
		}
		return tm.w.e.Orch.SetStatus(tm.w.ctx, tx, th, "done", "")
	})
	if err != nil {
		t.Fatal(err)
	}
	if s = scheduleOf(t, tm, daily); s.Status != timers.StatusCancelled || s.NextRunAt != nil {
		t.Fatalf("after the thread ended %+v", s)
	}
}

// TestCoordinatorSchedulesThreadAndUserOnes: the coordinator aims a wake-up at a thread's lead,
// may not touch the user's schedule, and the user sees both on the page; a schedule the user aims
// at the thread wakes the lead too.
func TestCoordinatorSchedulesThreadAndUserOnes(t *testing.T) {
	tm := startTeam(t)
	conv := tm.coordinatorConv(t)
	tm.b.must(202, "POST", "/api/conversations/"+conv+"/messages", map[string]string{"content": "你好"}, nil)
	coord, _ := expectOpen(t, tm.w, tm.n, "coordinator")

	res := coord.mustCall("schedule_create", map[string]any{"name": "提醒发版", "in_minutes": 30, "thread": tm.thread.No, "message": "检查发版清单"})
	id := scheduleID.FindString(text(res))
	var user timers.Schedule
	tm.b.must(201, "POST", "/api/projects/"+tm.projectID+"/schedules",
		map[string]any{"name": "周报", "cron": "0 18 * * 5", "message": "写周报", "thread_no": tm.thread.No}, &user)
	if user.Target != timers.TargetLead || user.CreatedBy != timers.ByUser || user.ThreadNo == nil || *user.ThreadNo != tm.thread.No {
		t.Fatalf("user schedule %+v", user)
	}
	if r := coord.call("schedule_cancel", map[string]any{"schedule_id": user.ID}); r.Ok || !strings.Contains(text(r), "用户") {
		t.Fatalf("coordinator cancelled the user's schedule: %s", text(r))
	}
	var list []timers.Schedule
	tm.b.must(200, "GET", "/api/projects/"+tm.projectID+"/schedules", nil, &list)
	if len(list) != 2 || list[0].CreatedBy != timers.ByCoordinator || list[0].Target != timers.TargetLead {
		t.Fatalf("list %+v", list)
	}

	dueNow(t, tm, id)
	fireDue(t, tm)
	if send := tm.lead.expectSend("【定时任务：提醒发版】（统筹设置的定时任务）"); !strings.Contains(send.Text, "检查发版清单") {
		t.Fatalf("lead got %q", send.Text)
	}
	tm.b.must(202, "POST", "/api/schedules/"+user.ID+"/run", nil, nil)
	tm.lead.expectSend("【定时任务：周报】")
}

// TestRiverFiresSchedules: the River client running in the engine works a due schedule_fire job
// by itself and queues the schedule's next run.
func TestRiverFiresSchedules(t *testing.T) {
	tm := startTeam(t)
	res := tm.lead.mustCall("schedule_create", map[string]any{"name": "整点检查", "cron": "0 * * * *", "message": "看看进展"})
	id := scheduleID.FindString(text(res))
	if _, err := store.Exec(tm.w.ctx, tm.w.e.Core.Pool, `UPDATE river_job SET scheduled_at = now() - interval '1 second'
		WHERE kind = 'schedule_fire' AND args->>'schedule_id' = $1`, id); err != nil {
		t.Fatal(err)
	}
	tm.lead.expectSend("【定时唤醒：整点检查】")
	eventually(t, "next run queued", func() bool {
		var n int
		_ = store.QueryRow(tm.w.ctx, tm.w.e.Core.Pool, `SELECT count(*) FROM river_job j JOIN at_schedules s ON s.id = j.args->>'schedule_id'
			WHERE j.kind = 'schedule_fire' AND j.state = 'scheduled' AND (j.args->>'run_at')::timestamptz = s.next_run_at AND s.run_count = 1`).Scan(&n)
		return n == 1
	})
}

func runsOf(t *testing.T, tm *team, id string) []timers.Run {
	t.Helper()
	var runs []timers.Run
	tm.b.must(200, "GET", "/api/schedules/"+id+"/runs", nil, &runs)
	return runs
}

// TestMemberWakeUp: a member sets only one-off wake-ups within a day, bound to its current task;
// it wakes the member and is recorded as a run; the next one is cancelled when the task ends.
func TestMemberWakeUp(t *testing.T) {
	tm := startTeam(t)
	for _, bad := range []map[string]any{
		{"name": "x", "message": "y", "cron": "0 9 * * *"},
		{"name": "x", "message": "y", "in_minutes": 25 * 60},
		{"name": "x", "message": "y", "in_minutes": 5, "thread": tm.thread.No},
	} {
		if r := tm.dev.call("schedule_create", bad); r.Ok {
			t.Fatalf("member set %v: %s", bad, text(r))
		}
	}
	res := tm.dev.mustCall("schedule_create", map[string]any{"name": "看 CI", "in_minutes": 20, "message": "看 CI 结果"})
	id := scheduleID.FindString(text(res))
	s := scheduleOf(t, tm, id)
	if s.Target != timers.TargetMember || s.CreatedBy != timers.ByMember || s.TaskID == nil || *s.TaskID != "t1" ||
		s.Employee == nil || *s.Employee != "dev-1" || s.ThreadNo == nil {
		t.Fatalf("member schedule %+v", s)
	}
	var note map[string]any
	if err := store.QueryRow(tm.w.ctx, tm.w.e.Core.Pool, `SELECT meta FROM at_messages WHERE meta->>'schedule_id' = $1 AND kind = 'note'`, id).
		Scan(&note); err != nil || note["schedule_undo"] != true {
		t.Fatalf("schedule note %v %v", note, err)
	}
	if list := text(tm.lead.mustCall("schedule_list", map[string]any{})); strings.Contains(list, id) {
		t.Fatalf("the lead lists the member's wake-up as its own: %q", list)
	}
	if r := tm.lead.call("schedule_cancel", map[string]any{"schedule_id": id}); r.Ok {
		t.Fatal("the lead cancelled the member's wake-up")
	}

	dueNow(t, tm, id)
	fireDue(t, tm)
	if send := tm.dev.expectSend("【定时唤醒：看 CI】"); !strings.Contains(send.Text, "任务 t1") || !strings.Contains(send.Text, "看 CI 结果") {
		t.Fatalf("member wake-up %q", send.Text)
	}
	if runs := runsOf(t, tm, id); len(runs) != 1 || runs[0].Outcome != timers.RunDelivered || runs[0].MessageID == nil || runs[0].Manual {
		t.Fatalf("runs %+v", runs)
	}

	res = tm.dev.mustCall("schedule_create", map[string]any{"name": "再看", "in_minutes": 60, "message": "再看一次"})
	next := scheduleID.FindString(text(res))
	tm.dev.mustCall("complete_task", map[string]any{"task_id": "t1", "summary": "done"})
	if s = scheduleOf(t, tm, next); s.Status != timers.StatusCancelled {
		t.Fatalf("after the task ended %+v", s)
	}
	if r := tm.dev.call("schedule_create", map[string]any{"name": "x", "in_minutes": 5, "message": "y"}); r.Ok {
		t.Fatal("a member without a task set a wake-up")
	}
}

// TestScheduleRunsMerge: a run whose previous message has not reached the target yet is skipped
// (recorded, no second message); once it has, the next run delivers again; run-now is recorded as
// manual.
func TestScheduleRunsMerge(t *testing.T) {
	tm := startTeam(t)
	res := tm.lead.mustCall("schedule_create", map[string]any{"name": "巡检", "cron": "0 * * * *", "message": "看看"})
	id := scheduleID.FindString(text(res))
	dueNow(t, tm, id)
	fireDue(t, tm)
	tm.lead.expectSend("【定时唤醒：巡检】")
	msg := *runsOf(t, tm, id)[0].MessageID
	eventually(t, "delivered", func() bool {
		var ok bool
		_ = store.QueryRow(tm.w.ctx, tm.w.e.Core.Pool, `SELECT delivered_at IS NOT NULL FROM at_messages WHERE id = $1`, msg).Scan(&ok)
		return ok
	})
	if _, err := store.Exec(tm.w.ctx, tm.w.e.Core.Pool, `UPDATE at_messages SET delivered_at = NULL WHERE id = $1`, msg); err != nil {
		t.Fatal(err)
	}
	dueNow(t, tm, id)
	fireDue(t, tm)
	runs := runsOf(t, tm, id)
	if len(runs) != 2 || runs[0].Outcome != timers.RunSkipped || !strings.Contains(runs[0].Error, "还没送到") || runs[0].MessageID != nil {
		t.Fatalf("runs after the merge %+v", runs)
	}
	if s := scheduleOf(t, tm, id); s.RunCount != 2 || s.NextRunAt == nil || s.Status != timers.StatusActive {
		t.Fatalf("schedule after the merge %+v", s)
	}
	if _, err := store.Exec(tm.w.ctx, tm.w.e.Core.Pool, `UPDATE at_messages SET delivered_at = now() WHERE id = $1`, msg); err != nil {
		t.Fatal(err)
	}
	dueNow(t, tm, id)
	fireDue(t, tm)
	tm.lead.expectSend("【定时唤醒：巡检】")
	tm.b.must(202, "POST", "/api/schedules/"+id+"/run", nil, nil)
	eventually(t, "manual run", func() bool {
		runs = runsOf(t, tm, id)
		return len(runs) == 4
	})
	if runs[0].Outcome != timers.RunDelivered || !runs[0].Manual || runs[0].PlannedAt != nil || runs[1].Outcome != timers.RunDelivered {
		t.Fatalf("runs %+v", runs)
	}
}
