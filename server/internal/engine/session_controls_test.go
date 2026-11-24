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
	modeSet(a, *set.RequestID, true, "plan", "claude-haiku-4-5", "", "")
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
	// A switch the node never answers (t33): from "no effort" to high, then the 2 minutes run out.
	// The view does not fall back to the unapplied pick; it says so and the employee's pick goes back.
	b.must(200, "PUT", base, map[string]any{"model": "claude-opus-5-5"}, &c)
	set = expectSetModeBody(t, a)
	modeSet(a, *set.RequestID, true, "plan", "claude-opus-5-5", "", "")
	eventually(t, "opus without effort", func() bool {
		c = controlsView{}
		b.must(200, "GET", base, nil, &c)
		return c.Pending == nil && c.Model == "claude-opus-5-5" && c.Effort == ""
	})
	b.must(200, "PUT", base, map[string]any{"effort": "high"}, &c)
	if c.Pending == nil || c.Pending.Effort != "high" {
		t.Fatalf("effort switch pending %+v", c)
	}
	expectSetModeBody(t, a)
	if _, err := store.Exec(w.ctx, w.db.Pool, `UPDATE at_agent_sessions SET controls = jsonb_set(controls, '{pending,at}', to_jsonb(now() - interval '3 minutes'))
		WHERE employee_id = (SELECT id FROM at_employees WHERE project_id = $1)`, pid); err != nil {
		t.Fatal(err)
	}
	c = controlsView{}
	b.must(200, "GET", base, nil, &c)
	if c.Pending != nil || c.Effort != "" || c.Error != "节点没有回应，切换未生效" {
		t.Fatalf("an unanswered switch shows what still runs and says so: %+v", c)
	}
	if err := w.db.Pool.Gorm(w.ctx).Table("at_employees").Select("session_mode, session_effort, model").
		Where("project_id = ?", pid).Scan(&pick).Error; err != nil || pick.SessionEffort != "" || pick.SessionMode != "plan" || pick.Model != "claude-opus-5-5" {
		t.Fatalf("the unanswered pick goes back on the employee, the rest stays: %+v %v", pick, err)
	}
	b.must(200, "PUT", base, map[string]any{"effort": "medium"}, &c) // not switch_pending any more
	set = expectSetModeBody(t, a)
	modeSet(a, *set.RequestID, true, "plan", "claude-opus-5-5", "medium", "")
	eventually(t, "medium in force", func() bool {
		c = controlsView{}
		b.must(200, "GET", base, nil, &c)
		return c.Pending == nil && c.Effort == "medium" && c.Error == ""
	})
	expire := func() {
		t.Helper()
		if _, err := store.Exec(w.ctx, w.db.Pool, `UPDATE at_agent_sessions SET controls = jsonb_set(controls, '{pending,at}', to_jsonb(now() - interval '3 minutes'))
			WHERE employee_id = (SELECT id FROM at_employees WHERE project_id = $1)`, pid); err != nil {
			t.Fatal(err)
		}
		c = controlsView{}
		b.must(200, "GET", base, nil, &c)
	}
	readPick := func() {
		t.Helper()
		if err := w.db.Pool.Gorm(w.ctx).Table("at_employees").Select("session_mode, session_effort, model").Where("project_id = ?", pid).Scan(&pick).Error; err != nil {
			t.Fatal(err)
		}
	}
	// A permission mode switch that times out: the mode goes back on the view and the employee.
	b.must(200, "PUT", base, map[string]any{"permission_mode": "default"}, &c)
	expectSetModeBody(t, a)
	expire()
	readPick()
	if c.PermissionMode != "plan" || c.Error != "节点没有回应，切换未生效" || pick.SessionMode != "plan" {
		t.Fatalf("unanswered mode switch: %+v %+v", c, pick)
	}
	// opus + 高 → haiku (which drops the effort at once), no answer: model and effort come back.
	b.must(200, "PUT", base, map[string]any{"effort": "high"}, &c)
	set = expectSetModeBody(t, a)
	modeSet(a, *set.RequestID, true, "plan", "claude-opus-5-5", "high", "")
	eventually(t, "high in force", func() bool {
		c = controlsView{}
		b.must(200, "GET", base, nil, &c)
		return c.Pending == nil && c.Effort == "high"
	})
	b.must(200, "PUT", base, map[string]any{"model": "claude-haiku-4-5"}, &c)
	late := expectSetModeBody(t, a)
	expire()
	readPick()
	if c.Pending != nil || c.Model != "claude-opus-5-5" || c.Effort != "high" || c.Error != "节点没有回应，切换未生效" ||
		pick.Model != "claude-opus-5-5" || pick.SessionEffort != "high" || pick.SessionMode != "plan" {
		t.Fatalf("unanswered model switch: model and the dropped effort come back: %+v %+v", c, pick)
	}
	// The node answers after all: the session runs haiku, and the employee's picks follow it.
	modeSet(a, *late.RequestID, true, "plan", "claude-haiku-4-5", "", "")
	eventually(t, "late answer followed", func() bool {
		c = controlsView{}
		b.must(200, "GET", base, nil, &c)
		readPick()
		return c.Model == "claude-haiku-4-5" && c.Error == "" && pick.Model == "claude-haiku-4-5" && pick.SessionEffort == ""
	})
	// A late answer while a newer switch waits (t40 review): haiku → opus times out, then the user
	// picks default mode; the node answers the opus switch late, then the new one. The late answer
	// must not overwrite the newer pick; the matched answer settles the employee on what runs.
	b.must(200, "PUT", base, map[string]any{"model": "claude-opus-5-5"}, &c)
	older := expectSetModeBody(t, a)
	expire()
	if c.Model != "claude-haiku-4-5" || c.Pending != nil {
		t.Fatalf("opus switch timed out: %+v", c)
	}
	b.must(200, "PUT", base, map[string]any{"permission_mode": "default"}, &c)
	newer := expectSetModeBody(t, a)
	modeSet(a, *older.RequestID, true, "plan", "claude-opus-5-5", "medium", "")
	eventually(t, "late answer applied to the session", func() bool {
		c = controlsView{}
		b.must(200, "GET", base, nil, &c)
		return c.Model == "claude-opus-5-5" && c.Pending != nil
	})
	readPick()
	if pick.SessionMode != "default" || pick.Model != "claude-haiku-4-5" || pick.SessionEffort != "" {
		t.Fatalf("a late answer leaves the newer pick alone: %+v", pick)
	}
	modeSet(a, *newer.RequestID, true, "default", "claude-opus-5-5", "medium", "")
	eventually(t, "newer switch applied", func() bool {
		c = controlsView{}
		b.must(200, "GET", base, nil, &c)
		readPick()
		return c.Pending == nil && c.PermissionMode == "default" && c.Model == "claude-opus-5-5" &&
			pick.SessionMode == "default" && pick.Model == "claude-opus-5-5"
	})
	if pick.SessionEffort != "" {
		t.Fatalf("a mode switch leaves an effort that follows the default alone: %+v", pick)
	}
	// The effort follows the default (the session runs medium, the employee has none): a switch to
	// high that times out leaves it following the default, not pinned to medium.
	b.must(200, "PUT", base, map[string]any{"effort": "high"}, &c)
	expectSetModeBody(t, a)
	readPick()
	if pick.SessionEffort != "high" {
		t.Fatalf("pick kept while pending: %+v", pick)
	}
	expire()
	readPick()
	if c.Pending != nil || c.Effort != "medium" || c.Error != "节点没有回应，切换未生效" || pick.SessionEffort != "" {
		t.Fatalf("an unanswered effort switch from the default goes back to the default: %+v %+v", c, pick)
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

	// The new-session page: options from the user's latest Claude session, picks kept on the new
	// session's employee and opened with.
	var opts struct {
		Runtime     string
		DefaultMode string `json:"default_mode"`
		Model       string
		Modes       []struct{ ID string }
		Models      []struct{ ID string }
		Efforts     []string
	}
	b.must(200, "GET", "/api/session-options?role=dev", nil, &opts)
	if opts.Runtime != "claude" || opts.DefaultMode != "auto" || len(opts.Models) != 2 || len(opts.Modes) != 5 || len(opts.Efforts) == 0 {
		t.Fatalf("session options %+v", opts)
	}
	b.must(200, "GET", "/api/session-options?role=dev&model=claude-haiku-4-5", nil, &opts)
	if opts.DefaultMode != "default" || len(opts.Modes) != 4 || len(opts.Efforts) != 0 {
		t.Fatalf("session options for a model without auto %+v", opts)
	}
	b.must(422, "GET", "/api/session-options?role=nope", nil, nil)
	b.must(422, "POST", "/api/sessions", map[string]any{"message": "x", "role": "dev", "permission_mode": "nope",
		"repos": []map[string]any{{"repo": "o/r", "git_connection_id": conn.ID}}}, nil)
	var second struct{ Project struct{ ID string } }
	b.must(201, "POST", "/api/sessions", map[string]any{"message": "第二个会话", "role": "dev", "node_id": node.Node.ID,
		"permission_mode": "plan", "effort": "high", "repos": []map[string]any{{"repo": "o/r", "git_connection_id": conn.ID}}}, &second)
	ctx, cancel = w.waitCtx()
	f, err = n.Wait(ctx, protocol.TypeSessionOpen, func(f nodetest.Frame) bool {
		return strings.Contains(string(f.Body), `"project_id":"`+second.Project.ID+`"`)
	})
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	var open2 protocol.SessionOpen
	_ = f.Decode(&open2)
	if open2.Claude.PermissionMode != "plan" || open2.Claude.PreferAuto != nil || open2.Claude.Effort == nil || *open2.Claude.Effort != "high" {
		t.Fatalf("a session opens with the picks of the new-session page: %+v", open2.Claude)
	}
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
