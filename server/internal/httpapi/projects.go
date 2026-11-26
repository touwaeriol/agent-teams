package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// ProjectView is api.md §2 ProjectView.
type ProjectView struct {
	ID               string    `json:"id"`
	Name             string    `json:"name"`
	Description      string    `json:"description"`
	Context          string    `json:"context" doc:"项目指令: the owner's instructions (like CLAUDE.md), read by every session"`
	Repos            []RepoRef `json:"repos" doc:"Bound repositories in project order; base branches belong in context (the project instructions)"`
	Pinned           bool      `json:"pinned"`
	Archived         bool      `json:"archived"`
	WorkMode         string    `json:"work_mode" enum:"worktree,clone"`
	AutoApprove      bool      `json:"auto_approve"`
	MergePolicy      string    `json:"merge_policy" enum:"pr,auto,manual" doc:"How a thread with PRs ends: pr = done once the checks pass, the PRs stay open for a person to merge; auto = merged into the base once the checks pass; manual = the user accepts"`
	Delivery         Delivery  `json:"delivery"`
	OfficeLayout     string    `json:"office_layout"`
	Icon             string    `json:"icon" doc:"'' = initial on the default color; an emoji; or color:#RRGGBB"`
	CoordinatorModel string    `json:"coordinator_model" doc:"Model of the coordinator; '' = the coordinator role's model"`
	ThreadModel      string    `json:"thread_model" doc:"Claude model of Claude leads and members; '' = each role's model"`
	ThreadCodexModel string    `json:"thread_codex_model" doc:"Codex model of Codex leads and members; '' = each role's model"`
	ThreadEffort     string    `json:"thread_effort" doc:"'' = each role's effort; low | medium | high | xhigh | max"`
	AutoResume       bool      `json:"auto_resume" doc:"Continue threads paused by a usage limit once it resets"`
	Kind             string    `json:"kind" enum:"project,session" doc:"project, or session (多仓库会话: one employee the user talks to directly in thread 1; no coordinator)"`
	Waiting          int       `json:"waiting"`
	ThreadsActive    int       `json:"threads_active"`
	SessionStatus    string    `json:"session_status" doc:"A session's state for the sidebar ('' for a project), the same rule as the session page: working (in a turn, waking or using a tool), finished (not in a turn and nothing to answer: 已完成), resolved (the user confirmed: 已解决), attention (node offline, session crashed or no node can run it: 等你处理), queued (waiting for a slot), or the thread's status (queued, waiting, review, paused, failed). Open cards are in waiting"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

func (s *Server) projectView(p store.Project, defaults Delivery) ProjectView {
	return ProjectViewOf(p, overlay(defaults, p.Delivery))
}

// ProjectViewOf renders a project with its effective delivery settings.
func ProjectViewOf(p store.Project, d Delivery) ProjectView {
	return ProjectView{ID: p.ID, Name: p.Name, Description: p.Description, Context: p.Context, Repos: RepoRefs(p.Repos),
		Pinned: p.Pinned, Archived: p.Archived,
		WorkMode: p.WorkMode, AutoApprove: p.AutoApprove, MergePolicy: p.MergePolicy,
		Delivery: d, OfficeLayout: p.OfficeLayout, Icon: p.Icon, CoordinatorModel: p.CoordinatorModel,
		ThreadModel: p.ThreadModel, ThreadCodexModel: p.ThreadCodexModel, ThreadEffort: p.ThreadEffort, AutoResume: p.AutoResume, Kind: p.Kind, Waiting: p.Waiting,
		ThreadsActive: p.ThreadsActive, SessionStatus: p.SessionStatus, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt}
}

// ProjectInput is POST /api/projects.
type ProjectInput struct {
	Name             string         `json:"name" maxLength:"200"`
	Description      string         `json:"description,omitempty" maxLength:"2000"`
	Context          string         `json:"context,omitempty" maxLength:"16000" doc:"项目指令"`
	Repos            []RepoRef      `json:"repos,omitempty" maxItems:"20"`
	WorkMode         string         `json:"work_mode,omitempty" enum:"worktree,clone"`
	AutoApprove      bool           `json:"auto_approve,omitempty"`
	MergePolicy      string         `json:"merge_policy,omitempty" enum:"pr,auto,manual" doc:"Default pr"`
	Delivery         *DeliveryPatch `json:"delivery,omitempty"`
	FirstMessage     string         `json:"first_message,omitempty" maxLength:"50000" doc:"Delivered to the project coordinator"`
	CoordinatorModel string         `json:"coordinator_model,omitempty" maxLength:"100" doc:"Model of the coordinator; '' = the coordinator role's model"`
	ThreadModel      string         `json:"thread_model,omitempty" maxLength:"100" doc:"Claude model of Claude leads and members; '' = each role's model"`
	ThreadCodexModel string         `json:"thread_codex_model,omitempty" maxLength:"100" doc:"Codex model of Codex leads and members; '' = each role's model"`
}

// ProjectUpdate is PATCH /api/projects/{pid}.
type ProjectUpdate struct {
	Name             *string        `json:"name,omitempty" maxLength:"200"`
	Description      *string        `json:"description,omitempty" maxLength:"2000"`
	Context          *string        `json:"context,omitempty" maxLength:"16000" doc:"项目指令"`
	Repos            *[]RepoRef     `json:"repos,omitempty" maxItems:"20" doc:"Replaces the bound repositories (order kept); [] unbinds all"`
	Pinned           *bool          `json:"pinned,omitempty"`
	Archived         *bool          `json:"archived,omitempty"`
	WorkMode         *string        `json:"work_mode,omitempty" enum:"worktree,clone"`
	AutoApprove      *bool          `json:"auto_approve,omitempty"`
	MergePolicy      *string        `json:"merge_policy,omitempty" enum:"pr,auto,manual"`
	Delivery         *DeliveryPatch `json:"delivery,omitempty"`
	OfficeLayout     *string        `json:"office_layout,omitempty" doc:"'' = global default"`
	Icon             *string        `json:"icon,omitempty" maxLength:"32" doc:"'' = initial; an emoji; or color:#RRGGBB"`
	CoordinatorModel *string        `json:"coordinator_model,omitempty" maxLength:"100" doc:"Model of the coordinator; '' = the coordinator role's model"`
	ThreadModel      *string        `json:"thread_model,omitempty" maxLength:"100" doc:"Claude model of Claude leads and members; '' = each role's model"`
	ThreadCodexModel *string        `json:"thread_codex_model,omitempty" maxLength:"100" doc:"Codex model of Codex leads and members; '' = each role's model"`
	ThreadEffort     *string        `json:"thread_effort,omitempty" maxLength:"16" doc:"'' | low | medium | high | xhigh | max"`
	AutoResume       *bool          `json:"auto_resume,omitempty"`
}

type projectsInput struct {
	Q string `query:"q" maxLength:"200"`
}

type projectIDInput struct {
	PID string `path:"pid" maxLength:"64"`
}

type projectOutput struct{ Body ProjectView }

type projectsOutput struct{ Body []ProjectView }

type createProjectInput struct{ Body ProjectInput }

type updateProjectInput struct {
	PID  string `path:"pid" maxLength:"64"`
	Body ProjectUpdate
}

func (s *Server) registerProjects(api huma.API) {
	tags := []string{"projects"}
	huma.Register(api, huma.Operation{OperationID: "list-projects", Method: "GET", Path: "/api/projects", Tags: tags,
		Summary: "My projects, pinned first"}, s.listProjects)
	huma.Register(api, huma.Operation{OperationID: "create-project", Method: "POST", Path: "/api/projects", Tags: tags,
		DefaultStatus: 201}, s.createProject)
	huma.Register(api, huma.Operation{OperationID: "get-project", Method: "GET", Path: "/api/projects/{pid}", Tags: tags}, s.getProject)
	huma.Register(api, huma.Operation{OperationID: "update-project", Method: "PATCH", Path: "/api/projects/{pid}", Tags: tags,
		Summary: "Rename, pin, archive, change repository and settings"}, s.updateProject)
	huma.Register(api, huma.Operation{OperationID: "delete-project", Method: "DELETE", Path: "/api/projects/{pid}", Tags: tags,
		DefaultStatus: 204}, s.deleteProject)
}

func (s *Server) listProjects(ctx context.Context, in *projectsInput) (*projectsOutput, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.store.ListProjects(ctx, p.User.ID, strings.TrimSpace(in.Q))
	if err != nil {
		return nil, err
	}
	defaults := s.deliveryDefault(ctx)
	out := make([]ProjectView, len(rows))
	for i, row := range rows {
		out[i] = s.projectView(row, defaults)
	}
	return &projectsOutput{Body: out}, nil
}

func (s *Server) getProject(ctx context.Context, in *projectIDInput) (*projectOutput, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	row, err := s.store.ProjectOf(ctx, p.User.ID, in.PID)
	if err != nil {
		return nil, storeErr(err, "没有这个项目")
	}
	return &projectOutput{Body: s.projectView(row, s.deliveryDefault(ctx))}, nil
}

func (s *Server) createProject(ctx context.Context, in *createProjectInput) (*projectOutput, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	b := in.Body
	name := strings.TrimSpace(b.Name)
	if name == "" {
		return nil, invalid([]FieldError{{"name", "请填写项目名称"}}, "请填写项目名称")
	}
	repos, err := s.checkRepos(ctx, p.User.ID, b.Repos, nil)
	if err != nil {
		return nil, err
	}
	np := newProject(b, name, repos, s.deliveryDefault(ctx))
	if err := checkProjectModels([]projectModel{{"coordinator_model", &np.CoordinatorModel},
		{"thread_model", &np.ThreadModel}, {"thread_codex_model", &np.ThreadCodexModel}}); err != nil {
		return nil, err
	}
	row, err := s.store.CreateProject(ctx, p.User.ID, np)
	if errors.Is(err, store.ErrConflict) {
		return nil, fail(http.StatusConflict, "project_exists", "已经有叫 "+name+" 的项目了")
	}
	if err != nil {
		return nil, err
	}
	if err := s.hooks.ProjectCreated(ctx, row, strings.TrimSpace(b.FirstMessage)); err != nil {
		s.log.Warn("project created hook", "project_id", row.ID, "err", err)
	}
	s.events.User(ctx, p.User.ID, "project", map[string]any{"project_id": row.ID, "op": "create"})
	return &projectOutput{Body: s.projectView(row, s.deliveryDefault(ctx))}, nil
}

func newProject(b ProjectInput, name string, repos []store.ProjectRepo, defaults Delivery) store.NewProject {
	raw, _ := json.Marshal(applyPatch(defaults, b.Delivery))
	workMode := b.WorkMode
	if workMode == "" {
		workMode = "worktree"
	}
	return store.NewProject{Name: name, Description: b.Description, Context: b.Context, Repos: repos,
		WorkMode: workMode, AutoApprove: b.AutoApprove,
		MergePolicy: b.MergePolicy, Delivery: raw, CoordinatorModel: strings.TrimSpace(b.CoordinatorModel),
		ThreadModel: strings.TrimSpace(b.ThreadModel), ThreadCodexModel: strings.TrimSpace(b.ThreadCodexModel)}
}

func (s *Server) updateProject(ctx context.Context, in *updateProjectInput) (*projectOutput, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	current, err := s.store.ProjectOf(ctx, p.User.ID, in.PID)
	if err != nil {
		return nil, storeErr(err, "没有这个项目")
	}
	changes, err := s.projectChanges(ctx, p.User.ID, current, in.Body)
	if err != nil {
		return nil, err
	}
	repos, err := s.patchRepos(ctx, p.User.ID, current, in.Body.Repos)
	if err != nil {
		return nil, err
	}
	row, err := s.store.UpdateProject(ctx, p.User.ID, in.PID, changes)
	if err == nil && repos != nil {
		row, err = s.store.SetProjectRepos(ctx, p.User.ID, in.PID, repos)
	}
	if errors.Is(err, store.ErrConflict) {
		return nil, fail(http.StatusConflict, "project_exists", "已经有叫 "+deref(in.Body.Name)+" 的项目了")
	}
	if err != nil {
		return nil, storeErr(err, "没有这个项目")
	}
	if row.Archived && !current.Archived {
		if err := s.hooks.ProjectArchived(ctx, p.User.ID, row.ID); err != nil {
			return nil, err
		}
	}
	s.events.User(ctx, p.User.ID, "project", map[string]any{"project_id": row.ID, "op": "update"})
	return &projectOutput{Body: s.projectView(row, s.deliveryDefault(ctx))}, nil
}

func (s *Server) projectChanges(ctx context.Context, userID string, cur store.Project, b ProjectUpdate) ([]store.Change, error) {
	var ch []store.Change
	add := func(cond bool, col string, val any) {
		if cond {
			ch = append(ch, store.Change{Col: col, Val: val})
		}
	}
	if b.Name != nil && strings.TrimSpace(*b.Name) == "" {
		return nil, invalid([]FieldError{{"name", "请填写项目名称"}}, "请填写项目名称")
	}
	add(b.Name != nil, "name", strings.TrimSpace(deref(b.Name)))
	add(b.Description != nil, "description", deref(b.Description))
	add(b.Context != nil, "context", deref(b.Context))
	add(b.Pinned != nil, "pinned", deref(b.Pinned))
	add(b.Archived != nil, "archived", deref(b.Archived))
	add(b.WorkMode != nil, "work_mode", deref(b.WorkMode))
	add(b.MergePolicy != nil, "merge_policy", deref(b.MergePolicy))
	add(b.AutoResume != nil, "auto_resume", deref(b.AutoResume))
	chromeCh, err := chromeChanges(b)
	if err != nil {
		return nil, err
	}
	modelCh, err := modelChanges(b)
	if err != nil {
		return nil, err
	}
	ch = append(ch, modelCh...)
	ch = append(ch, chromeCh...)
	settingCh, err := s.settingChanges(ctx, userID, cur, b)
	if err != nil {
		return nil, err
	}
	return append(ch, settingCh...), nil
}

func (s *Server) settingChanges(ctx context.Context, userID string, cur store.Project, b ProjectUpdate) ([]store.Change, error) {
	var ch []store.Change
	if b.AutoApprove != nil {
		ch = append(ch, store.Change{Col: "auto_approve", Val: *b.AutoApprove})
		if *b.AutoApprove != cur.AutoApprove {
			s.audit(ctx, userID, "auto_approve_toggle", "project", cur.ID, map[string]bool{"on": *b.AutoApprove})
		}
	}
	if b.Delivery != nil {
		merged := applyPatch(overlay(s.deliveryDefault(ctx), cur.Delivery), b.Delivery)
		raw, _ := json.Marshal(merged)
		ch = append(ch, store.Change{Col: "delivery", Val: raw})
	}
	if b.OfficeLayout != nil {
		if _, err := s.office.Get(*b.OfficeLayout); *b.OfficeLayout != "" && err != nil {
			return nil, invalid([]FieldError{{"office_layout", "没有这个布局"}}, "没有这个布局："+*b.OfficeLayout)
		}
		ch = append(ch, store.Change{Col: "office_layout", Val: *b.OfficeLayout})
	}
	return ch, nil
}

func (s *Server) deleteProject(ctx context.Context, in *projectIDInput) (*struct{}, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.store.ProjectOf(ctx, p.User.ID, in.PID); err != nil {
		return nil, storeErr(err, "没有这个项目")
	}
	if err := s.hooks.ProjectDeleting(ctx, p.User.ID, in.PID); err != nil {
		return nil, err
	}
	if err := s.store.DeleteProject(ctx, p.User.ID, in.PID); err != nil {
		return nil, storeErr(err, "没有这个项目")
	}
	s.hooks.ProjectDeleted(ctx, p.User.ID, in.PID)
	s.events.User(ctx, p.User.ID, "project", map[string]any{"project_id": in.PID, "op": "delete"})
	return nil, nil
}
