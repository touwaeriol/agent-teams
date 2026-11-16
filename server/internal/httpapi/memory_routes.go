package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// Project auto memory (architecture.md §6.7): MEMORY.md and topic files, readable and editable
// by the project owner; employees use the memory_* tools.

type memoryListOutput struct{ Body []store.MemoryBrief }

type memoryFileOutput struct{ Body store.MemoryFile }

type memoryNameInput struct {
	PID  string `path:"pid" maxLength:"64"`
	Name string `path:"name" maxLength:"100" doc:"File name, e.g. MEMORY.md or testing.md"`
}

type memoryPutInput struct {
	PID  string `path:"pid" maxLength:"64"`
	Name string `path:"name" maxLength:"100"`
	Body struct {
		Description string `json:"description,omitempty" maxLength:"300" doc:"One line listed in MEMORY.md; required except for MEMORY.md"`
		Content     string `json:"content" maxLength:"32000"`
	}
}

func (s *Server) registerMemory(api huma.API) {
	tags := []string{"projects"}
	huma.Register(api, huma.Operation{OperationID: "list-memory", Method: "GET", Path: "/api/projects/{pid}/memory", Tags: tags,
		Summary: "Memory files, MEMORY.md first (no content)"}, s.listMemory)
	huma.Register(api, huma.Operation{OperationID: "get-memory-file", Method: "GET", Path: "/api/projects/{pid}/memory/{name}", Tags: tags}, s.getMemoryFile)
	huma.Register(api, huma.Operation{OperationID: "put-memory-file", Method: "PUT", Path: "/api/projects/{pid}/memory/{name}", Tags: tags,
		Summary: "Create or replace a memory file; topic files keep their line in MEMORY.md"}, s.putMemoryFile)
	huma.Register(api, huma.Operation{OperationID: "delete-memory-file", Method: "DELETE", Path: "/api/projects/{pid}/memory/{name}", Tags: tags,
		DefaultStatus: 204}, s.deleteMemoryFile)
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
	files, err := store.ListMemoryFiles(ctx, s.store.Pool, in.PID)
	return &memoryListOutput{Body: files}, err
}

func (s *Server) getMemoryFile(ctx context.Context, in *memoryNameInput) (*memoryFileOutput, error) {
	if _, err := s.ownProject(ctx, in.PID); err != nil {
		return nil, err
	}
	f, err := store.GetMemoryFile(ctx, s.store.Pool, in.PID, in.Name)
	if err != nil {
		return nil, memoryErr(err)
	}
	return &memoryFileOutput{Body: f}, nil
}

func (s *Server) putMemoryFile(ctx context.Context, in *memoryPutInput) (*memoryFileOutput, error) {
	if _, err := s.ownProject(ctx, in.PID); err != nil {
		return nil, err
	}
	f, _, err := store.WriteMemoryFile(ctx, s.store.Pool, in.PID, store.MemoryWrite{Name: in.Name,
		Description: in.Body.Description, Content: in.Body.Content, By: "user"})
	if err != nil {
		return nil, memoryErr(err)
	}
	s.events.Project(ctx, in.PID, "memory", map[string]any{"op": "write", "name": f.Name})
	return &memoryFileOutput{Body: f}, nil
}

func (s *Server) deleteMemoryFile(ctx context.Context, in *memoryNameInput) (*struct{}, error) {
	if _, err := s.ownProject(ctx, in.PID); err != nil {
		return nil, err
	}
	if err := store.DeleteMemoryFile(ctx, s.store.Pool, in.PID, in.Name, "user"); err != nil {
		return nil, memoryErr(err)
	}
	s.events.Project(ctx, in.PID, "memory", map[string]any{"op": "delete", "name": in.Name})
	return nil, nil
}

// memoryErr maps a rejected write to 422 and a missing file to 404.
func memoryErr(err error) error {
	var me *store.MemoryError
	if errors.As(err, &me) {
		return fail(http.StatusUnprocessableEntity, "validation", me.Msg)
	}
	return storeErr(err, "没有这个记忆文件")
}
