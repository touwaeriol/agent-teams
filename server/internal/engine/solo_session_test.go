package engine_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	protocol "github.com/touwaeriol/agent-teams/protocol"

	"github.com/touwaeriol/agent-teams/server/internal/nodegw/nodetest"
	"github.com/touwaeriol/agent-teams/server/internal/orchestrator"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// TestSessionMultiRepo: POST /api/sessions starts a session (a project of kind session): its one
// employee is made from the chosen member role as a writing lead, checks out every repository
// side by side from the branch picked for it, gets only the session tools and the user's first
// message as written, and pushes its own session branch. No coordinator and no members exist.
// Repositories added while it runs are checked out at once and removed ones are cleaned up, and
// the employee is told.
func TestSessionMultiRepo(t *testing.T) {
	gh, ghSrv := newFakeGitHub(t)
	w := newWorld(t, ghSrv)
	b := w.signUp("session@example.com")
	var node nodeCreated
	b.must(201, "POST", "/api/nodes", map[string]string{"name": "fake"}, &node)
	b.must(200, "GET", "/api/roles", nil, nil)
	var conn struct{ ID string }
	b.must(201, "POST", "/api/git/connections", map[string]string{"name": "gh", "token": "ghp_good"}, &conn)
	n := w.connect(node.Token, nodetest.Options{})

	b.must(422, "POST", "/api/sessions", map[string]any{"message": "x", "role": "nope",
		"repos": []map[string]any{{"repo": "o/r", "git_connection_id": conn.ID}}}, nil)

	var created struct {
		Project struct {
			ID    string
			Kind  string
			Name  string
			Repos []struct{ Repo string }
		}
		ThreadNo int    `json:"thread_no"`
		ThreadID string `json:"thread_id"`
	}
	gh.setRefIn("o/r", "develop", "sha-develop")
	msg := "修复登录后的跳转\n前端在 o/web，接口在 o/r"
	b.must(201, "POST", "/api/sessions", map[string]any{"message": msg, "role": "dev", "node_id": node.Node.ID,
		"repos": []map[string]any{{"repo": "o/r", "git_connection_id": conn.ID, "branch": "develop"},
			{"repo": "o/web", "git_connection_id": conn.ID}}}, &created)
	p := created.Project
	if p.Kind != "session" || p.Name != "修复登录后的跳转" || created.ThreadNo != 1 || len(p.Repos) != 2 {
		t.Fatalf("session %+v", created)
	}

	emp, open := expectOpen(t, w, n, "lead-1")
	preps := prepares(t, w, n, 2)
	branch := "session/" + p.ID[:8] + "-r-web" // a Chinese title: named after the repositories
	if preps["o/r"].Branch != branch || *preps["o/r"].Base != "develop" || *preps["o/web"].Base != "main" ||
		!strings.HasSuffix(string(preps["o/web"].Cwd), "/threads/1/lead/o__web") {
		t.Fatalf("checkouts %+v %+v", preps["o/r"], preps["o/web"])
	}
	// A daemon without session_controls gets the role's mode as before (#53; TestSessionControls
	// covers the auto opening on newer daemons).
	if open.Claude == nil || open.Claude.PermissionMode != "bypassPermissions" || open.Claude.PreferAuto != nil ||
		!slices.Contains(open.Claude.Tools, "Edit") || open.Claude.ReadOnly != nil {
		t.Fatalf("the session employee writes: %+v", open.Claude)
	}
	var tools []string
	for _, tool := range open.PlatformTools {
		tools = append(tools, tool.Name)
	}
	if !slices.Contains(tools, "ask_user") || slices.Contains(tools, "assign_task") || slices.Contains(tools, "open_pr") {
		t.Fatalf("session tools %v", tools)
	}
	sys := *open.Claude.SystemAppend
	if !strings.Contains(sys, "多仓库会话") || !strings.Contains(sys, "o/web：目录 ./o__web") || strings.Contains(sys, "## 项目记忆") {
		t.Fatalf("session prompt:\n%s", sys)
	}
	if open.FirstMessage == nil || open.FirstMessage.Text != msg {
		t.Fatalf("first message %+v", open.FirstMessage)
	}
	if prot := open.Env["AT_PROTECTED_REFS"]; strings.Contains(prot, "session/") || !strings.Contains(prot, "develop") {
		t.Fatalf("protected refs %q", prot)
	}
	emp.send(protocol.TypeSessionDelivered, protocol.SessionDelivered{SessionID: protocol.SessionID(emp.session), MsgID: open.FirstMessage.MsgID})

	type sessionView struct {
		Cwd         string `json:"cwd"`
		Runtime     string `json:"runtime"`
		ContextUsed *int   `json:"context_used"`
		ContextMax  *int   `json:"context_max"`
	}
	var views []struct {
		No       int
		Dispatch *struct{}    `json:"dispatch"`
		Solo     bool         `json:"solo"`
		Session  *sessionView `json:"session"`
	}
	b.must(200, "GET", "/api/projects/"+p.ID+"/threads", nil, &views)
	if len(views) != 1 || views[0].Dispatch != nil {
		t.Fatalf("the first message shows as the user's own message, not a hand-over: %+v", views)
	}
	// 会话信息: the thread says it is a session's and carries the node's working directory; no
	// context use until a turn ends.
	if !views[0].Solo || views[0].Session == nil || !strings.HasSuffix(views[0].Session.Cwd, "/"+string(open.Cwd)) || !strings.HasPrefix(views[0].Session.Cwd, "/tmp/fake/") ||
		views[0].Session.Runtime != "claude" || views[0].Session.ContextUsed != nil {
		t.Fatalf("session view %+v %+v", views, views[0].Session)
	}
	sessionStatus := func() string {
		var list []struct {
			ID            string
			SessionStatus string `json:"session_status"`
		}
		b.must(200, "GET", "/api/projects", nil, &list)
		for _, x := range list {
			if x.ID == p.ID {
				return x.SessionStatus
			}
		}
		return "?"
	}
	if st := sessionStatus(); st != "working" {
		t.Fatalf("the user spoke last: %q", st)
	}
	// A turn runs: working. The user stops it before any reply: the turn ends without one and the
	// session is 已完成 all the same (not stuck on 进行中 because the user spoke last).
	emp.event("turn_started", func(ev *protocol.SessionEvent) { ev.TriggerMsgIds = []protocol.MsgID{open.FirstMessage.MsgID} })
	eventually(t, "working session", func() bool { return sessionStatus() == "working" })
	b.must(202, "POST", "/api/conversations/"+created.ThreadID+"/interrupt", nil, nil)
	ictx, icancel := w.waitCtx()
	if _, err := n.Wait(ictx, protocol.TypeSessionInterrupt, nil); err != nil {
		t.Fatal(err)
	}
	icancel()
	emp.turnEnded(12000, 1000000)
	eventually(t, "finished after the user stopped it", func() bool { return sessionStatus() == "finished" })
	// The employee replies in a later turn: 已完成 (finished), with the context it used.
	emp.event("turn_started", func(ev *protocol.SessionEvent) {})
	emp.event("text", func(ev *protocol.SessionEvent) { s, f := "改好了", true; ev.Text, ev.Final = &s, &f })
	emp.turnEnded(77000, 1000000)
	eventually(t, "finished session", func() bool { return sessionStatus() == "finished" })
	b.must(200, "GET", "/api/projects/"+p.ID+"/threads", nil, &views)
	if c := views[0].Session; c == nil || c.ContextUsed == nil || *c.ContextUsed != 77000 || c.ContextMax == nil || *c.ContextMax != 1000000 {
		t.Fatalf("context total %+v", c)
	}

	ctx := context.Background()
	var tiers []string
	if err := w.db.Pool.Gorm(ctx).Table("at_employees").Where("project_id = ?", p.ID).Pluck("tier", &tiers).Error; err != nil {
		t.Fatal(err)
	}
	if len(tiers) != 1 || tiers[0] != "lead" {
		t.Fatalf("a session has one employee, no coordinator nor members: %v", tiers)
	}
	var nodes []string
	if err := w.db.Pool.Gorm(ctx).Table("at_employees").Where("project_id = ?", p.ID).Pluck("spec->>'node_ids'", &nodes).Error; err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || !strings.Contains(nodes[0], node.Node.ID) {
		t.Fatalf("the picked node is the employee's only candidate: %v", nodes)
	}

	var after struct{ Kind string }
	gh.setRefIn("o/api", "v2", "sha-api-v2")
	b.must(200, "PUT", "/api/sessions/"+p.ID+"/repos", map[string]any{"repos": []map[string]any{
		{"repo": "o/r", "git_connection_id": conn.ID}, {"repo": "o/api", "git_connection_id": conn.ID, "branch": "v2"}}}, &after)
	added := prepares(t, w, n, 1)
	if *added["o/api"].Base != "v2" || !strings.HasSuffix(string(added["o/api"].Cwd), "/threads/1/lead/o__api") {
		t.Fatalf("added checkout %+v", added)
	}
	wctx, cancel := w.waitCtx()
	f, err := n.Wait(wctx, protocol.TypeGitCleanup, nil)
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	var clean protocol.GitCleanup
	_ = f.Decode(&clean)
	if len(clean.Paths) != 1 || !strings.HasSuffix(string(clean.Paths[0]), "/o__web") || !clean.KeepUnpushed ||
		string(clean.Shared) != "projects/"+p.ID+"/repos/o__web" {
		t.Fatalf("cleanup %+v", clean)
	}
	emp.expectSend("新增 o/api")

	if after.Kind != "session" {
		t.Fatalf("kind after repos change %q", after.Kind)
	}
	b.must(404, "PUT", "/api/sessions/nope/repos", map[string]any{"repos": []map[string]any{
		{"repo": "o/r", "git_connection_id": conn.ID}}}, nil)

	// 已解决: the user confirms; the thread is done and the sidebar says resolved.
	var th []struct{ ID string }
	b.must(200, "GET", "/api/projects/"+p.ID+"/threads", nil, &th)
	b.must(200, "POST", "/api/threads/"+th[0].ID+"/complete", nil, nil)
	if st := sessionStatus(); st != "resolved" {
		t.Fatalf("resolved session: %q", st)
	}
}

// TestSessionRoleTemplate: a session's role is only a template: its employee does not take the
// role's nodes (node_ids) nor count as one of its staff; auto placement may use any of the user's
// nodes.
func TestSessionRoleTemplate(t *testing.T) {
	r := orchestrator.SessionRole(store.Role{ID: "r1", Tier: "member", Role: "dev", Count: 3, NodeIDs: []string{"n-dev"}}, "", "", "")
	if r.ID != "" || r.Tier != "lead" || !r.Solo || r.Count != 1 || r.NodeIDs == nil || len(r.NodeIDs) != 0 {
		t.Fatalf("auto: %+v", r)
	}
	if r := orchestrator.SessionRole(store.Role{Role: "dev", NodeIDs: []string{"n-dev"}}, "", "", "n-2"); !slices.Equal(r.NodeIDs, []string{"n-2"}) {
		t.Fatalf("picked node: %v", r.NodeIDs)
	}
}
