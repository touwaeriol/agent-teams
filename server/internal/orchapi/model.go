package orchapi

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/touwaeriol/agent-teams/server/internal/composer"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/httpapi"
	"github.com/touwaeriol/agent-teams/server/internal/orchestrator"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// MemberModel is one employee of a thread and the model it runs.
type MemberModel struct {
	Name     string `json:"name"`
	Tier     string `json:"tier"`
	Runtime  string `json:"runtime"`
	Model    string `json:"model"`
	Override string `json:"override" doc:"Picked with /model ('' = project or role setting; always '' for the lead)"`
}

// ConversationModel is GET /api/conversations/{cid}/model: what /model shows.
type ConversationModel struct {
	Scope     string        `json:"scope" enum:"coordinator,thread"`
	Employee  string        `json:"employee"`
	Runtime   string        `json:"runtime"`
	Model     string        `json:"model" doc:"What the employee runs (or opens its next session with)"`
	Override  string        `json:"override" doc:"Always '': the coordinator and the lead run the default model (/model only changes members)"`
	Inherited string        `json:"inherited" doc:"What the employee runs without the override (= model)"`
	Session   string        `json:"session" doc:"The model the live session reported for its last turn ('' = no live session or no turn yet)"`
	Live      bool          `json:"live" doc:"A session is open on a node: a change takes effect from its next turn"`
	Members   []MemberModel `json:"members" doc:"Thread: every employee of the thread (lead first)"`
}

type conversationModelOutput struct{ Body ConversationModel }

type setModelInput struct {
	CID  string `path:"cid" maxLength:"64"`
	Body struct {
		Model string `json:"model" maxLength:"100" doc:"Model id; '' goes back to the project / role setting"`
		Scope string `json:"scope,omitempty" enum:"lead,thread" default:"lead" doc:"thread: every member of the thread with the lead's runtime (the lead keeps the default model). lead, and any change in the coordinator conversation, is refused (409 model_fixed): the coordinator and leads always run the default model"`
	}
}

type setModelOutput struct{ Body orchestrator.ModelResult }

func (a *API) registerModel(api huma.API) {
	t := "conversations"
	huma.Register(api, op("conversation-model", "GET", "/api/conversations/{cid}/model", t,
		summary("The model of the conversation's employee (and of the thread's employees)")), a.getModel)
	huma.Register(api, op("set-conversation-model", "PUT", "/api/conversations/{cid}/model", t,
		summary("Change the model of the thread's members (scope thread); the coordinator and leads cannot switch. Running sessions switch from their next turn")), a.setModel)
}

func (a *API) getModel(ctx context.Context, in *cidInput) (*conversationModelOutput, error) {
	u, c, e, err := a.conversationEmployee(ctx, in.CID)
	if err != nil {
		return nil, err
	}
	p, err := a.e.Store.ProjectOf(ctx, u.ID, e.ProjectID)
	if err != nil {
		return nil, err
	}
	q := a.e.Core.Pool
	out := ConversationModel{Scope: "thread", Employee: e.Name, Runtime: e.Runtime, Members: []MemberModel{}}
	out.Model = orchestrator.Model(e, p, composer.DefaultModel(ctx, q, p.UserID, e.Runtime))
	out.Inherited = out.Model // the coordinator and the lead have no override (orchestrator.Model)
	if s, err := core.LiveSession(ctx, q, e.ID); err == nil && s.NodeID != nil {
		out.Live = true
		if usage, err := sessionUsage(ctx, q, e.ID); err == nil && usage != nil {
			out.Session = usage.Model
		}
	}
	if c.Kind == "coordinator" {
		out.Scope = "coordinator"
		return &conversationModelOutput{Body: out}, nil
	}
	out.Members, err = a.memberModels(ctx, c.ID, p)
	return &conversationModelOutput{Body: out}, err
}

func (a *API) memberModels(ctx context.Context, threadID string, p store.Project) ([]MemberModel, error) {
	q := a.e.Core.Pool
	list, err := core.ThreadParticipants(ctx, q, threadID)
	if err != nil {
		return nil, err
	}
	out := make([]MemberModel, 0, len(list))
	for _, e := range list {
		out = append(out, MemberModel{Name: e.Name, Tier: e.Tier, Runtime: e.Runtime, Override: e.Model,
			Model: orchestrator.Model(e, p, composer.DefaultModel(ctx, q, p.UserID, e.Runtime))})
	}
	return out, nil
}

func (a *API) setModel(ctx context.Context, in *setModelInput) (*setModelOutput, error) {
	u, c, err := a.conversation(ctx, in.CID)
	if err != nil {
		return nil, err
	}
	if in.Body.Model != "" && !composer.ValidModel(in.Body.Model) {
		return nil, httpapi.Invalid("model", "模型 ID 只能用字母、数字和 . _ - : / @ [ ]，最多 100 个字符")
	}
	res, err := a.e.Orch.SetConversationModel(ctx, orchestrator.ModelChange{UserID: u.ID, ConvID: c.ID, Model: in.Body.Model,
		WholeThread: in.Body.Scope == "thread"})
	if err != nil {
		return nil, fail(err, "没有这个对话")
	}
	_ = a.e.Core.Pool.Gorm(ctx).Table("at_audit_log").Create(map[string]any{"user_id": u.ID, "actor": "user:" + u.ID,
		"action": "model_change", "target_type": "conversation", "target_id": c.ID,
		"detail": store.Param(map[string]any{"model": in.Body.Model, "scope": in.Body.Scope, "applied": res.Applied})}).Error
	return &setModelOutput{Body: res}, nil
}
