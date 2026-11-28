package httpapi

import (
	"context"
	"errors"
	"net/http"
	"slices"

	"github.com/danielgtaylor/huma/v2"

	"github.com/touwaeriol/agent-teams/server/internal/store"
	"github.com/touwaeriol/agent-teams/server/internal/workflows"
)

// WorkflowsBody is GET /api/workflows.
type WorkflowsBody struct {
	Workflows []workflows.Flow `json:"workflows"`
}

type workflowsOutput struct{ Body WorkflowsBody }

type workflowOutput struct{ Body workflows.Flow }

type workflowDraftInput struct{ Body workflows.FlowDraft }

type workflowIDInput struct {
	ID string `path:"id" maxLength:"64"`
}

type workflowUpdateInput struct {
	ID   string `path:"id" maxLength:"64"`
	Body workflows.FlowDraft
}

func (s *Server) registerWorkflows(api huma.API) {
	tags := []string{"team"}
	huma.Register(api, huma.Operation{OperationID: "list-workflows", Method: "GET", Path: "/api/workflows", Tags: tags,
		Summary: "My workflows (流程图), the default first"}, s.listWorkflows)
	huma.Register(api, huma.Operation{OperationID: "create-workflow", Method: "POST", Path: "/api/workflows", Tags: tags,
		DefaultStatus: http.StatusCreated, Summary: "Add a workflow (the first one becomes the default)"}, s.createWorkflow)
	huma.Register(api, huma.Operation{OperationID: "update-workflow", Method: "PUT", Path: "/api/workflows/{id}", Tags: tags,
		Summary: "Replace a workflow's name, steps and edges"}, s.updateWorkflow)
	huma.Register(api, huma.Operation{OperationID: "delete-workflow", Method: "DELETE", Path: "/api/workflows/{id}", Tags: tags,
		DefaultStatus: http.StatusNoContent, Summary: "Delete a workflow; projects that picked it fall back to the default"}, s.deleteWorkflow)
}

func (s *Server) listWorkflows(ctx context.Context, _ *struct{}) (*workflowsOutput, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	list, err := workflows.List(ctx, s.store.Pool, p.User.ID)
	return &workflowsOutput{Body: WorkflowsBody{Workflows: list}}, err
}

// memberRoleKeys is the set of the user's member role keys (what a step may name besides lead).
func (s *Server) memberRoleKeys(ctx context.Context, userID string) (map[string]bool, error) {
	list, err := s.ensureRoles(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, r := range list {
		if r.Tier == "member" {
			out[r.Role] = true
		}
	}
	return out, nil
}

func (s *Server) checkedDraft(ctx context.Context, userID string, d workflows.FlowDraft) (workflows.FlowDraft, error) {
	workflows.Normalize(&d)
	roles, err := s.memberRoleKeys(ctx, userID)
	if err != nil {
		return d, err
	}
	return d, workflowError(workflows.Validate(d, roles))
}

func (s *Server) createWorkflow(ctx context.Context, in *workflowDraftInput) (*workflowOutput, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	d, err := s.checkedDraft(ctx, p.User.ID, in.Body)
	if err != nil {
		return nil, err
	}
	w, err := workflows.Create(ctx, s.store.Pool, p.User.ID, d)
	if err != nil {
		return nil, workflowError(err)
	}
	s.audit(ctx, p.User.ID, "workflow_create", "workflow", w.ID, map[string]string{"name": w.Name})
	return &workflowOutput{Body: w}, nil
}

func (s *Server) updateWorkflow(ctx context.Context, in *workflowUpdateInput) (*workflowOutput, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	d, err := s.checkedDraft(ctx, p.User.ID, in.Body)
	if err != nil {
		return nil, err
	}
	w, err := workflows.Update(ctx, s.store.Pool, p.User.ID, in.ID, d)
	if err != nil {
		return nil, workflowError(err)
	}
	return &workflowOutput{Body: w}, nil
}

func (s *Server) deleteWorkflow(ctx context.Context, in *workflowIDInput) (*struct{}, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	if err := workflows.Delete(ctx, s.store.Pool, p.User.ID, in.ID); err != nil {
		return nil, workflowError(err)
	}
	s.audit(ctx, p.User.ID, "workflow_delete", "workflow", in.ID, nil)
	return nil, nil
}

func workflowError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, workflows.ErrNotFound):
		return fail(http.StatusNotFound, "not_found", "没有这个流程")
	case workflows.IsProblem(err):
		return fail(http.StatusUnprocessableEntity, "validation", err.Error())
	}
	return err
}

// workflowChange validates a project's flow pick: ” (the default), none, or one of the
// user's flows.
func (s *Server) workflowChange(ctx context.Context, userID string, picks *[]string) ([]store.Change, error) {
	if picks == nil {
		return nil, nil
	}
	ids := uniqueStrings(*picks)
	if slices.Contains(ids, workflows.ProjectNone) && len(ids) > 1 {
		return nil, invalid([]FieldError{{"workflows", "选了“不用流程”就不能再选别的流程"}}, "选了“不用流程”就不能再选别的流程")
	}
	if len(ids) > workflows.MaxPerProject {
		return nil, invalid([]FieldError{{"workflows", "一个项目最多选 10 个流程"}}, "一个项目最多选 10 个流程")
	}
	for _, id := range ids {
		if id == workflows.ProjectNone {
			continue
		}
		if _, err := workflows.Get(ctx, s.store.Pool, userID, id); err != nil {
			if errors.Is(err, workflows.ErrNotFound) {
				return nil, invalid([]FieldError{{"workflows", "没有这个流程"}}, "没有这个流程："+id)
			}
			return nil, err
		}
	}
	return []store.Change{{Col: "workflows", Val: store.TextArray(ids)}}, nil
}

// uniqueStrings keeps the first of each value, in order (never nil).
func uniqueStrings(in []string) []string {
	out := []string{}
	for _, v := range in {
		if !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}

func nonNilStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}
