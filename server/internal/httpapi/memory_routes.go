package httpapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/touwaeriol/agent-teams/server/internal/store"
)

type memoryListOutput struct{ Body []store.MemoryItem }

type memoryOutput struct{ Body store.MemoryItem }

type memoryAddInput struct {
	PID  string `path:"pid" maxLength:"64"`
	Body struct {
		Text string `json:"text" minLength:"1" maxLength:"8000"`
	}
}

type memoryPatchInput struct {
	PID  string `path:"pid" maxLength:"64"`
	ID   string `path:"id" maxLength:"64"`
	Body struct {
		Text   *string `json:"text,omitempty" minLength:"1" maxLength:"8000"`
		Pinned *bool   `json:"pinned,omitempty"`
	}
}

type memoryIDInput struct {
	PID string `path:"pid" maxLength:"64"`
	ID  string `path:"id" maxLength:"64"`
}

func (s *Server) registerMemory(api huma.API) {
	tags := []string{"projects"}
	huma.Register(api, huma.Operation{OperationID: "list-memory", Method: "GET", Path: "/api/projects/{pid}/memory", Tags: tags}, s.listMemory)
	huma.Register(api, huma.Operation{OperationID: "add-memory", Method: "POST", Path: "/api/projects/{pid}/memory", Tags: tags, DefaultStatus: 201}, s.addMemory)
	huma.Register(api, huma.Operation{OperationID: "update-memory", Method: "PATCH", Path: "/api/projects/{pid}/memory/{id}", Tags: tags}, s.updateMemory)
	huma.Register(api, huma.Operation{OperationID: "delete-memory", Method: "DELETE", Path: "/api/projects/{pid}/memory/{id}", Tags: tags, DefaultStatus: 204}, s.deleteMemory)
}

// ownProject checks the project belongs to the caller.
func (s *Server) ownProject(ctx context.Context, pid string) (*principal, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.store.ProjectOf(ctx, p.User.ID, pid); err != nil {
		return nil, storeErr(err, "没有这个项目")
	}
	return p, nil
}

func (s *Server) listMemory(ctx context.Context, in *projectIDInput) (*memoryListOutput, error) {
	if _, err := s.ownProject(ctx, in.PID); err != nil {
		return nil, err
	}
	items, err := s.store.ListMemory(ctx, in.PID)
	return &memoryListOutput{Body: items}, err
}

func (s *Server) addMemory(ctx context.Context, in *memoryAddInput) (*memoryOutput, error) {
	if _, err := s.ownProject(ctx, in.PID); err != nil {
		return nil, err
	}
	text := strings.TrimSpace(in.Body.Text)
	if text == "" {
		return nil, fail(http.StatusUnprocessableEntity, "validation", "记忆内容不能为空")
	}
	item, err := s.store.AddMemory(ctx, in.PID, text, "user")
	if err != nil {
		return nil, err
	}
	s.events.Project(ctx, in.PID, "memory", map[string]any{"item": item, "op": "add"})
	return &memoryOutput{Body: item}, nil
}

func (s *Server) updateMemory(ctx context.Context, in *memoryPatchInput) (*memoryOutput, error) {
	if _, err := s.ownProject(ctx, in.PID); err != nil {
		return nil, err
	}
	var ch []store.Change
	if in.Body.Text != nil {
		ch = append(ch, store.Change{Col: "text", Val: strings.TrimSpace(*in.Body.Text)})
	}
	if in.Body.Pinned != nil {
		ch = append(ch, store.Change{Col: "pinned", Val: *in.Body.Pinned})
	}
	item, err := s.store.UpdateMemory(ctx, in.PID, in.ID, ch)
	if err != nil {
		return nil, storeErr(err, "没有这条记忆")
	}
	s.events.Project(ctx, in.PID, "memory", map[string]any{"item": item, "op": "update"})
	return &memoryOutput{Body: item}, nil
}

func (s *Server) deleteMemory(ctx context.Context, in *memoryIDInput) (*struct{}, error) {
	if _, err := s.ownProject(ctx, in.PID); err != nil {
		return nil, err
	}
	if err := s.store.DeleteMemory(ctx, in.PID, in.ID); err != nil {
		return nil, storeErr(err, "没有这条记忆")
	}
	s.events.Project(ctx, in.PID, "memory", map[string]any{"item": map[string]string{"id": in.ID}, "op": "delete"})
	return nil, nil
}
