package engine_test

import (
	"strings"
	"testing"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw/nodetest"
)

// TestPostMessageSlowGitHub: the first message to an employee without a session opens one, which
// reads each repository's default branch and CLAUDE.md from GitHub. Those reads happen before the
// delivery's transaction, never while it holds the project row: with GitHub at 1 s a request, the
// rest of the project (other deliveries, events) is not held up, and the message is stored,
// streamed and delivered once the reads are back.
func TestPostMessageSlowGitHub(t *testing.T) {
	gh, ghSrv := newFakeGitHub(t)
	w := newWorld(t, ghSrv)
	w.signUp("admin-placeholder@example.com")
	b := w.signUp("slow-gh@example.com")
	var node nodeCreated
	b.must(201, "POST", "/api/nodes", map[string]string{"name": "fake"}, &node)
	n := w.connect(node.Token, nodetest.Options{})
	var conn struct{ ID string }
	b.must(201, "POST", "/api/git/connections", map[string]string{"name": "gh", "token": "ghp_good"}, &conn)
	var project struct{ ID string }
	b.must(201, "POST", "/api/projects", map[string]any{"name": "slow",
		"repos": []map[string]any{{"repo": "o/slow", "git_connection_id": conn.ID}}}, &project)
	var coord struct {
		ConversationID string `json:"conversation_id"`
	}
	b.must(200, "GET", "/api/projects/"+project.ID+"/coordinator", nil, &coord)
	events := b.stream("/api/projects/"+project.ID+"/events?topics=message", "")

	gh.mu.Lock()
	gh.latency, gh.files["o/slow:CLAUDE.md"] = time.Second, "# slow repo rules"
	seen := gh.requests
	gh.mu.Unlock()
	posted := make(chan time.Duration, 1)
	go func() {
		start := time.Now()
		b.must(202, "POST", "/api/conversations/"+coord.ConversationID+"/messages", map[string]string{"content": "慢仓库你好"}, nil)
		posted <- time.Since(start)
	}()
	eventually(t, "the session open reads GitHub", func() bool {
		gh.mu.Lock()
		defer gh.mu.Unlock()
		return gh.requests > seen
	})
	// Meanwhile the project row is free: a delivery or event elsewhere in the project goes through.
	start := time.Now()
	err := w.e.Core.InTx(w.ctx, func(tx *core.Tx) error { return core.LockProject(w.ctx, tx, project.ID) })
	if took := time.Since(start); err != nil || took > 300*time.Millisecond {
		t.Fatalf("project row waited %v for GitHub (err %v)", took, err)
	}
	took := <-posted
	t.Logf("POST took %v with GitHub at 1 s a request (the reads, outside the transaction)", took)
	events.wait(t, "message", "慢仓库你好")
	_, open := expectOpen(t, w, n, "coordinator")
	if open.FirstMessage == nil || !strings.Contains(open.FirstMessage.Text, "慢仓库你好") {
		t.Fatalf("first message %+v", open.FirstMessage)
	}
	if open.Claude == nil || open.Claude.SystemAppend == nil || !strings.Contains(*open.Claude.SystemAppend, "# slow repo rules") {
		t.Fatal("the session opened without the repository's CLAUDE.md")
	}
}
