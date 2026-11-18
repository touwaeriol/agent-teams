package engine_test

import (
	"encoding/json"
	"strings"
	"testing"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw/nodetest"
)

// TestTaskHandoff (shared-members.md §4.2): t2 depends on t1. Completing t1 without a hand-off
// note is refused once; the merge records what t1 added to the thread branch; before t2's brief
// goes out the member's worktree is synced to the thread branch (daemons with git_sync), and the
// brief carries the sync result, t1's note, its range and files.
func TestTaskHandoff(t *testing.T) {
	gh, ghSrv := newFakeGitHub(t)
	w := newWorld(t, ghSrv)
	b := w.signUp("handoff@example.com")
	var node nodeCreated
	b.must(201, "POST", "/api/nodes", map[string]string{"name": "fake"}, &node)
	b.must(200, "GET", "/api/roles", nil, nil)
	var conn struct{ ID string }
	b.must(201, "POST", "/api/git/connections", map[string]string{"name": "gh", "token": "ghp_good"}, &conn)
	var project struct{ ID string }
	b.must(201, "POST", "/api/projects", map[string]any{"name": "web", "merge_policy": "manual", "repos": []map[string]any{{"repo": "o/r", "git_connection_id": conn.ID}}}, &project)
	n := w.connect(node.Token, nodetest.Options{Features: []string{"sessions", "git_worktree", "cred_helper", "file_download", "git_sync"}})
	events := b.stream("/api/projects/"+project.ID+"/events?topics=merge,task", "")

	var thread core.ThreadView
	b.must(201, "POST", "/api/projects/"+project.ID+"/threads", map[string]any{"title": "Login", "brief": "登录页"}, &thread)
	lead, _ := expectOpen(t, w, n, "lead-1")
	answerPrepares(t, w, n)
	lead.mustCall("update_plan", map[string]any{"steps": []map[string]string{{"title": "页面", "status": "doing"}}})
	lead.mustCall("assign_task", map[string]any{"assignee": "dev", "instruction": "写接口", "main": "页面"})
	lead.mustCall("assign_task", map[string]any{"assignee": "dev-1", "instruction": "接着写表单", "main": "页面", "depends_on": []string{"t1"}})
	dev, devOpen := expectOpen(t, w, n, "dev-1")
	answerPrepares(t, w, n)
	dev.send(protocol.TypeSessionDelivered, protocol.SessionDelivered{SessionID: protocol.SessionID(dev.session), MsgID: devOpen.FirstMessage.MsgID})

	// t2 depends on t1: the first complete_task without a hand-off note is refused.
	if res := dev.call("complete_task", map[string]any{"task_id": "t1", "summary": "接口好了"}); res.Ok || !strings.Contains(text(res), "handoff") {
		t.Fatalf("missing handoff accepted: %+v", res)
	}
	callID := "tc_complete_t1"
	gen := protocol.OpenGen(dev.gen)
	dev.send(protocol.TypeToolCall, protocol.ToolCall{CallID: protocol.CallID(callID), SessionID: protocol.SessionID(dev.session), OpenGen: &gen,
		Tool: "complete_task", Args: []byte(`{"task_id":"t1","summary":"接口好了","handoff":"接口在 api.go，表单直接调 Login()"}`)})
	push(t, w, gh, n, "thread/1/dev-1", "sha-dev-1")
	if res := dev.result(callID); !res.Ok {
		t.Fatalf("complete_task t1: %+v", res)
	}
	lead.mustCall("enqueue_merge", map[string]any{"task_id": "t1"})
	events.wait(t, "task", `"id":"t1"`, `"status":"done"`)

	t1, err := core.TaskByID(w.ctx, w.e.Core.Pool, thread.ID, "t1")
	if err != nil {
		t.Fatal(err)
	}
	m := t1.Merged["o/r"]
	if t1.Handoff == "" || m.Head == "" || m.Base == "" || len(m.Files) != 1 {
		t.Fatalf("t1 hand-over not recorded: handoff %q merged %+v", t1.Handoff, t1.Merged)
	}

	// dev-1 claims t2: its worktree is synced to the thread branch before the brief goes out.
	ctx, cancel := w.waitCtx()
	defer cancel()
	f, err := n.Wait(ctx, protocol.TypeGitPrepare, func(f nodetest.Frame) bool { return strings.Contains(string(f.Body), `"sync"`) })
	if err != nil {
		t.Fatal(err)
	}
	var prep protocol.GitPrepare
	_ = f.Decode(&prep)
	if prep.Sync == nil || prep.Sync.Mode != protocol.GitPrepareSyncModeMember || prep.Sync.Ref != thread.Branch {
		t.Fatalf("sync request %+v", prep.Sync)
	}
	if _, err := n.Send(ctx, protocol.TypeGitResult, protocol.GitResult{JobID: prep.JobID, Ok: true, HeadSHA: &m.Head,
		Sync: &protocol.GitResultSync{Action: protocol.GitResultSyncActionReset, HeadSHA: m.Head}}); err != nil {
		t.Fatal(err)
	}
	brief := dev.expectSend("[Task t2")
	for _, want := range []string{"已同步到讨论串分支 " + thread.Branch, "交接说明：接口在 api.go", "t1（dev-1）：接口好了", m.Files[0]} {
		if !strings.Contains(brief.Text, want) {
			t.Errorf("brief misses %q:\n%s", want, brief.Text)
		}
	}

	// A turn ending with t2 still running pushes dev-1's committed work (shared-members.md §4.3),
	// at most once per AutoPushEvery: the second turn end pushes nothing.
	ended := func(e *protocol.SessionEvent) {
		sub, isErr, turns, ms := "success", false, 1, 10
		usage := json.RawMessage(`{"input_tokens":10,"output_tokens":5}`)
		cost := 0.01
		e.ResultSubtype, e.IsError, e.NumTurns, e.DurationMS, e.Usage, e.CostUSD = &sub, &isErr, &turns, &ms, &usage, &cost
	}
	dev.event("turn_ended", ended)
	auto := push(t, w, gh, n, "thread/1/dev-1", "sha-dev-1b")
	dev.expectSend("[平台提醒] 任务 t2 还没有完成")
	dev.event("turn_ended", ended)
	lead.expectSend("[Task t2 · dev-1 · 需要关注]")
	if f, ok := takeNow(n, protocol.TypeGitPush); ok {
		t.Fatalf("pushed again within AutoPushEvery: %s (first %s)", f.Body, auto.JobID)
	}
}
