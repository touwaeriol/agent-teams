package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/config"
	"github.com/touwaeriol/agent-teams/server/internal/github"
	"github.com/touwaeriol/agent-teams/server/internal/kv"
	"github.com/touwaeriol/agent-teams/server/internal/office"
	"github.com/touwaeriol/agent-teams/server/internal/platform"
	"github.com/touwaeriol/agent-teams/server/internal/store"
	"github.com/touwaeriol/agent-teams/server/internal/testdb"
)

// fakeNodes records what the REST API asks of the node gateway.
type fakeNodes struct {
	mu           sync.Mutex
	disconnected []string
	configured   []string
	online       map[string]bool
	upgrades     []string
	noSelfUpdate bool
}

func (f *fakeNodes) Status(id string) platform.NodeLive {
	f.mu.Lock()
	defer f.mu.Unlock()
	return platform.NodeLive{Online: f.online[id], Version: "2.0.0"}
}

func (f *fakeNodes) Disconnect(_ context.Context, id, _ string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.disconnected = append(f.disconnected, id)
}

func (f *fakeNodes) Configure(_ context.Context, n store.Node) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.configured = append(f.configured, n.ID)
}

func (f *fakeNodes) Upgrade(_ context.Context, id string, r platform.NodeRelease, o platform.UpgradeOptions) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.online[id] {
		return platform.ErrNodeOffline
	}
	if f.noSelfUpdate {
		return platform.ErrUpgradeUnsupported
	}
	f.upgrades = append(f.upgrades, r.Version+":"+o.Mode)
	return nil
}

func (f *fakeNodes) Drain(context.Context, string, time.Duration) error {
	return platform.ErrNodeOffline
}

func (f *fakeNodes) RefreshUsage(context.Context, string) (*platform.NodeUsage, bool, error) {
	return nil, false, platform.ErrNodeOffline
}

func (f *fakeNodes) RecheckRuntimes(context.Context, string) (bool, error) {
	return false, platform.ErrNodeOffline
}

// fakeEvents records published events.
type fakeEvents struct {
	mu     sync.Mutex
	events []string
}

func (f *fakeEvents) Project(_ context.Context, pid, typ string, _ any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, "project:"+pid+":"+typ)
}

func (f *fakeEvents) User(_ context.Context, uid, typ string, _ any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, "user:"+uid+":"+typ)
}

func (f *fakeEvents) has(prefix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, e := range f.events {
		if strings.HasPrefix(e, prefix) {
			return true
		}
	}
	return false
}

type harness struct {
	t      *testing.T
	srv    *httptest.Server
	server *Server
	db     *store.Store
	gh     *httptest.Server
	nodes  *fakeNodes
	events *fakeEvents
	ssh    *fakeSSH
	cfg    config.Config
}

// fakeSSH records the remote server hooks.
type fakeSSH struct {
	mu      sync.Mutex
	revoked []int64
	changed []string // project ids
}

func (f *fakeSSH) CertsRevoked(_ context.Context, serials []int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revoked = append(f.revoked, serials...)
}

func (f *fakeSSH) ServersChanged(_ context.Context, _ string, projectIDs []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.changed = append(f.changed, projectIDs...)
}

func (f *fakeSSH) take() (revoked []int64, changed []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	revoked, changed = f.revoked, f.changed
	f.revoked, f.changed = nil, nil
	return revoked, changed
}

const testSecret = "test-secret-key"

func newHarness(t *testing.T) *harness {
	t.Helper()
	dbURL := testdb.Migrated(t)
	ctx := context.Background()
	db, err := store.Open(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	h := &harness{t: t, db: db, nodes: &fakeNodes{online: map[string]bool{}}, events: &fakeEvents{}, ssh: &fakeSSH{}}
	h.gh = httptest.NewServer(fakeGitHub(t))
	t.Cleanup(h.gh.Close)
	h.cfg = config.Config{PublicURL: "http://console.test", NodeImage: "ghcr.io/example/node:test", SecretKeys: []string{testSecret}, AdminEmails: map[string]bool{"boss@example.com": true},
		WebDir: t.TempDir(), NodeDistDir: t.TempDir(), OfficeDir: t.TempDir()}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	validator, err := office.NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	reg := office.NewRegistry(h.cfg.OfficeDir, validator, store.OfficeDB{S: db}, log)
	if err := reg.Load(ctx); err != nil {
		t.Fatal(err)
	}
	h.server = New(Deps{Config: h.cfg, Store: db, KV: testKV(t), Office: reg, Nodes: h.nodes, Events: h.events, SSH: h.ssh, Log: log,
		GitHub: &github.Client{BaseURL: h.gh.URL, WebURL: h.gh.URL, HTTP: h.gh.Client()}, Version: func() int64 { return 30 }})
	h.srv = httptest.NewServer(h.server.Handler())
	t.Cleanup(h.srv.Close)
	return h
}

func testKV(t *testing.T) kv.KV {
	if url := os.Getenv("TEST_VALKEY_URL"); url != "" {
		v, err := kv.Dial(url)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(v.Close)
		_ = v.Client.Do(context.Background(), v.Client.B().Flushdb().Build()).Error()
		return v
	}
	return kv.NewMemory()
}

// client is one browser: a cookie jar plus the CSRF token from the cookie.
type client struct {
	h      *harness
	http   *http.Client
	bearer string
	noCSRF bool
}

func (h *harness) client() *client {
	jar, _ := cookiejar.New(nil)
	return &client{h: h, http: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}}
}

func (c *client) cookie(name string) string {
	u, _ := url.Parse(c.h.srv.URL)
	for _, ck := range c.http.Jar.Cookies(u) {
		if ck.Name == name {
			return ck.Value
		}
	}
	return ""
}

type response struct {
	Status int
	Header http.Header
	Body   []byte
}

func (r response) json(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal(r.Body, v); err != nil {
		t.Fatalf("decode %s: %v", r.Body, err)
	}
}

func (r response) apiError(t *testing.T) APIError {
	var e APIError
	r.json(t, &e)
	return e
}

func (c *client) do(method, path string, body any, headers ...string) response {
	c.h.t.Helper()
	var reader io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, c.h.srv.URL+path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+c.bearer)
	}
	if !c.noCSRF && method != "GET" {
		req.Header.Set(CSRFHeader, c.cookie(CSRFCookie))
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.h.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return response{Status: resp.StatusCode, Header: resp.Header, Body: raw}
}

func (c *client) must(status int, method, path string, body any, out any) response {
	c.h.t.Helper()
	r := c.do(method, path, body)
	if r.Status != status {
		c.h.t.Fatalf("%s %s = %d, want %d: %s", method, path, r.Status, status, r.Body)
	}
	if out != nil {
		r.json(c.h.t, out)
	}
	return r
}

// signUp registers and returns a signed-in client and the user.
func (h *harness) signUp(email string) (*client, UserView) {
	h.t.Helper()
	c := h.client()
	var u UserView
	c.must(201, "POST", "/auth/register", Credentials{Email: email, Password: "password123"}, &u)
	return c, u
}

func newRSAKey(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
}

// fakeGitHub answers the GitHub REST calls the console makes.
func fakeGitHub(t *testing.T) http.Handler {
	pemText := newRSAKey(t)
	mux := http.NewServeMux()
	writeJSON := func(w http.ResponseWriter, v any) { _ = json.NewEncoder(w).Encode(v) }
	authed := func(r *http.Request) bool {
		h := r.Header.Get("Authorization")
		return h == "Bearer ghp_good" || strings.HasPrefix(h, "Bearer ghs_")
	}
	mux.HandleFunc("GET /user", func(w http.ResponseWriter, r *http.Request) {
		if !authed(r) {
			w.WriteHeader(401)
			return
		}
		writeJSON(w, map[string]string{"login": "octo"})
	})
	mux.HandleFunc("GET /user/repos", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "1" {
			w.Header().Set("Link", `<x>; rel="next"`)
			writeJSON(w, []map[string]any{{"full_name": "o/r", "default_branch": "main", "private": true}, {"full_name": "o/web", "default_branch": "dev"}})
			return
		}
		writeJSON(w, []map[string]any{{"full_name": "other/tool", "default_branch": "main", "description": "a CLI"}})
	})
	mux.HandleFunc("GET /installation/repositories", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"repositories": []map[string]any{{"full_name": "octo-org/app", "default_branch": "main"}}})
	})
	mux.HandleFunc("GET /repos/{o}/{r}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("r") == "missing" {
			w.WriteHeader(404)
			return
		}
		writeJSON(w, map[string]any{"full_name": r.PathValue("o") + "/" + r.PathValue("r"), "default_branch": "main"})
	})
	mux.HandleFunc("GET /repos/{o}/{r}/git/ref/heads/{b...}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("r") == "empty" {
			w.WriteHeader(409)
			_, _ = w.Write([]byte(`{"message":"Git Repository is empty."}`))
			return
		}
		if b := r.PathValue("b"); b != "main" && b != "feature/x" && b != "dev" {
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
			return
		}
		writeJSON(w, map[string]any{"object": map[string]string{"sha": "abc"}})
	})
	mux.HandleFunc("GET /repos/{o}/{r}/branches", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, []map[string]string{{"name": "main"}, {"name": "feature/x"}, {"name": "dev"}})
	})
	mux.HandleFunc("POST /app-manifests/{code}/conversions", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("code") != "good-code" {
			w.WriteHeader(404)
			return
		}
		writeJSON(w, map[string]any{"id": 77, "slug": "agent-teams-test", "name": "Agent Teams test", "client_id": "cid",
			"client_secret": "csecret", "pem": pemText, "webhook_secret": "whsecret", "html_url": "https://github.com/apps/agent-teams-test",
			"owner": map[string]any{"login": "boss"}})
	})
	mux.HandleFunc("POST /app/installations/{id}/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"token": "ghs_install_" + r.PathValue("id"), "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
	})
	mux.HandleFunc("GET /app/installations/{id}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"id": 42, "account": map[string]string{"login": "octo-org", "type": "Organization"},
			"permissions": map[string]string{"contents": "write", "checks": "read"}})
	})
	mux.HandleFunc("POST /login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["code"] != "oauth-code" || body["client_secret"] != "csecret" {
			writeJSON(w, map[string]string{"error": "bad_verification_code", "error_description": "bad code"})
			return
		}
		writeJSON(w, map[string]string{"access_token": "ghu_user"})
	})
	mux.HandleFunc("GET /user/installations", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"installations": []map[string]any{{"id": 42,
			"account":     map[string]string{"login": "octo-org", "type": "Organization"},
			"permissions": map[string]string{"contents": "write"}}}})
	})
	return mux
}
