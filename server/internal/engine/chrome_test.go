package engine_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw/nodetest"
	"github.com/touwaeriol/agent-teams/server/internal/store"
	"github.com/touwaeriol/agent-teams/server/internal/timers"
)

// TestProjectChromeSettings: icon, effort and model overrides are validated; the thread effort
// reaches the lead's next session.open, its model stays the owner's default (leads do not follow
// thread_model), and coordinator_model is accepted but ignored.
func TestProjectChromeSettings(t *testing.T) {
	tm := startTeam(t)
	path := "/api/projects/" + tm.projectID
	for _, bad := range []map[string]any{{"icon": "color:#12"}, {"icon": "ab"}, {"thread_effort": "huge"}, {"thread_model": "bad model"}} {
		if code, raw := tm.b.do("PATCH", path, bad); code != 422 {
			t.Fatalf("PATCH %v = %d %s", bad, code, raw)
		}
	}
	tm.b.must(200, "PUT", "/api/models/mine", map[string]any{"models": []map[string]any{
		{"runtime": "claude", "model": "claude-opus-5-5", "default": true}, {"runtime": "claude", "model": "claude-thread-test"}}}, nil)
	var p struct {
		Icon             string
		CoordinatorModel string `json:"coordinator_model"`
		ThreadModel      string `json:"thread_model"`
		AutoResume       bool   `json:"auto_resume"`
	}
	body := map[string]any{"icon": "🚀", "coordinator_model": "claude-opus-5-5", "thread_model": "claude-thread-test",
		"thread_effort": "high", "auto_resume": true}
	tm.b.must(200, "PATCH", path, body, &p)
	tm.b.must(200, "GET", path, nil, &p)
	if p.Icon != "🚀" || !p.AutoResume || p.CoordinatorModel != "" || p.ThreadModel != "claude-thread-test" {
		t.Fatalf("project %+v", p)
	}
	tm.b.must(200, "PATCH", path, map[string]any{"icon": "color:#3B82F6"}, nil)
	var th core.ThreadView
	tm.b.must(201, "POST", path+"/threads", map[string]any{"title": "Two", "brief": "again"}, &th)
	_, open := expectOpen(t, tm.w, tm.n, "lead-2")
	if open.Claude.Model != "claude-opus-5-5" || open.Claude.Effort == nil || *open.Claude.Effort != "high" {
		t.Fatalf("lead open %q %v", open.Claude.Model, open.Claude.Effort)
	}
}

// TestLibraryUploadAndUsage: a file the user adds is a hosted deliverable owned by "user",
// counted in storage; the project usage sums turns per thread and employee.
func TestLibraryUploadAndUsage(t *testing.T) {
	tm := startTeam(t)
	path := "/api/projects/" + tm.projectID
	png := []byte("# 发布说明\n")
	if code, raw := tm.b.do("POST", path+"/library", map[string]any{"name": "run.sh", "data": base64.StdEncoding.EncodeToString(png)}); code != 422 {
		t.Fatalf("shell script accepted: %d %s", code, raw)
	}
	var d core.DeliverableView
	tm.b.must(201, "POST", path+"/library", map[string]any{"name": "C:\\docs\\main.md", "data": base64.StdEncoding.EncodeToString(png)}, &d)
	if d.Title != "main.md" || d.PublishedBy != "user" || d.Kind != "doc" || d.Detail["hosted"] != true || d.Detail["entry"] != "main.md" {
		t.Fatalf("uploaded %+v", d)
	}
	var page struct {
		Items        []core.DeliverableView
		StorageBytes int64 `json:"storage_bytes"`
	}
	tm.b.must(200, "GET", path+"/deliverables", nil, &page)
	if len(page.Items) != 1 || page.StorageBytes != int64(len(png)) {
		t.Fatalf("library %+v", page)
	}
	var files struct{ Files []struct{ Path string } }
	tm.b.must(200, "GET", "/api/deliverables/"+d.ID+"/files", nil, &files)
	if len(files.Files) != 1 || files.Files[0].Path != "main.md" {
		t.Fatalf("files %+v", files)
	}
	turnEnded(tm.dev, 0.02, 100, 40)
	turnEnded(tm.lead, 0.01, 50, 10)
	var usage struct {
		CostUSDEst float64 `json:"cost_usd_est"`
		Turns      int64
		Tokens     struct{ Input, Output int64 }
		ByThread   []struct {
			ThreadNo *int `json:"thread_no"`
			CostUSD  float64
		} `json:"by_thread"`
		ByEmployee []struct{ Employee string } `json:"by_employee"`
	}
	eventually(t, "usage recorded", func() bool {
		tm.b.must(200, "GET", path+"/usage?days=30", nil, &usage)
		return usage.Turns == 2
	})
	if usage.Tokens.Input != 150 || usage.Tokens.Output != 50 || len(usage.ByThread) != 1 || usage.ByThread[0].ThreadNo == nil ||
		*usage.ByThread[0].ThreadNo != 1 || len(usage.ByEmployee) != 2 || usage.ByEmployee[0].Employee != "dev-1" {
		t.Fatalf("usage %+v", usage)
	}
}

func turnEnded(a *fakeAgent, cost float64, in, out int) {
	a.event("turn_ended", func(e *protocol.SessionEvent) {
		sub, isErr, turns, ms := "success", false, 1, 10
		usage := json.RawMessage(`{"input_tokens":` + itoa(in) + `,"output_tokens":` + itoa(out) + `}`)
		e.ResultSubtype, e.IsError, e.NumTurns, e.DurationMS, e.Usage, e.CostUSD = &sub, &isErr, &turns, &ms, &usage, &cost
	})
}

func itoa(n int) string {
	raw, _ := json.Marshal(n)
	return string(raw)
}

func rateLimited(a *fakeAgent, resetIn time.Duration) {
	a.event("rate_limited", func(e *protocol.SessionEvent) {
		at := time.Now().Add(resetIn).UTC()
		detail := "five_hour"
		e.RetryAt, e.Detail = &at, &detail
	})
}

func threadStatus(tm *team) (string, string) {
	th, err := core.ThreadByID(tm.w.ctx, tm.w.e.Core.Pool, tm.thread.ID)
	if err != nil {
		tm.w.t.Fatal(err)
	}
	return th.Status, th.PauseReason
}

func expireWaits(t *testing.T, tm *team) {
	if _, err := store.Exec(tm.w.ctx, tm.w.e.Core.Pool, `UPDATE at_rate_limit_waits SET reset_at = now() - interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	tm.w.e.Timers.Tick(tm.w.ctx)
}

// TestUsageLimitPausesAndResumes: a usage limit pauses the thread without nudging; with
// auto_resume the thread runs again after the reset and the member is told to continue.
func TestUsageLimitPausesAndResumes(t *testing.T) {
	tm := startTeam(t)
	rateLimited(tm.dev, 5*time.Second) // an API retry: nothing to wait for
	if st, _ := threadStatus(tm); st != "running" {
		t.Fatalf("short retry paused the thread: %s", st)
	}
	rateLimited(tm.dev, time.Hour)
	eventually(t, "thread paused", func() bool { st, why := threadStatus(tm); return st == "paused" && why == timers.PauseReason })
	turnEnded(tm.dev, 0, 1, 1)
	ctx, cancel := context.WithTimeout(tm.w.ctx, 800*time.Millisecond)
	defer cancel()
	if f, err := tm.n.Wait(ctx, protocol.TypeSessionSend, func(f nodetest.Frame) bool {
		return tm.dev.mine(f) && strings.Contains(string(f.Body), "平台提醒")
	}); err == nil {
		t.Fatalf("waiting member nudged: %s", f.Body)
	}
	tm.b.must(200, "PATCH", "/api/projects/"+tm.projectID, map[string]any{"auto_resume": true}, nil)
	expireWaits(t, tm)
	tm.dev.expectSend("使用限额已重置")
	if st, _ := threadStatus(tm); st != "running" {
		t.Fatalf("thread not resumed: %s", st)
	}
}

// TestUsageLimitWithoutAutoResume: the wait ends at the reset but the thread stays paused.
func TestUsageLimitWithoutAutoResume(t *testing.T) {
	tm := startTeam(t)
	rateLimited(tm.dev, time.Hour)
	eventually(t, "thread paused", func() bool { st, _ := threadStatus(tm); return st == "paused" })
	expireWaits(t, tm)
	waiting, err := timers.Waiting(tm.w.ctx, tm.w.e.Core.Pool, "")
	if err != nil || waiting {
		t.Fatal(err)
	}
	var n int
	_ = store.QueryRow(tm.w.ctx, tm.w.e.Core.Pool, `SELECT count(*) FROM at_rate_limit_waits`).Scan(&n)
	if st, why := threadStatus(tm); n != 0 || st != "paused" || why != timers.PauseReason {
		t.Fatalf("waits %d thread %s/%s", n, st, why)
	}
}

// TestSchedulesFireOnce: a due schedule posts its message to the coordinator exactly once and
// moves on to its next run; bad expressions are refused.
func TestSchedulesFireOnce(t *testing.T) {
	tm := startTeam(t)
	path := "/api/projects/" + tm.projectID + "/schedules"
	if code, raw := tm.b.do("POST", path, map[string]any{"name": "x", "cron": "61 * * * *", "message": "hi"}); code != 422 || !strings.Contains(string(raw), "cron") {
		t.Fatalf("bad cron = %d %s", code, raw)
	}
	var s timers.Schedule
	tm.b.must(201, "POST", path, map[string]any{"name": "早报", "cron": "0 9 * * *", "timezone": "Asia/Shanghai", "message": "汇报昨天的进展"}, &s)
	if s.NextRunAt == nil || !s.NextRunAt.After(time.Now()) || !s.Enabled {
		t.Fatalf("schedule %+v", s)
	}
	if _, err := store.Exec(tm.w.ctx, tm.w.e.Core.Pool, `UPDATE at_schedules SET next_run_at = now() - interval '1 minute' WHERE id = $1`, s.ID); err != nil {
		t.Fatal(err)
	}
	tm.w.e.Timers.Tick(tm.w.ctx)
	tm.w.e.Timers.Tick(tm.w.ctx)
	_, open := expectOpen(t, tm.w, tm.n, "coordinator")
	if open.FirstMessage == nil || !strings.Contains(open.FirstMessage.Text, "【定时任务：早报】") {
		t.Fatalf("coordinator first message %+v", open.FirstMessage)
	}
	var list []timers.Schedule
	tm.b.must(200, "GET", path, nil, &list)
	if len(list) != 1 || list[0].LastRunAt == nil || list[0].NextRunAt == nil || !list[0].NextRunAt.After(time.Now()) {
		t.Fatalf("after firing %+v", list)
	}
	var msgs int
	_ = store.QueryRow(tm.w.ctx, tm.w.e.Core.Pool, `SELECT count(*) FROM at_messages WHERE meta->>'schedule_id' = $1`, s.ID).Scan(&msgs)
	if msgs != 1 {
		t.Fatalf("schedule fired %d times", msgs)
	}
	tm.b.must(200, "PATCH", "/api/schedules/"+s.ID, map[string]any{"enabled": false}, &s)
	if s.Enabled || s.NextRunAt != nil {
		t.Fatalf("disabled %+v", s)
	}
	tm.b.must(204, "DELETE", "/api/schedules/"+s.ID, nil, nil)
}

// TestUsageLimitWaitsForEveryone: two employees of a thread hit limits that reset at
// different times; the thread resumes only after the later reset, and both are told.
func TestUsageLimitWaitsForEveryone(t *testing.T) {
	tm := startTeam(t)
	tm.b.must(200, "PATCH", "/api/projects/"+tm.projectID, map[string]any{"auto_resume": true}, nil)
	rateLimited(tm.lead, time.Hour)
	rateLimited(tm.dev, 2*time.Hour)
	countWaits := func() int {
		var n int
		_ = store.QueryRow(tm.w.ctx, tm.w.e.Core.Pool, `SELECT count(*) FROM at_rate_limit_waits`).Scan(&n)
		return n
	}
	eventually(t, "both waiting", func() bool { return countWaits() == 2 })
	if _, err := store.Exec(tm.w.ctx, tm.w.e.Core.Pool, `UPDATE at_rate_limit_waits SET reset_at = now() - interval '1 second'
		WHERE employee_id = (SELECT id FROM at_employees WHERE name = 'lead-1')`); err != nil {
		t.Fatal(err)
	}
	tm.w.e.Timers.Tick(tm.w.ctx)
	if st, _ := threadStatus(tm); st != "paused" || countWaits() != 2 {
		t.Fatalf("resumed before the later reset: %s, %d waits", st, countWaits())
	}
	expireWaits(t, tm)
	tm.lead.expectSend("使用限额已重置")
	tm.dev.expectSend("使用限额已重置")
	if st, _ := threadStatus(tm); st != "running" || countWaits() != 0 {
		t.Fatalf("after both resets: %s, %d waits", st, countWaits())
	}
}

// TestProjectModels: the project's member model overrides (thread_model: Claude,
// thread_codex_model: Codex) are checked against the owner's model list on create and on update:
// ” follows each role, an id must be in the list with the field's runtime, and a stored value
// that has since left the list may be sent again unchanged. coordinator_model is accepted from
// older clients but ignored: never checked, always ”.
func TestProjectModels(t *testing.T) {
	tm := startTeam(t)
	tm.b.must(200, "PUT", "/api/models/mine", map[string]any{"models": []map[string]any{
		{"runtime": "claude", "model": "claude-opus-5-5", "default": true}, {"runtime": "claude", "model": "claude-sonnet-x"},
		{"runtime": "codex", "model": "gpt-x", "default": true}, {"runtime": "codex", "model": "gpt-y"}}}, nil)
	type project struct {
		ID               string
		CoordinatorModel string `json:"coordinator_model"`
		ThreadModel      string `json:"thread_model"`
		ThreadCodexModel string `json:"thread_codex_model"`
	}
	type fieldErr struct {
		Fields []struct{ Path, Message string }
	}
	refuse := func(method, path string, body map[string]any, field, msg string) {
		t.Helper()
		code, raw := tm.b.do(method, path, body)
		var e fieldErr
		_ = json.Unmarshal(raw, &e)
		if code != 422 || len(e.Fields) != 1 || e.Fields[0].Path != field || !strings.Contains(e.Fields[0].Message, msg) {
			t.Fatalf("%s %s %v = %d %s", method, path, body, code, raw)
		}
	}

	// Create: defaults are '' (follow the roles); models are checked like updates.
	var plain project
	tm.b.must(201, "POST", "/api/projects", map[string]any{"name": "plain"}, &plain)
	if plain.CoordinatorModel != "" || plain.ThreadModel != "" || plain.ThreadCodexModel != "" {
		t.Fatalf("defaults %+v", plain)
	}
	refuse("POST", "/api/projects", map[string]any{"name": "x", "thread_model": "claude-nope"}, "thread_model", "不在你的模型列表里")
	refuse("POST", "/api/projects", map[string]any{"name": "x", "thread_codex_model": "claude-sonnet-x"}, "thread_codex_model", "只能选 Codex 模型")
	var made project
	tm.b.must(201, "POST", "/api/projects", map[string]any{"name": "with models", "coordinator_model": "gpt-not-listed",
		"thread_model": " claude-opus-5-5 ", "thread_codex_model": "gpt-y"}, &made)
	if made.CoordinatorModel != "" || made.ThreadModel != "claude-opus-5-5" || made.ThreadCodexModel != "gpt-y" {
		t.Fatalf("created %+v", made)
	}

	// Update.
	path := "/api/projects/" + made.ID
	refuse("PATCH", path, map[string]any{"thread_codex_model": "gpt-nope"}, "thread_codex_model", "不在你的模型列表里")
	refuse("PATCH", path, map[string]any{"thread_model": "gpt-x"}, "thread_model", "只能选 Claude 模型")
	var p project
	tm.b.must(200, "PATCH", path, map[string]any{"thread_codex_model": "gpt-x", "coordinator_model": "claude-sonnet-x"}, &p)
	if p.ThreadCodexModel != "gpt-x" || p.ThreadModel != "claude-opus-5-5" || p.CoordinatorModel != "" {
		t.Fatalf("patched %+v", p)
	}
	var stored string
	if err := store.QueryRow(tm.w.ctx, tm.w.e.Core.Pool, `SELECT coordinator_model FROM at_projects WHERE id = $1`, made.ID).Scan(&stored); err != nil || stored != "" {
		t.Fatalf("stored coordinator_model %q %v", stored, err)
	}
	// gpt-x and claude-sonnet-x leave the list: the stored values still save unchanged, new ones do not.
	tm.b.must(200, "PUT", "/api/models/mine", map[string]any{"models": []map[string]any{
		{"runtime": "claude", "model": "claude-opus-5-5", "default": true}, {"runtime": "codex", "model": "gpt-y"}}}, nil)
	tm.b.must(200, "PATCH", path, map[string]any{"thread_codex_model": "gpt-x", "name": "renamed"}, &p)
	refuse("PATCH", "/api/projects/"+plain.ID, map[string]any{"thread_codex_model": "gpt-x"}, "thread_codex_model", "不在你的模型列表里")
	// '' goes back to the roles.
	tm.b.must(200, "PATCH", path, map[string]any{"coordinator_model": "", "thread_model": "", "thread_codex_model": ""}, &p)
	if p.CoordinatorModel != "" || p.ThreadModel != "" || p.ThreadCodexModel != "" {
		t.Fatalf("cleared %+v", p)
	}
}
