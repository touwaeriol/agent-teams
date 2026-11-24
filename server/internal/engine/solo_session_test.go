package engine_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	protocol "github.com/touwaeriol/agent-teams/protocol"

	"github.com/touwaeriol/agent-teams/server/internal/nodegw/nodetest"
)

// TestSessionMultiRepo: POST /api/sessions starts a session (a project of kind session): its one
// employee is made from the chosen member role as a writing lead, checks out every repository
// side by side from the branch picked for it, gets only the session tools and the user's first
// message as written, and pushes its own session branch. No coordinator and no members exist.
// Repositories added while it runs are checked out at once and removed ones are cleaned up, and
// the employee is told.
func TestSessionMultiRepo(t *testing.T) {
	_, ghSrv := newFakeGitHub(t)
	w := newWorld(t, ghSrv)
	b := w.signUp("session@example.com")
	var node nodeCreated
	b.must(201, "POST", "/api/nodes", map[string]string{"name": "fake"}, &node)
	b.must(200, "GET", "/api/roles", nil, nil)
	var conn struct{ ID string }
	b.must(201, "POST", "/api/git/connections", map[string]string{"name": "gh", "token": "ghp_good"}, &conn)
	n := w.connect(node.Token, nodetest.Options{})

	b.must(422, "POST", "/api/sessions", map[string]any{"message": "x", "repos": []map[string]any{}}, nil)
	b.must(422, "POST", "/api/sessions", map[string]any{"message": "x", "role": "nope",
		"repos": []map[string]any{{"repo": "o/r", "git_connection_id": conn.ID}}}, nil)

	var created struct {
		Project struct {
			ID    string
			Kind  string
			Name  string
			Repos []struct{ Repo string }
		}
		ThreadNo int `json:"thread_no"`
	}
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
	branch := "session/" + p.ID[:8] + "-"
	if !strings.HasPrefix(preps["o/r"].Branch, branch) || *preps["o/r"].Base != "develop" || *preps["o/web"].Base != "main" ||
		!strings.HasSuffix(string(preps["o/web"].Cwd), "/threads/1/lead/o__web") {
		t.Fatalf("checkouts %+v %+v", preps["o/r"], preps["o/web"])
	}
	if open.Claude == nil || open.Claude.PermissionMode != "bypassPermissions" || !slices.Contains(open.Claude.Tools, "Edit") ||
		open.Claude.ReadOnly != nil {
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

	var views []struct {
		No       int
		Dispatch *struct{} `json:"dispatch"`
	}
	b.must(200, "GET", "/api/projects/"+p.ID+"/threads", nil, &views)
	if len(views) != 1 || views[0].Dispatch != nil {
		t.Fatalf("the first message shows as the user's own message, not a hand-over: %+v", views)
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
}
