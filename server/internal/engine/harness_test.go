package engine_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/gin-gonic/gin"

	"github.com/touwaeriol/agent-teams/server/internal/auth"
	"github.com/touwaeriol/agent-teams/server/internal/config"
	"github.com/touwaeriol/agent-teams/server/internal/engine"
	"github.com/touwaeriol/agent-teams/server/internal/github"
	"github.com/touwaeriol/agent-teams/server/internal/httpapi"
	"github.com/touwaeriol/agent-teams/server/internal/kv"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw/nodetest"
	"github.com/touwaeriol/agent-teams/server/internal/office"
	"github.com/touwaeriol/agent-teams/server/internal/orchapi"
	"github.com/touwaeriol/agent-teams/server/internal/store"
	"github.com/touwaeriol/agent-teams/server/internal/testdb"
)

const secret = "e2e-secret"

// world is a running control plane (HTTP + engines) on a fresh database.
type world struct {
	t   *testing.T
	ctx context.Context
	srv *httptest.Server
	e   *engine.Engine
	db  *store.Store
	h   *httpapi.Server
}

func newWorld(t *testing.T, gh *httptest.Server) *world {
	t.Helper()
	dbURL := testdb.Migrated(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	db, err := store.Open(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if os.Getenv("E2E_LOG") != "" {
		log = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	cache := testKV(t)
	reg := officeRegistry(t, ctx, db, log)
	client := github.NewClient()
	if gh != nil {
		client = &github.Client{BaseURL: gh.URL, WebURL: gh.URL, HTTP: gh.Client()}
	}
	box := auth.NewSecretBox(secret)
	tokens := github.NewTokens(client, github.NewApps(db, box), cache, box)
	e, err := engine.New(engine.Deps{Store: db, KV: cache, GitHub: client, Tokens: tokens, Box: box, Office: reg, Log: log,
		Gateway: nodegw.Config{Validate: true, PingInterval: time.Hour}})
	if err != nil {
		t.Fatal(err)
	}
	e.Status.Debounce = 0
	e.Delivery.PollInterval = 200 * time.Millisecond
	cfg := config.Config{PublicURL: "http://console.test", SecretKeys: []string{secret}, WebDir: t.TempDir(),
		NodeDistDir: t.TempDir(), OfficeDir: t.TempDir()}
	h := httpapi.New(httpapi.Deps{Config: cfg, Store: db, KV: cache, GitHub: client, Tokens: tokens, Office: reg, Log: log,
		Nodes: e.Gateway, Events: e.Events, Hooks: e.Hooks(), Extensions: []func(api huma.API, r gin.IRoutes){orchapi.New(e, reg).Register}})
	srv := httptest.NewServer(h.Handler())
	t.Cleanup(func() {
		cancel()
		e.Gateway.CloseAll()
		srv.CloseClientConnections()
		srv.Close()
	})
	e.Start(ctx)
	return &world{t: t, ctx: ctx, srv: srv, e: e, db: db, h: h}
}

func testKV(t *testing.T) kv.KV {
	if u := os.Getenv("TEST_VALKEY_URL"); u != "" {
		v, err := kv.Dial(u)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(v.Close)
		return v
	}
	return kv.NewMemory()
}

func officeRegistry(t *testing.T, ctx context.Context, db *store.Store, log *slog.Logger) *office.Registry {
	v, err := office.NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	reg := office.NewRegistry(t.TempDir(), v, store.OfficeDB{S: db}, log)
	if err := reg.Load(ctx); err != nil {
		t.Fatal(err)
	}
	return reg
}

// browser is a signed-in web client (cookie + CSRF).
type browser struct {
	w    *world
	http *http.Client
	ip   string // client address reported through X-Forwarded-For (tests share one Valkey rate limiter)
}

var browserSeq atomic.Int64

func (w *world) signUp(email string) *browser {
	jar, _ := cookiejar.New(nil)
	n := browserSeq.Add(1)
	b := &browser{w: w, http: &http.Client{Jar: jar, Timeout: 20 * time.Second}, ip: fmt.Sprintf("10.77.%d.%d", n/250, n%250+1)}
	b.must(201, "POST", "/auth/register", httpapi.Credentials{Email: email, Password: "password123"}, nil)
	return b
}

func (b *browser) csrf() string {
	u, _ := url.Parse(b.w.srv.URL)
	for _, c := range b.http.Jar.Cookies(u) {
		if c.Name == httpapi.CSRFCookie {
			return c.Value
		}
	}
	return ""
}

func (b *browser) do(method, path string, body any) (int, []byte) {
	b.w.t.Helper()
	return b.send(method, path, body, method != "GET")
}

// send is do with the X-CSRF-Token header chosen by the caller.
func (b *browser) send(method, path string, body any, withCSRF bool) (int, []byte) {
	b.w.t.Helper()
	var r io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		r = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, b.w.srv.URL+path, r)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-For", b.ip)
	if withCSRF {
		req.Header.Set(httpapi.CSRFHeader, b.csrf())
	}
	resp, err := b.http.Do(req)
	if err != nil {
		b.w.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

func (b *browser) must(status int, method, path string, body, out any) {
	b.w.t.Helper()
	code, raw := b.do(method, path, body)
	if code != status {
		b.w.t.Fatalf("%s %s = %d, want %d: %s", method, path, code, status, raw)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			b.w.t.Fatalf("decode %s: %v", raw, err)
		}
	}
}

// sse collects the frames of one event stream.
type sse struct {
	mu     sync.Mutex
	frames []sseFrame
}

type sseFrame struct {
	ID    string
	Event string
	Data  string
}

func (b *browser) stream(path, lastID string) *sse {
	req, _ := http.NewRequestWithContext(b.w.ctx, "GET", b.w.srv.URL+path, nil)
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	client := &http.Client{Jar: b.http.Jar}
	resp, err := client.Do(req)
	if err != nil {
		b.w.t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		b.w.t.Fatalf("stream %s = %d", path, resp.StatusCode)
	}
	s := &sse{}
	go s.read(resp.Body)
	return s
}

func (s *sse) read(body io.ReadCloser) {
	defer body.Close()
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 1<<20), 1<<22)
	var cur sseFrame
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if cur.Event != "" {
				s.mu.Lock()
				s.frames = append(s.frames, cur)
				s.mu.Unlock()
			}
			cur = sseFrame{}
		case strings.HasPrefix(line, "id: "):
			cur.ID = line[4:]
		case strings.HasPrefix(line, "event: "):
			cur.Event = line[7:]
		case strings.HasPrefix(line, "data: "):
			cur.Data = line[6:]
		}
	}
}

// wait returns the first frame of the event type whose data contains every needle.
func (s *sse) wait(t *testing.T, event string, needles ...string) sseFrame {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if f, ok := s.find(event, needles); ok {
			return f
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatalf("no %s event containing %v; got %v", event, needles, s.types())
	return sseFrame{}
}

func (s *sse) find(event string, needles []string) (sseFrame, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, f := range s.frames {
		if f.Event == event && containsAll(f.Data, needles) {
			return f, true
		}
	}
	return sseFrame{}, false
}

func containsAll(s string, needles []string) bool {
	for _, n := range needles {
		if !strings.Contains(s, n) {
			return false
		}
	}
	return true
}

func (s *sse) types() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.frames))
	for _, f := range s.frames {
		out = append(out, f.Event)
	}
	return out
}

// connect attaches a fake daemon with the node token.
func (w *world) connect(token string, o nodetest.Options) *nodetest.Node {
	w.t.Helper()
	ctx, cancel := context.WithTimeout(w.ctx, 10*time.Second)
	defer cancel()
	n, err := nodetest.Dial(ctx, "ws"+strings.TrimPrefix(w.srv.URL, "http")+"/nodes/connect", token, o)
	if err != nil {
		w.t.Fatalf("dial node: %v", err)
	}
	w.t.Cleanup(n.Close)
	return n
}

func (w *world) waitCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(w.ctx, 20*time.Second)
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func contextShort() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 300*time.Millisecond)
}
