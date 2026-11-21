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
)

// statusFeatures is a daemon that reports its runtime CLIs (hello.features runtime_status).
var statusFeatures = []string{"sessions", "git_worktree", "cred_helper", "file_download", "runtime_status"}

// cliReport is a node.runtimes body: auth per runtime id ("" = not installed).
func cliReport(reason string, auth map[string]string) map[string]any {
	rts := map[string]any{}
	for id, a := range auth {
		st := map[string]any{"installed": a != "", "auth": a, "checked_at": time.Now().UTC().Format(time.RFC3339)}
		if a == "" {
			st["auth"] = "unknown"
			st["error"] = id + " not found in PATH"
		} else {
			st["version"], st["path"] = "1.2.3", "/usr/local/bin/"+id
		}
		rts[id] = st
	}
	return map[string]any{"reason": reason, "runtimes": rts}
}

type cliView struct {
	Runtime   string `json:"runtime"`
	Installed bool   `json:"installed"`
	Auth      string `json:"auth"`
	Ready     bool   `json:"ready"`
	Version   string `json:"version"`
	Path      string `json:"path"`
	Error     string `json:"error"`
	CheckedAt string `json:"checked_at"`
}

type cliNode struct {
	ID            string    `json:"id"`
	RuntimeStatus bool      `json:"runtime_status"`
	CLIs          []cliView `json:"clis"`
}

func (b *browser) nodeCLIs(t *testing.T, id string) cliNode {
	t.Helper()
	var nodes []cliNode
	b.must(200, "GET", "/api/nodes", nil, &nodes)
	for _, n := range nodes {
		if n.ID == id {
			return n
		}
	}
	t.Fatalf("node %s not listed", id)
	return cliNode{}
}

func holdOf(w *world, employeeID string) (core.SessionHold, error) {
	return core.HoldOf(w.ctx, w.e.Core.Pool, employeeID)
}

func employeeID(t *testing.T, w *world, projectID, name string) string {
	t.Helper()
	var id string
	eventually(t, name+" exists", func() bool {
		_ = w.e.Core.Pool.Gorm(w.ctx).Table("at_employees").Select("id").Where("project_id = ? AND name = ?", projectID, name).
			Scan(&id).Error
		return id != ""
	})
	return id
}

func statusBubble(w *world, employeeID string) string {
	var bubble string
	_ = w.e.Core.Pool.Gorm(w.ctx).Table("at_employee_status").Select("bubble").Where("employee_id = ?", employeeID).Scan(&bubble).Error
	return bubble
}

// TestRuntimeStatusHoldsUntilLoggedIn: a daemon with runtime_status that reports Claude Code
// installed but not logged in gets no session; the lead waits (queued · no_runtime) with the
// reason in its bubble and the thread phase, a message to it joins the held first message, and
// when the node reports the login the session opens there with both. node.runtimes is stored
// (GET /api/nodes clis, upsert on the next report, runtimes the report no longer has removed).
func TestRuntimeStatusHoldsUntilLoggedIn(t *testing.T) {
	w := newWorld(t, nil)
	b := w.signUp("cli@example.com")
	var node nodeCreated
	b.must(201, "POST", "/api/nodes", map[string]string{"name": "n1"}, &node)
	b.must(200, "GET", "/api/roles", nil, nil)
	var project struct{ ID string }
	b.must(201, "POST", "/api/projects", map[string]any{"name": "p"}, &project)
	n := w.connect(node.Token, nodetest.Options{Features: statusFeatures})
	ctx, cancel := w.waitCtx()
	defer cancel()
	if err := n.SendUnreliable(ctx, protocol.TypeNodeRuntimes, cliReport("hello", map[string]string{"claude": "none", "codex": "", "gemini": "none"})); err != nil {
		t.Fatal(err)
	}
	eventually(t, "report stored", func() bool { return len(b.nodeCLIs(t, node.Node.ID).CLIs) == 3 })
	view := b.nodeCLIs(t, node.Node.ID)
	if !view.RuntimeStatus || view.CLIs[0].Runtime != "claude" || view.CLIs[0].Ready || view.CLIs[0].Path != "/usr/local/bin/claude" ||
		view.CLIs[1].Runtime != "codex" || view.CLIs[1].Installed || view.CLIs[1].Error == "" || view.CLIs[2].Runtime != "gemini" {
		t.Fatalf("node view %+v", view)
	}

	var thread core.ThreadView
	b.must(201, "POST", "/api/projects/"+project.ID+"/threads", map[string]any{"title": "Work", "brief": "do it"}, &thread)
	lead := employeeID(t, w, project.ID, "lead-1")
	eventually(t, "lead held", func() bool { _, err := holdOf(w, lead); return err == nil })
	expectNone(t, n, protocol.TypeSessionOpen, 300*time.Millisecond, nil)
	eventually(t, "lead queued · no_runtime", func() bool {
		st, sub := statusOf(w, lead)
		return st == "queued" && sub == "no_runtime" && strings.Contains(statusBubble(w, lead), "没有已登录 Claude Code 的在线节点")
	})
	var tv core.ThreadView
	eventually(t, "thread phase names the reason", func() bool {
		b.must(200, "GET", "/api/threads/"+thread.ID, nil, &tv)
		return strings.Contains(core.PhaseOf(tv, 0).Note, "没有已登录 Claude Code")
	})
	b.must(202, "POST", "/api/conversations/"+thread.ID+"/messages", map[string]string{"content": "顺便加个测试"}, nil)
	eventually(t, "message joins the hold", func() bool {
		h, err := holdOf(w, lead)
		return err == nil && strings.Contains(h.First.Text, "顺便加个测试") && len(h.More) == 1
	})

	// Logged in: the held session opens with the brief and the later message.
	if err := n.SendUnreliable(ctx, protocol.TypeNodeRuntimes, cliReport("fs_event", map[string]string{"claude": "subscription", "codex": ""})); err != nil {
		t.Fatal(err)
	}
	_, open := expectOpen(t, w, n, "lead-1")
	if open.FirstMessage == nil || !strings.Contains(open.FirstMessage.Text, "do it") || !strings.Contains(open.FirstMessage.Text, "顺便加个测试") {
		t.Fatalf("first message %+v", open.FirstMessage)
	}
	if _, err := holdOf(w, lead); err != core.ErrNotFound {
		t.Fatalf("hold after release: %v", err)
	}
	view = b.nodeCLIs(t, node.Node.ID)
	if len(view.CLIs) != 2 || !view.CLIs[0].Ready || view.CLIs[0].Auth != "subscription" {
		t.Fatalf("after login %+v", view)
	}
	eventually(t, "lead no longer queued", func() bool { st, _ := statusOf(w, lead); return st != "queued" })
}

// TestRuntimeStatusOldDaemon: a daemon without runtime_status is scheduled as before, even when
// it (or anything claiming to be it) sent node.runtimes saying the CLI is not logged in.
func TestRuntimeStatusOldDaemon(t *testing.T) {
	w := newWorld(t, nil)
	b := w.signUp("old@example.com")
	var node nodeCreated
	b.must(201, "POST", "/api/nodes", map[string]string{"name": "old"}, &node)
	b.must(200, "GET", "/api/roles", nil, nil)
	var project struct{ ID string }
	b.must(201, "POST", "/api/projects", map[string]any{"name": "p"}, &project)
	n := w.connect(node.Token, nodetest.Options{})
	ctx, cancel := w.waitCtx()
	defer cancel()
	if err := n.SendUnreliable(ctx, protocol.TypeNodeRuntimes, cliReport("hello", map[string]string{"claude": "none"})); err != nil {
		t.Fatal(err)
	}
	eventually(t, "report stored", func() bool { return len(b.nodeCLIs(t, node.Node.ID).CLIs) == 1 })
	if b.nodeCLIs(t, node.Node.ID).RuntimeStatus {
		t.Fatal("old daemon shown with runtime_status")
	}
	b.must(201, "POST", "/api/projects/"+project.ID+"/threads", map[string]any{"title": "Work", "brief": "do it"}, nil)
	expectOpen(t, w, n, "lead-1")
}

// TestRuntimeStatusNodeComesOnline: with the only node offline the session waits for it as
// before; a second node that reports the runtime ready takes the held session of an employee
// whose candidate node said it is not logged in.
func TestRuntimeStatusSecondNode(t *testing.T) {
	w := newWorld(t, nil)
	b := w.signUp("two@example.com")
	var a, c nodeCreated
	b.must(201, "POST", "/api/nodes", map[string]string{"name": "a"}, &a)
	b.must(201, "POST", "/api/nodes", map[string]string{"name": "c"}, &c)
	b.must(200, "GET", "/api/roles", nil, nil)
	var project struct{ ID string }
	b.must(201, "POST", "/api/projects", map[string]any{"name": "p"}, &project)
	na := w.connect(a.Token, nodetest.Options{Features: statusFeatures})
	ctx, cancel := w.waitCtx()
	defer cancel()
	if err := na.SendUnreliable(ctx, protocol.TypeNodeRuntimes, cliReport("hello", map[string]string{"claude": ""})); err != nil {
		t.Fatal(err)
	}
	eventually(t, "a reported", func() bool { return len(b.nodeCLIs(t, a.Node.ID).CLIs) == 1 })
	b.must(201, "POST", "/api/projects/"+project.ID+"/threads", map[string]any{"title": "Work", "brief": "do it"}, nil)
	lead := employeeID(t, w, project.ID, "lead-1")
	eventually(t, "lead held (not installed)", func() bool {
		h, err := holdOf(w, lead)
		return err == nil && strings.Contains(h.Reason, "Claude Code 没有安装在任何在线的候选节点上")
	})
	nc := w.connect(c.Token, nodetest.Options{Features: statusFeatures})
	if err := nc.SendUnreliable(ctx, protocol.TypeNodeRuntimes, cliReport("hello", map[string]string{"claude": "api_key"})); err != nil {
		t.Fatal(err)
	}
	_, open := expectOpen(t, w, nc, "lead-1")
	if open.FirstMessage == nil || !strings.Contains(open.FirstMessage.Text, "do it") {
		t.Fatalf("first message %+v", open.FirstMessage)
	}
	expectNone(t, na, protocol.TypeSessionOpen, 200*time.Millisecond, nil)
}

type recheckResult struct {
	Node  cliNode `json:"node"`
	Stale bool    `json:"stale"`
}

// TestNodeRecheck: POST /api/nodes/{id}/recheck sends node.runtimes_refresh (and
// node.usage_refresh to a daemon that reports usage), returns the node with the CLI states the
// answer stored, stale when the node does not answer in time, and 409 for an old daemon
// (unsupported_daemon) or an offline node (node_offline). An answer to another node's refresh
// does not end the wait.
func TestNodeRecheck(t *testing.T) {
	w := newWorldWith(t, nil, func(e *engine.Engine) { e.Gateway.RefreshWait = time.Second })
	b := w.signUp("re@example.com")
	var old, node, off nodeCreated
	b.must(201, "POST", "/api/nodes", map[string]string{"name": "old"}, &old)
	b.must(201, "POST", "/api/nodes", map[string]string{"name": "n1"}, &node)
	b.must(201, "POST", "/api/nodes", map[string]string{"name": "off"}, &off)
	w.connect(old.Token, nodetest.Options{})
	if code, raw := b.do("POST", "/api/nodes/"+old.Node.ID+"/recheck", nil); code != 409 || !strings.Contains(string(raw), "unsupported_daemon") {
		t.Fatalf("old daemon: %d %s", code, raw)
	}
	if code, raw := b.do("POST", "/api/nodes/"+off.Node.ID+"/recheck", nil); code != 409 || !strings.Contains(string(raw), "node_offline") {
		t.Fatalf("offline: %d %s", code, raw)
	}
	n := w.connect(node.Token, nodetest.Options{Features: append([]string{"usage"}, statusFeatures...)})
	ctx, cancel := w.waitCtx()
	defer cancel()
	path := "/api/nodes/" + node.Node.ID + "/recheck"

	done := make(chan recheckResult, 1)
	go func() {
		var r recheckResult
		code, raw := b.do("POST", path, nil)
		if code != 200 || json.Unmarshal(raw, &r) != nil {
			r.Stale = true
		}
		done <- r
	}()
	f, err := n.Wait(ctx, protocol.TypeNodeRuntimesRefresh, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := n.Wait(ctx, protocol.TypeNodeUsageRefresh, nil); err != nil {
		t.Fatal(err)
	}
	other := cliReport("refresh", map[string]string{"claude": "none"})
	other["in_reply_to"] = "c_someone_else"
	if err := n.SendUnreliable(ctx, protocol.TypeNodeRuntimes, other); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	answer := cliReport("refresh", map[string]string{"claude": "subscription", "codex": "chatgpt"})
	answer["in_reply_to"] = f.ID
	if err := n.SendUnreliable(ctx, protocol.TypeNodeRuntimes, answer); err != nil {
		t.Fatal(err)
	}
	r := <-done
	if r.Stale || !r.Node.RuntimeStatus || len(r.Node.CLIs) != 2 || !r.Node.CLIs[0].Ready || r.Node.CLIs[1].Auth != "chatgpt" {
		t.Fatalf("recheck %+v", r)
	}

	// No answer: stale after RefreshWait, with the last report.
	start := time.Now()
	var stale recheckResult
	b.must(200, "POST", path, nil, &stale)
	if !stale.Stale || len(stale.Node.CLIs) != 2 || time.Since(start) < 900*time.Millisecond {
		t.Fatalf("no answer: %+v after %s", stale, time.Since(start))
	}
}
