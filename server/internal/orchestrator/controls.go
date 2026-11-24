package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw"
	"github.com/touwaeriol/agent-teams/server/internal/runtimes"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// Session controls (#53): the user of a session (多仓库会话, Spec.Solo) switches its employee's
// permission mode, model and thinking effort while it runs. The pick is kept on the employee
// (at_employees.session_mode / model / session_effort), so a reopened session keeps it and the
// role is left alone. Claude applies a switch at once, Codex and Antigravity from the next turn;
// the node answers with session.mode_set and the web shows what the server wrote back.

// FeatureSessionControls is the hello feature of daemons that answer set_mode with session.mode_set.
const FeatureSessionControls = "session_controls"

// controlsPendingFor is how long a switch waits for session.mode_set before the view stops
// showing it as on its way (a daemon that restarted in between never answers).
const controlsPendingFor = 2 * time.Minute

// modeLabels are the menu labels of the set_mode values.
var modeLabels = map[string]string{
	"auto":              "自动",
	"default":           "手动",
	"acceptEdits":       "接受编辑",
	"plan":              "计划",
	"bypassPermissions": "跳过权限确认",
}

// modeHints say what each mode does, by runtime where it differs.
var modeHints = map[string]string{
	"auto":              "模型自动判断哪些操作可以直接做，有风险的才拦下",
	"default":           "需要权限的操作逐条请你确认，不会超时",
	"acceptEdits":       "文件编辑直接通过，其他需要权限的操作请你确认",
	"plan":              "只读代码、给出计划，不改文件",
	"bypassPermissions": "所有操作直接执行，不再确认",
	"codex:auto":        "Codex 的审核子代理判断哪些请求可以放行",
}

// ModeOption is one entry of the permission menu.
type ModeOption struct {
	ID      string `json:"id" enum:"auto,default,acceptEdits,plan,bypassPermissions" doc:"session.set_mode value"`
	Label   string `json:"label"`
	Hint    string `json:"hint"`
	Applies string `json:"applies" enum:"now,next_turn" doc:"now: Claude switches at once; next_turn: Codex/Antigravity from the next turn"`
}

// ModelChoice is a model the session can switch to.
type ModelChoice struct {
	ID            string   `json:"id"`
	Name          string   `json:"name,omitempty"`
	Description   string   `json:"description,omitempty"`
	Efforts       []string `json:"efforts" doc:"Thinking effort levels of the model; [] = none to pick (Antigravity: the effort is part of the model id)"`
	DefaultEffort string   `json:"default_effort,omitempty"`
	SupportsAuto  bool     `json:"supports_auto" doc:"Claude: the model supports the auto permission mode"`
	Default       bool     `json:"default,omitempty" doc:"The runtime's default model"`
}

// ControlsPending is a switch sent to the node and not answered yet.
type ControlsPending struct {
	PermissionMode string    `json:"permission_mode,omitempty"`
	Model          string    `json:"model,omitempty"`
	Effort         string    `json:"effort,omitempty"`
	Since          time.Time `json:"since"`
}

// SessionControls is GET /api/sessions/{pid}/controls: what the session's employee runs in and
// what it may switch to.
type SessionControls struct {
	ThreadID       string           `json:"thread_id"`
	Runtime        string           `json:"runtime"`
	Live           bool             `json:"live" doc:"A session is open on a node: a switch goes to it now; false: the next session opens with it"`
	PermissionMode string           `json:"permission_mode" doc:"The mode in force (set_mode value), as the node wrote it back; without a live session the one the next session opens in"`
	Model          string           `json:"model"`
	Effort         string           `json:"effort" doc:"'' = the model's default"`
	Pending        *ControlsPending `json:"pending" doc:"A switch on its way to the node (null: none); a session_controls event follows its answer"`
	Error          string           `json:"error,omitempty" doc:"Why the last switch failed (nothing of it applied)"`
	Modes          []ModeOption     `json:"modes" doc:"Permission menu in order: auto only when the model supports it, bypassPermissions only when the role allows skipping permission prompts"`
	Models         []ModelChoice    `json:"models" doc:"Models reported by the session's runtime ([] until a session opened on a daemon with session_controls)"`
	Efforts        []string         `json:"efforts" doc:"Effort levels of the current model ([] = none to pick)"`
}

// Errors of SetSessionControls.
var (
	ErrNotSession   = errors.New("只有普通会话可以切换权限模式、模型和思考强度")
	ErrBadControl   = errors.New("invalid session control")
	ErrControlsBusy = errors.New("上一次切换还没有回音，请稍后再试")
)

// ControlError is a refused value: the field and why.
type ControlError struct{ Field, Message string }

func (e *ControlError) Error() string { return e.Message }
func (e *ControlError) Unwrap() error { return ErrBadControl }

// soloMode is the permission mode (a set_mode value) a session's employee opens in: the user's
// pick kept on the employee, else Claude's auto (the node falls back to default when the model
// cannot run it) and, for Codex and Antigravity, the role's (bypass or ask).
func soloMode(e core.Employee) string {
	rt := runtimes.Of(e.Runtime)
	if m := e.SessionMode; m != "" && slices.Contains(rt.Modes, m) && (m != "bypassPermissions" || Bypass(e)) {
		return m
	}
	switch {
	case rt.ID == runtimes.Claude:
		return "auto"
	case Bypass(e):
		return "bypassPermissions"
	}
	return "default"
}

// soloClaude sets a session employee's Claude mode: auto opens as default with prefer_auto (the
// daemon switches once the model says it supports auto); bypassPermissions may be picked later
// whenever the role allows it.
func soloClaude(c *protocol.ClaudeSessionOptions, e core.Employee) {
	mode := soloMode(e)
	c.PermissionMode = protocol.PermissionMode(mode)
	if mode == "auto" {
		t := true
		c.PermissionMode, c.PreferAuto = "default", &t
	}
	if Bypass(e) {
		t := true
		c.AllowDangerouslySkipPermissions = &t
	} else {
		c.AllowDangerouslySkipPermissions = nil
	}
}

// soloCodex sets a session employee's Codex sandbox, approval policy, reviewer and effort.
func soloCodex(c *protocol.CodexSessionOptions, e core.Employee, writable json.RawMessage) {
	switch soloMode(e) {
	case "bypassPermissions":
		c.Sandbox, c.ApprovalPolicy, c.Config = "danger-full-access", "never", nil
	case "auto":
		c.Sandbox, c.ApprovalPolicy = "workspace-write", "on-request"
		r := protocol.CodexSessionOptionsApprovalsReviewerAutoReview
		c.ApprovalsReviewer = &r
		c.Config = &writable
	default:
		c.Sandbox, c.ApprovalPolicy, c.Config = "workspace-write", "untrusted", &writable
	}
	if e.SessionEffort != "" {
		eff := e.SessionEffort
		c.Effort = &eff
	}
}

// soloAntigravity: bypassPermissions answers permission requests on the node, default asks.
func soloAntigravity(a *protocol.AntigravitySessionOptions, e core.Employee) {
	a.Approval = protocol.AntigravitySessionOptionsApprovalAsk
	if soloMode(e) == "bypassPermissions" {
		a.Approval = protocol.AntigravitySessionOptionsApprovalAuto
	}
}

// openMode is the set_mode value a session.open runs in (recorded as the session's permission_mode).
func openMode(open protocol.SessionOpen) string {
	switch {
	case open.Claude != nil:
		return string(open.Claude.PermissionMode)
	case open.Codex != nil:
		if open.Codex.Sandbox == "danger-full-access" {
			return "bypassPermissions"
		}
		if open.Codex.ApprovalsReviewer != nil && *open.Codex.ApprovalsReviewer == protocol.CodexSessionOptionsApprovalsReviewerAutoReview {
			return "auto"
		}
		return "default"
	case open.Antigravity != nil:
		if open.Antigravity.Approval == protocol.AntigravitySessionOptionsApprovalAuto {
			return "bypassPermissions"
		}
		return "default"
	}
	return ""
}

// openControls is the model and effort a session.open runs with.
func openControls(open protocol.SessionOpen) core.SessionControls {
	var c core.SessionControls
	switch {
	case open.Claude != nil:
		c.Model, c.Effort = open.Claude.Model, deref(open.Claude.Effort)
	case open.Codex != nil:
		c.Model, c.Effort = open.Codex.Model, deref(open.Codex.Effort)
	case open.Antigravity != nil:
		c.Model = open.Antigravity.Model
	}
	return c
}

// recordOpenControls keeps the mode, model and effort a session opens with.
func recordOpenControls(ctx context.Context, tx *core.Tx, sessionID string, open protocol.SessionOpen) error {
	c := openControls(open)
	return tx.Gorm(ctx).Table("at_agent_sessions").Where("id = ?", sessionID).
		Updates(map[string]any{"permission_mode": openMode(open), "controls": jsonOf(c)}).Error
}

// recordInitControls takes what session.opened says the session runs: Claude's mode (prefer_auto
// opens in default and may have switched to auto), the model the runtime resolved and the effort.
func recordInitControls(ctx context.Context, tx *core.Tx, s core.AgentSession, init *protocol.SessionInit) error {
	if init == nil {
		return nil
	}
	set := map[string]any{}
	c := s.Controls
	if m := deref(init.Model); m != "" {
		c.Model = m
	}
	if eff := deref(init.Effort); eff != "" {
		c.Effort = eff
	}
	set["controls"] = jsonOf(c)
	if pm := deref(init.PermissionMode); runtimes.Of(s.Runtime).ID == runtimes.Claude && pm != "" {
		set["permission_mode"] = pm
	}
	return tx.Gorm(ctx).Table("at_agent_sessions").Where("id = ?", s.ID).Updates(set).Error
}

// ControlsChange is PUT /api/sessions/{pid}/controls: nil fields stay.
type ControlsChange struct {
	ProjectID      string
	PermissionMode *string
	Model          *string
	Effort         *string
}

// SessionControlsOf is the controls of a session (the project of kind session).
func (o *Orchestrator) SessionControlsOf(ctx context.Context, projectID string) (SessionControls, error) {
	var out SessionControls
	err := o.core.InTx(ctx, func(tx *core.Tx) error {
		lead, t, err := soloLead(ctx, tx, projectID)
		if err != nil {
			return err
		}
		p, err := o.project(ctx, tx, projectID)
		if err != nil {
			return err
		}
		out, err = controlsView(ctx, tx, t, lead, p)
		return err
	})
	return out, err
}

// SetSessionControls switches a session's permission mode, model and/or effort: the pick is kept
// on the employee and, when a session runs on a node, sent to it (session.set_mode). It returns
// the controls with the switch pending until session.mode_set comes back (daemons without
// session_controls: in force at once, as they never answer).
func (o *Orchestrator) SetSessionControls(ctx context.Context, in ControlsChange) (SessionControls, error) {
	var out SessionControls
	err := o.core.InTx(ctx, func(tx *core.Tx) error {
		lead, t, err := soloLead(ctx, tx, in.ProjectID)
		if err != nil {
			return err
		}
		if err := tx.Gorm(ctx).Exec(`SELECT 1 FROM at_employees WHERE id = ? FOR UPDATE`, lead.ID).Error; err != nil {
			return err
		}
		p, err := o.project(ctx, tx, in.ProjectID)
		if err != nil {
			return err
		}
		cur, err := controlsView(ctx, tx, t, lead, p)
		if err != nil {
			return err
		}
		ch, err := checkChange(cur, in)
		if err != nil {
			return err
		}
		if ch == (ControlsPending{}) {
			out = cur
			return nil
		}
		if err := saveChoice(ctx, tx, lead.ID, ch); err != nil {
			return err
		}
		if err := o.sendControls(ctx, tx, lead, ch); err != nil {
			return err
		}
		if lead, err = core.EmployeeByID(ctx, tx, lead.ID); err != nil {
			return err
		}
		if out, err = controlsView(ctx, tx, t, lead, p); err != nil {
			return err
		}
		return emitControls(ctx, tx, t, "", out.Pending == nil)
	})
	return out, err
}

// soloLead is the employee of a session's thread; ErrNotSession for anything else.
func soloLead(ctx context.Context, q core.DB, projectID string) (core.Employee, core.Thread, error) {
	t, err := SessionThread(ctx, q, projectID)
	if errors.Is(err, core.ErrNotFound) {
		return core.Employee{}, t, ErrNotSession
	}
	if err != nil {
		return core.Employee{}, t, err
	}
	lead, err := core.Lead(ctx, q, t.ID)
	if err != nil {
		return lead, t, err
	}
	if !lead.Spec.Solo {
		return lead, t, ErrNotSession
	}
	return lead, t, nil
}

// checkChange refuses values the session cannot take and drops the ones it already runs.
func checkChange(cur SessionControls, in ControlsChange) (ControlsPending, error) {
	var ch ControlsPending
	if cur.Pending != nil {
		return ch, ErrControlsBusy
	}
	model := cur.Model
	if in.Model != nil && *in.Model != "" && *in.Model != cur.Model {
		if len(cur.Models) > 0 && !slices.ContainsFunc(cur.Models, func(m ModelChoice) bool { return m.ID == *in.Model }) {
			return ch, &ControlError{"model", fmt.Sprintf("这个会话不能切换到模型 %s", *in.Model)}
		}
		ch.Model, model = *in.Model, *in.Model
	}
	if in.PermissionMode != nil && *in.PermissionMode != "" && *in.PermissionMode != cur.PermissionMode {
		modes := cur.Modes
		if ch.Model != "" {
			modes = modesFor(cur.Runtime, cur.Models, model, slices.ContainsFunc(cur.Modes, func(m ModeOption) bool { return m.ID == "bypassPermissions" }))
		}
		if !slices.ContainsFunc(modes, func(m ModeOption) bool { return m.ID == *in.PermissionMode }) {
			return ch, &ControlError{"permission_mode", fmt.Sprintf("这个会话不能用 %s 模式", *in.PermissionMode)}
		}
		ch.PermissionMode = *in.PermissionMode
	}
	if in.Effort != nil && *in.Effort != "" && (*in.Effort != cur.Effort || ch.Model != "") {
		if !runtimes.Of(cur.Runtime).Efforts {
			return ch, &ControlError{"effort", "这个运行时的思考强度写在模型里，请切换模型"}
		}
		if efforts := effortsOf(cur.Models, model); len(cur.Models) > 0 && !slices.Contains(efforts, *in.Effort) {
			return ch, &ControlError{"effort", fmt.Sprintf("模型 %s 没有 %s 这一档思考强度", model, *in.Effort)}
		}
		ch.Effort = *in.Effort
	}
	return ch, nil
}

// saveChoice keeps the picks on the employee: a reopened session opens with them.
func saveChoice(ctx context.Context, tx *core.Tx, employeeID string, ch ControlsPending) error {
	set := map[string]any{}
	if ch.PermissionMode != "" {
		set["session_mode"] = ch.PermissionMode
	}
	if ch.Model != "" {
		set["model"] = ch.Model
	}
	if ch.Effort != "" {
		set["session_effort"] = ch.Effort
	}
	return tx.Gorm(ctx).Table("at_employees").Where("id = ?", employeeID).Updates(set).Error
}

// sendControls sends the switch to the employee's live session, if it has one on a node.
func (o *Orchestrator) sendControls(ctx context.Context, tx *core.Tx, e core.Employee, ch ControlsPending) error {
	s, err := core.LiveSession(ctx, tx, e.ID)
	if errors.Is(err, core.ErrNotFound) || (err == nil && s.NodeID == nil) {
		return nil // the next session opens with the picks
	}
	if err != nil {
		return err
	}
	body := protocol.SessionSetMode{SessionID: protocol.SessionID(s.ID)}
	if ch.PermissionMode != "" {
		m := protocol.SessionSetModePermissionMode(ch.PermissionMode)
		body.PermissionMode = &m
	}
	if ch.Model != "" {
		body.Model = &ch.Model
	}
	if ch.Effort != "" {
		body.Effort = &ch.Effort
	}
	answers, err := core.NodeHasFeature(ctx, tx, *s.NodeID, FeatureSessionControls)
	if err != nil {
		return err
	}
	c := s.Controls
	c.Error = ""
	set := map[string]any{}
	if answers {
		id := core.NewID("ms")
		body.RequestID = &id
		c.Pending = &core.ControlsSwitch{RequestID: id, PermissionMode: ch.PermissionMode, Model: ch.Model, Effort: ch.Effort, At: time.Now().UTC()}
	} else {
		applyControls(&c, set, ch.PermissionMode, ch.Model, ch.Effort)
	}
	set["controls"] = jsonOf(c)
	if err := tx.Gorm(ctx).Table("at_agent_sessions").Where("id = ?", s.ID).Updates(set).Error; err != nil {
		return err
	}
	_, err = o.gw.Enqueue(ctx, tx, *s.NodeID, nodegw.Command{Type: protocol.TypeSessionSetMode, Body: body,
		SessionID: s.ID, OpenGen: s.OpenGen})
	return err
}

func applyControls(c *core.SessionControls, set map[string]any, mode, model, effort string) {
	if mode != "" {
		set["permission_mode"] = mode
	}
	if model != "" {
		c.Model = model
	}
	if effort != "" {
		c.Effort = effort
	}
}

// HandleModeSet takes session.mode_set from nodeID: the session runs in what it reports; a failed
// switch is recorded and the employee's picks go back to what still runs. A session_controls event
// tells the web to refetch.
func (o *Orchestrator) HandleModeSet(ctx context.Context, nodeID string, m *protocol.SessionModeSet) error {
	return o.core.InTx(ctx, func(tx *core.Tx) error {
		s, err := core.LockSession(ctx, tx, string(m.SessionID))
		if err != nil || s.NodeID == nil || *s.NodeID != nodeID || int(m.OpenGen) != s.OpenGen {
			return ignoreMissing(err)
		}
		c := s.Controls
		if c.Pending != nil && c.Pending.RequestID == deref(m.RequestID) {
			c.Pending = nil
		}
		set := map[string]any{}
		applyControls(&c, set, deref(m.PermissionMode), deref(m.Model), deref(m.Effort))
		c.Error = ""
		if !m.Ok {
			c.Error = deref(m.Error)
			if c.Error == "" {
				c.Error = "节点没有接受这次切换"
			}
			if err := revertChoice(ctx, tx, s.EmployeeID, m); err != nil {
				return err
			}
		}
		set["controls"] = jsonOf(c)
		if err := tx.Gorm(ctx).Table("at_agent_sessions").Where("id = ?", s.ID).Updates(set).Error; err != nil {
			return err
		}
		if s.ThreadID == nil {
			return nil
		}
		t, err := core.ThreadByID(ctx, tx, *s.ThreadID)
		if err != nil {
			return ignoreMissing(err)
		}
		return emitControls(ctx, tx, t, c.Error, m.Ok)
	})
}

// revertChoice puts the employee's picks back to what the session still runs after a failed switch.
func revertChoice(ctx context.Context, tx *core.Tx, employeeID string, m *protocol.SessionModeSet) error {
	set := map[string]any{}
	if v := deref(m.PermissionMode); v != "" {
		set["session_mode"] = v
	}
	if v := deref(m.Model); v != "" {
		set["model"] = v
	}
	if v := deref(m.Effort); v != "" {
		set["session_effort"] = v
	}
	if len(set) == 0 {
		return nil
	}
	return tx.Gorm(ctx).Table("at_employees").Where("id = ?", employeeID).Updates(set).Error
}

// emitControls tells the session's page that its controls changed (api.md §1.1 session_controls).
func emitControls(ctx context.Context, tx *core.Tx, t core.Thread, errText string, applied bool) error {
	payload := map[string]any{"thread_id": t.ID, "applied": applied}
	if errText != "" {
		payload["error"] = errText
	}
	_, err := tx.Emit(ctx, t.ProjectID, &t.ID, core.EvSessionControls, "", payload)
	return err
}

// controlsView computes SessionControls for a session's employee.
func controlsView(ctx context.Context, q core.DB, t core.Thread, e core.Employee, p store.Project) (SessionControls, error) {
	out := SessionControls{ThreadID: t.ID, Runtime: runtimes.Of(e.Runtime).ID, Modes: []ModeOption{}, Models: []ModelChoice{}, Efforts: []string{}}
	out.PermissionMode, out.Model, out.Effort = soloMode(e), Model(e, p), soloEffort(e, p)
	latest, err := core.LatestSession(ctx, q, e.ID)
	if err != nil && !errors.Is(err, core.ErrNotFound) {
		return out, err
	}
	if err == nil {
		out.Models = initModels(latest.Init)
	}
	if err == nil && latest.State != "closed" && latest.NodeID != nil {
		out.Live = true
		if latest.PermissionMode != "" {
			out.PermissionMode = latest.PermissionMode
		}
		if latest.Controls.Model != "" {
			out.Model = latest.Controls.Model
		}
		if latest.Controls.Effort != "" {
			out.Effort = latest.Controls.Effort
		}
		out.Error = latest.Controls.Error
		if pd := latest.Controls.Pending; pd != nil && time.Since(pd.At) < controlsPendingFor {
			out.Pending = &ControlsPending{PermissionMode: pd.PermissionMode, Model: pd.Model, Effort: pd.Effort, Since: pd.At}
		}
	}
	out.Modes = modesFor(out.Runtime, out.Models, out.Model, Bypass(e) && !readOnly(e))
	out.Efforts = effortsOf(out.Models, out.Model)
	if !runtimes.Of(out.Runtime).Efforts {
		out.Efforts = []string{}
	}
	return out, nil
}

// soloEffort is the effort a session's employee runs with: the user's pick, else (Claude) the
// project's thread effort or the role's; Codex sessions only take an effort the user picked.
func soloEffort(e core.Employee, p store.Project) string {
	if runtimes.Of(e.Runtime).ID == runtimes.Claude {
		return effort(e, p)
	}
	if runtimes.Of(e.Runtime).Efforts {
		return e.SessionEffort
	}
	return ""
}

// modesFor is the permission menu of a runtime for a model: auto only when the model supports it
// (Claude; unknown models keep it, the daemon refuses it if not), bypassPermissions only when allowed.
func modesFor(runtime string, models []ModelChoice, model string, bypass bool) []ModeOption {
	rt := runtimes.Of(runtime)
	applies := "next_turn"
	if rt.ModeSwitchNow {
		applies = "now"
	}
	out := []ModeOption{}
	for _, id := range rt.Modes {
		if id == "bypassPermissions" && !bypass {
			continue
		}
		if id == "auto" && rt.ID == runtimes.Claude {
			if i := slices.IndexFunc(models, func(m ModelChoice) bool { return m.ID == model }); i >= 0 && !models[i].SupportsAuto {
				continue
			}
		}
		hint := modeHints[id]
		if h, ok := modeHints[rt.ID+":"+id]; ok {
			hint = h
		}
		out = append(out, ModeOption{ID: id, Label: modeLabels[id], Hint: hint, Applies: applies})
	}
	return out
}

func effortsOf(models []ModelChoice, model string) []string {
	if i := slices.IndexFunc(models, func(m ModelChoice) bool { return m.ID == model }); i >= 0 {
		return slices.Clone(models[i].Efforts)
	}
	return []string{}
}

// initModels reads the models session.opened reported.
func initModels(raw json.RawMessage) []ModelChoice {
	var init protocol.SessionInit
	out := []ModelChoice{}
	if len(raw) == 0 || json.Unmarshal(raw, &init) != nil {
		return out
	}
	for _, m := range init.Models {
		c := ModelChoice{ID: m.ID, Name: deref(m.Name), Description: deref(m.Description), Efforts: slices.Clone(m.Efforts),
			DefaultEffort: deref(m.DefaultEffort), SupportsAuto: m.SupportsAuto != nil && *m.SupportsAuto, Default: m.Default != nil && *m.Default}
		if c.Efforts == nil {
			c.Efforts = []string{}
		}
		out = append(out, c)
	}
	return out
}

func jsonOf(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
