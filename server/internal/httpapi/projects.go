package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/touwaeriol/agent-teams/server/internal/github"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// ProjectView is api.md §2 ProjectView.
type ProjectView struct {
	ID                string    `json:"id"`
	Name              string    `json:"name"`
	Description       string    `json:"description"`
	Context           string    `json:"context"`
	Repo              *string   `json:"repo"`
	Branch            string    `json:"branch"`
	GitConnectionID   *string   `json:"git_connection_id"`
	Pinned            bool      `json:"pinned"`
	Archived          bool      `json:"archived"`
	WorkMode          string    `json:"work_mode" enum:"worktree,clone"`
	AutoApprove       bool      `json:"auto_approve"`
	RequireAcceptance bool      `json:"require_acceptance"`
	Delivery          Delivery  `json:"delivery"`
	OfficeLayout      string    `json:"office_layout"`
	Waiting           int       `json:"waiting"`
	ThreadsActive     int       `json:"threads_active"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

func (s *Server) projectView(p store.Project, defaults Delivery) ProjectView {
	return ProjectView{ID: p.ID, Name: p.Name, Description: p.Description, Context: p.Context, Repo: p.Repo,
		Branch: p.Branch, GitConnectionID: p.GitConnectionID, Pinned: p.Pinned, Archived: p.Archived,
		WorkMode: p.WorkMode, AutoApprove: p.AutoApprove, RequireAcceptance: p.RequireAcceptance,
		Delivery: overlay(defaults, p.Delivery), OfficeLayout: p.OfficeLayout, Waiting: p.Waiting,
		ThreadsActive: p.ThreadsActive, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt}
}

// ProjectInput is POST /api/projects.
type ProjectInput struct {
	Name              string         `json:"name" maxLength:"200"`
	Description       string         `json:"description,omitempty" maxLength:"2000"`
	Context           string         `json:"context,omitempty" maxLength:"50000"`
	Repo              *string        `json:"repo,omitempty" nullable:"true" doc:"GitHub URL, git@ URL or owner/repo"`
	Branch            string         `json:"branch,omitempty" maxLength:"255"`
	GitConnectionID   *string        `json:"git_connection_id,omitempty" nullable:"true"`
	WorkMode          string         `json:"work_mode,omitempty" enum:"worktree,clone"`
	AutoApprove       bool           `json:"auto_approve,omitempty"`
	RequireAcceptance *bool          `json:"require_acceptance,omitempty"`
	Delivery          *DeliveryPatch `json:"delivery,omitempty"`
	FirstMessage      string         `json:"first_message,omitempty" maxLength:"50000" doc:"Delivered to the project coordinator"`
}

// ProjectUpdate is PATCH /api/projects/{pid}.
type ProjectUpdate struct {
	Name              *string        `json:"name,omitempty" maxLength:"200"`
	Description       *string        `json:"description,omitempty" maxLength:"2000"`
	Context           *string        `json:"context,omitempty" maxLength:"50000"`
	Repo              *string        `json:"repo,omitempty" nullable:"true"`
	Branch            *string        `json:"branch,omitempty" maxLength:"255"`
	GitConnectionID   *string        `json:"git_connection_id,omitempty" nullable:"true"`
	ClearRepo         bool           `json:"clear_repo,omitempty"`
	Pinned            *bool          `json:"pinned,omitempty"`
	Archived          *bool          `json:"archived,omitempty"`
	WorkMode          *string        `json:"work_mode,omitempty" enum:"worktree,clone"`
	AutoApprove       *bool          `json:"auto_approve,omitempty"`
	RequireAcceptance *bool          `json:"require_acceptance,omitempty"`
	Delivery          *DeliveryPatch `json:"delivery,omitempty"`
	OfficeLayout      *string        `json:"office_layout,omitempty" doc:"'' = global default"`
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

// repoFields is the normalized repository part of a project.
type repoFields struct {
	Repo            *string
	Branch          string
	GitConnectionID *string
}

// checkRepo normalizes a repository the chosen connection can reach (v1 _checked).
func (s *Server) checkRepo(ctx context.Context, userID string, f repoFields, revalidate bool) (repoFields, error) {
	repo := strings.TrimSpace(deref(f.Repo))
	if repo == "" {
		return repoFields{}, nil
	}
	if deref(f.GitConnectionID) == "" {
		return f, invalid([]FieldError{{"git_connection_id", "关联仓库需要先选择一个 Git 连接"}}, "关联仓库需要先选择一个 Git 连接")
	}
	slug, err := github.ParseRepo(repo)
	if err != nil {
		return f, githubErr(err)
	}
	token, _, err := s.connectionToken(ctx, userID, *f.GitConnectionID)
	if err != nil {
		return f, err
	}
	branch := strings.TrimSpace(f.Branch)
	if !revalidate && branch != "" {
		return repoFields{Repo: &slug, Branch: branch, GitConnectionID: f.GitConnectionID}, nil
	}
	found, err := s.gh.Repo(ctx, token, slug)
	if err != nil {
		return f, githubErr(err)
	}
	if found.FullName != "" {
		slug = found.FullName
	}
	if branch == "" {
		branch = found.DefaultBranch
	}
	return repoFields{Repo: &slug, Branch: branch, GitConnectionID: f.GitConnectionID}, nil
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
	repo, err := s.checkRepo(ctx, p.User.ID, repoFields{b.Repo, b.Branch, b.GitConnectionID}, true)
	if err != nil {
		return nil, err
	}
	if err := s.checkAutoApprove(ctx, p.User.ID, b.AutoApprove); err != nil {
		return nil, err
	}
	row, err := s.store.CreateProject(ctx, p.User.ID, newProject(b, name, repo, s.deliveryDefault(ctx)))
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

func newProject(b ProjectInput, name string, repo repoFields, defaults Delivery) store.NewProject {
	raw, _ := json.Marshal(applyPatch(defaults, b.Delivery))
	requireAcceptance := true
	if b.RequireAcceptance != nil {
		requireAcceptance = *b.RequireAcceptance
	}
	workMode := b.WorkMode
	if workMode == "" {
		workMode = "worktree"
	}
	return store.NewProject{Name: name, Description: b.Description, Context: b.Context, Repo: repo.Repo,
		Branch: repo.Branch, GitConnectionID: repo.GitConnectionID, WorkMode: workMode, AutoApprove: b.AutoApprove,
		RequireAcceptance: requireAcceptance, Delivery: raw}
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
	row, err := s.store.UpdateProject(ctx, p.User.ID, in.PID, changes)
	if errors.Is(err, store.ErrConflict) {
		return nil, fail(http.StatusConflict, "project_exists", "已经有叫 "+deref(in.Body.Name)+" 的项目了")
	}
	if err != nil {
		return nil, storeErr(err, "没有这个项目")
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
	add(b.RequireAcceptance != nil, "require_acceptance", deref(b.RequireAcceptance))
	repoCh, err := s.repoChanges(ctx, userID, cur, b)
	if err != nil {
		return nil, err
	}
	settingCh, err := s.settingChanges(ctx, userID, cur, b)
	if err != nil {
		return nil, err
	}
	return append(append(ch, repoCh...), settingCh...), nil
}

func (s *Server) repoChanges(ctx context.Context, userID string, cur store.Project, b ProjectUpdate) ([]store.Change, error) {
	if b.ClearRepo {
		return []store.Change{{Col: "repo", Val: nil}, {Col: "branch", Val: ""}, {Col: "git_connection_id", Val: nil}}, nil
	}
	if b.Repo == nil && b.Branch == nil && b.GitConnectionID == nil {
		return nil, nil
	}
	merged := repoFields{Repo: cur.Repo, Branch: cur.Branch, GitConnectionID: cur.GitConnectionID}
	if b.Repo != nil {
		merged.Repo = b.Repo
	}
	if b.Branch != nil {
		merged.Branch = *b.Branch
	}
	if b.GitConnectionID != nil {
		merged.GitConnectionID = b.GitConnectionID
	}
	f, err := s.checkRepo(ctx, userID, merged, b.Repo != nil)
	if err != nil {
		return nil, err
	}
	return []store.Change{{Col: "repo", Val: f.Repo}, {Col: "branch", Val: f.Branch}, {Col: "git_connection_id", Val: f.GitConnectionID}}, nil
}

func (s *Server) settingChanges(ctx context.Context, userID string, cur store.Project, b ProjectUpdate) ([]store.Change, error) {
	var ch []store.Change
	if b.AutoApprove != nil {
		if err := s.checkAutoApprove(ctx, userID, *b.AutoApprove && !cur.AutoApprove); err != nil {
			return nil, err
		}
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

// checkAutoApprove: "全部放行" needs every candidate node at least sandbox-isolated (architecture.md §10.3).
func (s *Server) checkAutoApprove(ctx context.Context, userID string, turningOn bool) error {
	if !turningOn {
		return nil
	}
	nodes, err := s.candidateNodes(ctx, userID)
	if err != nil {
		return err
	}
	ok := len(nodes) > 0
	for _, n := range nodes {
		ok = ok && (n.Isolated || n.Isolation == "agent_user" || n.Isolation == "sandbox")
	}
	if !ok {
		return fail(http.StatusConflict, "isolation_required",
			"“全部放行”要求员工所在节点的隔离级别至少是 sandbox（或标记为 isolated）")
	}
	return nil
}

// candidateNodes are the nodes the user's roles may run on (all nodes when a role names none).
func (s *Server) candidateNodes(ctx context.Context, userID string) ([]store.Node, error) {
	nodes, err := s.store.ListNodes(ctx, userID)
	if err != nil {
		return nil, err
	}
	roles, err := s.store.Roles(ctx, userID)
	if err != nil {
		return nil, err
	}
	wanted := map[string]bool{}
	for _, r := range roles {
		if len(r.NodeIDs) == 0 {
			return nodes, nil
		}
		for _, id := range r.NodeIDs {
			wanted[id] = true
		}
	}
	if len(wanted) == 0 {
		return nodes, nil
	}
	var out []store.Node
	for _, n := range nodes {
		if wanted[n.ID] {
			out = append(out, n)
		}
	}
	return out, nil
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
	s.events.User(ctx, p.User.ID, "project", map[string]any{"project_id": in.PID, "op": "delete"})
	return nil, nil
}
