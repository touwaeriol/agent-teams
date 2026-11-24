package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/danielgtaylor/huma/v2"

	"github.com/touwaeriol/agent-teams/server/internal/platform"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// Sessions (多仓库会话): a lightweight entry next to projects. A session is a project of kind
// session: one employee works in a directory holding every chosen repository side by side and
// the user talks to it directly (one thread, no coordinator, no members).

// SessionRepoRef is a repository of a session with the branch the employee starts from.
type SessionRepoRef struct {
	RepoRef
	Branch string `json:"branch,omitempty" maxLength:"255" doc:"Branch the employee's work branch is cut from; '' = the repository's default branch"`
}

// SessionInput is POST /api/sessions.
type SessionInput struct {
	Message string           `json:"message" minLength:"1" maxLength:"50000" doc:"The task: the employee's first message"`
	Title   string           `json:"title,omitempty" maxLength:"200" doc:"'' = the first line of message"`
	Repos   []SessionRepoRef `json:"repos" maxItems:"20" doc:"Repositories, all checked out side by side in the employee's directory"`
	Role    string           `json:"role,omitempty" maxLength:"64" doc:"Member role template the employee is made from; '' = the first member role"`
	Model   string           `json:"model,omitempty" maxLength:"100" doc:"'' = the role's model"`
	NodeID  string           `json:"node_id,omitempty" maxLength:"64" doc:"Run on this node; '' = picked automatically (most quota left)"`
}

// WorkSessionView is the 多仓库会话 just created (SessionView is the login session).
type WorkSessionView struct {
	Project  ProjectView `json:"project"`
	ThreadID string      `json:"thread_id"`
	ThreadNo int         `json:"thread_no"`
}

type createSessionInput struct{ Body SessionInput }

type sessionOutput struct{ Body WorkSessionView }

type sessionReposInput struct {
	PID  string `path:"pid" maxLength:"64"`
	Body struct {
		Repos []SessionRepoRef `json:"repos" maxItems:"20" doc:"The session's repositories after the change (order kept); new ones are checked out, missing ones removed"`
	}
}

func (s *Server) registerSessions(api huma.API) {
	tags := []string{"sessions"}
	huma.Register(api, huma.Operation{OperationID: "create-session", Method: "POST", Path: "/api/sessions", Tags: tags,
		DefaultStatus: 201, Summary: "Start a session: one employee working in several repositories, talked to directly"}, s.createSession)
	huma.Register(api, huma.Operation{OperationID: "set-session-repos", Method: "PUT", Path: "/api/sessions/{pid}/repos", Tags: tags,
		Summary: "Add or remove a session's repositories while it runs"}, s.setSessionRepos)
}

func (s *Server) createSession(ctx context.Context, in *createSessionInput) (*sessionOutput, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	b := in.Body
	message := strings.TrimSpace(b.Message)
	if message == "" {
		return nil, invalid([]FieldError{{"message", "请写下要做什么"}}, "请写下要做什么")
	}
	if len(b.Repos) == 0 {
		return nil, invalid([]FieldError{{"repos", "至少选一个仓库"}}, "至少选一个仓库")
	}
	start, err := s.sessionStart(ctx, p.User.ID, b)
	if err != nil {
		return nil, err
	}
	repos, err := s.checkRepos(ctx, p.User.ID, refsOf(b.Repos), nil)
	if err != nil {
		return nil, err
	}
	start.Message, start.Repos = message, sessionRepos(repos, b.Repos)
	row, err := s.createSessionProject(ctx, p.User.ID, start.Title, repos)
	if err != nil {
		return nil, err
	}
	t, err := s.hooks.SessionCreated(ctx, row, start)
	if err != nil {
		_ = s.store.DeleteProject(ctx, p.User.ID, row.ID)
		return nil, err
	}
	s.events.User(ctx, p.User.ID, "project", map[string]any{"project_id": row.ID, "op": "create"})
	row, err = s.store.ProjectOf(ctx, p.User.ID, row.ID)
	if err != nil {
		return nil, err
	}
	return &sessionOutput{Body: WorkSessionView{Project: s.projectView(row, s.deliveryDefault(ctx)), ThreadID: t.ID, ThreadNo: t.No}}, nil
}

// sessionStart checks the role and the node and names the session.
func (s *Server) sessionStart(ctx context.Context, userID string, b SessionInput) (platform.SessionStart, error) {
	role, err := s.sessionRole(ctx, userID, strings.TrimSpace(b.Role))
	if err != nil {
		return platform.SessionStart{}, err
	}
	if b.NodeID != "" {
		if _, err := s.store.NodeOf(ctx, userID, b.NodeID); err != nil {
			return platform.SessionStart{}, invalid([]FieldError{{"node_id", "没有这个节点"}}, "没有这个节点")
		}
	}
	title := strings.TrimSpace(b.Title)
	if title == "" {
		title = titleOf(b.Message)
	}
	return platform.SessionStart{Title: title, Role: role, Model: strings.TrimSpace(b.Model), NodeID: b.NodeID}, nil
}

// sessionRole is the user's member role template named name (the first member role for ”).
func (s *Server) sessionRole(ctx context.Context, userID, name string) (store.Role, error) {
	roles, err := s.store.Roles(ctx, userID)
	if err != nil {
		return store.Role{}, err
	}
	for _, r := range roles {
		if r.Tier == "member" && (name == "" || r.Role == name) {
			return r, nil
		}
	}
	msg := "没有这个岗位：" + name
	if name == "" {
		msg = "团队里还没有成员岗位，先在设置里添加一个"
	}
	return store.Role{}, invalid([]FieldError{{"role", msg}}, msg)
}

// sessionTitleRunes bounds a title taken from the first message.
const sessionTitleRunes = 40

// titleOf is the first non-empty line of the message, shortened.
func titleOf(message string) string {
	line := strings.TrimSpace(message)
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = strings.TrimSpace(line[:i])
	}
	if utf8.RuneCountInString(line) > sessionTitleRunes {
		line = string([]rune(line)[:sessionTitleRunes]) + "…"
	}
	return line
}

// createSessionProject stores the session; a name another project or session has gets a number.
func (s *Server) createSessionProject(ctx context.Context, userID, title string, repos []store.ProjectRepo) (store.Project, error) {
	np := store.NewProject{Name: title, Repos: repos, WorkMode: "worktree", Kind: store.KindSession, Delivery: []byte(`{}`)}
	for i := 2; i <= 50; i++ {
		row, err := s.store.CreateProject(ctx, userID, np)
		if !errors.Is(err, store.ErrConflict) {
			return row, err
		}
		np.Name = fmt.Sprintf("%s (%d)", title, i)
	}
	return store.Project{}, fail(http.StatusConflict, "project_exists", "同名的会话太多了，换个标题吧")
}

func refsOf(in []SessionRepoRef) []RepoRef {
	out := make([]RepoRef, len(in))
	for i, r := range in {
		out[i] = r.RepoRef
	}
	return out
}

// sessionRepos pairs the checked repositories (same order as the input) with their branches.
func sessionRepos(repos []store.ProjectRepo, in []SessionRepoRef) []platform.SessionRepo {
	out := make([]platform.SessionRepo, len(repos))
	for i, r := range repos {
		out[i] = platform.SessionRepo{Repo: r.Repo, Branch: strings.TrimSpace(in[i].Branch)}
	}
	return out
}

func (s *Server) setSessionRepos(ctx context.Context, in *sessionReposInput) (*projectOutput, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	cur, err := s.store.ProjectOf(ctx, p.User.ID, in.PID)
	if err != nil || !cur.Session() {
		return nil, storeErr(orNotFound(err), "没有这个会话")
	}
	if len(in.Body.Repos) == 0 {
		return nil, invalid([]FieldError{{"repos", "至少留一个仓库"}}, "至少留一个仓库")
	}
	repos, err := s.checkRepos(ctx, p.User.ID, refsOf(in.Body.Repos), cur.Repos)
	if err != nil {
		return nil, err
	}
	row, err := s.store.SetProjectRepos(ctx, p.User.ID, in.PID, repos)
	if err != nil {
		return nil, storeErr(err, "没有这个会话")
	}
	added, removed := repoDiff(cur.Repos, sessionRepos(repos, in.Body.Repos))
	if err := s.hooks.SessionReposChanged(ctx, row.ID, added, removed); err != nil {
		return nil, err
	}
	s.events.User(ctx, p.User.ID, "project", map[string]any{"project_id": row.ID, "op": "update"})
	return &projectOutput{Body: s.projectView(row, s.deliveryDefault(ctx))}, nil
}

func orNotFound(err error) error {
	if err == nil {
		return store.ErrNotFound
	}
	return err
}

// repoDiff: the repositories in next and not in cur (with their branches), and the other way round.
func repoDiff(cur []store.ProjectRepo, next []platform.SessionRepo) ([]platform.SessionRepo, []string) {
	had := map[string]bool{}
	for _, r := range cur {
		had[strings.ToLower(r.Repo)] = true
	}
	keep := map[string]bool{}
	var added []platform.SessionRepo
	for _, r := range next {
		keep[strings.ToLower(r.Repo)] = true
		if !had[strings.ToLower(r.Repo)] {
			added = append(added, r)
		}
	}
	var removed []string
	for _, r := range cur {
		if !keep[strings.ToLower(r.Repo)] {
			removed = append(removed, r.Repo)
		}
	}
	return added, removed
}
