package engine_test

import (
	"encoding/json"
	"strings"
	"testing"

	protocol "github.com/touwaeriol/agent-teams/protocol"

	"github.com/touwaeriol/agent-teams/server/internal/nodegw/nodetest"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

type controlsView struct {
	ThreadID       string `json:"thread_id"`
	Runtime        string
	Live           bool
	PermissionMode string `json:"permission_mode"`
	Model          string
	Effort         string
	Pending        *struct {
		PermissionMode string `json:"permission_mode"`
		Model, Effort  string
	}
	Error  string
	Modes  []struct{ ID, Label, Applies string }
	Models []struct {
		ID           string
		Efforts      []string
		SupportsAuto bool `json:"supports_auto"`
	}
	Efforts      []string
	NodeOutdated bool `json:"node_outdated"`
}

func (c controlsView) modeIDs() string {
	ids := []string{}
	for _, m := range c.Modes {
		ids = append(ids, m.ID)
	}
	return strings.Join(ids, ",")
}

// TestSessionControls (#53): the user of a session switches its permission mode, model and
// effort. The session opens for auto; GET lists what the session's models allow; PUT keeps the
// pick on the employee, sends session.set_mode with a request_id and shows the switch pending
// until session.mode_set; a failed switch is shown and the picks go back. Approvals of the
// session go to the user directly and never time out. Other projects have no controls.
func TestSessionControls(t *testing.T) {
	_, ghSrv := newFakeGitHub(t)
	w := newWorld(t, ghSrv)
	b := w.signUp("controls@example.com")
	var node nodeCreated
	b.must(201, "POST", "/api/nodes", map[string]string{"name": "fake"}, &node)
	b.must(200, "GET", "/api/roles", nil, nil)
	var conn struct{ ID string }
	b.must(201, "POST", "/api/git/connections", map[string]string{"name": "gh", "token": "ghp_good"}, &conn)
	n := w.connect(node.Token, nodetest.Options{Features: []string{"sessions", "git_worktree", "cred_helper", "file_download", "session_controls"}})

	var created struct{ Project struct{ ID string } }
	b.must(201, "POST", "/api/sessions", map[string]any{"message": "改一下登录页", "role": "dev", "node_id": node.Node.ID,
		"repos": []map[string]any{{"repo": "o/r", "git_connection_id": conn.ID}}}, &created)
	pid := created.Project.ID
	base := "/api/projects/" + pid + "/session-controls"

	ctx, cancel := w.waitCtx()
	f, err := n.Wait(ctx, protocol.TypeSessionOpen, func(f nodetest.Frame) bool { return strings.Contains(string(f.Body), `"name":"lead-1"`) })
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	var open protocol.SessionOpen
	_ = f.Decode(&open)
	if open.Claude.PermissionMode != "default" || open.Claude.PreferAuto == nil || !*open.Claude.PreferAuto {
		t.Fatalf("a Claude session opens for auto: %+v", open.Claude)
	}
	a := &fakeAgent{t: t, w: w, node: n, session: string(open.SessionID), gen: int(open.OpenGen)}
	tools := []string{}
	for _, p := range open.PlatformTools {
		tools = append(tools, "mcp__platform__"+p.Name)
	}
	tools = append(tools, open.Claude.Tools...)
	yes, auto, opus := true, "auto", "claude-opus-5-5"
	sdk := "sdk-1"
	a.send(protocol.TypeSessionOpened, protocol.SessionOpened{SessionID: open.SessionID, OpenGen: open.OpenGen, SDKSessionID: &sdk,
		Init: &protocol.SessionInit{Tools: tools, PermissionMode: &auto, Model: &opus, Models: []protocol.ModelOption{
			{ID: "claude-opus-5-5", Efforts: []string{"low", "medium", "high", "max"}, SupportsAuto: &yes, Default: &yes},
			{ID: "claude-haiku-4-5", Efforts: []string{}}}}})
	ctx, cancel = w.waitCtx()
	if _, err := n.Wait(ctx, protocol.TypeSessionRelease, a.mine); err != nil {
		t.Fatal(err)
	}
	cancel()

	var c controlsView
	b.must(200, "GET", base, nil, &c)
	if !c.Live || c.Runtime != "claude" || c.PermissionMode != "auto" || c.Model != "claude-opus-5-5" || c.Pending != nil ||
		c.modeIDs() != "auto,default,acceptEdits,plan,bypassPermissions" || len(c.Models) != 2 || strings.Join(c.Efforts, ",") != "low,medium,high,max" {
		t.Fatalf("controls after open %+v", c)
	}
	if c.Modes[0].Label != "自动" || c.Modes[0].Applies != "now" {
		t.Fatalf("mode labels %+v", c.Modes)
	}

	b.must(422, "PUT", base, map[string]any{"effort": "ultra"}, nil)
	b.must(422, "PUT", base, map[string]any{"model": "gpt-5"}, nil)
	b.must(422, "PUT", base, map[string]any{"model": "claude-haiku-4-5"}, nil) // auto is in force, haiku has none
	b.must(200, "PUT", base, map[string]any{"permission_mode": "plan", "effort": "low"}, &c)
	if c.Pending == nil || c.Pending.PermissionMode != "plan" || c.Pending.Effort != "low" || c.PermissionMode != "auto" {
		t.Fatalf("switch pending %+v", c)
	}
	b.must(409, "PUT", base, map[string]any{"permission_mode": "default"}, nil)
	set := expectSetModeBody(t, a)
	if set.RequestID == nil || set.PermissionMode == nil || *set.PermissionMode != "plan" || set.Effort == nil || *set.Effort != "low" || set.Model != nil {
		t.Fatalf("set_mode %+v", set)
	}
	modeSet(a, *set.RequestID, true, "plan", "claude-opus-5-5", "low", "")
	eventually(t, "switch applied", func() bool {
		c = controlsView{}
		b.must(200, "GET", base, nil, &c)
		return c.Pending == nil && c.PermissionMode == "plan" && c.Effort == "low"
	})
	var pick struct{ SessionMode, SessionEffort, Model string }
	if err := w.db.Pool.Gorm(w.ctx).Table("at_employees").Select("session_mode, session_effort, model").
		Where("project_id = ?", pid).Scan(&pick).Error; err != nil {
		t.Fatal(err)
	}
	if pick.SessionMode != "plan" || pick.SessionEffort != "low" {
		t.Fatalf("picks kept on the employee: %+v", pick)
	}

	b.must(200, "PUT", base, map[string]any{"model": "claude-haiku-4-5"}, &c)
	set = expectSetModeBody(t, a)
	if set.Model == nil || *set.Model != "claude-haiku-4-5" {
		t.Fatalf("model switch %+v", set)
	}
	modeSet(a, *set.RequestID, false, "plan", "claude-opus-5-5", "low", "model not available")
	eventually(t, "failure shown", func() bool {
		c = controlsView{}
		b.must(200, "GET", base, nil, &c)
		return c.Pending == nil && c.Error == "model not available" && c.Model == "claude-opus-5-5"
	})
	if err := w.db.Pool.Gorm(w.ctx).Table("at_employees").Select("session_mode, session_effort, model").Where("project_id = ?", pid).Scan(&pick).Error; err != nil ||
		pick.Model != "claude-opus-5-5" || pick.SessionEffort != "low" || pick.SessionMode != "plan" {
		t.Fatalf("the picks the failed switch changed go back (model, the dropped effort), the rest stays: %+v %v", pick, err)
	}
	b.must(200, "PUT", base, map[string]any{"model": "claude-haiku-4-5"}, &c)
	set = expectSetModeBody(t, a)
	modeSet(a, *set.RequestID, true, "plan", "claude-haiku-4-5", "low", "")
	eventually(t, "haiku in force", func() bool {
		c = controlsView{}
		b.must(200, "GET", base, nil, &c)
		return c.Pending == nil && c.Model == "claude-haiku-4-5" && c.Error == ""
	})
	if c.modeIDs() != "default,acceptEdits,plan,bypassPermissions" || len(c.Efforts) != 0 || c.Effort != "" {
		t.Fatalf("a model without auto hides it, and the effort it does not take: %+v", c)
	}
	if err := w.db.Pool.Gorm(w.ctx).Table("at_employees").Select("session_mode, session_effort, model").
		Where("project_id = ?", pid).Scan(&pick).Error; err != nil || pick.SessionEffort != "" || pick.Model != "claude-haiku-4-5" {
		t.Fatalf("the effort pick is dropped with a model that lacks it: %+v %v", pick, err)
	}
	var events int64
	if err := w.db.Pool.Gorm(w.ctx).Table("at_events").Where("project_id = ? AND type = 'session_controls'", pid).Count(&events).Error; err != nil || events < 6 {
		t.Fatalf("session_controls events %d %v", events, err)
	}

	// Approvals: straight to the user (no lead review), and no timeout in a session.
	if _, err := store.Exec(w.ctx, w.db.Pool, `UPDATE at_projects SET delivery = coalesce(delivery, '{}'::jsonb) || '{"approval_timeout_s": 60}' WHERE id = $1`, pid); err != nil {
		t.Fatal(err)
	}
	askApproval(a, "ap_solo", "make deploy")
	var audience string
	eventually(t, "the card", func() bool {
		_ = w.db.Pool.Gorm(w.ctx).Table("at_questions").Select("audience").Where("approval_id = 'ap_solo'").Scan(&audience).Error
		return audience != ""
	})
	if audience != "user" {
		t.Fatalf("a session's approval goes to the user: %q", audience)
	}
	if _, err := store.Exec(w.ctx, w.db.Pool, `UPDATE at_questions SET created_at = now() - interval '2 hours' WHERE approval_id = 'ap_solo'`); err != nil {
		t.Fatal(err)
	}
	w.e.Approvals.SweepTimeouts(w.ctx)
	var status string
	if err := w.db.Pool.Gorm(w.ctx).Table("at_questions").Select("status").Where("approval_id = 'ap_solo'").Scan(&status).Error; err != nil || status != "open" {
		t.Fatalf("a session's approval never times out: %q %v", status, err)
	}

	var proj struct{ ID string }
	b.must(201, "POST", "/api/projects", map[string]any{"name": "plain"}, &proj)
	b.must(409, "GET", "/api/projects/"+proj.ID+"/session-controls", nil, nil)
	b.must(404, "GET", "/api/projects/nope/session-controls", nil, nil)
	other := w.signUp("other-controls@example.com")
	other.must(404, "GET", base, nil, nil)
}

func expectSetModeBody(t *testing.T, a *fakeAgent) protocol.SessionSetMode {
	t.Helper()
	ctx, cancel := a.w.waitCtx()
	defer cancel()
	f, err := a.node.Wait(ctx, protocol.TypeSessionSetMode, a.mine)
	if err != nil {
		t.Fatal(err)
	}
	var m protocol.SessionSetMode
	_ = json.Unmarshal(f.Body, &m)
	return m
}

func modeSet(a *fakeAgent, requestID string, ok bool, mode, model, effort, errText string) {
	m := protocol.SessionModeSet{SessionID: protocol.SessionID(a.session), OpenGen: protocol.OpenGen(a.gen), RequestID: &requestID, Ok: ok,
		PermissionMode: &mode, Model: &model, Effort: &effort}
	if errText != "" {
		m.Error = &errText
	}
	a.send(protocol.TypeSessionModeSet, m)
}

// TestSessionControlsOldDaemon: on a daemon without session_controls a session opens in its
// role's mode as before (no prefer_auto), the menu keeps the old set_mode values, there is no
// effort, and a switch goes out without the new fields and counts at once (no answer comes).
func TestSessionControlsOldDaemon(t *testing.T) {
	_, ghSrv := newFakeGitHub(t)
	w := newWorld(t, ghSrv)
	b := w.signUp("controls-old@example.com")
	var node nodeCreated
	b.must(201, "POST", "/api/nodes", map[string]string{"name": "old"}, &node)
	b.must(200, "GET", "/api/roles", nil, nil)
	var conn struct{ ID string }
	b.must(201, "POST", "/api/git/connections", map[string]string{"name": "gh", "token": "ghp_good"}, &conn)
	n := w.connect(node.Token, nodetest.Options{})
	var created struct{ Project struct{ ID string } }
	b.must(201, "POST", "/api/sessions", map[string]any{"message": "看看日志", "role": "dev", "node_id": node.Node.ID,
		"repos": []map[string]any{{"repo": "o/r", "git_connection_id": conn.ID}}}, &created)
	base := "/api/projects/" + created.Project.ID + "/session-controls"
	a, open := expectOpen(t, w, n, "lead-1")
	if open.Claude.PermissionMode != "bypassPermissions" || open.Claude.PreferAuto != nil {
		t.Fatalf("an old daemon gets the role's mode: %+v", open.Claude)
	}
	var c controlsView
	b.must(200, "GET", base, nil, &c)
	if c.PermissionMode != "bypassPermissions" || c.modeIDs() != "default,acceptEdits,bypassPermissions" || len(c.Efforts) != 0 || !c.NodeOutdated {
		t.Fatalf("old daemon controls %+v", c)
	}
	b.must(422, "PUT", base, map[string]any{"effort": "low"}, nil)
	b.must(422, "PUT", base, map[string]any{"permission_mode": "plan"}, nil)
	b.must(422, "PUT", base, map[string]any{"permission_mode": "plan", "model": "claude-sonnet-5-5"}, nil)
	c = controlsView{}
	b.must(200, "PUT", base, map[string]any{"permission_mode": "default", "model": "claude-sonnet-5-5"}, &c)
	if c.Pending != nil || c.PermissionMode != "default" || c.Model != "claude-sonnet-5-5" {
		t.Fatalf("in force at once on an old daemon: %+v", c)
	}
	set := expectSetModeBody(t, a)
	if set.RequestID != nil || set.Effort != nil || set.PermissionMode == nil || *set.PermissionMode != "default" || set.Model == nil {
		t.Fatalf("old daemon set_mode %+v", set)
	}
}
