package engine_test

import (
	"strings"
	"testing"
	"time"

	protocol "github.com/touwaeriol/agent-teams/protocol"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw/nodetest"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// TestSessionLocalFolder (#53 t48): a session without repositories works in a local folder on
// the node it starts on. It is pinned there (never moved, even with another node online), 已解决
// keeps the folder and the next message reopens it on the same node, repositories can be added
// into the folder and all removed again, and deleting the session purges its directory.
func TestSessionLocalFolder(t *testing.T) {
	_, ghSrv := newFakeGitHub(t)
	w := newWorld(t, ghSrv)
	b := w.signUp("local-folder@example.com")
	var node1 nodeCreated
	b.must(201, "POST", "/api/nodes", map[string]string{"name": "one"}, &node1)
	b.must(200, "GET", "/api/roles", nil, nil)
	var conn struct{ ID string }
	b.must(201, "POST", "/api/git/connections", map[string]string{"name": "gh", "token": "ghp_good"}, &conn)
	n1 := w.connect(node1.Token, nodetest.Options{})

	var created struct {
		Project  struct{ ID string }
		ThreadID string `json:"thread_id"`
	}
	b.must(201, "POST", "/api/sessions", map[string]any{"message": "整理一下这些笔记", "role": "dev", "repos": []map[string]any{}}, &created)
	pid := created.Project.ID
	emp, open := expectOpen(t, w, n1, "lead-1")
	if !strings.HasSuffix(string(open.Cwd), "projects/"+pid+"/threads/1/lead") {
		t.Fatalf("cwd %q", open.Cwd)
	}
	sys := *open.Claude.SystemAppend
	if !strings.Contains(sys, "local folder") || strings.Contains(sys, "git push") || strings.Contains(sys, "gh pr create") {
		t.Fatalf("local session prompt:\n%s", sys)
	}
	emp.send(protocol.TypeSessionDelivered, protocol.SessionDelivered{SessionID: protocol.SessionID(emp.session), MsgID: open.FirstMessage.MsgID})

	spec := func() store.Role {
		var e core.Employee
		if err := w.db.Pool.Gorm(w.ctx).Table("at_employees").Where("project_id = ? AND tier = 'lead'", pid).Take(&e).Error; err != nil {
			t.Fatal(err)
		}
		return e.Spec
	}
	if s := spec(); !s.Local || len(s.NodeIDs) != 1 || s.NodeIDs[0] != node1.Node.ID {
		t.Fatalf("pinned to the first node: local=%v nodes=%v", s.Local, s.NodeIDs)
	}
	// Never moved on a usage limit: MoveOnLimit refuses a local session.
	err := w.e.Core.InTx(w.ctx, func(tx *core.Tx) error {
		sc, err := core.SessionContextOf(w.ctx, tx, emp.session)
		if err != nil {
			return err
		}
		moved, err := w.e.Orch.MoveOnLimit(w.ctx, tx, sc, time.Now().Add(time.Hour))
		if moved {
			t.Errorf("a local session moved")
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	// 已解决: the session closes, the folder is not cleaned up.
	b.must(200, "POST", "/api/threads/"+created.ThreadID+"/complete", nil, nil)
	ctx, cancel := w.waitCtx()
	if _, err := n1.Wait(ctx, protocol.TypeSessionClose, emp.mine); err != nil {
		t.Fatal(err)
	}
	cancel()
	emp.send(protocol.TypeSessionClosed, protocol.SessionClosed{SessionID: protocol.SessionID(emp.session), Reason: ptr("thread_ended")})
	var cleanups int64
	w.db.Pool.Gorm(w.ctx).Table("at_node_commands").Where("type = ? AND body::text LIKE ?", protocol.TypeGitCleanup, "%"+pid+"%").Count(&cleanups)
	if cleanups != 0 {
		t.Fatalf("已解决 cleaned the local folder (%d git.cleanup)", cleanups)
	}

	// Another node comes online; the next message reopens the session on the first one.
	var node2 nodeCreated
	b.must(201, "POST", "/api/nodes", map[string]string{"name": "two"}, &node2)
	w.connect(node2.Token, nodetest.Options{})
	b.must(202, "POST", "/api/conversations/"+created.ThreadID+"/messages", map[string]any{"content": "再补一段"}, nil)
	emp2, open2 := expectOpen(t, w, n1, "lead-1")
	if open2.Cwd != open.Cwd {
		t.Fatalf("reopened in another folder: %q", open2.Cwd)
	}
	_ = emp2

	// A repository added mid-way is checked out inside the folder; the session stays pinned.
	b.must(200, "PUT", "/api/sessions/"+pid+"/repos", map[string]any{"repos": []map[string]any{{"repo": "o/r", "git_connection_id": conn.ID}}}, nil)
	added := prepares(t, w, n1, 1)
	if !strings.HasSuffix(string(added["o/r"].Cwd), "/threads/1/lead/o__r") {
		t.Fatalf("added checkout %+v", added["o/r"])
	}
	if s := spec(); !s.Local || len(s.NodeIDs) != 1 || s.NodeIDs[0] != node1.Node.ID {
		t.Fatalf("still pinned after adding a repository: %v %v", s.Local, s.NodeIDs)
	}
	// Removing every repository is allowed.
	b.must(200, "PUT", "/api/sessions/"+pid+"/repos", map[string]any{"repos": []map[string]any{}}, nil)

	// Deleting the session purges its directory on the node.
	b.must(204, "DELETE", "/api/projects/"+pid, nil, nil)
	ctx, cancel = w.waitCtx()
	defer cancel()
	if _, err := n1.Wait(ctx, protocol.TypeGitPurge, func(f nodetest.Frame) bool { return strings.Contains(string(f.Body), `"projects/`+pid+`"`) }); err != nil {
		t.Fatalf("no purge of the session's directory: %v", err)
	}
}
