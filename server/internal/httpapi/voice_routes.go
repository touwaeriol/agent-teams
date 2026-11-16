package httpapi

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/touwaeriol/agent-teams/server/internal/voice"
)

// Voice input: the admin configures an OpenAI key once (sealed, write-only); every signed-in user
// gets short-lived client secrets and talks to OpenAI from the browser. No audio reaches us.

const voiceSettingKey = "voice"

// VoiceAdmin is GET /api/admin/voice: the configuration without the key itself.
type VoiceAdmin struct {
	Enabled        bool      `json:"enabled"`
	Provider       string    `json:"provider" enum:"openai"`
	Model          string    `json:"model"`
	BaseURL        string    `json:"base_url" doc:"Empty = https://api.openai.com/v1"`
	NoiseReduction string    `json:"noise_reduction" enum:"near_field,far_field,off"`
	Delay          string    `json:"delay" enum:",minimal,low,medium,high,xhigh" doc:"gpt-realtime-whisper only; empty = OpenAI's default"`
	KeySet         bool      `json:"key_set"`
	KeyLast4       string    `json:"key_last4"`
	Usable         bool      `json:"usable" doc:"Enabled, a key is saved and the server can unseal it"`
	DefaultModel   string    `json:"default_model"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// VoiceAdminInput is PUT /api/admin/voice. APIKey is write-only: empty keeps the saved key.
type VoiceAdminInput struct {
	Enabled        bool   `json:"enabled"`
	Model          string `json:"model,omitempty" maxLength:"100"`
	BaseURL        string `json:"base_url,omitempty" maxLength:"300"`
	NoiseReduction string `json:"noise_reduction,omitempty" enum:"near_field,far_field,off"`
	Delay          string `json:"delay,omitempty" enum:",minimal,low,medium,high,xhigh"`
	APIKey         string `json:"api_key,omitempty" maxLength:"500" doc:"New key; omit to keep the saved one"`
	ClearKey       bool   `json:"clear_key,omitempty"`
}

type voiceAdminOutput struct{ Body VoiceAdmin }

type voiceAdminSaveInput struct{ Body VoiceAdminInput }

type voiceTestInput struct {
	Body struct {
		APIKey  string `json:"api_key,omitempty" maxLength:"500" doc:"Test this key instead of the saved one"`
		Model   string `json:"model,omitempty" maxLength:"100"`
		BaseURL string `json:"base_url,omitempty" maxLength:"300"`
	}
}

type voiceTestOutput struct {
	Body struct {
		OK        bool   `json:"ok"`
		Model     string `json:"model"`
		ExpiresAt int64  `json:"expires_at"`
	}
}

// VoiceConfig is GET /api/voice/config for the composer.
type VoiceConfig struct {
	Enabled   bool             `json:"enabled"`
	Provider  string           `json:"provider,omitempty"`
	Languages []voice.Language `json:"languages"`
}

type voiceConfigOutput struct{ Body VoiceConfig }

type voiceSessionInput struct {
	Body struct {
		Language string `json:"language,omitempty" maxLength:"10" doc:"ISO-639-1 code or auto"`
	}
}

// VoiceSession is a client secret for one recording: the browser posts its SDP offer to
// connect_url with Authorization: Bearer client_secret (WebRTC, data channel oai-events).
type VoiceSession struct {
	Provider      string `json:"provider"`
	Transport     string `json:"transport" enum:"webrtc"`
	ConnectURL    string `json:"connect_url"`
	ClientSecret  string `json:"client_secret"`
	ExpiresAt     int64  `json:"expires_at" doc:"Unix seconds"`
	Model         string `json:"model"`
	Language      string `json:"language"`
	ClientCommits bool   `json:"client_commits" doc:"No server VAD: send input_audio_buffer.commit at the end of each utterance"`
}

type voiceSessionOutput struct{ Body VoiceSession }

func (s *Server) registerVoice(api huma.API) {
	admin := []string{"admin"}
	huma.Register(api, huma.Operation{OperationID: "admin-get-voice", Method: "GET", Path: "/api/admin/voice", Tags: admin}, s.adminGetVoice)
	huma.Register(api, huma.Operation{OperationID: "admin-save-voice", Method: "PUT", Path: "/api/admin/voice", Tags: admin}, s.adminSaveVoice)
	huma.Register(api, huma.Operation{OperationID: "admin-test-voice", Method: "POST", Path: "/api/admin/voice/test", Tags: admin}, s.adminTestVoice)
	tags := []string{"voice"}
	huma.Register(api, huma.Operation{OperationID: "voice-config", Method: "GET", Path: "/api/voice/config", Tags: tags}, s.voiceConfig)
	huma.Register(api, huma.Operation{OperationID: "voice-session", Method: "POST", Path: "/api/voice/session", Tags: tags}, s.voiceSession)
}

func (s *Server) voiceSettings(ctx context.Context) (voice.Settings, error) {
	var v voice.Settings
	if _, err := s.store.Setting(ctx, voiceSettingKey, &v); err != nil {
		return voice.Settings{}, err
	}
	return v.WithDefaults(), nil
}

func (s *Server) voiceUsable(v voice.Settings) bool {
	return v.Enabled && v.SealedKey != "" && s.box.Ready()
}

func (s *Server) voiceAdminView(v voice.Settings) VoiceAdmin {
	return VoiceAdmin{Enabled: v.Enabled, Provider: v.Provider, Model: v.Model, BaseURL: v.BaseURL, NoiseReduction: v.NoiseReduction,
		Delay: v.Delay, KeySet: v.SealedKey != "", KeyLast4: v.KeyLast4, Usable: s.voiceUsable(v), DefaultModel: voice.DefaultModel, UpdatedAt: v.UpdatedAt}
}

func (s *Server) adminGetVoice(ctx context.Context, _ *struct{}) (*voiceAdminOutput, error) {
	if _, err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	v, err := s.voiceSettings(ctx)
	if err != nil {
		return nil, err
	}
	return &voiceAdminOutput{Body: s.voiceAdminView(v)}, nil
}

func (s *Server) adminSaveVoice(ctx context.Context, in *voiceAdminSaveInput) (*voiceAdminOutput, error) {
	admin, err := requireAdmin(ctx)
	if err != nil {
		return nil, err
	}
	b := in.Body
	if err := voice.CheckBaseURL(b.BaseURL); err != nil {
		return nil, invalid([]FieldError{{"base_url", err.Error()}}, err.Error())
	}
	v, err := s.voiceSettings(ctx)
	if err != nil {
		return nil, err
	}
	keyChanged, err := s.applyVoiceKey(&v, b)
	if err != nil {
		return nil, err
	}
	v.Enabled, v.Model, v.BaseURL, v.NoiseReduction, v.Delay = b.Enabled, strings.TrimSpace(b.Model), b.BaseURL, b.NoiseReduction, b.Delay
	v = v.WithDefaults()
	if v.Enabled && v.SealedKey == "" {
		return nil, invalid([]FieldError{{"api_key", "先填 API Key 再启用"}}, "先填 API Key 再启用")
	}
	v.UpdatedAt = time.Now().UTC()
	if err := s.store.SaveSetting(ctx, voiceSettingKey, v); err != nil {
		return nil, err
	}
	s.audit(ctx, admin.User.ID, "voice_settings_change", "settings", voiceSettingKey,
		map[string]any{"enabled": v.Enabled, "model": v.Model, "key_changed": keyChanged})
	return &voiceAdminOutput{Body: s.voiceAdminView(v)}, nil
}

// applyVoiceKey seals a new key or clears the saved one; it reports whether the key changed.
func (s *Server) applyVoiceKey(v *voice.Settings, b VoiceAdminInput) (bool, error) {
	key := strings.TrimSpace(b.APIKey)
	if b.ClearKey && key == "" {
		v.SealedKey, v.KeyLast4 = "", ""
		return true, nil
	}
	if key == "" {
		return false, nil
	}
	sealed, err := s.box.Seal(key)
	if err != nil {
		return false, fail(http.StatusServiceUnavailable, "no_secret_key", "服务没有配置 AGENT_TEAMS_SECRET_KEY，不能保存 API Key")
	}
	v.SealedKey, v.KeyLast4 = sealed, voice.Last4(key)
	return true, nil
}

func (s *Server) adminTestVoice(ctx context.Context, in *voiceTestInput) (*voiceTestOutput, error) {
	admin, err := requireAdmin(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.rateLimit(ctx, "voice-test:"+admin.User.ID, 10, time.Minute); err != nil {
		return nil, err
	}
	if err := voice.CheckBaseURL(in.Body.BaseURL); err != nil {
		return nil, invalid([]FieldError{{"base_url", err.Error()}}, err.Error())
	}
	v, err := s.voiceSettings(ctx)
	if err != nil {
		return nil, err
	}
	key := strings.TrimSpace(in.Body.APIKey)
	if key == "" {
		if key, err = s.voiceKey(v); err != nil {
			return nil, err
		}
	}
	v.BaseURL = in.Body.BaseURL
	if m := strings.TrimSpace(in.Body.Model); m != "" {
		v.Model = m
	}
	secret, err := voice.Mint(ctx, s.voiceHTTP(), v, key, voice.Request{Model: v.Model, NoiseReduction: v.NoiseReduction, Delay: v.Delay, TTL: 10})
	s.audit(ctx, admin.User.ID, "voice_test", "settings", voiceSettingKey, map[string]any{"model": v.Model, "ok": err == nil})
	if err != nil {
		return nil, s.voiceErr(err)
	}
	out := &voiceTestOutput{}
	out.Body.OK, out.Body.Model, out.Body.ExpiresAt = true, v.Model, secret.ExpiresAt
	return out, nil
}

func (s *Server) voiceKey(v voice.Settings) (string, error) {
	if v.SealedKey == "" {
		return "", fail(http.StatusBadRequest, "voice_no_key", "还没有保存 API Key")
	}
	key, err := s.box.Open(v.SealedKey)
	if err != nil {
		return "", fail(http.StatusServiceUnavailable, "voice_unseal", "无法解密已保存的 API Key，请重新填写")
	}
	return key, nil
}

func (s *Server) voiceConfig(ctx context.Context, _ *struct{}) (*voiceConfigOutput, error) {
	if _, err := requireUser(ctx); err != nil {
		return nil, err
	}
	v, err := s.voiceSettings(ctx)
	if err != nil {
		return nil, err
	}
	if !s.voiceUsable(v) {
		return &voiceConfigOutput{Body: VoiceConfig{Languages: []voice.Language{}}}, nil
	}
	return &voiceConfigOutput{Body: VoiceConfig{Enabled: true, Provider: v.Provider, Languages: slices.Clone(voice.Languages)}}, nil
}

func (s *Server) voiceSession(ctx context.Context, in *voiceSessionInput) (*voiceSessionOutput, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	lang := strings.ToLower(strings.TrimSpace(in.Body.Language))
	if !voice.KnownLanguage(lang) {
		return nil, invalid([]FieldError{{"language", "不支持这个语言"}}, "不支持这个语言："+lang)
	}
	if lang == "" {
		lang = "auto"
	}
	v, err := s.voiceSettings(ctx)
	if err != nil {
		return nil, err
	}
	if !s.voiceUsable(v) {
		return nil, fail(http.StatusNotFound, "voice_disabled", "管理员没有开启语音输入")
	}
	if err := s.rateLimit(ctx, "voice-session:"+p.User.ID, 20, time.Minute); err != nil {
		return nil, err
	}
	key, err := s.voiceKey(v)
	if err != nil {
		return nil, err
	}
	secret, err := voice.Mint(ctx, s.voiceHTTP(), v, key, voice.Request{Model: v.Model, Language: lang, NoiseReduction: v.NoiseReduction, Delay: v.Delay})
	s.audit(ctx, p.User.ID, "voice_session", "voice", secret.SessionID, map[string]any{"model": v.Model, "language": lang, "ok": err == nil})
	if err != nil {
		return nil, s.voiceErr(err)
	}
	return &voiceSessionOutput{Body: VoiceSession{Provider: v.Provider, Transport: "webrtc", ConnectURL: v.ConnectURL(), ClientSecret: secret.Value,
		ExpiresAt: secret.ExpiresAt, Model: v.Model, Language: lang, ClientCommits: voice.ClientCommits(v.Model)}}, nil
}

func (s *Server) voiceErr(err error) error {
	var e *voice.Error
	if !errors.As(err, &e) {
		return err
	}
	s.log.Warn("voice: client secret", "status", e.Status, "code", e.Code, "msg", e.Message)
	switch {
	case e.Status == 0:
		return fail(http.StatusBadGateway, "voice_unreachable", "连不上语音服务")
	case e.Unauthorized():
		return fail(http.StatusBadGateway, "voice_key_rejected", "语音服务拒绝了 API Key")
	case e.Status == http.StatusTooManyRequests:
		return fail(http.StatusServiceUnavailable, "voice_busy", "语音服务繁忙，请稍后再试")
	}
	detail := "语音服务出错"
	if e.Message != "" {
		detail += "：" + e.Message
	}
	return fail(http.StatusBadGateway, "voice_upstream", detail)
}

// voiceHTTPClient calls OpenAI for client secrets.
var voiceHTTPClient = &http.Client{Timeout: 15 * time.Second}

func (s *Server) voiceHTTP() *http.Client { return voiceHTTPClient }
