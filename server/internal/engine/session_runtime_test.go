package engine_test

import (
	"strings"
	"testing"

	protocol "github.com/touwaeriol/agent-teams/protocol"

	"github.com/touwaeriol/agent-teams/server/internal/nodegw/nodetest"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// TestSessionRuntimePick (#53 t46): the new-session page picks the agent (runtime) among the
// role's runtimes. The session keeps to it with the role's model for it (or the picked model),
// session-options follow it, and a runtime the role does not have is refused (422).
func TestSessionRuntimePick(t *testing.T) {
	_, ghSrv := newFakeGitHub(t)
	w := newWorld(t, ghSrv)
	b := w.signUp("runtime-pick@example.com")
	var node nodeCreated
	b.must(201, "POST", "/api/nodes", map[string]string{"name": "fake"}, &node)
	b.must(200, "GET", "/api/roles", nil, nil)
	var conn struct{ ID string }
	b.must(201, "POST", "/api/git/connections", map[string]string{"name": "gh", "token": "ghp_good"}, &conn)
	n := w.connect(node.Token, nodetest.Options{Isolation: "agent_user", Features: statusFeatures})
	// The dev role runs on Claude Code and Codex (the model of Codex in models).
	if _, err := store.Exec(w.ctx, w.db.Pool, `UPDATE at_roles SET runtime = 'claude', runtimes = '{claude,codex}', models = '{"codex":"gpt-5.5-codex"}'::jsonb
		WHERE role = 'dev' AND tier = 'member'`); err != nil {
		t.Fatal(err)
	}

	var opts struct {
		Runtime string
		Model   string
		Modes   []struct{ ID string }
	}
	b.must(200, "GET", "/api/session-options?role=dev&runtime=codex", nil, &opts)
	if opts.Runtime != "codex" || opts.Model != "gpt-5.5-codex" {
		t.Fatalf("codex options %+v", opts)
	}
	b.must(200, "GET", "/api/session-options?role=dev", nil, &opts)
	if opts.Runtime != "claude" {
		t.Fatalf("default runtime is the role's first: %+v", opts)
	}
	b.must(422, "GET", "/api/session-options?role=dev&runtime=antigravity", nil, nil)

	repos := []map[string]any{{"repo": "o/r", "git_connection_id": conn.ID}}
	b.must(422, "POST", "/api/sessions", map[string]any{"message": "x", "role": "dev", "runtime": "antigravity", "repos": repos}, nil)
	b.must(422, "POST", "/api/sessions", map[string]any{"message": "x", "role": "dev", "runtime": "gemini", "repos": repos}, nil)

	created := func(body map[string]any) (runtime, model string, runtimes []string) {
		t.Helper()
		var s struct{ Project struct{ ID string } }
		body["message"], body["role"], body["repos"] = "改一下", "dev", repos
		b.must(201, "POST", "/api/sessions", body, &s)
		var row struct {
			Runtime  string
			Model    string
			Runtimes string
		}
		if err := w.db.Pool.Gorm(w.ctx).Raw(`SELECT runtime, COALESCE(spec->>'model', '') AS model, COALESCE(spec->>'runtimes', '') AS runtimes
			FROM at_employees WHERE project_id = ?`, s.Project.ID).Scan(&row).Error; err != nil {
			t.Fatal(err)
		}
		if row.Runtimes != "" && row.Runtimes != "null" {
			runtimes = []string{row.Runtimes}
		}
		return row.Runtime, row.Model, runtimes
	}
	if rt, model, list := created(map[string]any{"runtime": "codex"}); rt != "codex" || model != "gpt-5.5-codex" || list != nil {
		t.Fatalf("picked codex: %q %q %v", rt, model, list)
	}
	if rt, model, _ := created(map[string]any{"runtime": "codex", "model": "gpt-5.4"}); rt != "codex" || model != "gpt-5.4" {
		t.Fatalf("picked codex and a model: %q %q", rt, model)
	}
	if rt, _, _ := created(map[string]any{"runtime": "claude"}); rt != "claude" {
		t.Fatalf("picked claude: %q", rt)
	}

	// A mode the picked agent does not have (Codex has no plan / acceptEdits): 422.
	b.must(422, "POST", "/api/sessions", map[string]any{"message": "x", "role": "dev", "runtime": "codex", "permission_mode": "plan", "repos": repos}, nil)
	b.must(422, "POST", "/api/sessions", map[string]any{"message": "x", "role": "dev", "runtime": "codex", "permission_mode": "acceptEdits", "repos": repos}, nil)

	// A picked node that cannot run the picked agent (its report: Codex not logged in): 422 on node_id.
	ctx, cancel := w.waitCtx()
	defer cancel()
	if err := n.SendUnreliable(ctx, protocol.TypeNodeRuntimes, cliReport("hello", map[string]string{"claude": "subscription", "codex": "none"})); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the node's CLI report", func() bool {
		var c int64
		w.db.Pool.Gorm(w.ctx).Table("at_node_runtimes").Where("node_id = ? AND runtime = 'codex'", node.Node.ID).Count(&c)
		return c == 1
	})
	code, raw := b.do("POST", "/api/sessions", map[string]any{"message": "x", "role": "dev", "runtime": "codex", "node_id": node.Node.ID, "repos": repos})
	body := string(raw)
	if code != 422 || !strings.Contains(body, "node_id") || !strings.Contains(body, "没有登录") {
		t.Fatalf("node without the agent's login: %d %s", code, body)
	}
	created(map[string]any{"runtime": "claude", "node_id": node.Node.ID})
}
