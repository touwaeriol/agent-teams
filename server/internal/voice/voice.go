// Package voice mints short-lived OpenAI Realtime client secrets for speech-to-text in the
// composers. The browser connects to OpenAI directly with the secret (WebRTC); audio never
// passes through the control plane, and the platform API key never leaves it.
//
// Request shape (developers.openai.com, POST /v1/realtime/client_secrets, 2026-11):
//
//	{"expires_after": {"anchor": "created_at", "seconds": N},
//	 "session": {"type": "transcription", "audio": {"input": {
//	   "format": {"type": "audio/pcm", "rate": 24000},
//	   "noise_reduction": {"type": "near_field"} | null,
//	   "transcription": {"model": "gpt-realtime-whisper", "language": "zh", "delay": "low"},
//	   "turn_detection": null}}}}
//
// Response: {"value": "ek_…", "expires_at": <unix seconds>, "session": {"id": "sess_…", …}}.
package voice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultModel is OpenAI's streaming speech-to-text model for realtime transcription sessions.
const DefaultModel = "gpt-realtime-whisper"

// DefaultBaseURL is OpenAI's API root.
const DefaultBaseURL = "https://api.openai.com/v1"

// SecretTTL is how long a minted client secret can be used to open a connection.
const SecretTTL = 120

// Noise reduction modes; NoiseOff sends null.
const (
	NoiseNear = "near_field"
	NoiseFar  = "far_field"
	NoiseOff  = "off"
)

// Delays are gpt-realtime-whisper's latency/accuracy settings ("" = OpenAI's default).
var Delays = []string{"minimal", "low", "medium", "high", "xhigh"}

// Settings is the admin configuration kept in at_settings["voice"]. APIKey is sealed.
type Settings struct {
	Enabled        bool      `json:"enabled"`
	Provider       string    `json:"provider"`
	SealedKey      string    `json:"sealed_key,omitempty"`
	KeyLast4       string    `json:"key_last4,omitempty"`
	Model          string    `json:"model"`
	BaseURL        string    `json:"base_url,omitempty"`
	NoiseReduction string    `json:"noise_reduction,omitempty"`
	Delay          string    `json:"delay,omitempty"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// WithDefaults fills the provider, model and noise reduction.
func (s Settings) WithDefaults() Settings {
	if s.Provider == "" {
		s.Provider = "openai"
	}
	if s.Model == "" {
		s.Model = DefaultModel
	}
	if s.NoiseReduction == "" {
		s.NoiseReduction = NoiseNear
	}
	return s
}

// API is the base URL requests go to (DefaultBaseURL unless overridden), without a trailing slash.
func (s Settings) API() string {
	if s.BaseURL == "" {
		return DefaultBaseURL
	}
	return strings.TrimRight(s.BaseURL, "/")
}

// SameAPI reports whether two base URL settings reach the same API root ("" = DefaultBaseURL).
func SameAPI(a, b string) bool {
	return Settings{BaseURL: strings.TrimSpace(a)}.API() == Settings{BaseURL: strings.TrimSpace(b)}.API()
}

// ConnectURL is where the browser posts its WebRTC SDP offer with the client secret.
func (s Settings) ConnectURL() string { return s.API() + "/realtime/calls" }

// CheckBaseURL accepts an absolute http(s) URL without credentials, query or fragment.
func CheckBaseURL(raw string) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("要填 http(s) 开头的完整地址，例如 https://api.openai.com/v1")
	}
	return nil
}

// ClientCommits reports whether the model has no server-side VAD, so the client must send
// input_audio_buffer.commit at the end of each utterance to get a final transcript.
func ClientCommits(model string) bool {
	switch model {
	case "gpt-realtime-whisper", "gpt-live-transcribe", "gpt-transcribe":
		return true
	}
	return false
}

// usesLanguages: these models take languages: [..] instead of language (OpenAI: don't send both).
func usesLanguages(model string) bool {
	return model == "gpt-live-transcribe" || model == "gpt-transcribe"
}

// Request is one client secret to mint. Language "" or "auto" lets the model detect it.
type Request struct {
	Model          string
	Language       string
	NoiseReduction string
	Delay          string
	TTL            int
}

// Body is the JSON sent to POST /realtime/client_secrets.
func Body(r Request) map[string]any {
	tr := map[string]any{"model": r.Model}
	if lang := r.Language; lang != "" && lang != "auto" {
		if usesLanguages(r.Model) {
			tr["languages"] = []string{lang}
		} else {
			tr["language"] = lang
		}
	}
	if r.Delay != "" && r.Model == DefaultModel {
		tr["delay"] = r.Delay
	}
	input := map[string]any{
		"format":          map[string]any{"type": "audio/pcm", "rate": 24000},
		"transcription":   tr,
		"noise_reduction": noise(r.NoiseReduction),
		"turn_detection":  turnDetection(r.Model),
	}
	ttl := r.TTL
	if ttl == 0 {
		ttl = SecretTTL
	}
	return map[string]any{
		"expires_after": map[string]any{"anchor": "created_at", "seconds": ttl},
		"session":       map[string]any{"type": "transcription", "audio": map[string]any{"input": input}},
	}
}

func noise(mode string) any {
	if mode == NoiseOff {
		return nil
	}
	if mode == "" {
		mode = NoiseNear
	}
	return map[string]any{"type": mode}
}

// turnDetection: null for models without VAD (gpt-realtime-whisper: "turn detection must be set
// to null"), server VAD for the older transcribe models.
func turnDetection(model string) any {
	if ClientCommits(model) {
		return nil
	}
	return map[string]any{"type": "server_vad", "silence_duration_ms": 500}
}

// Secret is a minted client secret.
type Secret struct {
	Value     string
	ExpiresAt int64
	SessionID string
}

// Error is a failed mint: Status is OpenAI's HTTP status (0 = no response).
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string {
	return fmt.Sprintf("openai %d %s: %s", e.Status, e.Code, e.Message)
}

// Unauthorized reports a rejected API key.
func (e *Error) Unauthorized() bool {
	return e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden
}

// Mint asks OpenAI for a client secret configured for r.
func Mint(ctx context.Context, hc *http.Client, s Settings, apiKey string, r Request) (Secret, error) {
	raw, err := json.Marshal(Body(r))
	if err != nil {
		return Secret{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.API()+"/realtime/client_secrets", bytes.NewReader(raw))
	if err != nil {
		return Secret{}, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	res, err := hc.Do(req)
	if err != nil {
		return Secret{}, &Error{Message: err.Error()}
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode/100 != 2 {
		e := apiError(res.StatusCode, body)
		e.Message = redact(e.Message, apiKey)
		return Secret{}, e
	}
	var out struct {
		Value     string `json:"value"`
		ExpiresAt int64  `json:"expires_at"`
		Session   struct {
			ID string `json:"id"`
		} `json:"session"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.Value == "" {
		return Secret{}, &Error{Status: res.StatusCode, Message: "response has no client secret"}
	}
	return Secret{Value: out.Value, ExpiresAt: out.ExpiresAt, SessionID: out.Session.ID}, nil
}

func apiError(status int, body []byte) *Error {
	var e struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &e)
	msg := e.Error.Message
	if len(msg) > 300 {
		msg = msg[:300]
	}
	return &Error{Status: status, Code: e.Error.Code, Message: msg}
}

// redact removes the API key from an upstream error message before it is logged or shown.
func redact(msg, key string) string {
	if len(key) < 8 {
		return msg
	}
	return strings.ReplaceAll(msg, key, "***")
}

// Last4 is what the admin page shows of a key.
func Last4(key string) string {
	if len(key) <= 4 {
		return ""
	}
	return key[len(key)-4:]
}

// Language is one choice in the composer's language list (ISO-639-1 code).
type Language struct {
	Code string `json:"code"`
	Name string `json:"name" doc:"The language's own name"`
	Zh   string `json:"zh" doc:"Chinese name"`
}

// Languages are offered in the composer besides 自动.
var Languages = []Language{
	{"zh", "中文", "中文"}, {"en", "English", "英语"}, {"ja", "日本語", "日语"}, {"ko", "한국어", "韩语"},
	{"es", "Español", "西班牙语"}, {"fr", "Français", "法语"}, {"de", "Deutsch", "德语"}, {"ru", "Русский", "俄语"},
	{"pt", "Português", "葡萄牙语"}, {"it", "Italiano", "意大利语"}, {"nl", "Nederlands", "荷兰语"}, {"tr", "Türkçe", "土耳其语"},
	{"pl", "Polski", "波兰语"}, {"uk", "Українська", "乌克兰语"}, {"vi", "Tiếng Việt", "越南语"}, {"th", "ไทย", "泰语"},
	{"id", "Bahasa Indonesia", "印尼语"}, {"ms", "Bahasa Melayu", "马来语"}, {"ar", "العربية", "阿拉伯语"}, {"hi", "हिन्दी", "印地语"},
	{"he", "עברית", "希伯来语"}, {"el", "Ελληνικά", "希腊语"}, {"sv", "Svenska", "瑞典语"}, {"da", "Dansk", "丹麦语"},
	{"no", "Norsk", "挪威语"}, {"fi", "Suomi", "芬兰语"}, {"cs", "Čeština", "捷克语"}, {"hu", "Magyar", "匈牙利语"},
	{"ro", "Română", "罗马尼亚语"}, {"tl", "Tagalog", "他加禄语"},
}

// KnownLanguage reports whether code is "auto" or one of Languages.
func KnownLanguage(code string) bool {
	if code == "" || code == "auto" {
		return true
	}
	for _, l := range Languages {
		if l.Code == code {
			return true
		}
	}
	return false
}
