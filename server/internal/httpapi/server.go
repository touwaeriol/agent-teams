package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humagin"
	"github.com/gin-gonic/gin"

	"github.com/touwaeriol/agent-teams/server/internal/auth"
	"github.com/touwaeriol/agent-teams/server/internal/config"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/credvault"
	"github.com/touwaeriol/agent-teams/server/internal/github"
	"github.com/touwaeriol/agent-teams/server/internal/gitrelay"
	"github.com/touwaeriol/agent-teams/server/internal/gitremote"
	"github.com/touwaeriol/agent-teams/server/internal/kv"
	"github.com/touwaeriol/agent-teams/server/internal/msg"
	"github.com/touwaeriol/agent-teams/server/internal/office"
	"github.com/touwaeriol/agent-teams/server/internal/platform"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// Version is reported in the OpenAPI document and /healthz.
var Version = "2.0.0-dev"

// Deps are the server's collaborators. Store may be nil only for OpenAPI generation.
type Deps struct {
	Config config.Config
	Store  *store.Store
	KV     kv.KV
	GitHub *github.Client
	Tokens *github.Tokens // installation token cache; shared with cred.request handling
	Office *office.Registry
	Nodes  platform.NodeRegistry
	Events platform.Events
	Hooks  platform.Hooks
	SSH    platform.SSHHooks // remote servers → SSH gateway / node gateway; nil = platform.NoSSHHooks
	// Memory keeps auto memory in the owners' shared folders (nil: core.NoMemoryFiles).
	Memory core.MemoryFiles
	// Relay serves standalone Git repositories to nodes at /git/ (nil: answers 503).
	Relay   *gitrelay.Relay
	Log     *slog.Logger
	Version func() (expected int64) // goose version this binary ships (readyz)
	// Extensions register more operations/routes (node gateway, threads, SSE…)
	// before the web app fallback is mounted.
	// Raw routes go on r, which runs the session and authorization middleware.
	Extensions []func(api huma.API, r gin.IRoutes)
}

// Server holds the HTTP handlers.
type Server struct {
	cfg     config.Config
	store   *store.Store
	kv      kv.KV
	limiter *kv.Limiter
	box     *auth.SecretBox
	states  *auth.StateSigner
	gh      *github.Client
	apps    *github.Apps
	tokens  *github.Tokens
	office  *office.Registry
	nodes   platform.NodeRegistry
	events  platform.Events
	hooks   platform.Hooks
	memory  core.MemoryFiles
	ssh     platform.SSHHooks
	vault   *credvault.Vault
	git     gitremote.Git
	relay   *gitrelay.Relay
	log     *slog.Logger
	expect  func() int64
	repos   *repoCache
	authz   *authorizer

	router *gin.Engine
	api    huma.API
}

// New wires the router and every operation.
func New(d Deps) *Server {
	d = withDefaults(d)
	box := auth.NewSecretBox(d.Config.SecretKeys...)
	s := &Server{cfg: d.Config, store: d.Store, kv: d.KV, limiter: kv.NewLimiter(d.KV), box: box,
		states: auth.NewStateSigner(box), gh: d.GitHub, office: d.Office, nodes: d.Nodes, events: d.Events,
		hooks: d.Hooks, memory: d.Memory, ssh: d.SSH, vault: mustVault(d.Config.SecretKeys), log: d.Log, expect: d.Version, repos: newRepoCache(), authz: mustAuthorizer(d.Store), relay: d.Relay}
	if s.relay == nil {
		s.relay = &gitrelay.Relay{}
	}
	s.git = s.relay.Git
	if d.Store != nil {
		s.apps = github.NewApps(d.Store, box)
		s.tokens = d.Tokens
	}
	if s.tokens == nil && s.apps != nil {
		s.tokens = github.NewTokens(d.GitHub, s.apps, d.KV, box)
	}
	s.router = newEngine()
	s.router.Use(realIP, requestID, securityHeaders(d.Config.SecureCookies()), s.logRequests, s.recoverer, artifactHostOnly(d.Config.ArtifactOrigin))
	routes := s.router.Group("", s.session, s.authorize)
	s.api = humagin.NewWithGroup(s.router, routes, apiConfig())
	s.register(s.api, routes)
	for _, ext := range d.Extensions {
		ext(s.api, routes)
	}
	s.mountPublic(s.router)
	if s.office != nil {
		s.office.OnChange = s.layoutChanged
	}
	return s
}

// newEngine is a bare gin engine that routes like the previous chi router: no trailing-slash
// redirects or path fixing, 405 for a known path with another method.
func newEngine() *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	e := gin.New()
	e.RedirectTrailingSlash = false
	e.RedirectFixedPath = false
	e.HandleMethodNotAllowed = true
	e.NoMethod(methodNotAllowed)
	return e
}

func withDefaults(d Deps) Deps {
	if d.Log == nil {
		d.Log = slog.Default()
	}
	if d.KV == nil {
		d.KV = kv.NewMemory()
	}
	if d.GitHub == nil {
		d.GitHub = github.NewClient()
	}
	if d.Nodes == nil {
		d.Nodes = platform.Offline{}
	}
	if d.Events == nil {
		d.Events = platform.LogEvents{Log: d.Log}
	}
	if d.Memory == nil {
		d.Memory = core.NoMemoryFiles{}
		if d.Store != nil {
			d.Memory = core.NoMemoryFiles{DB: d.Store.Pool}
		}
	}
	if d.Hooks == nil {
		d.Hooks = platform.NoHooks{}
	}
	if d.SSH == nil {
		d.SSH = platform.NoSSHHooks{}
	}
	if d.Version == nil {
		d.Version = func() int64 { return 0 }
	}
	return d
}

func apiConfig() huma.Config {
	cfg := huma.DefaultConfig("Agent Teams control plane", Version)
	cfg.OpenAPIPath = "/api/openapi"
	cfg.DocsPath = "/api/docs"
	cfg.SchemasPath = ""
	cfg.CreateHooks = nil // no $schema links in bodies: keep v1 JSON shapes
	cfg.Info.Description = "REST API of agent-teams v2 (docs/v2/api.md). Writes with cookie auth need X-CSRF-Token = agent_teams_csrf cookie."
	cfg.Components.SecuritySchemes = map[string]*huma.SecurityScheme{
		"cookie": {Type: "apiKey", In: "cookie", Name: SessionCookie},
		"bearer": {Type: "http", Scheme: "bearer"},
	}
	return cfg
}

func (s *Server) register(api huma.API, r gin.IRoutes) {
	s.registerAuth(api)
	s.registerScheduling(api)
	s.registerAdmin(api)
	s.registerProjects(api)
	s.registerSessions(api)
	s.registerMemory(api)
	s.registerGit(api)
	s.registerServers(api)
	s.registerGitRepos(api)
	s.registerGitHubApp(api, r)
	s.registerNodes(api)
	s.registerRoles(api)
	s.registerCommands(api)
	s.registerOffice(api)
	s.registerHealth(api)
	s.registerVoice(api)
	s.registerVoiceStream(r)
}

// Handler is the root http.Handler.
func (s *Server) Handler() http.Handler { return s.router }

// Routes lists every registered route (method + gin route template).
func (s *Server) Routes() gin.RoutesInfo { return s.router.Routes() }

// Allows reports whether the route policy lets subject (Subject*) call method on a route template.
func (s *Server) Allows(subject, method, route string) (bool, error) {
	return s.authz.allowed(subject, route, method)
}

// RunAuthz re-reads the users' roles from casbin_rule every authzReloadEvery until ctx ends,
// so role changes made on another console replica reach this one.
func (s *Server) RunAuthz(ctx context.Context) {
	t := time.NewTicker(authzReloadEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.authz.reload(); err != nil {
				s.log.Warn("authz reload failed", "err", err)
			}
		}
	}
}

// SetUserRole assigns admin or user through the Casbin enforcer (atctl admin set-role); running
// consoles pick it up within authzReloadEvery.
func SetUserRole(db *store.Store, userID, role string) error {
	if role != roleAdmin && role != roleUser {
		return fmt.Errorf("role must be %s or %s", roleAdmin, roleUser)
	}
	a, err := newAuthorizer(db)
	if err != nil {
		return err
	}
	return a.setRole(userID, role)
}

// Policy returns the (subject, route, method) permission lines of the embedded policy.
func (s *Server) Policy() ([][]string, error) { return s.authz.e.GetPolicy() }

// API exposes the huma API (OpenAPI generation, extensions).
func (s *Server) API() huma.API { return s.api }

// OpenAPI returns the OpenAPI 3.1 document as JSON.
func (s *Server) OpenAPI() ([]byte, error) { return json.MarshalIndent(s.api.OpenAPI(), "", "  ") }

func writeError(w http.ResponseWriter, err error) {
	e, ok := err.(*APIError)
	if !ok {
		e = failMsg(http.StatusInternalServerError, "internal", msg.T("服务器出错了")).(*APIError)
	}
	writeJSON(w, e.Status, e)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
