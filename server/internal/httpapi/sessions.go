package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/danielgtaylor/huma/v2"

	"github.com/touwaeriol/agent-teams/server/internal/platform"
	"github.com/touwaeriol/agent-teams/server/internal/runtimes"
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
	Repos   []SessionRepoRef `json:"repos" maxItems:"20" doc:"Repositories, all checked out side by side in the employee's directory; [] = none: the employee works in a local folder on the node it starts on, pinned there"`
	Role    string           `json:"role,omitempty" maxLength:"64" doc:"Member role template the employee is made from; '' = the first member role"`
	Runtime string           `json:"runtime,omitempty" maxLength:"32" doc:"The agent (CLI) the session runs on: one of the role's runtimes; '' = the role's (several: spread like any employee). Fixed once the session starts"`
	Model   string           `json:"model,omitempty" maxLength:"100" doc:"'' = the role's model for the runtime"`
	// Session controls (#53): picked on the new-session page; GET /api/session-options lists them.
	PermissionMode string `json:"permission_mode,omitempty" enum:"auto,default,acceptEdits,plan,bypassPermissions" doc:"'' = the initial mode (Claude: auto when the model supports it, else default; Codex/Antigravity: the role's)"`
	Effort         string `json:"effort,omitempty" maxLength:"32" doc:"Thinking effort; '' = project / role setting"`
	NodeID         string `json:"node_id,omitempty" maxLength:"64" doc:"Run on this node; '' = picked automatically (spread over the nodes by load score; nodes whose quota is tight only when all are)"`
	Branch         string `json:"branch,omitempty" maxLength:"60" doc:"Name of the work branch (after session/<id>-); '' = from the title, or the repositories when the title has no Latin letters"`
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
		Repos []SessionRepoRef `json:"repos" maxItems:"20" doc:"The session's repositories after the change (order kept); new ones are checked out, missing ones removed; [] = none (the session goes on in its local folder, pinned to its node)"`
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
	// No repository: the employee works in a local folder on the node it starts on (#53).
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
	var node *store.Node
	if b.NodeID != "" {
		n, err := s.store.NodeOf(ctx, userID, b.NodeID)
		if err != nil {
			return platform.SessionStart{}, invalid([]FieldError{{"node_id", "没有这个节点"}}, "没有这个节点")
		}
		node = &n
	}
	title := strings.TrimSpace(b.Title)
	if title == "" {
		title = titleOf(b.Message)
	}
	rt := strings.TrimSpace(b.Runtime)
	if rt != "" && !slices.Contains(role.RuntimeList(), rt) {
		msg := "这个岗位没有配置 " + runtimes.Of(rt).Title
		if runtimes.Of(rt).ID != rt {
			msg = "没有这个 agent：" + rt
		}
		return platform.SessionStart{}, invalid([]FieldError{{"runtime", msg}}, msg)
	}
	sessRT := rt
	if sessRT == "" {
		sessRT = role.Runtime
	}
	if node != nil && (rt != "" || len(role.RuntimeList()) == 1) {
		if msg, err := s.nodeCannotRun(ctx, *node, sessRT); err != nil || msg != "" {
			if err != nil {
				return platform.SessionStart{}, err
			}
			return platform.SessionStart{}, invalid([]FieldError{{"node_id", msg}}, msg)
		}
	}
	if err := checkSessionControls(role, sessRT, b.PermissionMode, b.Effort); err != nil {
		return platform.SessionStart{}, err
	}
	return platform.SessionStart{Title: title, Role: role, Runtime: rt, Model: strings.TrimSpace(b.Model), PermissionMode: b.PermissionMode,
		Effort: strings.TrimSpace(b.Effort), NodeID: b.NodeID, Branch: strings.TrimSpace(b.Branch)}, nil
}

// nodeCannotRun says why a session of runtime cannot run on the node the user picked ("" when it
// can): a runtime that needs an isolated node on one that is not, or a daemon that reports its CLIs
// (at_node_runtimes) without that one installed and logged in. A daemon that never reported is
// not judged (the session waits for it as before).
func (s *Server) nodeCannotRun(ctx context.Context, n store.Node, runtime string) (string, error) {
	rt := runtimes.Of(runtime)
	if !runtimes.RunsOn(rt.ID, n.Isolated, n.Isolation) {
		return fmt.Sprintf("节点 %s 没有隔离运行，不能跑 %s", n.Name, rt.Title), nil
	}
	rows, err := s.store.NodeRuntimes(ctx, []string{n.ID})
	if err != nil || len(rows[n.ID]) == 0 {
		return "", err
	}
	for _, r := range rows[n.ID] {
		if r.Runtime != rt.ID {
			continue
		}
		switch {
		case !r.Installed:
			return fmt.Sprintf("节点 %s 上没有安装 %s", n.Name, rt.Title), nil
		case r.Expired:
			return fmt.Sprintf("节点 %s 上 %s 的登录已过期", n.Name, rt.Title), nil
		case !r.Ready(time.Now()):
			return fmt.Sprintf("节点 %s 上 %s 没有登录", n.Name, rt.Title), nil
		}
		return "", nil
	}
	return fmt.Sprintf("节点 %s 上没有检测到 %s", n.Name, rt.Title), nil
}

// checkSessionControls refuses a mode the session's runtime does not have, bypass on an ask role and
// an effort on a runtime without one (the effort level itself is the model's: the daemon checks).
func checkSessionControls(role store.Role, runtime, mode, effort string) error {
	rt := runtimes.Of(runtime)
	if mode != "" && (!slices.Contains(rt.Modes, mode) || (mode == "bypassPermissions" && role.PermissionMode == store.PermAsk)) {
		return invalid([]FieldError{{"permission_mode", "这个岗位的会话不能用这个权限模式"}}, "这个岗位的会话不能用这个权限模式")
	}
	if strings.TrimSpace(effort) != "" && !rt.Efforts {
		return invalid([]FieldError{{"effort", "这个运行时的思考强度写在模型里"}}, "这个运行时的思考强度写在模型里")
	}
	return nil
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
	// Removing every repository is fine: the session goes on in its local folder.
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
