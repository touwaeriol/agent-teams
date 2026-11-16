package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/gin-gonic/gin"

	"github.com/touwaeriol/agent-teams/server/internal/auth"
	"github.com/touwaeriol/agent-teams/server/internal/github"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// appBase is the v1 path prefix; GitHub's App settings point at it, so it never changes.
const appBase = "/api/git/github-app"

// appPermissions are requested in the manifest (v1 set + checks/statuses for CI, api.md §3.10).
var appPermissions = map[string]string{
	"contents": "write", "pull_requests": "write", "workflows": "write", "metadata": "read",
	"checks": "read", "statuses": "read",
}

// AppPublic is the App as users see it.
type AppPublic struct {
	Slug          string            `json:"slug"`
	Name          string            `json:"name"`
	Owner         string            `json:"owner"`
	HTMLURL       string            `json:"html_url"`
	Permissions   map[string]string `json:"permissions"`
	WebhookActive bool              `json:"webhook_active"`
}

type appStatusOutput struct {
	Body struct {
		Configured bool       `json:"configured"`
		App        *AppPublic `json:"app,omitempty"`
		InstallURL string     `json:"install_url,omitempty"`
	}
}

type manifestInput struct {
	Body struct {
		Organization string `json:"organization,omitempty" maxLength:"100"`
	}
}

type manifestOutput struct {
	Body struct {
		Action   string `json:"action" doc:"URL the browser form posts to (GitHub)"`
		Manifest string `json:"manifest" doc:"JSON manifest for the form's manifest field"`
	}
}

func (s *Server) registerGitHubApp(api huma.API, r gin.IRoutes) {
	huma.Register(api, huma.Operation{OperationID: "github-app-status", Method: "GET", Path: appBase, Tags: []string{"git"}}, s.appStatus)
	huma.Register(api, huma.Operation{OperationID: "admin-github-app-manifest", Method: "POST", Path: "/api/admin/github-app/manifest",
		Tags: []string{"admin"}, Summary: "Form data to create the platform GitHub App from a manifest"}, s.appManifest)
	huma.Register(api, huma.Operation{OperationID: "admin-github-app-delete", Method: "DELETE", Path: "/api/admin/github-app",
		Tags: []string{"admin"}, DefaultStatus: 204}, s.appForget)
	r.GET(appBase+"/install", gin.WrapF(s.appInstall))
	r.GET(appBase+"/created", gin.WrapF(s.appCreated))
	r.GET(appBase+"/installed", gin.WrapF(s.appInstalled))
	r.POST(appBase+"/webhook", gin.WrapF(s.appWebhook))
}

func (s *Server) loadApp(ctx context.Context) *github.App {
	app, err := s.apps.Load(ctx)
	if err != nil {
		s.log.Warn("github app unreadable", "err", err)
		return nil
	}
	return app
}

func (s *Server) appStatus(ctx context.Context, _ *struct{}) (*appStatusOutput, error) {
	if _, err := requireUser(ctx); err != nil {
		return nil, err
	}
	out := &appStatusOutput{}
	app := s.loadApp(ctx)
	if app == nil {
		return out, nil
	}
	perms := app.Permissions
	if perms == nil {
		perms = map[string]string{}
	}
	out.Body.Configured = true
	out.Body.App = &AppPublic{Slug: app.Slug, Name: app.Name, Owner: app.Owner, HTMLURL: app.HTMLURL,
		Permissions: perms, WebhookActive: app.WebhookSecret != ""}
	out.Body.InstallURL = appBase + "/install"
	return out, nil
}

func (s *Server) appManifest(ctx context.Context, in *manifestInput) (*manifestOutput, error) {
	admin, err := requireAdmin(ctx)
	if err != nil {
		return nil, err
	}
	if !s.box.Ready() {
		return nil, fail(http.StatusServiceUnavailable, "no_secret_key", "服务没有配置 AGENT_TEAMS_SECRET_KEY")
	}
	target := github.Web + "/settings/apps/new"
	if org := strings.TrimSpace(in.Body.Organization); org != "" {
		target = github.Web + "/organizations/" + url.PathEscape(org) + "/settings/apps/new"
	}
	raw, _ := json.Marshal(s.manifest())
	out := &manifestOutput{}
	out.Body.Action = target + "?" + url.Values{"state": {s.states.Sign(auth.PurposeCreate, admin.User.ID)}}.Encode()
	out.Body.Manifest = string(raw)
	return out, nil
}

func (s *Server) manifest() map[string]any {
	base := s.cfg.PublicURL
	host := "agent-teams"
	if u, err := url.Parse(base); err == nil && u.Hostname() != "" {
		host = strings.Split(u.Hostname(), ".")[0]
	}
	suffix := make([]byte, 2)
	_, _ = rand.Read(suffix)
	return map[string]any{
		"name":                     "Agent Teams " + host + " " + hex.EncodeToString(suffix),
		"url":                      base,
		"redirect_url":             base + appBase + "/created",
		"callback_urls":            []string{base + appBase + "/installed"},
		"setup_url":                base + appBase + "/installed",
		"setup_on_update":          true,
		"request_oauth_on_install": true,
		"public":                   true,
		"hook_attributes":          map[string]any{"url": base + appBase + "/webhook", "active": true},
		"default_permissions":      appPermissions,
		"default_events":           []string{"check_run", "check_suite", "pull_request"},
	}
}

func (s *Server) appForget(ctx context.Context, _ *struct{}) (*struct{}, error) {
	admin, err := requireAdmin(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.apps.Forget(ctx); err != nil {
		return nil, err
	}
	if err := s.tokens.Reset(ctx); err != nil {
		s.log.Warn("token cache reset failed", "err", err)
	}
	s.audit(ctx, admin.User.ID, "github_app_delete", "github_app", "", nil)
	return nil, nil
}

func back(w http.ResponseWriter, r *http.Request, path string, params map[string]string) {
	q := url.Values{}
	for k, v := range params {
		q.Set(k, v)
	}
	http.Redirect(w, r, path+"?"+q.Encode(), http.StatusSeeOther)
}

func (s *Server) appInstall(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r.Context())
	if p == nil {
		writeError(w, fail(http.StatusUnauthorized, "unauthorized", "请先登录"))
		return
	}
	app := s.loadApp(r.Context())
	if app == nil {
		back(w, r, "/settings/git", map[string]string{"error": "管理员还没有创建平台的 GitHub App"})
		return
	}
	state := s.states.Sign(auth.PurposeInstall, p.User.ID)
	http.Redirect(w, r, app.InstallURL()+"?"+url.Values{"state": {state}}.Encode(), http.StatusSeeOther)
}

// signedUser resolves the active user named by a valid signed state.
func (s *Server) signedUser(ctx context.Context, purpose, state string) *store.User {
	id := s.states.Verify(purpose, state)
	if id == "" {
		return nil
	}
	u, err := s.store.UserByID(ctx, id)
	if err != nil || !u.Active {
		return nil
	}
	return &u
}

// appCreated: GitHub created the App and sent a one-time code for its secrets.
func (s *Server) appCreated(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()
	admin := s.signedUser(ctx, auth.PurposeCreate, q.Get("state"))
	if admin == nil || admin.Role != "admin" {
		back(w, r, "/setup/github", map[string]string{"error": "创建请求已过期或不是你发起的，请重新创建"})
		return
	}
	conv, err := s.gh.ConvertManifest(ctx, q.Get("code"))
	var app *github.App
	if err == nil {
		app, err = s.apps.Save(ctx, conv, appPermissions)
	}
	if err != nil {
		back(w, r, "/setup/github", map[string]string{"error": "保存 GitHub App 失败：" + err.Error()})
		return
	}
	_ = s.tokens.Reset(ctx)
	s.audit(ctx, admin.ID, "github_app_create", "github_app", app.Slug, nil)
	back(w, r, "/setup/github", map[string]string{"github_app": app.Slug})
}

// appInstalled: back from GitHub after installing (or changing) the App.
func (s *Server) appInstalled(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()
	user := s.installingUser(r)
	if user == nil {
		back(w, r, "/settings/git", map[string]string{"error": "授权请求已过期或不是你发起的，请重新连接"})
		return
	}
	installationID, _ := strconv.ParseInt(q.Get("installation_id"), 10, 64)
	if name := s.existingInstallation(ctx, user.ID, installationID); name != "" {
		back(w, r, "/settings/git", map[string]string{"connected": name})
		return
	}
	if installationID == 0 || q.Get("code") == "" {
		back(w, r, "/settings/git", map[string]string{"error": "GitHub 没有返回安装信息，请重新连接"})
		return
	}
	name, err := s.connectInstallation(ctx, user.ID, q.Get("code"), installationID)
	if err != nil {
		back(w, r, "/settings/git", map[string]string{"error": err.Error()})
		return
	}
	back(w, r, "/settings/git", map[string]string{"connected": name})
}

func (s *Server) installingUser(r *http.Request) *store.User {
	if state := r.URL.Query().Get("state"); state != "" {
		return s.signedUser(r.Context(), auth.PurposeInstall, state)
	}
	if p := principalOf(r.Context()); p != nil {
		return &p.User
	}
	return nil
}

func (s *Server) existingInstallation(ctx context.Context, userID string, installationID int64) string {
	conns, err := s.store.Connections(ctx, userID)
	if err != nil {
		return ""
	}
	for _, c := range conns {
		if c.InstallationID != nil && *c.InstallationID == installationID {
			return c.Name
		}
	}
	return ""
}

// connectInstallation proves (OAuth code) the GitHub user can see the installation, then saves it.
func (s *Server) connectInstallation(ctx context.Context, userID, code string, installationID int64) (string, error) {
	app := s.loadApp(ctx)
	if app == nil {
		return "", errors.New("管理员还没有创建平台的 GitHub App")
	}
	userToken, err := s.gh.UserToken(ctx, app.ClientID, app.ClientSecret, code)
	if err != nil {
		return "", err
	}
	installs, err := s.gh.UserInstallations(ctx, userToken)
	if err != nil {
		return "", err
	}
	match := findInstallation(installs, installationID)
	if match == nil {
		return "", errors.New("这个安装不属于你的 GitHub 账号")
	}
	return s.saveInstallation(ctx, userID, *match)
}

func findInstallation(installs []github.Installation, id int64) *github.Installation {
	for i := range installs {
		if installs[i].ID == id {
			return &installs[i]
		}
	}
	return nil
}

func (s *Server) saveInstallation(ctx context.Context, userID string, inst github.Installation) (string, error) {
	label := inst.Account.Login
	if label == "" {
		label = strconv.FormatInt(inst.ID, 10)
	}
	name := "GitHub App · " + label
	c, err := s.store.CreateConnection(ctx, userID, name, inst.Account.Login, "", &inst.ID)
	if errors.Is(err, store.ErrConflict) {
		name = name + " · " + strconv.FormatInt(inst.ID, 10)
		c, err = s.store.CreateConnection(ctx, userID, name, inst.Account.Login, "", &inst.ID)
	}
	if err != nil {
		return "", err
	}
	if err := s.store.SetConnectionPermissions(ctx, c.ID, inst.Permissions, inst.Account.Type); err != nil {
		s.log.Warn("save installation permissions", "err", err)
	}
	s.audit(ctx, userID, "git_connection_add", "git_connection", c.ID, map[string]any{"kind": "app", "installation_id": inst.ID})
	return name, nil
}

const maxWebhookBody = 5 << 20

// appWebhook verifies X-Hub-Signature-256, refreshes installation permissions and
// hands every delivery to the engines.
func (s *Server) appWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBody))
	if err != nil {
		writeError(w, fail(http.StatusBadRequest, "bad_request", "读取请求失败"))
		return
	}
	if !s.validWebhook(r.Context(), r.Header.Get("X-Hub-Signature-256"), body) {
		writeError(w, fail(http.StatusUnauthorized, "bad_signature", "签名不对"))
		return
	}
	event := r.Header.Get("X-GitHub-Event")
	if event == "installation" || event == "new_permissions_accepted" {
		s.refreshFromWebhook(r.Context(), body)
	}
	if err := s.hooks.GitHubEvent(r.Context(), event, body); err != nil {
		s.log.Warn("github webhook hook", "event", event, "err", err)
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) validWebhook(ctx context.Context, signature string, body []byte) bool {
	secret := s.cfg.WebhookToken
	if secret == "" {
		if app := s.loadApp(ctx); app != nil {
			secret = app.WebhookSecret
		}
	}
	if secret == "" || !strings.HasPrefix(signature, "sha256=") {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(want), []byte(signature))
}

func (s *Server) refreshFromWebhook(ctx context.Context, body []byte) {
	var payload struct {
		Installation github.Installation `json:"installation"`
	}
	if json.Unmarshal(body, &payload) != nil || payload.Installation.ID == 0 {
		return
	}
	conns, err := s.store.ConnectionsByInstallation(ctx, payload.Installation.ID)
	if err != nil {
		return
	}
	for _, c := range conns {
		_ = s.store.SetConnectionPermissions(ctx, c.ID, payload.Installation.Permissions, payload.Installation.Account.Type)
	}
}
