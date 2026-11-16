package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/touwaeriol/agent-teams/server/internal/voice"
)

// fakeOpenAI records client secret requests and answers like POST /v1/realtime/client_secrets.
type fakeOpenAI struct {
	mu     sync.Mutex
	bodies []map[string]any
	auths  []string
}

func (f *fakeOpenAI) start(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v1/realtime/client_secrets" {
			w.WriteHeader(404)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var b map[string]any
		_ = json.Unmarshal(raw, &b)
		f.mu.Lock()
		f.bodies = append(f.bodies, b)
		f.auths = append(f.auths, r.Header.Get("Authorization"))
		f.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer sk-live-abcd9876" {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"error":{"code":"invalid_api_key","message":"Incorrect API key provided"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"value":"ek_test_1","expires_at":1900000000,"session":{"id":"sess_abc","object":"realtime.transcription_session","type":"transcription"}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (f *fakeOpenAI) last() (map[string]any, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bodies[len(f.bodies)-1], f.auths[len(f.auths)-1]
}

func TestVoiceAdminConfig(t *testing.T) {
	h := newHarness(t)
	admin, _ := h.signUp("a@example.com")
	user, _ := h.signUp("b@example.com")
	oa := (&fakeOpenAI{}).start(t)

	for _, r := range []response{user.do("GET", "/api/admin/voice", nil), user.do("PUT", "/api/admin/voice", map[string]any{"enabled": false}),
		user.do("POST", "/api/admin/voice/test", map[string]any{})} {
		if r.Status != 403 {
			t.Fatalf("non-admin = %d", r.Status)
		}
	}
	var v VoiceAdmin
	admin.must(200, "GET", "/api/admin/voice", nil, &v)
	if v.Enabled || v.KeySet || v.Model != voice.DefaultModel || v.NoiseReduction != "near_field" || v.Usable {
		t.Fatalf("defaults %+v", v)
	}
	if r := admin.do("PUT", "/api/admin/voice", map[string]any{"enabled": true}); r.Status != 422 {
		t.Fatalf("enable without key = %d", r.Status)
	}
	if r := admin.do("PUT", "/api/admin/voice", map[string]any{"enabled": false, "base_url": "api.openai.com"}); r.Status != 422 {
		t.Fatalf("bad base url = %d", r.Status)
	}
	admin.must(200, "PUT", "/api/admin/voice", map[string]any{"enabled": true, "api_key": "sk-live-abcd9876", "base_url": oa.URL + "/v1"}, &v)
	if !v.Usable || v.KeyLast4 != "9876" {
		t.Fatalf("saved %+v", v)
	}
	r := admin.do("GET", "/api/admin/voice", nil)
	if strings.Contains(string(r.Body), "sk-live") || strings.Contains(string(r.Body), "sealed") {
		t.Fatalf("key leaked: %s", r.Body)
	}
	var stored voice.Settings
	if _, err := h.db.Setting(t.Context(), "voice", &stored); err != nil || stored.SealedKey == "" || strings.Contains(stored.SealedKey, "abcd9876") {
		t.Fatalf("stored %+v %v", stored, err)
	}
	if plain, err := h.server.box.Open(stored.SealedKey); err != nil || plain != "sk-live-abcd9876" {
		t.Fatalf("unseal %q %v", plain, err)
	}
	// Saving without api_key keeps the key.
	admin.must(200, "PUT", "/api/admin/voice", map[string]any{"enabled": true, "base_url": oa.URL + "/v1", "delay": "low"}, &v)
	if !v.KeySet || v.Delay != "low" {
		t.Fatalf("kept %+v", v)
	}
	// The saved key is only ever sent to the address it was saved for: moving the base URL (or
	// testing against another one) needs the key typed again.
	for _, r := range []response{
		admin.do("PUT", "/api/admin/voice", map[string]any{"enabled": true, "base_url": "https://attacker.example/v1"}),
		admin.do("PUT", "/api/admin/voice", map[string]any{"enabled": true}),
		admin.do("POST", "/api/admin/voice/test", map[string]any{"base_url": "https://attacker.example/v1"}),
	} {
		if r.Status != 422 {
			t.Fatalf("saved key sent elsewhere = %d %s", r.Status, r.Body)
		}
	}
	var test struct {
		OK bool `json:"ok"`
	}
	admin.must(200, "POST", "/api/admin/voice/test", map[string]any{"base_url": oa.URL + "/v1"}, &test)
	if r := admin.do("POST", "/api/admin/voice/test", map[string]any{"api_key": "sk-wrong", "base_url": oa.URL + "/v1"}); r.Status != 502 || r.apiError(t).Code != "voice_key_rejected" {
		t.Fatalf("wrong key test = %d %s", r.Status, r.Body)
	}
	var audit struct {
		Items []map[string]any `json:"items"`
	}
	admin.must(200, "GET", "/api/admin/audit?action=voice_settings_change", nil, &audit)
	if len(audit.Items) != 2 || strings.Contains(string(admin.do("GET", "/api/admin/audit", nil).Body), "abcd9876") {
		t.Fatalf("audit %+v", audit)
	}
	admin.must(200, "PUT", "/api/admin/voice", map[string]any{"enabled": false, "clear_key": true}, &v)
	if v.KeySet {
		t.Fatalf("cleared %+v", v)
	}
}

func TestVoiceSession(t *testing.T) {
	h := newHarness(t)
	admin, _ := h.signUp("a@example.com")
	user, u := h.signUp("b@example.com")
	fake := &fakeOpenAI{}
	oa := fake.start(t)

	var cfg VoiceConfig
	user.must(200, "GET", "/api/voice/config", nil, &cfg)
	if cfg.Enabled || len(cfg.Languages) != 0 {
		t.Fatalf("unconfigured %+v", cfg)
	}
	if r := user.do("POST", "/api/voice/session", map[string]any{}); r.Status != 404 {
		t.Fatalf("disabled session = %d", r.Status)
	}
	if r := h.client().do("GET", "/api/voice/config", nil); r.Status != 401 {
		t.Fatalf("anonymous config = %d", r.Status)
	}
	admin.must(200, "PUT", "/api/admin/voice", map[string]any{"enabled": true, "api_key": "sk-live-abcd9876", "base_url": oa.URL + "/v1", "delay": "low"}, nil)
	user.must(200, "GET", "/api/voice/config", nil, &cfg)
	if !cfg.Enabled || len(cfg.Languages) < 10 || cfg.Languages[0].Code != "zh" {
		t.Fatalf("config %+v", cfg)
	}
	r := user.do("GET", "/api/voice/config", nil)
	if strings.Contains(string(r.Body), "sk-") || strings.Contains(string(r.Body), oa.URL) {
		t.Fatalf("config leaks: %s", r.Body)
	}

	var s VoiceSession
	user.must(200, "POST", "/api/voice/session", map[string]any{"language": "zh"}, &s)
	if s.ClientSecret != "ek_test_1" || s.ExpiresAt != 1900000000 || s.Transport != "webrtc" || s.ConnectURL != oa.URL+"/v1/realtime/calls" ||
		!s.ClientCommits || s.Model != voice.DefaultModel || s.Language != "zh" {
		t.Fatalf("session %+v", s)
	}
	body, auth := fake.last()
	if auth != "Bearer sk-live-abcd9876" {
		t.Fatalf("auth %s", auth)
	}
	tr := body["session"].(map[string]any)["audio"].(map[string]any)["input"].(map[string]any)
	if tr["turn_detection"] != nil {
		t.Fatalf("turn detection %v", tr)
	}
	want := map[string]any{"model": "gpt-realtime-whisper", "language": "zh", "delay": "low"}
	got := tr["transcription"].(map[string]any)
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("transcription %v", got)
		}
	}
	user.must(200, "POST", "/api/voice/session", map[string]any{"language": "auto"}, &s)
	body, _ = fake.last()
	got = body["session"].(map[string]any)["audio"].(map[string]any)["input"].(map[string]any)["transcription"].(map[string]any)
	if _, ok := got["language"]; ok || s.Language != "auto" {
		t.Fatalf("auto sent a language: %v", got)
	}
	if r := user.do("POST", "/api/voice/session", map[string]any{"language": "klingon"}); r.Status != 422 {
		t.Fatalf("unknown language = %d", r.Status)
	}
	var audit struct {
		Items []map[string]any `json:"items"`
	}
	admin.must(200, "GET", "/api/admin/audit?action=voice_session&user="+u.ID, nil, &audit)
	if len(audit.Items) != 2 || strings.Contains(string(admin.do("GET", "/api/admin/audit?action=voice_session", nil).Body), "ek_test") {
		t.Fatalf("audit %+v", audit)
	}

	// Per-user rate limit: 20 per minute (2 used above).
	for i := 0; i < 18; i++ {
		user.must(200, "POST", "/api/voice/session", map[string]any{}, nil)
	}
	if r := user.do("POST", "/api/voice/session", map[string]any{}); r.Status != 429 || r.Header.Get("Retry-After") == "" {
		t.Fatalf("21st session = %d", r.Status)
	}
	admin.must(200, "POST", "/api/voice/session", map[string]any{}, nil) // another user's budget

	admin.must(200, "PUT", "/api/admin/voice", map[string]any{"enabled": false, "base_url": oa.URL + "/v1"}, nil)
	user.must(200, "GET", "/api/voice/config", nil, &cfg)
	if cfg.Enabled {
		t.Fatal("still enabled")
	}
}

func TestVoiceUpstreamRejectsKey(t *testing.T) {
	h := newHarness(t)
	admin, _ := h.signUp("a@example.com")
	oa := (&fakeOpenAI{}).start(t)
	admin.must(200, "PUT", "/api/admin/voice", map[string]any{"enabled": true, "api_key": "sk-revoked-0000", "base_url": oa.URL + "/v1"}, nil)
	r := admin.do("POST", "/api/voice/session", map[string]any{"language": "en"})
	if r.Status != 502 || r.apiError(t).Code != "voice_key_rejected" || strings.Contains(string(r.Body), "sk-") {
		t.Fatalf("rejected = %d %s", r.Status, r.Body)
	}
}
