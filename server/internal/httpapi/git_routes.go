package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/touwaeriol/agent-teams/server/internal/github"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// GitConnectionView is api.md §2 GitConnectionView (token never leaves the server).
type GitConnectionView struct {
	ID                      string            `json:"id"`
	Name                    string            `json:"name"`
	Provider                string            `json:"provider"`
	Kind                    string            `json:"kind" enum:"token,app"`
	Login                   string            `json:"login"`
	InstallationID          *int64            `json:"installation_id"`
	CreatedAt               time.Time         `json:"created_at"`
	Permissions             map[string]string `json:"permissions"`
	NeedsPermissionApproval bool              `json:"needs_permission_approval" doc:"App installation lacks checks:read"`
	InstallationSettingsURL *string           `json:"installation_settings_url"`
}

func connectionView(c store.GitConnection) GitConnectionView {
	v := GitConnectionView{ID: c.ID, Name: c.Name, Provider: c.Provider, Kind: c.Kind, Login: c.Login,
		InstallationID: c.InstallationID, CreatedAt: c.CreatedAt, Permissions: c.Permissions}
	if v.Permissions == nil {
		v.Permissions = map[string]string{}
	}
	if c.Kind == "app" && c.InstallationID != nil {
		v.NeedsPermissionApproval = len(c.Permissions) > 0 && c.Permissions["checks"] == ""
		v.InstallationSettingsURL = ptr(installationSettingsURL(c))
	}
	return v
}

func installationSettingsURL(c store.GitConnection) string {
	if c.AccountType == "Organization" && c.Login != "" {
		return fmt.Sprintf("%s/organizations/%s/settings/installations/%d", github.Web, c.Login, *c.InstallationID)
	}
	return fmt.Sprintf("%s/settings/installations/%d", github.Web, *c.InstallationID)
}

// RepoItem is one row of the repository dropdown.
type RepoItem struct {
	FullName       string             `json:"full_name"`
	Private        bool               `json:"private"`
	DefaultBranch  string             `json:"default_branch"`
	Description    string             `json:"description"`
	HTMLURL        string             `json:"html_url"`
	PushedAt       *string            `json:"pushed_at"`
	UsedByProjects []store.ProjectRef `json:"used_by_projects"`
}

type connectionsOutput struct{ Body []GitConnectionView }

type connectionOutput struct{ Body GitConnectionView }

type newConnectionInput struct {
	Body struct {
		Name     string `json:"name,omitempty" maxLength:"100"`
		Provider string `json:"provider,omitempty" enum:"github"`
		Token    string `json:"token" maxLength:"500"`
	}
}

type connectionIDInput struct {
	ID string `path:"id" maxLength:"64"`
}

type reposInput struct {
	ID     string `path:"id" maxLength:"64"`
	Q      string `query:"q" maxLength:"200"`
	Cursor string `query:"cursor"`
	Limit  int    `query:"limit" minimum:"0" maximum:"200"`
}

type reposOutput struct {
	Body struct {
		Items      []RepoItem `json:"items"`
		NextCursor *string    `json:"next_cursor"`
	}
}

type branchesInput struct {
	ID   string `path:"id" maxLength:"64"`
	Repo string `query:"repo" required:"true" doc:"GitHub URL or owner/repo"`
	Q    string `query:"q" maxLength:"200"`
}

type branchesOutput struct{ Body []string }

func (s *Server) registerGit(api huma.API) {
	tags := []string{"git"}
	huma.Register(api, huma.Operation{OperationID: "list-git-connections", Method: "GET", Path: "/api/git/connections", Tags: tags}, s.listConnections)
	huma.Register(api, huma.Operation{OperationID: "add-git-connection", Method: "POST", Path: "/api/git/connections", Tags: tags,
		Summary: "Save a pasted GitHub token (sealed)", DefaultStatus: 201}, s.addConnection)
	huma.Register(api, huma.Operation{OperationID: "delete-git-connection", Method: "DELETE", Path: "/api/git/connections/{id}", Tags: tags, DefaultStatus: 204}, s.deleteConnection)
	huma.Register(api, huma.Operation{OperationID: "list-repos", Method: "GET", Path: "/api/git/connections/{id}/repos", Tags: tags,
		Summary: "Repositories the connection can see (searchable, paged)"}, s.listRepos)
	huma.Register(api, huma.Operation{OperationID: "list-branches", Method: "GET", Path: "/api/git/connections/{id}/branches", Tags: tags}, s.listBranches)
	huma.Register(api, huma.Operation{OperationID: "refresh-git-connection", Method: "POST", Path: "/api/git/connections/{id}/refresh", Tags: tags,
		Summary: "Re-read the installation's permissions"}, s.refreshConnection)
}

func (s *Server) listConnections(ctx context.Context, _ *struct{}) (*connectionsOutput, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.store.Connections(ctx, p.User.ID)
	if err != nil {
		return nil, err
	}
	out := make([]GitConnectionView, len(rows))
	for i, c := range rows {
		out[i] = connectionView(c)
	}
	return &connectionsOutput{Body: out}, nil
}

func (s *Server) addConnection(ctx context.Context, in *newConnectionInput) (*connectionOutput, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	token := strings.TrimSpace(in.Body.Token)
	if token == "" {
		return nil, fail(http.StatusUnprocessableEntity, "validation", "请填写访问令牌")
	}
	if !s.box.Ready() {
		return nil, fail(http.StatusServiceUnavailable, "no_secret_key", "服务没有配置 AGENT_TEAMS_SECRET_KEY，不能保存 Git 凭证")
	}
	login, err := s.gh.Login(ctx, token)
	if err != nil {
		return nil, githubErr(err)
	}
	name := strings.TrimSpace(in.Body.Name)
	if name == "" {
		name = "GitHub · " + login
	}
	sealed, err := s.box.Seal(token)
	if err != nil {
		return nil, err
	}
	c, err := s.store.CreateConnection(ctx, p.User.ID, name, login, sealed, nil)
	if errors.Is(err, store.ErrConflict) {
		return nil, failf(http.StatusConflict, "connection_exists", "已经有叫 {name} 的连接了", "name", name)
	}
	if err != nil {
		return nil, err
	}
	s.audit(ctx, p.User.ID, "git_connection_add", "git_connection", c.ID, map[string]string{"kind": "token", "login": login})
	return &connectionOutput{Body: connectionView(c)}, nil
}

func (s *Server) deleteConnection(ctx context.Context, in *connectionIDInput) (*struct{}, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.store.DeleteConnection(ctx, p.User.ID, in.ID); err != nil {
		return nil, storeErr(err, "没有这个连接")
	}
	s.repos.forget(in.ID)
	s.audit(ctx, p.User.ID, "git_connection_delete", "git_connection", in.ID, nil)
	return nil, nil
}

// connectionToken returns a token for console-side GitHub calls on one of the user's connections.
func (s *Server) connectionToken(ctx context.Context, userID, id string) (string, store.GitConnection, error) {
	c, err := s.store.Connection(ctx, userID, id)
	if err != nil {
		return "", c, storeErr(err, "没有这个 Git 连接")
	}
	if c.InstallationID != nil {
		token, err := s.tokens.Installation(ctx, *c.InstallationID, github.TokenRequest{})
		return token, c, githubErr(err)
	}
	sealed, err := s.store.ConnectionSecret(ctx, userID, id)
	if err != nil {
		return "", c, err
	}
	token, err := s.box.Open(sealed)
	return token, c, githubErr(err)
}

func (s *Server) listRepos(ctx context.Context, in *reposInput) (*reposOutput, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	all, err := s.connectionRepos(ctx, p.User.ID, in.ID)
	if err != nil {
		return nil, err
	}
	used, err := s.store.ProjectsByRepo(ctx, p.User.ID)
	if err != nil {
		return nil, err
	}
	matched := filterRepos(all, in.Q)
	offset, _ := strconv.Atoi(in.Cursor)
	limit := pageLimit(in.Limit)
	out := &reposOutput{}
	out.Body.Items = []RepoItem{}
	for i := offset; i < len(matched) && i < offset+limit; i++ {
		out.Body.Items = append(out.Body.Items, repoItem(matched[i], used))
	}
	if offset+limit < len(matched) {
		out.Body.NextCursor = ptr(strconv.Itoa(offset + limit))
	}
	return out, nil
}

func repoItem(r github.Repo, used map[string][]store.ProjectRef) RepoItem {
	refs := used[strings.ToLower(r.FullName)]
	if refs == nil {
		refs = []store.ProjectRef{}
	}
	return RepoItem{FullName: r.FullName, Private: r.Private, DefaultBranch: r.DefaultBranch, Description: r.Description,
		HTMLURL: r.HTMLURL, PushedAt: r.PushedAt, UsedByProjects: refs}
}

func filterRepos(all []github.Repo, q string) []github.Repo {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return all
	}
	var out []github.Repo
	for _, r := range all {
		if strings.Contains(strings.ToLower(r.FullName), q) || strings.Contains(strings.ToLower(r.Description), q) {
			out = append(out, r)
		}
	}
	return out
}

// maxListPages bounds how many GitHub pages a dropdown listing reads (100 per page).
const maxListPages = 10

func (s *Server) connectionRepos(ctx context.Context, userID, id string) ([]github.Repo, error) {
	token, c, err := s.connectionToken(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	if cached, ok := s.repos.get(id); ok {
		return cached, nil
	}
	list := s.gh.UserRepos
	if c.InstallationID != nil {
		list = func(ctx context.Context, token string, n int) ([]github.Repo, bool, error) {
			return s.gh.InstallationRepos(ctx, token, n)
		}
	}
	var all []github.Repo
	for n := 1; n <= maxListPages; n++ {
		page, more, err := list(ctx, token, n)
		if err != nil {
			return nil, githubErr(err)
		}
		all = append(all, page...)
		if !more {
			break
		}
	}
	s.repos.put(id, all)
	return all, nil
}

func (s *Server) listBranches(ctx context.Context, in *branchesInput) (*branchesOutput, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	slug, err := github.ParseRepo(in.Repo)
	if err != nil {
		return nil, githubErr(err)
	}
	token, _, err := s.connectionToken(ctx, p.User.ID, in.ID)
	if err != nil {
		return nil, err
	}
	names := []string{}
	for n := 1; n <= maxListPages; n++ {
		page, more, err := s.gh.Branches(ctx, token, slug, n)
		if err != nil {
			return nil, githubErr(err)
		}
		names = append(names, filterNames(page, in.Q)...)
		if !more {
			break
		}
	}
	return &branchesOutput{Body: names}, nil
}

func filterNames(names []string, q string) []string {
	q = strings.ToLower(strings.TrimSpace(q))
	out := []string{}
	for _, n := range names {
		if q == "" || strings.Contains(strings.ToLower(n), q) {
			out = append(out, n)
		}
	}
	return out
}

func (s *Server) refreshConnection(ctx context.Context, in *connectionIDInput) (*connectionOutput, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	c, err := s.store.Connection(ctx, p.User.ID, in.ID)
	if err != nil {
		return nil, storeErr(err, "没有这个连接")
	}
	s.repos.forget(c.ID)
	if c.InstallationID == nil {
		return &connectionOutput{Body: connectionView(c)}, nil
	}
	inst, err := s.readInstallation(ctx, *c.InstallationID)
	if err != nil {
		return nil, githubErr(err)
	}
	if err := s.store.SetConnectionPermissions(ctx, c.ID, inst.Permissions, inst.Account.Type); err != nil {
		return nil, err
	}
	c, err = s.store.Connection(ctx, p.User.ID, in.ID)
	return &connectionOutput{Body: connectionView(c)}, err
}

func (s *Server) readInstallation(ctx context.Context, installationID int64) (github.Installation, error) {
	app, err := s.apps.Load(ctx)
	if err != nil {
		return github.Installation{}, err
	}
	if app == nil {
		return github.Installation{}, github.ErrNoApp
	}
	jwt, err := github.AppJWT(app.AppID, app.PEM, time.Now())
	if err != nil {
		return github.Installation{}, err
	}
	return s.gh.Installation(ctx, jwt, installationID)
}

// repoCache keeps a connection's repository list for a minute (dropdown typing).
type repoCache struct {
	mu    sync.Mutex
	items map[string]repoEntry
}

type repoEntry struct {
	repos   []github.Repo
	expires time.Time
}

func newRepoCache() *repoCache { return &repoCache{items: map[string]repoEntry{}} }

func (c *repoCache) get(id string) ([]github.Repo, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.items[id]
	if !ok || time.Now().After(e.expires) {
		return nil, false
	}
	return e.repos, true
}

func (c *repoCache) put(id string, repos []github.Repo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[id] = repoEntry{repos: repos, expires: time.Now().Add(time.Minute)}
}

func (c *repoCache) forget(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.items, id)
}
