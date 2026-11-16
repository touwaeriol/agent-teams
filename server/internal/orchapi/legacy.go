package orchapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/events"
	"github.com/touwaeriol/agent-teams/server/internal/httpapi"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// LegacyConversation is v1's ConversationView.
type LegacyConversation struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
	ProjectID *string   `json:"project_id"`
	Kind      string    `json:"kind"`
}

// LegacyTask is v1's TaskView (status is any string).
type LegacyTask struct {
	ID          string   `json:"id"`
	Assignee    string   `json:"assignee"`
	Instruction string   `json:"instruction"`
	DependsOn   []string `json:"depends_on"`
	Status      string   `json:"status"`
	Output      string   `json:"output"`
	Parent      string   `json:"parent"`
}

type legacyListInput struct {
	ProjectID string `query:"project_id" maxLength:"64"`
	Kind      string `query:"kind" enum:"chat,coordinator,thread," default:"chat"`
}

type legacyListOutput struct{ Body []LegacyConversation }

type legacyTasksOutput struct{ Body []LegacyTask }

type legacyMailOutput struct{ Body []core.MailView }

type legacyPlanOutput struct {
	Body struct {
		Steps any `json:"steps" doc:"v1 plan steps [{title, status}]"`
	}
}

func (a *API) registerLegacy(api huma.API) {
	t := "legacy"
	huma.Register(api, op("list-conversations", "GET", "/api/conversations", t,
		summary("v1 conversations (read only); kind defaults to chat")), a.listConversations)
	huma.Register(api, op("create-conversation", "POST", "/api/conversations", t,
		summary("v1 endpoint: always 409 legacy_read_only")), a.createConversation)
	huma.Register(api, op("conversation-tasks", "GET", "/api/conversations/{cid}/tasks", t), a.legacyTasks)
	huma.Register(api, op("conversation-mail", "GET", "/api/conversations/{cid}/mail", t), a.legacyMail)
	huma.Register(api, op("conversation-plan", "GET", "/api/conversations/{cid}/plan", t), a.legacyPlan)
	huma.Register(api, sseOp("conversation-events", "/api/conversations/{cid}/events",
		"v1 conversation stream: one snapshot of a finished conversation, then idle"), a.legacyEvents)
}

func (a *API) listConversations(ctx context.Context, in *legacyListInput) (*legacyListOutput, error) {
	u, err := a.user(ctx)
	if err != nil {
		return nil, err
	}
	kind := in.Kind
	if kind == "" {
		kind = "chat"
	}
	convs, err := core.LegacyConversations(ctx, a.e.Core.Pool, u.ID, in.ProjectID, kind)
	out := make([]LegacyConversation, 0, len(convs))
	for _, c := range convs {
		out = append(out, LegacyConversation{ID: c.ID, Title: c.Title, CreatedAt: c.CreatedAt, ProjectID: c.ProjectID, Kind: c.Kind})
	}
	return &legacyListOutput{Body: out}, err
}

func (a *API) createConversation(ctx context.Context, _ *struct{}) (*struct{}, error) {
	if _, err := a.user(ctx); err != nil {
		return nil, err
	}
	return nil, httpapi.Fail(http.StatusConflict, "legacy_read_only", "旧对话只读；请在项目里和项目统筹对话")
}

func (a *API) legacyTasks(ctx context.Context, in *cidInput) (*legacyTasksOutput, error) {
	_, c, err := a.conversation(ctx, in.CID)
	if err != nil {
		return nil, err
	}
	rows, err := store.Query(ctx, a.e.Core.Pool, `SELECT id, assignee, instruction, depends_on, status, output, parent FROM at_tasks
		WHERE conversation_id = $1 ORDER BY CASE WHEN id ~ '^t[0-9]+$' THEN substring(id from 2)::int END, id`, c.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LegacyTask{}
	for rows.Next() {
		var t LegacyTask
		if err := rows.Scan(&t.ID, &t.Assignee, &t.Instruction, &t.DependsOn, &t.Status, &t.Output, &t.Parent); err != nil {
			return nil, err
		}
		if t.DependsOn == nil {
			t.DependsOn = []string{}
		}
		out = append(out, t)
	}
	return &legacyTasksOutput{Body: out}, rows.Err()
}

func (a *API) legacyMail(ctx context.Context, in *cidInput) (*legacyMailOutput, error) {
	_, c, err := a.conversation(ctx, in.CID)
	if err != nil {
		return nil, err
	}
	mail, err := core.MailPage(ctx, a.e.Core.Pool, c.ID, 0, 200)
	out := make([]core.MailView, 0, len(mail))
	for i := len(mail) - 1; i >= 0; i-- {
		out = append(out, core.MailViewOf(mail[i]))
	}
	return &legacyMailOutput{Body: out}, err
}

func (a *API) legacyPlan(ctx context.Context, in *cidInput) (*legacyPlanOutput, error) {
	_, c, err := a.conversation(ctx, in.CID)
	if err != nil {
		return nil, err
	}
	out := &legacyPlanOutput{}
	out.Body.Steps = []any{}
	if len(c.Plan) > 0 {
		_ = json.Unmarshal(c.Plan, &out.Body.Steps)
	}
	return out, nil
}

// legacyEvents sends the conversation's state once (old conversations get no new events), then pings.
func (a *API) legacyEvents(ctx context.Context, in *cidInput) (*huma.StreamResponse, error) {
	_, c, err := a.conversation(ctx, in.CID)
	if err != nil {
		return nil, err
	}
	snapshot, err := json.Marshal(map[string]any{"type": "snapshot", "conversation_id": c.ID, "kind": c.Kind})
	if err != nil {
		return nil, err
	}
	return a.stream(func(ctx context.Context, w events.Writer) error {
		if _, err := fmt.Fprintf(w.W, "data: %s\n\n", snapshot); err != nil {
			return err
		}
		w.Flush()
		return idle(ctx, w, a.e.Events.Ping)
	}), nil
}

func idle(ctx context.Context, w events.Writer, every time.Duration) error {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if _, err := fmt.Fprint(w.W, ": ping\n\n"); err != nil {
				return err
			}
			w.Flush()
		}
	}
}
