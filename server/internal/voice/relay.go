package voice

// Server relay: the browser streams microphone audio to the control plane over a WebSocket and
// the control plane forwards it to OpenAI's realtime transcription WebSocket with the platform
// key, so the browser never has to reach api.openai.com (proxies, regions without access, UDP
// blocked for WebRTC).
//
// OpenAI side (developers.openai.com, realtime transcription guide, 2026-11):
//
//	wss://api.openai.com/v1/realtime?intent=transcription   (no model in the URL: a model URL
//	                                                          opens a conversation session)
//	Authorization: Bearer <API key>
//	→ {"type":"session.update","session":{"type":"transcription","audio":{"input":{…}}}}
//	→ {"type":"input_audio_buffer.append","audio":"<base64 PCM16 24 kHz mono>"}
//	→ {"type":"input_audio_buffer.commit"}                  (client-side end of utterance)
//	← session.created, session.updated, input_audio_buffer.committed,
//	  conversation.item.input_audio_transcription.delta|completed|failed, error
//
// Browser side (subprotocol "agent-teams-voice"):
//
//	→ binary frames: PCM16 little-endian, 24 kHz mono (≈100 ms each)
//	→ {"type":"commit"} | {"type":"stop"}
//	← {"type":"ready","model","client_commits","max_seconds"} once OpenAI accepted the session
//	← the transcript events above, reduced to type/item_id/delta/transcript/error{code,message}
//	← {"type":"error","fatal":true,"error":{"code","message"}} before the relay closes on a problem

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Relay limits.
const (
	// MaxFrame is the largest browser message accepted (100 ms of audio is 4800 bytes).
	MaxFrame = 64 << 10
	// MaxStream is how long one recording may last.
	MaxStream = 5 * time.Minute
	// IdleTimeout closes a stream when the browser sends nothing for this long.
	IdleTimeout = 30 * time.Second
	// HandshakeTimeout bounds dialing OpenAI and getting the session accepted.
	HandshakeTimeout = 10 * time.Second
	// BytesPerSecond is PCM16 mono at 24 kHz.
	BytesPerSecond = 48000
	// burstBytes is how far ahead of real time a browser may send (buffered audio after a stall).
	burstBytes = 10 * BytesPerSecond
)

// StreamURL is OpenAI's realtime transcription WebSocket under the configured base URL.
func (s Settings) StreamURL() string {
	api := s.API()
	switch {
	case strings.HasPrefix(api, "https://"):
		api = "wss://" + strings.TrimPrefix(api, "https://")
	case strings.HasPrefix(api, "http://"):
		api = "ws://" + strings.TrimPrefix(api, "http://")
	}
	return api + "/realtime?intent=transcription"
}

// SessionUpdate is the session.update event that configures a transcription session; the
// session object is the one client secrets are minted with.
func SessionUpdate(r Request) map[string]any {
	return map[string]any{"type": "session.update", "session": Body(r)["session"]}
}

// SafetyID is the OpenAI-Safety-Identifier for a user: a hash, never the id itself.
func SafetyID(userID string) string {
	sum := sha256.Sum256([]byte("agent-teams-voice:" + userID))
	return hex.EncodeToString(sum[:16])
}

// Dial opens OpenAI's transcription WebSocket with the API key. A refused handshake comes back
// as *Error with OpenAI's status and (redacted) message.
func Dial(ctx context.Context, s Settings, apiKey, safetyID string) (*websocket.Conn, error) {
	h := http.Header{"Authorization": {"Bearer " + apiKey}}
	if safetyID != "" {
		h.Set("OpenAI-Safety-Identifier", safetyID)
	}
	conn, res, err := websocket.Dial(ctx, s.StreamURL(), &websocket.DialOptions{HTTPHeader: h})
	if err == nil {
		conn.SetReadLimit(1 << 20)
		return conn, nil
	}
	if res == nil || res.StatusCode == http.StatusSwitchingProtocols {
		return nil, &Error{Message: redact(err.Error(), apiKey)}
	}
	var body []byte
	if res.Body != nil {
		body, _ = io.ReadAll(io.LimitReader(res.Body, 1<<16))
		_ = res.Body.Close()
	}
	e := apiError(res.StatusCode, body)
	e.Message = redact(e.Message, apiKey)
	return nil, e
}

// RelayOptions configure one relayed recording.
type RelayOptions struct {
	Request Request
	APIKey  string // only used to redact upstream messages
	// Overridable in tests; zero = the constants above.
	MaxDuration time.Duration
	Idle        time.Duration
	Handshake   time.Duration
}

func (o RelayOptions) withDefaults() RelayOptions {
	if o.MaxDuration == 0 {
		o.MaxDuration = MaxStream
	}
	if o.Idle == 0 {
		o.Idle = IdleTimeout
	}
	if o.Handshake == 0 {
		o.Handshake = HandshakeTimeout
	}
	return o
}

// RelayResult is what the audit log gets: no audio, no text.
type RelayResult struct {
	SessionID string
	Duration  time.Duration
	Bytes     int64
	Commits   int
	Reason    string // stopped | closed | idle | max_duration | too_fast | bad_frame | upstream_closed | upstream_error | handshake: …
}

// Fatal is a problem the browser is told about before the relay closes.
type Fatal struct {
	Code    string
	Message string
}

func (f *Fatal) Error() string { return f.Code + ": " + f.Message }

// SendFatal tells the browser why the stream ends and closes it.
func SendFatal(ctx context.Context, browser *websocket.Conn, code, message string) {
	raw, _ := json.Marshal(map[string]any{"type": "error", "fatal": true, "error": map[string]string{"code": code, "message": message}})
	wctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_ = browser.Write(wctx, websocket.MessageText, raw)
	_ = browser.Close(websocket.StatusPolicyViolation, code)
}

// Relay configures the OpenAI session, tells the browser it is ready, then pumps audio up and
// transcript events down until either side ends, the browser goes idle or MaxDuration passes.
// Both connections are closed when it returns.
func Relay(ctx context.Context, browser, upstream *websocket.Conn, o RelayOptions) RelayResult {
	o = o.withDefaults()
	r := &relay{browser: browser, upstream: upstream, o: o, start: time.Now(), seen: time.Now()}
	defer upstream.CloseNow()
	defer browser.CloseNow()
	if err := r.handshake(ctx); err != nil {
		var f *Fatal
		if !errors.As(err, &f) {
			f = &Fatal{Code: "voice_unreachable", Message: "语音服务没有响应"}
		}
		SendFatal(ctx, browser, f.Code, "连不上语音服务："+f.Message)
		r.res.Reason = "handshake: " + f.Code
		return r.result()
	}
	r.pump(ctx)
	return r.result()
}

type relay struct {
	browser, upstream *websocket.Conn
	o                 RelayOptions
	start             time.Time
	mu                sync.Mutex
	res               RelayResult
	seen              time.Time // last message from the browser
}

func (r *relay) result() RelayResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.res
	out.Duration = time.Since(r.start)
	return out
}

// handshake sends session.update and waits for session.updated (or an error) within Handshake.
func (r *relay) handshake(ctx context.Context) error {
	hctx, cancel := context.WithTimeout(ctx, r.o.Handshake)
	defer cancel()
	if err := writeJSON(hctx, r.upstream, SessionUpdate(r.o.Request)); err != nil {
		return &Fatal{Code: "voice_unreachable", Message: "发不出会话设置"}
	}
	for {
		_, raw, err := r.upstream.Read(hctx)
		if err != nil {
			if hctx.Err() != nil {
				return &Fatal{Code: "voice_timeout", Message: "语音服务 10 秒内没有接受会话"}
			}
			return &Fatal{Code: "voice_unreachable", Message: "语音服务断开了连接"}
		}
		var ev upstreamEvent
		if json.Unmarshal(raw, &ev) != nil {
			continue
		}
		switch ev.Type {
		case "session.created", "session.updated":
			if ev.Session.ID != "" {
				r.res.SessionID = ev.Session.ID
			}
			if ev.Type == "session.updated" {
				return writeJSON(hctx, r.browser, map[string]any{"type": "ready", "model": r.o.Request.Model,
					"client_commits": ClientCommits(r.o.Request.Model), "max_seconds": int(r.o.MaxDuration / time.Second)})
			}
		case "error":
			return &Fatal{Code: "voice_upstream", Message: redact(ev.Error.Message, r.o.APIKey)}
		}
	}
}

// pump runs both directions until one ends, the browser goes quiet for Idle or MaxDuration
// passes. Timers are checked here rather than through read contexts: coder/websocket closes a
// connection whose Read context ends, and the browser must still get the reason.
func (r *relay) pump(ctx context.Context) {
	reasons := make(chan string, 2)
	go func() { reasons <- r.up(ctx) }()
	go func() { reasons <- r.down(ctx) }()
	deadline := time.NewTimer(r.o.MaxDuration)
	defer deadline.Stop()
	tick := time.NewTicker(min(r.o.Idle/4, time.Second))
	defer tick.Stop()
	var reason string
	for reason == "" {
		select {
		case reason = <-reasons:
		case <-ctx.Done():
			reason = "closed"
		case <-deadline.C:
			reason = "max_duration"
		case <-tick.C:
			if time.Since(r.lastSeen()) >= r.o.Idle {
				reason = "idle"
			}
		}
	}
	r.mu.Lock()
	r.res.Reason = reason
	r.mu.Unlock()
	r.finish(reason)
}

func (r *relay) lastSeen() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.seen
}

// finish closes the browser side with a message the client can show.
func (r *relay) finish(reason string) {
	bg := context.Background()
	switch reason {
	case "max_duration":
		SendFatal(bg, r.browser, "voice_max_duration", "单次录音最长 5 分钟")
	case "idle":
		SendFatal(bg, r.browser, "voice_idle", "太久没有收到声音，已停止")
	case "too_fast":
		SendFatal(bg, r.browser, "voice_too_fast", "音频发送太快")
	case "bad_frame":
		SendFatal(bg, r.browser, "voice_bad_frame", "音频格式不对")
	case "upstream_closed", "upstream_error":
		SendFatal(bg, r.browser, "voice_upstream_closed", "和语音服务的连接断开了")
	default:
		_ = r.browser.Close(websocket.StatusNormalClosure, "")
	}
	_ = r.upstream.Close(websocket.StatusNormalClosure, "")
}

// up reads the browser: audio frames become input_audio_buffer.append, commit/stop control.
func (r *relay) up(ctx context.Context) string {
	for {
		typ, data, err := r.browser.Read(ctx)
		if err != nil {
			return "closed"
		}
		r.mu.Lock()
		r.seen = time.Now()
		r.mu.Unlock()
		if reason := r.fromBrowser(ctx, typ, data); reason != "" {
			return reason
		}
	}
}

func (r *relay) fromBrowser(ctx context.Context, typ websocket.MessageType, data []byte) string {
	if typ == websocket.MessageBinary {
		return r.audio(ctx, data)
	}
	var msg struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(data, &msg)
	switch msg.Type {
	case "commit":
		r.mu.Lock()
		r.res.Commits++
		r.mu.Unlock()
		if writeJSON(ctx, r.upstream, map[string]string{"type": "input_audio_buffer.commit"}) != nil {
			return "upstream_error"
		}
	case "stop":
		return "stopped"
	}
	return ""
}

func (r *relay) audio(ctx context.Context, data []byte) string {
	if len(data) == 0 || len(data)%2 != 0 {
		return "bad_frame"
	}
	r.mu.Lock()
	r.res.Bytes += int64(len(data))
	total := r.res.Bytes
	r.mu.Unlock()
	if total > int64(time.Since(r.start).Seconds()*BytesPerSecond)+burstBytes {
		return "too_fast"
	}
	ev := map[string]string{"type": "input_audio_buffer.append", "audio": base64.StdEncoding.EncodeToString(data)}
	if writeJSON(ctx, r.upstream, ev) != nil {
		return "upstream_error"
	}
	return ""
}

// down forwards OpenAI's transcript events to the browser, reduced to the fields it uses.
func (r *relay) down(ctx context.Context) string {
	for {
		_, raw, err := r.upstream.Read(ctx)
		if err != nil {
			return "upstream_closed"
		}
		out, ok := BrowserEvent(raw, r.o.APIKey)
		if !ok {
			continue
		}
		if r.browser.Write(ctx, websocket.MessageText, out) != nil {
			return "closed"
		}
	}
}

type upstreamEvent struct {
	Type       string `json:"type"`
	ItemID     string `json:"item_id"`
	Delta      string `json:"delta"`
	Transcript string `json:"transcript"`
	Session    struct {
		ID string `json:"id"`
	} `json:"session"`
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// BrowserEvent reduces an OpenAI event to what the composer needs; anything else is dropped.
func BrowserEvent(raw []byte, apiKey string) ([]byte, bool) {
	var ev upstreamEvent
	if json.Unmarshal(raw, &ev) != nil {
		return nil, false
	}
	out := map[string]any{"type": ev.Type}
	switch ev.Type {
	case "input_audio_buffer.committed":
		out["item_id"] = ev.ItemID
	case "conversation.item.input_audio_transcription.delta":
		out["item_id"], out["delta"] = ev.ItemID, ev.Delta
	case "conversation.item.input_audio_transcription.completed":
		out["item_id"], out["transcript"] = ev.ItemID, ev.Transcript
	case "conversation.item.input_audio_transcription.failed", "error":
		if ev.ItemID != "" {
			out["item_id"] = ev.ItemID
		}
		out["error"] = map[string]string{"code": ev.Error.Code, "message": redact(ev.Error.Message, apiKey)}
	default:
		return nil, false
	}
	b, err := json.Marshal(out)
	return b, err == nil
}

func writeJSON(ctx context.Context, c *websocket.Conn, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.Write(ctx, websocket.MessageText, raw)
}
