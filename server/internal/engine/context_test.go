package engine_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/engine"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw/nodetest"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

var contextFeatures = []string{"sessions", "git_worktree", "cred_helper", "file_download", "context_usage", "session_compact"}

// contextTeam is a project with one dev (limit 100K, compact between tasks at 60%, clear after
// clearAfter tasks) on a node with the given features.
func contextTeam(t *testing.T, features []string, clearAfter int, tune func(*engine.Engine)) *team {
	t.Helper()
	w := newWorldWith(t, nil, tune)
	tm := &team{w: w, b: w.signUp("context@example.com")}
	var node nodeCreated
	tm.b.must(201, "POST", "/api/nodes", map[string]string{"name": "fake"}, &node)
	var roles struct{ Roles []store.Role }
	tm.b.must(200, "GET", "/api/roles", nil, &roles)
	limit, between, inTask := 100000, 60, 80
	dev := &roles.Roles[2]
	dev.Count, dev.ContextLimit, dev.CompactBetweenPct, dev.CompactInTaskPct, dev.ClearAfterTasks = 1, &limit, &between, &inTask, &clearAfter
	tm.b.must(200, "PUT", "/api/roles", map[string]any{"roles": roles.Roles}, nil)
	var project struct{ ID string }
	tm.b.must(201, "POST", "/api/projects", map[string]any{"name": "p"}, &project)
	tm.projectID = project.ID
	tm.n = w.connect(node.Token, nodetest.Options{Features: features})
	return tm
}

// turnEnded reports a turn end with the context the session uses.
func (a *fakeAgent) turnEnded(used, max int) {
	a.event("turn_ended", func(e *protocol.SessionEvent) {
		sub, isErr, turns, ms := "success", false, 1, 10
		usage := json.RawMessage(`{}`)
		e.ResultSubtype, e.IsError, e.NumTurns, e.DurationMS, e.Usage = &sub, &isErr, &turns, &ms, &usage
		e.Context = &protocol.ContextUsage{UsedTokens: used, MaxTokens: max}
	})
}

// firstTask runs t1 for dev-1 up to its completion, the session reporting used tokens.
func firstTask(t *testing.T, tm *team, used int) (*fakeAgent, *fakeAgent, core.ThreadView) {
	t.Helper()
	th, lead := tm.openThread(t, "One", nil)
	lead.mustCall("assign_task", map[string]any{"assignee": "dev", "instruction": "build the api", "main": "做"})
	dev, open := startTask(t, tm, "dev-1", "build the api")
	if open.Context == nil || intOf(open.Context.LimitTokens) != 100000 || intOf(open.Context.InTaskPct) != 80 || open.Context.Focus == nil {
		t.Fatalf("session.open context %+v", open.Context)
	}
	if open.FirstMessage.Anchor == nil || !strings.Contains(*open.FirstMessage.Anchor, "[任务锚点]") || !strings.Contains(*open.FirstMessage.Anchor, "build the api") {
		t.Fatalf("first brief anchor %v", open.FirstMessage.Anchor)
	}
	dev.turnEnded(used, 200000)
	dev.mustCall("complete_task", map[string]any{"task_id": "t1", "summary": "api done"})
	lead.expectSend("[Task t1 · dev-1 · done]")
	return dev, lead, th
}

func intOf(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// expectCompact waits for session.compact of the agent's session.
func expectCompact(t *testing.T, tm *team, a *fakeAgent) protocol.SessionCompact {
	t.Helper()
	ctx, cancel := tm.w.waitCtx()
	defer cancel()
	f, err := tm.n.Wait(ctx, protocol.TypeSessionCompact, a.mine)
	if err != nil {
		t.Fatal(err)
	}
	var c protocol.SessionCompact
	_ = f.Decode(&c)
	return c
}

// noSendWith asserts that no session.send containing needle goes out within a moment.
func noSendWith(t *testing.T, tm *team, needle string) {
	t.Helper()
	ctx, cancel := contextShort()
	defer cancel()
	if f, err := tm.n.Wait(ctx, protocol.TypeSessionSend, func(f nodetest.Frame) bool { return strings.Contains(string(f.Body), needle) }); err == nil {
		t.Fatalf("unexpected session.send: %s", f.Body)
	}
}

func noBrief(t *testing.T, tm *team) { t.Helper(); noSendWith(t, tm, "[Task t2") }

func sessionRow(t *testing.T, tm *team, id string) core.AgentSession {
	t.Helper()
	s, err := core.SessionByID(tm.w.ctx, tm.w.e.Core.Pool, id)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestClaimCompactsFullSession (shared-members.md §5): dev-1's session used 70K of the role's
// 100K limit (≥ 60%) when it claims t2 in the same thread: the console compacts it first (focus
// and the next task in the instructions), the task waits in claimed, and the brief with its
// anchor goes out on session.compacted. The task starts at the compacted context.
func TestClaimCompactsFullSession(t *testing.T) {
	tm := contextTeam(t, contextFeatures, 8, nil)
	dev, lead, th := firstTask(t, tm, 70000)
	if s := sessionRow(t, tm, dev.session); intOf(s.ContextUsed) != 70000 || intOf(s.ContextMax) != 200000 || s.TasksDone != 1 {
		t.Fatalf("session context %v/%v tasks %d", s.ContextUsed, s.ContextMax, s.TasksDone)
	}
	lead.mustCall("assign_task", map[string]any{"assignee": "dev", "instruction": "add rate limits", "main": "做"})
	c := expectCompact(t, tm, dev)
	if c.Instructions == nil || !strings.Contains(*c.Instructions, "保留当前任务 id") || !strings.Contains(*c.Instructions, "t2：add rate limits") {
		t.Fatalf("compact instructions %v", c.Instructions)
	}
	noBrief(t, tm)
	if task := tm.taskRow(t, th.ID, "t2"); task.Status != "claimed" || task.CompactID != c.CompactID {
		t.Fatalf("t2 while compacting: %s %q", task.Status, task.CompactID)
	}
	dev.send(protocol.TypeSessionCompacted, protocol.SessionCompacted{SessionID: c.SessionID, OpenGen: c.OpenGen, CompactID: c.CompactID, Ok: true,
		Context: &protocol.ContextUsage{UsedTokens: 9000, MaxTokens: 200000}})
	brief := dev.expectSend("[Task t2")
	if brief.Anchor == nil || !strings.Contains(*brief.Anchor, "任务 t2") || !strings.Contains(*brief.Anchor, ".agent-notes/t2.md") {
		t.Fatalf("brief anchor %v", brief.Anchor)
	}
	task := tm.taskRow(t, th.ID, "t2")
	if task.Status != "running" || task.CompactID != "" || intOf(task.CtxStart) != 9000 {
		t.Fatalf("t2 after the brief: %s %q start %v", task.Status, task.CompactID, task.CtxStart)
	}
	if s := sessionRow(t, tm, dev.session); s.TasksDone != 2 || s.State == "closed" {
		t.Fatalf("session after t2's brief: tasks %d state %s", s.TasksDone, s.State)
	}
	// a late repeat of session.compacted sends nothing more
	dev.send(protocol.TypeSessionCompacted, protocol.SessionCompacted{SessionID: c.SessionID, OpenGen: c.OpenGen, CompactID: c.CompactID, Ok: true})
	noBrief(t, tm)
}

// TestClaimContinuesBelowThreshold: under 60% of the limit the brief goes into the same session.
func TestClaimContinuesBelowThreshold(t *testing.T) {
	tm := contextTeam(t, contextFeatures, 8, nil)
	dev, lead, _ := firstTask(t, tm, 20000)
	lead.mustCall("assign_task", map[string]any{"assignee": "dev", "instruction": "add rate limits", "main": "做"})
	dev.expectSend("[Task t2")
	if _, ok := takeNow(tm.n, protocol.TypeSessionCompact); ok {
		t.Fatal("compacted below the threshold")
	}
}

// TestClaimClearsWithoutCompact: a daemon without session_compact cannot compact, so a full
// session is cleared instead: closed (switch_thread on the wire for such a daemon, context_clear
// in the database) and the brief opens a fresh session headed by the thread digest.
func TestClaimClearsWithoutCompact(t *testing.T) {
	tm := contextTeam(t, []string{"sessions", "git_worktree", "cred_helper", "file_download", "context_usage"}, 8, nil)
	dev, lead, _ := firstTask(t, tm, 70000)
	lead.mustCall("assign_task", map[string]any{"assignee": "dev", "instruction": "add rate limits", "main": "做"})
	if reason := expectClosed(t, tm.w, tm.n, dev); reason != "switch_thread" {
		t.Fatalf("close reason on the wire %q", reason)
	}
	if s := sessionRow(t, tm, dev.session); s.CloseReason != "context_clear" {
		t.Fatalf("close reason %q", s.CloseReason)
	}
	_, open := startTask(t, tm, "dev-1", "[Task t2")
	if open.Resume != nil || !strings.Contains(open.FirstMessage.Text, "[新会话]") || open.FirstMessage.Anchor == nil {
		t.Fatalf("fresh session: resume %+v first %q anchor %v", open.Resume, open.FirstMessage.Text, open.FirstMessage.Anchor)
	}
}

// TestClaimClearsAfterTasks: clear_after_tasks = 1 (a gate role's default) gives every task a
// fresh session, whatever the context use; a daemon with session_compact gets context_clear.
func TestClaimClearsAfterTasks(t *testing.T) {
	tm := contextTeam(t, contextFeatures, 1, nil)
	dev, lead, _ := firstTask(t, tm, 1000)
	lead.mustCall("assign_task", map[string]any{"assignee": "dev", "instruction": "add rate limits", "main": "做"})
	if reason := expectClosed(t, tm.w, tm.n, dev); reason != "context_clear" {
		t.Fatalf("close reason %q", reason)
	}
	startTask(t, tm, "dev-1", "[新会话]")
}

// TestCompactTimeout: without session.compacted the brief goes out after CompactTimeout.
func TestCompactTimeout(t *testing.T) {
	tm := contextTeam(t, contextFeatures, 8, func(e *engine.Engine) { e.Sched.Interval = 200 * time.Millisecond })
	dev, lead, th := firstTask(t, tm, 90000)
	lead.mustCall("assign_task", map[string]any{"assignee": "dev", "instruction": "add rate limits", "main": "做"})
	expectCompact(t, tm, dev)
	noBrief(t, tm)
	tm.sql(t, `UPDATE at_tasks SET wait_since = now() - interval '11 minutes' WHERE thread_id = $1 AND id = 't2'`, th.ID)
	dev.expectSend("[Task t2")
	if task := tm.taskRow(t, th.ID, "t2"); task.CompactID != "" {
		t.Fatalf("compact id kept %q", task.CompactID)
	}
}

// TestLongTaskMetrics (§5.2): compactions during the running task are counted and the lead hears
// once, at the second, that the task may be too big; turn ends record the context it ends with;
// report_progress goes into the task (for its anchor).
func TestLongTaskMetrics(t *testing.T) {
	tm := contextTeam(t, contextFeatures, 8, nil)
	th, lead := tm.openThread(t, "Long", nil)
	lead.mustCall("assign_task", map[string]any{"assignee": "dev", "instruction": "rewrite everything", "main": "做"})
	dev, _ := startTask(t, tm, "dev-1", "rewrite everything")
	dev.mustCall("report_progress", map[string]any{"text": "half of the handlers moved"})
	off := func(e *protocol.SessionEvent) { on := false; e.On = &on }
	dev.event("compacting", off)
	dev.event("compacting", off)
	lead.expectSend("[Task t1 · dev-1] 任务很长，已压缩 2 次，考虑拆小")
	dev.event("compacting", off)
	dev.turnEnded(64000, 200000)
	task := tm.taskRow(t, th.ID, "t1")
	if task.Compactions != 3 || !task.SplitHinted || intOf(task.CtxEnd) != 64000 || task.Progress != "half of the handlers moved" {
		t.Fatalf("metrics: compactions %d hinted %v end %v progress %q", task.Compactions, task.SplitHinted, task.CtxEnd, task.Progress)
	}
	noSendWith(t, tm, "考虑拆小")
}
