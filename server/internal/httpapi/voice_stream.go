package httpapi

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/gin-gonic/gin"

	"github.com/touwaeriol/agent-teams/server/internal/voice"
)

// GET /api/voice/stream: the browser streams microphone audio here (WebSocket) and the server
// relays it to OpenAI with the sealed key (voice.Relay). Browsers can't set headers on a
// WebSocket, so the CSRF token rides in the subprotocol list: ["agent-teams-voice",
// "csrf.<agent_teams_csrf cookie>"]; the server answers with "agent-teams-voice". The Origin must
// match the host (coder/websocket's default check).

const (
	voiceStreamPath     = "/api/voice/stream"
	voiceStreamProtocol = "agent-teams-voice"
	voiceCSRFPrefix     = "csrf."
	// voiceMaxStreams is how many recordings one user may relay at the same time.
	voiceMaxStreams = 2
	// voiceStreamsPerMinute is how many recordings one user may start per minute.
	voiceStreamsPerMinute = 20
)

// voiceStreamOptions are the relay limits; tests shorten them.
var voiceStreamOptions = voice.RelayOptions{}

func (s *Server) registerVoiceStream(r gin.IRoutes) {
	r.GET(voiceStreamPath, s.voiceStream)
}

// voiceCSRF finds csrf.<token> among the offered subprotocols and checks it against the session.
func voiceCSRF(r *http.Request, secret string) bool {
	for _, h := range r.Header.Values("Sec-WebSocket-Protocol") {
		for _, p := range strings.Split(h, ",") {
			tok, ok := strings.CutPrefix(strings.TrimSpace(p), voiceCSRFPrefix)
			if ok && secret != "" && subtle.ConstantTimeCompare([]byte(tok), []byte(secret)) == 1 {
				return true
			}
		}
	}
	return false
}

func (s *Server) voiceStream(c *gin.Context) {
	r := c.Request
	ctx := r.Context()
	p := principalOf(ctx)
	if p == nil {
		writeError(c.Writer, fail(http.StatusUnauthorized, "unauthorized", "请先登录"))
		return
	}
	if !p.Bearer && !voiceCSRF(r, p.Session.CSRFSecret) {
		writeError(c.Writer, fail(http.StatusForbidden, "csrf", "安全校验失败，请刷新页面后重试"))
		return
	}
	lang := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("language")))
	if !voice.KnownLanguage(lang) {
		writeError(c.Writer, invalid([]FieldError{{"language", "不支持这个语言"}}, "不支持这个语言："+lang))
		return
	}
	if lang == "" {
		lang = "auto"
	}
	conn, err := websocket.Accept(c.Writer, r, &websocket.AcceptOptions{Subprotocols: []string{voiceStreamProtocol}})
	if err != nil {
		return // Accept answered (bad origin, not a WebSocket request)
	}
	accepted := time.Now()
	defer conn.CloseNow()
	conn.SetReadLimit(voice.MaxFrame)
	if conn.Subprotocol() != voiceStreamProtocol {
		voice.SendFatal(ctx, conn, "voice_protocol", "客户端版本不对，请刷新页面")
		return
	}
	s.relayVoice(ctx, conn, p, lang, accepted)
}

// relayVoice checks the settings and limits, dials OpenAI and relays until the recording ends.
// Dialing and the session handshake share one deadline from Accept (voice.HandshakeTimeout,
// the browser's own connect timeout); dial_ms and handshake_ms go to the audit log.
func (s *Server) relayVoice(ctx context.Context, conn *websocket.Conn, p *principal, lang string, accepted time.Time) {
	v, err := s.voiceSettings(ctx)
	if err != nil || !s.voiceUsable(v) {
		voice.SendFatal(ctx, conn, "voice_disabled", "管理员没有开启语音输入")
		return
	}
	if err := s.rateLimit(ctx, "voice-stream:"+p.User.ID, voiceStreamsPerMinute, time.Minute); err != nil {
		voice.SendFatal(ctx, conn, "rate_limited", "开始录音太频繁，请稍后再试")
		return
	}
	release, ok := s.voiceSlot(ctx, p.User.ID)
	if !ok {
		voice.SendFatal(ctx, conn, "voice_too_many", fmt.Sprintf("同时最多 %d 路语音输入，请先结束其他录音", voiceMaxStreams))
		return
	}
	defer release()
	key, err := s.voiceKey(v)
	if err != nil {
		voice.SendFatal(ctx, conn, "voice_unseal", "服务器读不出语音服务的 API Key，请联系管理员")
		return
	}
	req := voice.Request{Model: v.Model, Language: lang, NoiseReduction: v.NoiseReduction, Delay: v.Delay}
	opts := voiceStreamOptions
	opts.Request, opts.APIKey, opts.Started = req, key, accepted
	if opts.Handshake == 0 {
		opts.Handshake = voice.HandshakeTimeout
	}
	dctx, cancel := context.WithDeadline(ctx, accepted.Add(opts.Handshake))
	dialStart := time.Now()
	up, err := voice.Dial(dctx, v, key, voice.SafetyID(p.User.ID))
	dialMS := time.Since(dialStart).Milliseconds()
	timedOut := dctx.Err() != nil
	cancel()
	if err != nil {
		code, msg := s.voiceDialErr(err)
		if timedOut && ctx.Err() == nil {
			code, msg = "voice_timeout", fmt.Sprintf("%d 秒内没有连上 OpenAI", int(opts.Handshake.Seconds()))
		}
		s.log.Warn("voice: relay dial failed", "code", code, "dial_ms", dialMS)
		s.audit(ctx, p.User.ID, "voice_stream_stop", "voice", "", map[string]any{"model": v.Model, "language": lang, "ok": false, "reason": code, "dial_ms": dialMS})
		voice.SendFatal(ctx, conn, code, "连不上语音服务："+msg)
		return
	}
	opts.OnReady = func(sessionID string, handshake time.Duration) {
		s.log.Info("voice: relay ready", "dial_ms", dialMS, "handshake_ms", handshake.Milliseconds(), "setup_ms", time.Since(accepted).Milliseconds())
		s.audit(ctx, p.User.ID, "voice_stream_start", "voice", sessionID, map[string]any{"model": v.Model, "language": lang,
			"dial_ms": dialMS, "handshake_ms": handshake.Milliseconds()})
	}
	res := voice.Relay(ctx, conn, up, opts)
	succeeded := !strings.HasPrefix(res.Reason, "handshake")
	detail := map[string]any{"model": v.Model, "language": lang,
		"seconds": res.Duration.Round(time.Second).Seconds(), "audio_seconds": float64(res.Bytes) / voice.BytesPerSecond, "commits": res.Commits,
		"reason": res.Reason, "ok": succeeded}
	if !succeeded {
		detail["dial_ms"], detail["handshake_ms"] = dialMS, res.Handshake.Milliseconds()
		s.log.Warn("voice: relay handshake failed", "reason", res.Reason, "dial_ms", dialMS, "handshake_ms", res.Handshake.Milliseconds())
	}
	s.audit(context.WithoutCancel(ctx), p.User.ID, "voice_stream_stop", "voice", res.SessionID, detail)
}

func (s *Server) voiceDialErr(err error) (string, string) {
	e, ok := err.(*voice.Error)
	if !ok {
		return "voice_unreachable", "无法连接"
	}
	s.log.Warn("voice: relay dial", "status", e.Status, "code", e.Code, "msg", e.Message)
	switch {
	case e.Status == 0:
		return "voice_unreachable", "服务器连不上 OpenAI"
	case e.Unauthorized():
		return "voice_key_rejected", "OpenAI 拒绝了 API Key"
	case e.Status == http.StatusTooManyRequests:
		return "voice_busy", "OpenAI 繁忙，请稍后再试"
	}
	msg := fmt.Sprintf("OpenAI 返回 %d", e.Status)
	if e.Message != "" {
		msg += "：" + e.Message
	}
	return "voice_upstream", msg
}

// voiceSlot claims one of the user's voiceMaxStreams relay slots in Valkey (shared by every
// console replica). A slot key expires on its own after the longest recording, so a crashed
// replica can't hold it forever.
func (s *Server) voiceSlot(ctx context.Context, userID string) (func(), bool) {
	ttl := voice.MaxStream + time.Minute
	for i := range voiceMaxStreams {
		key := fmt.Sprintf("voice-stream-slot:%s:%d", userID, i)
		n, err := s.kv.Incr(ctx, key, ttl)
		if err != nil {
			return nil, false
		}
		if n == 1 {
			return func() { _ = s.kv.Del(context.WithoutCancel(ctx), key) }, true
		}
	}
	return nil, false
}
