package engine_test

import (
	"context"
	"strings"
	"testing"
	"time"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw/nodetest"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// TestAntigravityNeedsAReadyNode: an Antigravity member's session goes to the node whose hello
// reports agy installed and logged in, never to one that did not report it, even though that one
// comes first and has more free slots (pickNode's fallback would otherwise take it).
func TestAntigravityNeedsAReadyNode(t *testing.T) {
	w := newWorld(t, nil)
	tm := &team{w: w, b: w.signUp("agy@example.com")}
	var bare, ready nodeCreated
	tm.b.must(201, "POST", "/api/nodes", map[string]string{"name": "bare"}, &bare)
	tm.b.must(201, "POST", "/api/nodes", map[string]string{"name": "agy"}, &ready)
	tm.n = w.connect(bare.Token, nodetest.Options{MaxActive: 8, Isolation: "agent_user"})
	cli, auth := "1.5.14", protocol.AntigravityRuntimeAuth("oauth")
	n2 := w.connect(ready.Token, nodetest.Options{MaxActive: 2, Isolation: "agent_user", Runtimes: &protocol.Runtimes{Antigravity: &protocol.AntigravityRuntime{CLI: &cli, Auth: &auth}}})
	var roles struct{ Roles []store.Role }
	tm.b.must(200, "GET", "/api/roles", nil, &roles)
	for i := range roles.Roles {
		r := &roles.Roles[i]
		switch {
		case r.Tier != "member":
			r.NodeIDs = []string{bare.Node.ID}
		case r.Role == "dev":
			r.Count, r.Runtime, r.Runtimes, r.Model = 1, "antigravity", []string{"antigravity"}, ""
		}
	}
	tm.b.must(200, "PUT", "/api/roles", map[string]any{"roles": roles.Roles}, nil)
	var project struct{ ID string }
	tm.b.must(201, "POST", "/api/projects", map[string]any{"name": "p"}, &project)
	tm.projectID = project.ID

	_, lead := tm.openThread(t, "Agy", nil)
	lead.mustCall("assign_task", map[string]any{"assignee": "dev", "instruction": "port the parser", "main": "做"})
	_, open := expectOpen(t, w, n2, "dev-1")
	if open.Runtime != "antigravity" || open.Antigravity == nil {
		t.Fatalf("dev-1 open: runtime %q antigravity %+v", open.Runtime, open.Antigravity)
	}
	done, cancel := context.WithCancel(w.ctx)
	cancel()
	if f, err := tm.n.Wait(done, protocol.TypeSessionOpen, func(f nodetest.Frame) bool { return strings.Contains(string(f.Body), `"name":"dev-1"`) }); err == nil {
		t.Fatalf("the node without agy got dev-1's session.open: %s", f.Body)
	}
}

// TestAntigravityRuntimeStatus: on a daemon with runtime_status the node.runtimes report decides
// for Antigravity too, whatever hello says: reported not logged in, the member's session is held
// with the reason naming Antigravity; once the node reports agy logged in, it opens there.
func TestAntigravityRuntimeStatus(t *testing.T) {
	w := newWorld(t, nil)
	tm := &team{w: w, b: w.signUp("agy-status@example.com")}
	var node nodeCreated
	tm.b.must(201, "POST", "/api/nodes", map[string]string{"name": "n1"}, &node)
	// hello says agy is logged in; a daemon with runtime_status is judged by its report instead.
	cli, auth := "1.5.14", protocol.AntigravityRuntimeAuth("oauth")
	tm.n = w.connect(node.Token, nodetest.Options{MaxActive: 8, Isolation: "agent_user", Features: statusFeatures,
		Runtimes: &protocol.Runtimes{Antigravity: &protocol.AntigravityRuntime{CLI: &cli, Auth: &auth}}})
	ctx, cancel := w.waitCtx()
	defer cancel()
	if err := tm.n.SendUnreliable(ctx, protocol.TypeNodeRuntimes, cliReport("hello", map[string]string{"claude": "subscription", "antigravity": "none"})); err != nil {
		t.Fatal(err)
	}
	eventually(t, "report stored", func() bool { return len(tm.b.nodeCLIs(t, node.Node.ID).CLIs) == 2 })
	var roles struct{ Roles []store.Role }
	tm.b.must(200, "GET", "/api/roles", nil, &roles)
	for i := range roles.Roles {
		if r := &roles.Roles[i]; r.Tier == "member" && r.Role == "dev" {
			r.Count, r.Runtime, r.Runtimes, r.Model = 1, "antigravity", []string{"antigravity"}, ""
		}
	}
	tm.b.must(200, "PUT", "/api/roles", map[string]any{"roles": roles.Roles}, nil)
	var project struct{ ID string }
	tm.b.must(201, "POST", "/api/projects", map[string]any{"name": "p"}, &project)
	tm.projectID = project.ID

	_, lead := tm.openThread(t, "Agy", nil)
	lead.mustCall("assign_task", map[string]any{"assignee": "dev", "instruction": "port the parser", "main": "做"})
	dev := employeeID(t, w, project.ID, "dev-1")
	eventually(t, "dev-1 held: agy not logged in", func() bool {
		h, err := holdOf(w, dev)
		return err == nil && strings.Contains(h.Reason, "没有已登录 Antigravity 的在线节点")
	})
	expectNone(t, tm.n, protocol.TypeSessionOpen, 300*time.Millisecond, func(f nodetest.Frame) bool { return strings.Contains(string(f.Body), `"name":"dev-1"`) })

	if err := tm.n.SendUnreliable(ctx, protocol.TypeNodeRuntimes, cliReport("fs_event", map[string]string{"claude": "subscription", "antigravity": "subscription"})); err != nil {
		t.Fatal(err)
	}
	_, open := expectOpen(t, w, tm.n, "dev-1")
	if open.Runtime != "antigravity" || open.Antigravity == nil {
		t.Fatalf("dev-1 open: runtime %q antigravity %+v", open.Runtime, open.Antigravity)
	}
	ready := map[string]bool{}
	for _, c := range tm.b.nodeCLIs(t, node.Node.ID).CLIs {
		ready[c.Runtime] = c.Ready
	}
	if len(ready) != 2 || !ready["antigravity"] || !ready["claude"] {
		t.Fatalf("node clis ready %v", ready)
	}
}
