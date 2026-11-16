package voice

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBodyForRealtimeWhisper(t *testing.T) {
	b := Body(Request{Model: DefaultModel, Language: "zh", Delay: "low"})
	raw, _ := json.Marshal(b)
	var got struct {
		ExpiresAfter struct {
			Anchor  string `json:"anchor"`
			Seconds int    `json:"seconds"`
		} `json:"expires_after"`
		Session struct {
			Type  string `json:"type"`
			Audio struct {
				Input map[string]json.RawMessage `json:"input"`
			} `json:"audio"`
		} `json:"session"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.ExpiresAfter.Anchor != "created_at" || got.ExpiresAfter.Seconds != SecretTTL || got.Session.Type != "transcription" {
		t.Fatalf("envelope %s", raw)
	}
	in := got.Session.Audio.Input
	if string(in["turn_detection"]) != "null" {
		t.Fatalf("gpt-realtime-whisper needs turn_detection null: %s", raw)
	}
	if string(in["transcription"]) != `{"delay":"low","language":"zh","model":"gpt-realtime-whisper"}` {
		t.Fatalf("transcription %s", in["transcription"])
	}
	if string(in["noise_reduction"]) != `{"type":"near_field"}` || string(in["format"]) != `{"rate":24000,"type":"audio/pcm"}` {
		t.Fatalf("input %s", raw)
	}
}

func TestBodyVariants(t *testing.T) {
	cases := []struct {
		req  Request
		want string // transcription
		turn string
	}{
		{Request{Model: DefaultModel, Language: "auto"}, `{"model":"gpt-realtime-whisper"}`, "null"},
		{Request{Model: "gpt-live-transcribe", Language: "ja", Delay: "low"}, `{"languages":["ja"],"model":"gpt-live-transcribe"}`, "null"},
		{Request{Model: "gpt-4o-transcribe", Language: "en"}, `{"language":"en","model":"gpt-4o-transcribe"}`, `{"silence_duration_ms":500,"type":"server_vad"}`},
	}
	for _, c := range cases {
		raw, _ := json.Marshal(Body(c.req)["session"])
		var s struct {
			Audio struct {
				Input map[string]json.RawMessage `json:"input"`
			} `json:"audio"`
		}
		_ = json.Unmarshal(raw, &s)
		if got := string(s.Audio.Input["transcription"]); got != c.want {
			t.Errorf("%+v transcription %s", c.req, got)
		}
		if got := string(s.Audio.Input["turn_detection"]); got != c.turn {
			t.Errorf("%+v turn %s", c.req, got)
		}
	}
	raw, _ := json.Marshal(Body(Request{Model: DefaultModel, NoiseReduction: NoiseOff}))
	var s map[string]map[string]map[string]map[string]json.RawMessage
	_ = json.Unmarshal(raw, &s)
	if string(s["session"]["audio"]["input"]["noise_reduction"]) != "null" {
		t.Fatalf("noise off %s", raw)
	}
}

func TestMint(t *testing.T) {
	var gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		_, _ = io.ReadAll(r.Body)
		if r.Header.Get("Authorization") == "Bearer bad" {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"error":{"code":"invalid_api_key","message":"Incorrect API key provided: bad"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"value":"ek_123","expires_at":1900000000,"session":{"id":"sess_1","type":"transcription"}}`))
	}))
	defer srv.Close()
	s := Settings{BaseURL: srv.URL + "/v1/"}
	sec, err := Mint(context.Background(), srv.Client(), s, "sk-test", Request{Model: DefaultModel})
	if err != nil || sec.Value != "ek_123" || sec.ExpiresAt != 1900000000 || sec.SessionID != "sess_1" {
		t.Fatalf("mint %+v %v", sec, err)
	}
	if gotAuth != "Bearer sk-test" || gotPath != "/v1/realtime/client_secrets" {
		t.Fatalf("request %s %s", gotAuth, gotPath)
	}
	if s.ConnectURL() != srv.URL+"/v1/realtime/calls" {
		t.Fatalf("connect %s", s.ConnectURL())
	}
	_, err = Mint(context.Background(), srv.Client(), s, "bad", Request{Model: DefaultModel})
	e, ok := err.(*Error)
	if !ok || !e.Unauthorized() || e.Code != "invalid_api_key" {
		t.Fatalf("bad key %v", err)
	}
}

func TestHelpers(t *testing.T) {
	if Last4("sk-abcdef1234") != "1234" || Last4("abc") != "" {
		t.Fatal("last4")
	}
	for _, bad := range []string{"ftp://x", "api.openai.com", "https://u:p@x.com", "https://x.com/?a=1"} {
		if CheckBaseURL(bad) == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	if CheckBaseURL("") != nil || CheckBaseURL("https://api.openai.com/v1") != nil {
		t.Fatal("rejected good url")
	}
	if !KnownLanguage("auto") || !KnownLanguage("zh") || KnownLanguage("xx") {
		t.Fatal("languages")
	}
	if (Settings{}).WithDefaults().Model != DefaultModel || (Settings{}).API() != DefaultBaseURL {
		t.Fatal("defaults")
	}
}

func TestRedactAndSameAPI(t *testing.T) {
	if got := redact("bad key sk-live-abcd9876 given", "sk-live-abcd9876"); got != "bad key *** given" {
		t.Fatalf("redact = %q", got)
	}
	if !SameAPI("", "https://api.openai.com/v1/") || SameAPI("", "https://proxy.example/v1") {
		t.Fatal("SameAPI")
	}
}
