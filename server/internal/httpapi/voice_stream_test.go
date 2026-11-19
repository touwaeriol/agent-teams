package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/touwaeriol/agent-teams/server/internal/store"
	"github.com/touwaeriol/agent-teams/server/internal/voice"
)

const relayKey = "sk-live-relay5555"

// fakeRealtime is OpenAI's realtime transcription WebSocket: it checks the key, answers
// session.update with session.created/updated, collects appended audio and, on every commit,
// sends committed → delta → completed for a new item (plus an event the browser must not see).
type fakeRealtime struct {
	mu       sync.Mutex
	auths    []string
	queries  []string
	safety   []string
	sessions []map[string]any
	audio    bytes.Buffer
	commits  int
	// failSession answers session.update with an error carrying the key.
	failSession bool
	silent      bool // never answers session.update
	// dialDelay holds the WebSocket upgrade, updateDelay the session.updated answer.
	dialDelay, updateDelay time.Duration
}

func (f *fakeRealtime) start(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/realtime" {
			w.WriteHeader(404)
			return
		}
		f.mu.Lock()
		f.auths = append(f.auths, r.Header.Get("Authorization"))
		f.queries = append(f.queries, r.URL.RawQuery)
		f.safety = append(f.safety, r.Header.Get("OpenAI-Safety-Identifier"))
		f.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+relayKey {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"error":{"code":"invalid_api_key","message":"Incorrect API key provided"}}`))
			return
		}
		time.Sleep(f.dialDelay)
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		f.serve(r.Context(), c)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (f *fakeRealtime) serve(ctx context.Context, c *websocket.Conn) {
	send := func(v map[string]any) {
		raw, _ := json.Marshal(v)
		_ = c.Write(ctx, websocket.MessageText, raw)
	}
	send(map[string]any{"type": "session.created", "session": map[string]any{"id": "sess_relay1", "type": "transcription"}})
	for {
		_, raw, err := c.Read(ctx)
		if err != nil {
			return
		}
		var ev map[string]any
		_ = json.Unmarshal(raw, &ev)
		switch ev["type"] {
		case "session.update":
			f.mu.Lock()
			f.sessions = append(f.sessions, ev["session"].(map[string]any))
			f.mu.Unlock()
			switch {
			case f.silent:
			case f.failSession:
				send(map[string]any{"type": "error", "error": map[string]any{"code": "invalid_value", "message": "bad session for key " + relayKey}})
			default:
				time.Sleep(f.updateDelay)
				send(map[string]any{"type": "session.updated", "session": map[string]any{"id": "sess_relay1", "type": "transcription"}})
			}
		case "input_audio_buffer.append":
			b, _ := base64.StdEncoding.DecodeString(ev["audio"].(string))
			f.mu.Lock()
			f.audio.Write(b)
			f.mu.Unlock()
		case "input_audio_buffer.commit":
			f.mu.Lock()
			f.commits++
			item := fmt.Sprintf("item_%d", f.commits)
			f.mu.Unlock()
			send(map[string]any{"type": "input_audio_buffer.committed", "item_id": item, "previous_item_id": nil})
			send(map[string]any{"type": "rate_limits.updated", "secret": relayKey})
			send(map[string]any{"type": "conversation.item.input_audio_transcription.delta", "item_id": item, "content_index": 0, "delta": "你好", "logprobs": []any{}})
			send(map[string]any{"type": "conversation.item.input_audio_transcription.completed", "item_id": item, "content_index": 0, "transcript": "你好。", "usage": map[string]any{"seconds": 1}})
		}
	}
}

func (f *fakeRealtime) snapshot() (auths, queries, safety []string, sessions []map[string]any, audio []byte, commits int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.auths...), append([]string(nil), f.queries...), append([]string(nil), f.safety...),
		append([]map[string]any(nil), f.sessions...), append([]byte(nil), f.audio.Bytes()...), f.commits
}

// voiceWS dials /api/voice/stream as the client's browser would (cookies + CSRF subprotocol).
func (c *client) voiceWS(t *testing.T, query string, protocols []string, header http.Header) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	u := "ws" + strings.TrimPrefix(c.h.srv.URL, "http") + "/api/voice/stream" + query
	if protocols == nil {
		protocols = []string{voiceStreamProtocol, voiceCSRFPrefix + c.cookie(CSRFCookie)}
	}
	conn, res, err := websocket.Dial(ctx, u, &websocket.DialOptions{HTTPClient: c.http, Subprotocols: protocols, HTTPHeader: header})
	if conn != nil {
		t.Cleanup(func() { conn.CloseNow() })
	}
	return conn, res, err
}

type browserEvent struct {
	Type          string `json:"type"`
	Fatal         bool   `json:"fatal"`
	ItemID        string `json:"item_id"`
	Delta         string `json:"delta"`
	Transcript    string `json:"transcript"`
	Model         string `json:"model"`
	ClientCommits bool   `json:"client_commits"`
	MaxSeconds    int    `json:"max_seconds"`
	Error         struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	raw string
}

func readEvent(t *testing.T, c *websocket.Conn) browserEvent {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, raw, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var ev browserEvent
	if err := json.Unmarshal(raw, &ev); err != nil {
		t.Fatalf("decode %s", raw)
	}
	ev.raw = string(raw)
	if strings.Contains(ev.raw, relayKey) || strings.Contains(ev.raw, "relay5555") {
		t.Fatalf("key reached the browser: %s", raw)
	}
	return ev
}

// drain reads until the server's close arrives (answering the close handshake).
func drain(t *testing.T, c *websocket.Conn) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for {
		if _, _, err := c.Read(ctx); err != nil {
			return
		}
	}
}

func relayHarness(t *testing.T, fake *fakeRealtime) (*harness, *client, UserView) {
	h := newHarness(t)
	admin, _ := h.signUp("a@example.com")
	user, u := h.signUp("b@example.com")
	oa := fake.start(t)
	admin.must(200, "PUT", "/api/admin/voice", map[string]any{"enabled": true, "api_key": relayKey, "base_url": oa.URL + "/v1", "delay": "low"}, nil)
	return h, user, u
}

func TestVoiceStreamRelay(t *testing.T) {
	fake := &fakeRealtime{}
	h, user, u := relayHarness(t, fake)

	var cfg VoiceConfig
	user.must(200, "GET", "/api/voice/config", nil, &cfg)
	if cfg.Transport != voice.TransportRelay {
		t.Fatalf("default transport %+v", cfg)
	}
	conn, res, err := user.voiceWS(t, "?language=zh", nil, nil)
	if err != nil {
		t.Fatalf("dial: %v %v", err, res)
	}
	if conn.Subprotocol() != voiceStreamProtocol {
		t.Fatalf("subprotocol %q", conn.Subprotocol())
	}
	ready := readEvent(t, conn)
	if ready.Type != "ready" || ready.Model != voice.DefaultModel || !ready.ClientCommits || ready.MaxSeconds != 300 {
		t.Fatalf("ready %+v", ready)
	}
	frame := make([]byte, 4800) // 100 ms of PCM16 at 24 kHz
	for i := range frame {
		frame[i] = byte(i % 251)
	}
	ctx := t.Context()
	for range 3 {
		if err := conn.Write(ctx, websocket.MessageBinary, frame); err != nil {
			t.Fatal(err)
		}
	}
	_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"commit"}`))
	want := []browserEvent{{Type: "input_audio_buffer.committed", ItemID: "item_1"},
		{Type: "conversation.item.input_audio_transcription.delta", ItemID: "item_1", Delta: "你好"},
		{Type: "conversation.item.input_audio_transcription.completed", ItemID: "item_1", Transcript: "你好。"}}
	for _, w := range want {
		got := readEvent(t, conn)
		if got.Type != w.Type || got.ItemID != w.ItemID || got.Delta != w.Delta || got.Transcript != w.Transcript {
			t.Fatalf("event %s, want %+v", got.raw, w)
		}
		if strings.Contains(got.raw, "usage") || strings.Contains(got.raw, "content_index") || strings.Contains(got.raw, "logprobs") {
			t.Fatalf("extra fields relayed: %s", got.raw)
		}
	}
	_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"stop"}`))
	rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, _, err := conn.Read(rctx); websocket.CloseStatus(err) != websocket.StatusNormalClosure {
		t.Fatalf("after stop: %v", err)
	}

	auths, queries, safety, sessions, audio, commits := fake.snapshot()
	if auths[0] != "Bearer "+relayKey || queries[0] != "intent=transcription" || safety[0] != voice.SafetyID(u.ID) || strings.Contains(safety[0], u.ID) {
		t.Fatalf("upstream handshake %v %v %v", auths, queries, safety)
	}
	in := sessions[0]["audio"].(map[string]any)["input"].(map[string]any)
	tr := in["transcription"].(map[string]any)
	format := in["format"].(map[string]any)
	if sessions[0]["type"] != "transcription" || format["type"] != "audio/pcm" || format["rate"] != float64(24000) || in["turn_detection"] != nil ||
		tr["model"] != voice.DefaultModel || tr["language"] != "zh" || tr["delay"] != "low" || in["noise_reduction"].(map[string]any)["type"] != "near_field" {
		t.Fatalf("session.update %v", sessions[0])
	}
	if !bytes.Equal(audio, bytes.Repeat(frame, 3)) || commits != 1 {
		t.Fatalf("audio %d bytes, %d commits", len(audio), commits)
	}
	var entries []store.AuditEntry // the stop audit is written after the socket closes
	for i := 0; i < 50 && len(entries) == 0 && err == nil; i++ {
		time.Sleep(100 * time.Millisecond)
		entries, err = h.db.ListAudit(t.Context(), store.AuditFilter{Action: "voice_stream_stop", Limit: 10})
	}
	var detail map[string]any
	if err != nil || len(entries) != 1 || json.Unmarshal(entries[0].Detail, &detail) != nil || detail["reason"] != "stopped" || detail["commits"] != float64(1) ||
		detail["audio_seconds"] != 0.3 || entries[0].TargetID != "sess_relay1" {
		t.Fatalf("audit %+v %s %v", entries, detail, err)
	}
	if starts, _ := h.db.ListAudit(t.Context(), store.AuditFilter{Action: "voice_stream_start", Limit: 10}); len(starts) != 1 {
		t.Fatalf("start audit %+v", starts)
	}
}

func TestVoiceStreamAuth(t *testing.T) {
	fake := &fakeRealtime{}
	h, user, _ := relayHarness(t, fake)

	anon := h.client()
	if _, res, err := anon.voiceWS(t, "", []string{voiceStreamProtocol}, nil); err == nil || res == nil || res.StatusCode != 401 {
		t.Fatalf("anonymous = %v %v", err, res)
	}
	for name, protos := range map[string][]string{
		"no csrf":    {voiceStreamProtocol},
		"wrong csrf": {voiceStreamProtocol, voiceCSRFPrefix + "nope"},
	} {
		if _, res, err := user.voiceWS(t, "", protos, nil); err == nil || res == nil || res.StatusCode != 403 {
			t.Fatalf("%s = %v %v", name, err, res)
		}
	}
	if _, res, err := user.voiceWS(t, "", nil, http.Header{"Origin": {"https://evil.example"}}); err == nil || res == nil || res.StatusCode != 403 {
		t.Fatalf("cross-origin = %v %v", err, res)
	}
	if _, res, err := user.voiceWS(t, "?language=xx", nil, nil); err == nil || res == nil || res.StatusCode != 422 {
		t.Fatalf("bad language = %v %v", err, res)
	}
	// Without our subprotocol (an old client) the stream is refused with a message.
	conn, _, err := user.voiceWS(t, "", []string{voiceCSRFPrefix + user.cookie(CSRFCookie)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ev := readEvent(t, conn); !ev.Fatal || ev.Error.Code != "voice_protocol" {
		t.Fatalf("no protocol %+v", ev)
	}
	if _, _, _, sessions, _, _ := fake.snapshot(); len(sessions) != 0 {
		t.Fatalf("refused streams reached OpenAI: %d", len(sessions))
	}
}

func TestVoiceStreamLimits(t *testing.T) {
	fake := &fakeRealtime{}
	_, user, _ := relayHarness(t, fake)

	var open []*websocket.Conn
	for range voiceMaxStreams {
		c, _, err := user.voiceWS(t, "", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if ev := readEvent(t, c); ev.Type != "ready" {
			t.Fatalf("stream %+v", ev)
		}
		open = append(open, c)
	}
	c, _, err := user.voiceWS(t, "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ev := readEvent(t, c); !ev.Fatal || ev.Error.Code != "voice_too_many" {
		t.Fatalf("third stream %+v", ev)
	}
	// Ending one frees its slot.
	_ = open[0].Write(t.Context(), websocket.MessageText, []byte(`{"type":"stop"}`))
	_, _, _ = open[0].Read(t.Context())
	time.Sleep(100 * time.Millisecond)
	c, _, _ = user.voiceWS(t, "", nil, nil)
	if ev := readEvent(t, c); ev.Type != "ready" {
		t.Fatalf("after freeing a slot %+v", ev)
	}
	// An odd-length frame is not PCM16.
	_ = c.Write(t.Context(), websocket.MessageBinary, []byte{1, 2, 3})
	if ev := readEvent(t, c); !ev.Fatal || ev.Error.Code != "voice_bad_frame" {
		t.Fatalf("odd frame %+v", ev)
	}
}

func TestVoiceStreamTimeouts(t *testing.T) {
	old := voiceStreamOptions
	voiceStreamOptions = voice.RelayOptions{Idle: 300 * time.Millisecond, MaxDuration: 2 * time.Second, Handshake: 500 * time.Millisecond}
	t.Cleanup(func() { voiceStreamOptions = old })
	fake := &fakeRealtime{}
	_, user, _ := relayHarness(t, fake)

	c, _, _ := user.voiceWS(t, "", nil, nil)
	if ev := readEvent(t, c); ev.Type != "ready" || ev.MaxSeconds != 2 {
		t.Fatalf("ready %+v", ev)
	}
	if ev := readEvent(t, c); !ev.Fatal || ev.Error.Code != "voice_idle" {
		t.Fatalf("idle %+v", ev)
	}
	drain(t, c)
	// Keep talking past the maximum length.
	c, _, _ = user.voiceWS(t, "", nil, nil)
	readEvent(t, c)
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(100 * time.Millisecond):
				_ = c.Write(context.Background(), websocket.MessageBinary, make([]byte, 4800))
			}
		}
	}()
	if ev := readEvent(t, c); !ev.Fatal || ev.Error.Code != "voice_max_duration" {
		t.Fatalf("max duration %+v", ev)
	}
	drain(t, c)
	time.Sleep(100 * time.Millisecond) // slots are released after the close handshake
	// OpenAI that never accepts the session: the browser hears within the handshake timeout.
	fake.mu.Lock()
	fake.silent = true
	fake.mu.Unlock()
	start := time.Now()
	c, _, _ = user.voiceWS(t, "", nil, nil)
	if ev := readEvent(t, c); !ev.Fatal || ev.Error.Code != "voice_timeout" || !strings.HasPrefix(ev.Error.Message, "连不上语音服务") || time.Since(start) > 3*time.Second {
		t.Fatalf("silent upstream %+v after %s", ev, time.Since(start))
	}
}

// Dialing and the handshake share one deadline from Accept: each alone fits, together they don't.
func TestVoiceStreamSharedDeadline(t *testing.T) {
	old := voiceStreamOptions
	voiceStreamOptions = voice.RelayOptions{Handshake: 600 * time.Millisecond}
	t.Cleanup(func() { voiceStreamOptions = old })

	fake := &fakeRealtime{dialDelay: 400 * time.Millisecond, updateDelay: 400 * time.Millisecond}
	h, user, _ := relayHarness(t, fake)
	start := time.Now()
	c, _, err := user.voiceWS(t, "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ev := readEvent(t, c)
	if took := time.Since(start); !ev.Fatal || ev.Error.Code != "voice_timeout" || took > 750*time.Millisecond {
		t.Fatalf("dial+handshake over the deadline: %+v after %s", ev, took)
	}
	drain(t, c)
	stop := waitAudit(t, h, "voice_stream_stop")
	if stop["reason"] != "handshake: voice_timeout" || stop["dial_ms"].(float64) < 400 || stop["handshake_ms"].(float64) < 100 {
		t.Fatalf("stop audit %v", stop)
	}

	// A dial alone slower than the deadline fails as a timeout too.
	slow := &fakeRealtime{dialDelay: 900 * time.Millisecond}
	h2, user2, _ := relayHarness(t, slow)
	start = time.Now()
	c, _, _ = user2.voiceWS(t, "", nil, nil)
	ev = readEvent(t, c)
	if took := time.Since(start); !ev.Fatal || ev.Error.Code != "voice_timeout" || took > 750*time.Millisecond {
		t.Fatalf("slow dial: %+v after %s", ev, took)
	}
	drain(t, c)
	if stop := waitAudit(t, h2, "voice_stream_stop"); stop["reason"] != "voice_timeout" || stop["dial_ms"].(float64) < 500 {
		t.Fatalf("slow dial audit %v", stop)
	}
}

// The start audit carries how long dialing and the session handshake took.
func TestVoiceStreamTimings(t *testing.T) {
	fake := &fakeRealtime{dialDelay: 60 * time.Millisecond, updateDelay: 80 * time.Millisecond}
	h, user, _ := relayHarness(t, fake)
	c, _, _ := user.voiceWS(t, "", nil, nil)
	if ev := readEvent(t, c); ev.Type != "ready" {
		t.Fatalf("ready %+v", ev)
	}
	start := waitAudit(t, h, "voice_stream_start")
	if start["dial_ms"].(float64) < 60 || start["handshake_ms"].(float64) < 80 || start["handshake_ms"].(float64) > 2000 {
		t.Fatalf("start audit %v", start)
	}
	_ = c.Write(t.Context(), websocket.MessageText, []byte(`{"type":"stop"}`))
	drain(t, c)
}

// Audio the browser recorded while the relay connected arrives in one burst after ready: the rate
// clock starts at Accept, so that burst is allowed, and the limit still holds beyond it.
func TestVoiceStreamBufferedBurst(t *testing.T) {
	fake := &fakeRealtime{dialDelay: time.Second}
	_, user, _ := relayHarness(t, fake)
	frame := make([]byte, 4800) // 100 ms
	c, _, _ := user.voiceWS(t, "", nil, nil)
	if ev := readEvent(t, c); ev.Type != "ready" {
		t.Fatalf("ready %+v", ev)
	}
	// 1 s of connecting plus up to 8 s buffered, and some slack: 10.5 s at once.
	for range 105 {
		if err := c.Write(t.Context(), websocket.MessageBinary, frame); err != nil {
			t.Fatal(err)
		}
	}
	_ = c.Write(t.Context(), websocket.MessageText, []byte(`{"type":"commit"}`))
	if ev := readEvent(t, c); ev.Type != "input_audio_buffer.committed" {
		t.Fatalf("after the buffered burst %+v", ev)
	}
	_ = c.Write(t.Context(), websocket.MessageText, []byte(`{"type":"stop"}`))
	drain(t, c)
	time.Sleep(100 * time.Millisecond) // slot released

	// Far more than real time plus the burst allowance is still refused.
	c, _, _ = user.voiceWS(t, "", nil, nil)
	readEvent(t, c)
	for range 130 {
		if c.Write(t.Context(), websocket.MessageBinary, frame) != nil {
			break
		}
	}
	for {
		ev := readEvent(t, c)
		if ev.Fatal {
			if ev.Error.Code != "voice_too_fast" {
				t.Fatalf("flood %+v", ev)
			}
			break
		}
	}
}

// waitAudit returns the detail of the newest audit entry with this action (written after the socket closes).
func waitAudit(t *testing.T, h *harness, action string) map[string]any {
	t.Helper()
	for range 50 {
		entries, err := h.db.ListAudit(t.Context(), store.AuditFilter{Action: action, Limit: 1})
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) == 1 {
			var d map[string]any
			_ = json.Unmarshal(entries[0].Detail, &d)
			return d
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("no %s audit", action)
	return nil
}

func TestVoiceStreamUpstreamErrors(t *testing.T) {
	fake := &fakeRealtime{failSession: true}
	h, user, _ := relayHarness(t, fake)

	c, _, _ := user.voiceWS(t, "", nil, nil)
	ev := readEvent(t, c) // readEvent fails if the key is in the message
	if !ev.Fatal || ev.Error.Code != "voice_upstream" || !strings.Contains(ev.Error.Message, "bad session for key ***") {
		t.Fatalf("session rejected %+v", ev)
	}
	// A key OpenAI refuses at the handshake.
	sealed, err := h.server.box.Seal("sk-wrong-key-0000")
	if err != nil {
		t.Fatal(err)
	}
	var v voice.Settings
	if _, err := h.db.Setting(t.Context(), "voice", &v); err != nil {
		t.Fatal(err)
	}
	v.SealedKey = sealed
	if err := h.db.SaveSetting(t.Context(), "voice", v); err != nil {
		t.Fatal(err)
	}
	c, _, _ = user.voiceWS(t, "", nil, nil)
	if ev := readEvent(t, c); !ev.Fatal || ev.Error.Code != "voice_key_rejected" {
		t.Fatalf("rejected key %+v", ev)
	}
	// Voice switched off.
	v.Enabled = false
	_ = h.db.SaveSetting(t.Context(), "voice", v)
	c, _, _ = user.voiceWS(t, "", nil, nil)
	if ev := readEvent(t, c); !ev.Fatal || ev.Error.Code != "voice_disabled" {
		t.Fatalf("disabled %+v", ev)
	}
}

func TestVoiceTransportSetting(t *testing.T) {
	h := newHarness(t)
	admin, _ := h.signUp("a@example.com")
	user, _ := h.signUp("b@example.com")
	oa := (&fakeOpenAI{}).start(t)
	var v VoiceAdmin
	admin.must(200, "PUT", "/api/admin/voice", map[string]any{"enabled": true, "api_key": "sk-live-abcd9876", "base_url": oa.URL + "/v1"}, &v)
	if v.Transport != "relay" {
		t.Fatalf("default %+v", v)
	}
	admin.must(200, "PUT", "/api/admin/voice", map[string]any{"enabled": true, "base_url": oa.URL + "/v1", "transport": "webrtc"}, &v)
	var cfg VoiceConfig
	user.must(200, "GET", "/api/voice/config", nil, &cfg)
	if v.Transport != "webrtc" || cfg.Transport != "webrtc" {
		t.Fatalf("webrtc %+v %+v", v, cfg)
	}
	if r := admin.do("PUT", "/api/admin/voice", map[string]any{"enabled": true, "base_url": oa.URL + "/v1", "transport": "carrier-pigeon"}); r.Status != 422 {
		t.Fatalf("bad transport = %d", r.Status)
	}
}
