package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// Auto memory (architecture.md §6.7): a project's MEMORY.md and topic files, and the user's own
// memory every project reads, readable and editable by their owner; employees use the memory_*
// tools or, with the shared folder, edit the files themselves. With the shared folder on the
// files there are the source: a list reads the folder first, a write is written out right after.

type memoryListOutput struct{ Body []store.MemoryBrief }

type memoryFileOutput struct{ Body store.MemoryFile }

type memoryNameInput struct {
	PID  string `path:"pid" maxLength:"64"`
	Name string `path:"name" maxLength:"100" doc:"File name, e.g. MEMORY.md or testing.md"`
}

type memoryBody struct {
	Description string `json:"description,omitempty" maxLength:"300" doc:"One line listed in MEMORY.md; required except for MEMORY.md"`
	Type        string `json:"type,omitempty" enum:"project,feedback,reference,user," doc:"Optional kind of a topic file"`
	Content     string `json:"content" maxLength:"32000"`
}

type memoryPutInput struct {
	PID  string `path:"pid" maxLength:"64"`
	Name string `path:"name" maxLength:"100"`
	Body memoryBody
}

type userMemoryNameInput struct {
	Name string `path:"name" maxLength:"100" doc:"File name, e.g. MEMORY.md or style.md"`
}

type userMemoryPutInput struct {
	Name string `path:"name" maxLength:"100"`
	Body memoryBody
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

	me := []string{"me"}
	huma.Register(api, huma.Operation{OperationID: "list-user-memory", Method: "GET", Path: "/api/me/memory", Tags: me,
		Summary: "My own memory files (every project reads them), MEMORY.md first (no content)"}, s.listUserMemory)
	huma.Register(api, huma.Operation{OperationID: "get-user-memory-file", Method: "GET", Path: "/api/me/memory/{name}", Tags: me}, s.getUserMemoryFile)
	huma.Register(api, huma.Operation{OperationID: "put-user-memory-file", Method: "PUT", Path: "/api/me/memory/{name}", Tags: me,
		Summary: "Create or replace one of my memory files"}, s.putUserMemoryFile)
	huma.Register(api, huma.Operation{OperationID: "delete-user-memory-file", Method: "DELETE", Path: "/api/me/memory/{name}", Tags: me,
		DefaultStatus: 204}, s.deleteUserMemoryFile)
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

func (s *Server) projectMemory(ctx context.Context, pid string) (store.MemoryScope, error) {
	_, err := s.ownProject(ctx, pid)
	return store.ProjectMemory(pid), err
}

func (s *Server) userMemory(ctx context.Context) (store.MemoryScope, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return store.MemoryScope{}, err
	}
	return store.UserMemory(p.User.ID), nil
}

// memoryList reads the folder first (an edit on a node shows at once); a folder out of reach
// leaves the cached rows.
func (s *Server) memoryList(ctx context.Context, sc store.MemoryScope) (*memoryListOutput, error) {
	if err := s.memory.Sync(ctx, sc); err != nil {
		s.log.Warn("memory folder not read", "scope", sc.Kind, "id", sc.ID, "err", err)
	}
	files, err := store.ListMemoryFiles(ctx, s.store.Pool, sc)
	return &memoryListOutput{Body: files}, err
}

func (s *Server) memoryGet(ctx context.Context, sc store.MemoryScope, name string) (*memoryFileOutput, error) {
	f, err := store.GetMemoryFile(ctx, s.store.Pool, sc, name)
	if err != nil {
		return nil, memoryErr(err)
	}
	return &memoryFileOutput{Body: f}, nil
}

func (s *Server) memoryPut(ctx context.Context, sc store.MemoryScope, name string, b memoryBody) (*memoryFileOutput, error) {
	f, _, err := store.WriteMemoryFile(ctx, s.store.Pool, sc, store.MemoryWrite{Name: name,
		Description: b.Description, Type: b.Type, Content: b.Content, By: "user"})
	if err != nil {
		return nil, memoryErr(err)
	}
	s.memory.Changed(sc)
	s.memoryEvent(ctx, sc, "write", f.Name)
	return &memoryFileOutput{Body: f}, nil
}

func (s *Server) memoryDelete(ctx context.Context, sc store.MemoryScope, name string) error {
	if err := s.memory.Delete(ctx, s.store.Pool, sc, name, "user"); err != nil {
		return memoryErr(err)
	}
	s.memory.Changed(sc) // MEMORY.md lost the file's line
	s.memoryEvent(ctx, sc, "delete", name)
	return nil
}

// memoryEvent refreshes the pages showing the scope's memory: the project's, or the user's own.
func (s *Server) memoryEvent(ctx context.Context, sc store.MemoryScope, op, name string) {
	payload := map[string]any{"op": op, "name": name}
	if sc.Kind == store.MemoryUser {
		s.events.User(ctx, sc.ID, "memory", payload)
		return
	}
	s.events.Project(ctx, sc.ID, "memory", payload)
}

func (s *Server) listMemory(ctx context.Context, in *projectIDInput) (*memoryListOutput, error) {
	sc, err := s.projectMemory(ctx, in.PID)
	if err != nil {
		return nil, err
	}
	return s.memoryList(ctx, sc)
}

func (s *Server) getMemoryFile(ctx context.Context, in *memoryNameInput) (*memoryFileOutput, error) {
	sc, err := s.projectMemory(ctx, in.PID)
	if err != nil {
		return nil, err
	}
	return s.memoryGet(ctx, sc, in.Name)
}

func (s *Server) putMemoryFile(ctx context.Context, in *memoryPutInput) (*memoryFileOutput, error) {
	sc, err := s.projectMemory(ctx, in.PID)
	if err != nil {
		return nil, err
	}
	return s.memoryPut(ctx, sc, in.Name, in.Body)
}

func (s *Server) deleteMemoryFile(ctx context.Context, in *memoryNameInput) (*struct{}, error) {
	sc, err := s.projectMemory(ctx, in.PID)
	if err != nil {
		return nil, err
	}
	return nil, s.memoryDelete(ctx, sc, in.Name)
}

func (s *Server) listUserMemory(ctx context.Context, _ *struct{}) (*memoryListOutput, error) {
	sc, err := s.userMemory(ctx)
	if err != nil {
		return nil, err
	}
	return s.memoryList(ctx, sc)
}

func (s *Server) getUserMemoryFile(ctx context.Context, in *userMemoryNameInput) (*memoryFileOutput, error) {
	sc, err := s.userMemory(ctx)
	if err != nil {
		return nil, err
	}
	return s.memoryGet(ctx, sc, in.Name)
}

func (s *Server) putUserMemoryFile(ctx context.Context, in *userMemoryPutInput) (*memoryFileOutput, error) {
	sc, err := s.userMemory(ctx)
	if err != nil {
		return nil, err
	}
	return s.memoryPut(ctx, sc, in.Name, in.Body)
}

func (s *Server) deleteUserMemoryFile(ctx context.Context, in *userMemoryNameInput) (*struct{}, error) {
	sc, err := s.userMemory(ctx)
	if err != nil {
		return nil, err
	}
	return nil, s.memoryDelete(ctx, sc, in.Name)
}

// memoryErr maps a rejected write to 422 and a missing file to 404.
func memoryErr(err error) error {
	var me *store.MemoryError
	if errors.As(err, &me) {
		return fail(http.StatusUnprocessableEntity, "validation", me.Msg)
	}
	return storeErr(err, "没有这个记忆文件")
}
