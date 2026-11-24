package engine_test

import (
	"strings"
	"testing"
	"time"

	protocol "github.com/touwaeriol/agent-teams/protocol"

	"github.com/touwaeriol/agent-teams/server/internal/nodegw/nodetest"
)

// TestSessionContextDetail (protocol.md §4.10 上下文细分): a session's Claude employee reports its
// context breakdown after a turn (session.context); the console stores it on the session, tells the
// web with the project event context, and serves it on GET …/context/detail. POST …/context/refresh
// asks the node (session.context_refresh) and the answer comes back the same way.
func TestSessionContextDetail(t *testing.T) {
	_, ghSrv := newFakeGitHub(t)
	w := newWorld(t, ghSrv)
	b := w.signUp("ctx@example.com")
	var node nodeCreated
	b.must(201, "POST", "/api/nodes", map[string]string{"name": "fake"}, &node)
	b.must(200, "GET", "/api/roles", nil, nil)
	var conn struct{ ID string }
	b.must(201, "POST", "/api/git/connections", map[string]string{"name": "gh", "token": "ghp_good"}, &conn)
	n := w.connect(node.Token, nodetest.Options{Features: []string{"sessions", "git_worktree", "cred_helper", "file_download", "context_detail"}})

	var created struct {
		Project  struct{ ID string }
		ThreadNo int `json:"thread_no"`
	}
	b.must(201, "POST", "/api/sessions", map[string]any{"message": "看看上下文", "role": "dev", "node_id": node.Node.ID,
		"repos": []map[string]any{{"repo": "o/r", "git_connection_id": conn.ID}}}, &created)
	pid := created.Project.ID
	emp, open := expectOpen(t, w, n, "lead-1")
	prepares(t, w, n, 1)
	emp.send(protocol.TypeSessionDelivered, protocol.SessionDelivered{SessionID: protocol.SessionID(emp.session), MsgID: open.FirstMessage.MsgID})

	var threads []struct{ ID string }
	b.must(200, "GET", "/api/projects/"+pid+"/threads", nil, &threads)
	if len(threads) != 1 {
		t.Fatalf("threads %+v", threads)
	}
	cid := threads[0].ID
	path := "/api/conversations/" + cid + "/context/detail"

	type view struct {
		Supported   bool    `json:"supported"`
		Runtime     string  `json:"runtime"`
		SessionID   string  `json:"session_id"`
		CanRefresh  bool    `json:"can_refresh"`
		Detail      string  `json:"detail"`
		UpdatedAt   *string `json:"updated_at"`
		Model       string  `json:"model"`
		TotalTokens int     `json:"total_tokens"`
		MaxTokens   int     `json:"max_tokens"`
		AutoCompact bool    `json:"auto_compact"`
		Categories  []struct {
			ID, Name, Kind string
			Tokens         int
		}
		McpTools []struct {
			Name, Server string
			Tokens       int
			Loaded       bool
		} `json:"mcp_tools"`
		MemoryFiles []struct{ Path string } `json:"memory_files"`
	}
	var before view
	b.must(200, "GET", path, nil, &before)
	if !before.Supported || before.Runtime != "claude" || before.SessionID != emp.session || !before.CanRefresh ||
		before.Detail != "" || before.UpdatedAt != nil || len(before.Categories) != 0 {
		t.Fatalf("before any report %+v", before)
	}

	events := b.stream("/api/projects/"+pid+"/events?topics=context", "")
	model, raw, yes, no := "claude-opus-5-5", 1000000, true, false
	catID := "mcp_tools"
	sctx, cancel := w.waitCtx()
	err := n.SendUnreliable(sctx, protocol.TypeSessionContext, protocol.SessionContext{SessionID: protocol.SessionID(emp.session), OpenGen: protocol.OpenGen(emp.gen),
		Reason: protocol.SessionContextReasonTurn, Context: &protocol.ContextDetail{Detail: protocol.ContextDetailDetailSummary, Model: &model,
			TotalTokens: 77400, MaxTokens: 1000000, RawMaxTokens: &raw, AutoCompact: &yes,
			Categories: []protocol.ContextCategory{{ID: &catID, Name: "MCP tools", Tokens: 9600, Kind: protocol.ContextCategoryKindUsed},
				{Name: "Free space", Tokens: 889600, Kind: protocol.ContextCategoryKindFree}},
			McpTools: []protocol.ContextMcpTool{{Name: "mcp__platform__ask_user", Server: "platform", Tokens: 1240},
				{Name: "mcp__github__search_code", Server: "github", Tokens: 500, Loaded: &no}},
			MemoryFiles: []protocol.ContextMemoryFile{{Path: "/w/CLAUDE.md", Tokens: 2600}}}})
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	events.wait(t, "context", `"session_id":"`+emp.session+`"`, `"reason":"turn"`, `"thread_no":1`, `"conversation_id":"`+cid+`"`)

	var got view
	b.must(200, "GET", path, nil, &got)
	if got.Detail != "summary" || got.UpdatedAt == nil || got.Model != model || got.TotalTokens != 77400 || got.MaxTokens != 1000000 || !got.AutoCompact ||
		len(got.Categories) != 2 || got.Categories[0].ID != "mcp_tools" || got.Categories[1].ID != "" || got.Categories[1].Kind != "free" ||
		len(got.McpTools) != 2 || !got.McpTools[0].Loaded || got.McpTools[1].Loaded || len(got.MemoryFiles) != 1 {
		t.Fatalf("stored breakdown %+v", got)
	}
	var used, max int
	if err := w.db.Pool.Gorm(w.ctx).Table("at_agent_sessions").Where("id = ?", emp.session).Select("context_used, context_max").Row().Scan(&used, &max); err != nil || used != 77400 || max != 1000000 {
		t.Fatalf("totals %d/%d %v", used, max, err)
	}

	var started struct {
		RefreshID string `json:"refresh_id"`
		SessionID string `json:"session_id"`
	}
	b.must(202, "POST", "/api/conversations/"+cid+"/context/refresh", nil, &started)
	if !strings.HasPrefix(started.RefreshID, "cr_") || started.SessionID != emp.session {
		t.Fatalf("refresh %+v", started)
	}
	wctx, wcancel := w.waitCtx()
	f, err := n.Wait(wctx, protocol.TypeSessionContextRefresh, nil)
	wcancel()
	if err != nil {
		t.Fatal(err)
	}
	var req protocol.SessionContextRefresh
	_ = f.Decode(&req)
	if req.RefreshID != started.RefreshID || string(req.SessionID) != emp.session || int(req.OpenGen) != emp.gen {
		t.Fatalf("session.context_refresh %+v", req)
	}
	msg := "the session has no live process"
	sctx, cancel = w.waitCtx()
	err = n.SendUnreliable(sctx, protocol.TypeSessionContext, protocol.SessionContext{SessionID: req.SessionID, OpenGen: req.OpenGen,
		Reason: protocol.SessionContextReasonRefresh, RefreshID: &req.RefreshID, Error: &msg})
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	events.wait(t, "context", `"refresh_id":"`+req.RefreshID+`"`, `"error":"`+msg+`"`)
	var kept view
	b.must(200, "GET", path, nil, &kept)
	if kept.Detail != "summary" || kept.TotalTokens != 77400 {
		t.Fatalf("a failed refresh keeps the last breakdown %+v", kept)
	}

	// A slow full refresh sampled before a later turn's summary does not overwrite it.
	later := time.Now().UTC().Add(time.Second).Truncate(time.Second) // after the first report (stored at arrival)
	earlier := later.Add(-time.Minute)
	report := func(reason protocol.SessionContextReason, refreshID *string, sampled time.Time, total int, detail protocol.ContextDetailDetail) {
		t.Helper()
		sctx, cancel := w.waitCtx()
		defer cancel()
		if err := n.SendUnreliable(sctx, protocol.TypeSessionContext, protocol.SessionContext{SessionID: protocol.SessionID(emp.session), OpenGen: protocol.OpenGen(emp.gen),
			Reason: reason, RefreshID: refreshID, Context: &protocol.ContextDetail{Detail: detail, SampledAt: &sampled, TotalTokens: total, MaxTokens: 1000000,
				Categories: []protocol.ContextCategory{}}}); err != nil {
			t.Fatal(err)
		}
	}
	report(protocol.SessionContextReasonTurn, nil, later, 90000, protocol.ContextDetailDetailSummary)
	events.wait(t, "context", `"reason":"turn"`, `"updated_at":"`+later.Format(time.RFC3339)+`"`)
	slow := "cr_slow"
	report(protocol.SessionContextReasonRefresh, &slow, earlier, 80000, protocol.ContextDetailDetailFull)
	stale := events.wait(t, "context", `"refresh_id":"cr_slow"`)
	if strings.Contains(stale.Data, "updated_at") {
		t.Fatalf("a stale refresh is answered without updated_at: %s", stale.Data)
	}
	var newest view
	b.must(200, "GET", path, nil, &newest)
	if newest.Detail != "summary" || newest.TotalTokens != 90000 {
		t.Fatalf("the newer turn breakdown stays %+v", newest)
	}
	if err := w.db.Pool.Gorm(w.ctx).Table("at_agent_sessions").Where("id = ?", emp.session).Select("context_used").Row().Scan(&used); err != nil || used != 90000 {
		t.Fatalf("totals of a stale refresh are not stored: %d %v", used, err)
	}

	// A Codex session has no breakdown: hidden, and nothing to refresh.
	if err := w.db.Pool.Gorm(w.ctx).Exec("UPDATE at_agent_sessions SET runtime = 'codex' WHERE id = ?", emp.session).Error; err != nil {
		t.Fatal(err)
	}
	var codex view
	b.must(200, "GET", path, nil, &codex)
	if codex.Supported || codex.CanRefresh || codex.Runtime != "codex" || len(codex.Categories) != 0 {
		t.Fatalf("codex %+v", codex)
	}
	if code, body := b.do("POST", "/api/conversations/"+cid+"/context/refresh", nil); code != 409 || !strings.Contains(string(body), "unsupported") {
		t.Fatalf("codex refresh %d %s", code, body)
	}
	if err := w.db.Pool.Gorm(w.ctx).Exec("UPDATE at_agent_sessions SET runtime = 'claude' WHERE id = ?", emp.session).Error; err != nil {
		t.Fatal(err)
	}

	// A parked session is not started for a refresh.
	gen := protocol.OpenGen(emp.gen)
	emp.send(protocol.TypeSessionState, protocol.SessionState{SessionID: protocol.SessionID(emp.session), OpenGen: &gen, State: protocol.SessionStateStateParked})
	eventually(t, "session parked", func() bool {
		var st string
		_ = w.db.Pool.Gorm(w.ctx).Table("at_agent_sessions").Where("id = ?", emp.session).Pluck("state", &st).Error
		return st == "parked"
	})
	var parked view
	b.must(200, "GET", path, nil, &parked)
	if !parked.Supported || parked.CanRefresh || parked.TotalTokens != 90000 {
		t.Fatalf("parked %+v", parked)
	}
	if code, body := b.do("POST", "/api/conversations/"+cid+"/context/refresh", nil); code != 409 || !strings.Contains(string(body), "session_not_running") {
		t.Fatalf("parked refresh %d %s", code, body)
	}

	b.must(404, "GET", "/api/conversations/nope/context/detail", nil, nil)
}

// TestSessionContextOldDaemon: a daemon without context_detail cannot refresh (409 node_unsupported)
// and the breakdown is only what is stored (none).
func TestSessionContextOldDaemon(t *testing.T) {
	_, ghSrv := newFakeGitHub(t)
	w := newWorld(t, ghSrv)
	b := w.signUp("ctx-old@example.com")
	var node nodeCreated
	b.must(201, "POST", "/api/nodes", map[string]string{"name": "fake"}, &node)
	b.must(200, "GET", "/api/roles", nil, nil)
	var conn struct{ ID string }
	b.must(201, "POST", "/api/git/connections", map[string]string{"name": "gh", "token": "ghp_good"}, &conn)
	n := w.connect(node.Token, nodetest.Options{})
	var created struct{ Project struct{ ID string } }
	b.must(201, "POST", "/api/sessions", map[string]any{"message": "x", "role": "dev", "node_id": node.Node.ID,
		"repos": []map[string]any{{"repo": "o/r", "git_connection_id": conn.ID}}}, &created)
	expectOpen(t, w, n, "lead-1")
	var threads []struct{ ID string }
	b.must(200, "GET", "/api/projects/"+created.Project.ID+"/threads", nil, &threads)
	var v struct {
		Supported  bool `json:"supported"`
		CanRefresh bool `json:"can_refresh"`
	}
	b.must(200, "GET", "/api/conversations/"+threads[0].ID+"/context/detail", nil, &v)
	if !v.Supported || v.CanRefresh {
		t.Fatalf("old daemon %+v", v)
	}
	b.must(409, "POST", "/api/conversations/"+threads[0].ID+"/context/refresh", nil, nil)

	// A team thread's conversation has no context breakdown (sessions only).
	var project struct{ ID string }
	b.must(201, "POST", "/api/projects", map[string]any{"name": "team", "repos": []map[string]any{{"repo": "o/r", "git_connection_id": conn.ID}}}, &project)
	var thread struct{ ID string }
	b.must(201, "POST", "/api/projects/"+project.ID+"/threads", map[string]any{"title": "Login", "brief": "登录页"}, &thread)
	b.must(404, "GET", "/api/conversations/"+thread.ID+"/context/detail", nil, nil)
	b.must(404, "POST", "/api/conversations/"+thread.ID+"/context/refresh", nil, nil)
}
