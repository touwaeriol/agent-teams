package orchapi

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/touwaeriol/agent-teams/server/internal/composer"
	"github.com/touwaeriol/agent-teams/server/internal/httpapi"
	"github.com/touwaeriol/agent-teams/server/internal/orchestrator"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// Session controls (#53): the permission mode, model and thinking effort of a session's employee
// (多仓库会话 only; orchestrator/controls.go).

type controlsInput struct {
	PID string `path:"pid" maxLength:"64"`
}

type controlsOutput struct{ Body orchestrator.SessionControls }

type setControlsInput struct {
	PID  string `path:"pid" maxLength:"64"`
	Body struct {
		PermissionMode *string `json:"permission_mode,omitempty" enum:"auto,default,acceptEdits,plan,bypassPermissions" doc:"One of the modes GET lists; absent = unchanged"`
		Model          *string `json:"model,omitempty" maxLength:"100" doc:"One of the models GET lists (any valid id while it lists none); absent = unchanged"`
		Effort         *string `json:"effort,omitempty" maxLength:"32" doc:"One of the model's efforts; absent = unchanged"`
	}
}

func (a *API) registerControls(api huma.API) {
	t := "sessions"
	huma.Register(api, op("session-controls", "GET", "/api/projects/{pid}/session-controls", t,
		summary("The permission mode, model and thinking effort of a session, and what it may switch to")), a.getControls)
	huma.Register(api, op("set-session-controls", "PUT", "/api/projects/{pid}/session-controls", t,
		summary("Switch a session's permission mode, model and/or thinking effort: Claude at once, Codex and Antigravity from the next turn; the answer comes as a session_controls event")), a.setControls)
}

// sessionProject loads one of the caller's sessions (a project of kind session).
func (a *API) sessionProject(ctx context.Context, pid string) (store.User, store.Project, error) {
	u, p, err := a.project(ctx, pid)
	if err != nil {
		return u, p, err
	}
	if p.Kind != "session" {
		return u, p, httpapi.FailErr(409, "not_session", orchestrator.ErrNotSession)
	}
	return u, p, nil
}

func (a *API) getControls(ctx context.Context, in *controlsInput) (*controlsOutput, error) {
	_, p, err := a.sessionProject(ctx, in.PID)
	if err != nil {
		return nil, err
	}
	out, err := a.e.Orch.SessionControlsOf(ctx, p.ID)
	if err != nil {
		return nil, fail(err, "没有这个会话")
	}
	return &controlsOutput{Body: out}, nil
}

func (a *API) setControls(ctx context.Context, in *setControlsInput) (*controlsOutput, error) {
	u, p, err := a.sessionProject(ctx, in.PID)
	if err != nil {
		return nil, err
	}
	b := in.Body
	if b.Model != nil {
		m := strings.TrimSpace(*b.Model)
		if m != "" && !composer.ValidModel(m) {
			return nil, httpapi.Invalid("model", composer.ModelFormatProblem)
		}
		b.Model = &m
	}
	out, err := a.e.Orch.SetSessionControls(ctx, orchestrator.ControlsChange{ProjectID: p.ID, PermissionMode: b.PermissionMode,
		Model: b.Model, Effort: b.Effort})
	var bad *orchestrator.ControlError
	if errors.As(err, &bad) {
		return nil, httpapi.InvalidMsg(bad.Field, bad.Text)
	}
	if err != nil {
		return nil, fail(err, "没有这个会话")
	}
	_ = a.e.Core.Pool.Gorm(ctx).Table("at_audit_log").Create(map[string]any{"user_id": u.ID, "actor": "user:" + u.ID,
		"action": "session_controls", "target_type": "project", "target_id": p.ID,
		"detail": store.Param(map[string]any{"permission_mode": b.PermissionMode, "model": b.Model, "effort": b.Effort})}).Error
	return &controlsOutput{Body: out}, nil
}

type sessionOptionsInput struct {
	Role    string `query:"role" maxLength:"64" doc:"Member role template ('' = the first member role)"`
	Runtime string `query:"runtime" maxLength:"32" doc:"The agent picked on the page, one of the role's runtimes ('' = the role's first)"`
	Model   string `query:"model" maxLength:"100" doc:"The model picked on the page ('' = the role's for the runtime)"`
}

type sessionOptionsOutput struct{ Body orchestrator.SessionOptions }

func (a *API) registerSessionOptions(api huma.API) {
	huma.Register(api, op("session-options", "GET", "/api/session-options", "sessions",
		summary("The permission modes, models and efforts a new session made from a role can start with")), a.sessionOptions)
}

func (a *API) sessionOptions(ctx context.Context, in *sessionOptionsInput) (*sessionOptionsOutput, error) {
	u, err := a.user(ctx)
	if err != nil {
		return nil, err
	}
	roles, err := a.e.Store.Roles(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(roles, func(r store.Role) bool { return r.Tier == "member" && (in.Role == "" || r.Role == in.Role) })
	if i < 0 {
		return nil, httpapi.Invalidf("role", "没有这个岗位：{role}", "role", in.Role)
	}
	rt := strings.TrimSpace(in.Runtime)
	if rt != "" && !slices.Contains(roles[i].RuntimeList(), rt) {
		return nil, httpapi.Invalidf("runtime", "这个岗位没有配置这个 agent：{runtime}", "runtime", rt)
	}
	out, err := a.e.Orch.SessionOptionsFor(ctx, u.ID, roles[i], rt, strings.TrimSpace(in.Model))
	if err != nil {
		return nil, err
	}
	return &sessionOptionsOutput{Body: out}, nil
}
