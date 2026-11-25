package orchapi

import (
	"context"
	"fmt"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/delivery"
)

type deliverablesInput struct {
	PID    string `path:"pid" maxLength:"64"`
	Kind   string `query:"kind" maxLength:"32"`
	Thread int    `query:"thread" minimum:"0"`
	Q      string `query:"q" maxLength:"200"`
	Cursor string `query:"cursor" maxLength:"64"`
	Limit  int    `query:"limit" minimum:"0" maximum:"200"`
	// IDs looks up the given deliverables of this project (ids mentioned in a message); ids of
	// other projects or that do not exist are simply not in the page.
	IDs []string `query:"ids" maxItems:"50" doc:"Only these deliverables (comma-separated ids, at most 50); unknown ids and ids of other projects are left out"`
	// NoImages leaves out hosted lone images published before screenshots stopped being deliverables.
	NoImages bool `query:"no_images" doc:"Leave out hosted lone images (render=image), published before screenshots stopped being deliverables"`
}

// DeliverablePage is a page of deliverables (newest first).
type DeliverablePage struct {
	Items        []core.DeliverableView `json:"items"`
	NextCursor   *string                `json:"next_cursor"`
	StorageBytes int64                  `json:"storage_bytes" doc:"Bytes the project's hosted files take (every version)"`
}

type deliverablePageOutput struct{ Body DeliverablePage }

type newDeliverableInput struct {
	PID  string `path:"pid" maxLength:"64"`
	Body struct {
		Kind     string `json:"kind" minLength:"1" maxLength:"32"`
		Title    string `json:"title" minLength:"1" maxLength:"300"`
		URL      string `json:"url" maxLength:"2000"`
		ThreadNo *int   `json:"thread_no,omitempty" minimum:"1"`
	}
}

type deliverableOutput struct{ Body core.DeliverableView }

type requestDeliverableInput struct {
	PID  string `path:"pid" maxLength:"64"`
	Body struct {
		Kind  string `json:"kind" minLength:"1" maxLength:"32"`
		Brief string `json:"brief" minLength:"1" maxLength:"20000"`
	}
}

type deliverableIDInput struct {
	ID string `path:"id" maxLength:"64"`
}

type patchDeliverableInput struct {
	ID   string `path:"id" maxLength:"64"`
	Body struct {
		Title *string `json:"title,omitempty" minLength:"1" maxLength:"300"`
	}
}

// kindNames are the Chinese names used in the "新做一个" message (api.md §3.5).
var kindNames = map[string]string{"doc": "文档", "design": "设计", "slides": "幻灯片", "dashboard": "看板", "board": "白板",
	"data": "数据"}

func (a *API) registerDeliverables(api huma.API) {
	t := "deliverables"
	huma.Register(api, op("list-deliverables", "GET", "/api/projects/{pid}/deliverables", t), a.listDeliverables)
	huma.Register(api, op("add-deliverable", "POST", "/api/projects/{pid}/deliverables", t, status(201)), a.addDeliverable)
	huma.Register(api, op("request-deliverable", "POST", "/api/projects/{pid}/deliverables/request", t, status(202),
		summary("Ask the coordinator to make one (sends 做一个<kind>：<brief>)")), a.requestDeliverable)
	huma.Register(api, op("update-deliverable", "PATCH", "/api/deliverables/{id}", t), a.updateDeliverable)
	huma.Register(api, op("delete-deliverable", "DELETE", "/api/deliverables/{id}", t, status(204)), a.deleteDeliverable)
}

func (a *API) deliverables(ctx context.Context, pid string, f core.DeliverableFilter) ([]core.DeliverableView, error) {
	ds, err := core.Deliverables(ctx, a.e.Core.Pool, pid, f)
	if err != nil {
		return nil, err
	}
	out := make([]core.DeliverableView, 0, len(ds))
	for _, d := range ds {
		v, err := core.DeliverableViewOf(ctx, a.e.Core.Pool, d)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func (a *API) listDeliverables(ctx context.Context, in *deliverablesInput) (*deliverablePageOutput, error) {
	_, p, err := a.project(ctx, in.PID)
	if err != nil {
		return nil, err
	}
	f := core.DeliverableFilter{Kind: in.Kind, Q: strings.TrimSpace(in.Q), Before: in.Cursor, Limit: pageLimit(in.Limit, 50),
		NoImages: in.NoImages, IDs: in.IDs}
	if in.Thread > 0 {
		t, err := core.ThreadByNo(ctx, a.e.Core.Pool, p.ID, in.Thread)
		if err != nil {
			return nil, fail(err, "没有这个讨论串")
		}
		f.ThreadID = t.ID
	}
	items, err := a.deliverables(ctx, p.ID, f)
	if err != nil {
		return nil, err
	}
	page := DeliverablePage{Items: items}
	if page.StorageBytes, err = a.storageBytes(ctx, p.ID); err != nil {
		return nil, err
	}
	if len(items) == f.Limit {
		last := items[len(items)-1].ID
		page.NextCursor = &last
	}
	return &deliverablePageOutput{Body: page}, nil
}

func (a *API) addDeliverable(ctx context.Context, in *newDeliverableInput) (*deliverableOutput, error) {
	_, p, err := a.project(ctx, in.PID)
	if err != nil {
		return nil, err
	}
	var threadID *string
	if in.Body.ThreadNo != nil {
		t, err := core.ThreadByNo(ctx, a.e.Core.Pool, p.ID, *in.Body.ThreadNo)
		if err != nil {
			return nil, fail(err, "没有这个讨论串")
		}
		threadID = &t.ID
	}
	var d core.Deliverable
	err = a.e.Core.InTx(ctx, func(tx *core.Tx) error {
		var err error
		d, err = a.e.Delivery.Publish(ctx, tx, p.ID, threadID, in.Body.Kind, in.Body.Title, in.Body.URL, "user",
			map[string]any{"by": "user"})
		return err
	})
	if err != nil {
		return nil, fail(err, "没有这个项目")
	}
	v, err := core.DeliverableViewOf(ctx, a.e.Core.Pool, d)
	return &deliverableOutput{Body: v}, err
}

func (a *API) requestDeliverable(ctx context.Context, in *requestDeliverableInput) (*messageOutput, error) {
	if delivery.IsPRKind(in.Body.Kind) {
		return nil, fail(delivery.ErrPRKind, "")
	}
	u, p, err := a.project(ctx, in.PID)
	if err != nil {
		return nil, err
	}
	_, conv, err := a.e.Orch.EnsureCoordinator(ctx, u.ID, p.ID)
	if err != nil {
		return nil, err
	}
	name := kindNames[in.Body.Kind]
	if name == "" {
		name = in.Body.Kind
	}
	text := fmt.Sprintf("做一个%s：%s", name, strings.TrimSpace(in.Body.Brief))
	m, err := a.e.Orch.PostUserMessage(ctx, u.ID, conv, text, false, map[string]any{"request_kind": in.Body.Kind})
	if err != nil {
		return nil, fail(err, "没有这个项目")
	}
	return &messageOutput{Body: core.MessageViewOf(m)}, nil
}

func (a *API) ownDeliverable(ctx context.Context, id string) (core.Deliverable, error) {
	u, err := a.user(ctx)
	if err != nil {
		return core.Deliverable{}, err
	}
	d, err := core.DeliverableByID(ctx, a.e.Core.Pool, u.ID, id)
	return d, fail(err, "没有这个交付物")
}

func (a *API) updateDeliverable(ctx context.Context, in *patchDeliverableInput) (*deliverableOutput, error) {
	d, err := a.ownDeliverable(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	err = a.e.Core.InTx(ctx, func(tx *core.Tx) error {
		var title *string
		if in.Body.Title != nil {
			t := strings.TrimSpace(*in.Body.Title)
			title = &t
		}
		var err error
		if d, err = core.UpdateDeliverable(ctx, tx, d.ID, title, nil); err != nil {
			return err
		}
		return tx.EmitDeliverable(ctx, d)
	})
	if err != nil {
		return nil, err
	}
	v, err := core.DeliverableViewOf(ctx, a.e.Core.Pool, d)
	return &deliverableOutput{Body: v}, err
}

func (a *API) deleteDeliverable(ctx context.Context, in *deliverableIDInput) (*struct{}, error) {
	d, err := a.ownDeliverable(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	return nil, a.e.Core.InTx(ctx, func(tx *core.Tx) error {
		if err := core.DeleteDeliverable(ctx, tx, d.ID); err != nil {
			return err
		}
		_, err := tx.Emit(ctx, d.ProjectID, d.ThreadID, core.EvDeliverable, "user",
			map[string]any{"deliverable": map[string]any{"id": d.ID}, "op": "delete"})
		return err
	})
}
