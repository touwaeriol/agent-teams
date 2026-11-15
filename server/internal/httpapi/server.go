package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/touwaeriol/agent-teams/server/internal/auth"
	"github.com/touwaeriol/agent-teams/server/internal/config"
	"github.com/touwaeriol/agent-teams/server/internal/github"
	"github.com/touwaeriol/agent-teams/server/internal/kv"
	"github.com/touwaeriol/agent-teams/server/internal/office"
	"github.com/touwaeriol/agent-teams/server/internal/platform"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// Version is reported in the OpenAPI document and /healthz.
var Version = "2.0.0-dev"

// Deps are the server's collaborators. Store may be nil only for OpenAPI generation.
type Deps struct {
	Config  config.Config
	Store   *store.Store
	KV      kv.KV
	GitHub  *github.Client
	Tokens  *github.Tokens // installation token cache; shared with cred.request handling
	Office  *office.Registry
	Nodes   platform.NodeRegistry
	Events  platform.Events
	Hooks   platform.Hooks
	Log     *slog.Logger
	Version func() (expected int64) // goose version this binary ships (readyz)
	// Extensions register more operations/routes (node gateway, threads, SSE…)
	// before the web app fallback is mounted.
	Extensions []func(api huma.API, r chi.Router)
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
	log     *slog.Logger
	expect  func() int64
	repos   *repoCache

	router chi.Router
	api    huma.API
}

// New wires the router and every operation.
func New(d Deps) *Server {
	d = withDefaults(d)
	box := auth.NewSecretBox(d.Config.SecretKeys...)
	s := &Server{cfg: d.Config, store: d.Store, kv: d.KV, limiter: kv.NewLimiter(d.KV), box: box,
		states: auth.NewStateSigner(box), gh: d.GitHub, office: d.Office, nodes: d.Nodes, events: d.Events,
		hooks: d.Hooks, log: d.Log, expect: d.Version, repos: newRepoCache()}
	if d.Store != nil {
		s.apps = github.NewApps(d.Store, box)
		s.tokens = d.Tokens
	}
	if s.tokens == nil && s.apps != nil {
		s.tokens = github.NewTokens(d.GitHub, s.apps, d.KV, box)
	}
	s.router = chi.NewRouter()
	s.router.Use(realIP, middleware.RequestID, s.logRequests, middleware.Recoverer, artifactHostOnly(d.Config.ArtifactOrigin))
	s.router.Group(func(r chi.Router) {
		r.Use(s.session)
		s.api = humachi.New(r, apiConfig())
		s.register(s.api, r)
		for _, ext := range d.Extensions {
			ext(s.api, r)
		}
	})
	s.mountPublic(s.router)
	if s.office != nil {
		s.office.OnChange = s.layoutChanged
	}
	return s
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
	if d.Hooks == nil {
		d.Hooks = platform.NoHooks{}
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

func (s *Server) register(api huma.API, r chi.Router) {
	s.registerAuth(api)
	s.registerAdmin(api)
	s.registerProjects(api)
	s.registerMemory(api)
	s.registerGit(api)
	s.registerGitHubApp(api, r)
	s.registerNodes(api)
	s.registerRoles(api)
	s.registerOffice(api, r)
	s.registerHealth(api)
}

// Handler is the root http.Handler.
func (s *Server) Handler() http.Handler { return s.router }

// API exposes the huma API (OpenAPI generation, extensions).
func (s *Server) API() huma.API { return s.api }

// OpenAPI returns the OpenAPI 3.1 document as JSON.
func (s *Server) OpenAPI() ([]byte, error) { return json.MarshalIndent(s.api.OpenAPI(), "", "  ") }

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		start := time.Now()
		next.ServeHTTP(ww, r)
		if r.URL.Path == "/healthz" {
			return
		}
		s.log.Info("http", "request_id", middleware.GetReqID(r.Context()), "method", r.Method, "path", r.URL.Path,
			"status", ww.Status(), "ms", time.Since(start).Milliseconds(), "ip", clientIP(r))
	})
}

func writeError(w http.ResponseWriter, err error) {
	e, ok := err.(*APIError)
	if !ok {
		e = &APIError{Status: http.StatusInternalServerError, Code: "internal", Detail: "服务器出错了"}
	}
	writeJSON(w, e.Status, e)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
