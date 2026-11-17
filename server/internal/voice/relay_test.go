package voice

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestStreamURL(t *testing.T) {
	cases := map[string]string{
		"":                          "wss://api.openai.com/v1/realtime?intent=transcription",
		"https://proxy.example/v1/": "wss://proxy.example/v1/realtime?intent=transcription",
		"http://127.0.0.1:9999/v1":  "ws://127.0.0.1:9999/v1/realtime?intent=transcription",
	}
	for base, want := range cases {
		if got := (Settings{BaseURL: base}).StreamURL(); got != want {
			t.Errorf("%q → %q, want %q", base, got, want)
		}
	}
}

func TestSessionUpdate(t *testing.T) {
	ev := SessionUpdate(Request{Model: DefaultModel, Language: "auto", NoiseReduction: NoiseOff, Delay: "minimal"})
	raw, _ := json.Marshal(ev)
	want := `{"session":{"audio":{"input":{"format":{"rate":24000,"type":"audio/pcm"},"noise_reduction":null,"transcription":{"delay":"minimal","model":"gpt-realtime-whisper"},"turn_detection":null}},"type":"transcription"},"type":"session.update"}`
	if string(raw) != want {
		t.Fatalf("got %s", raw)
	}
	if SafetyID("u1") == SafetyID("u2") || strings.Contains(SafetyID("user-123"), "user-123") || len(SafetyID("u1")) != 32 {
		t.Fatal("safety id")
	}
}

func TestBrowserEvent(t *testing.T) {
	cases := []struct{ in, want string }{
		{`{"type":"input_audio_buffer.committed","event_id":"e1","item_id":"i1","previous_item_id":"i0"}`, `{"item_id":"i1","type":"input_audio_buffer.committed"}`},
		{`{"type":"conversation.item.input_audio_transcription.delta","item_id":"i1","content_index":0,"delta":"hi","logprobs":[]}`, `{"delta":"hi","item_id":"i1","type":"conversation.item.input_audio_transcription.delta"}`},
		{`{"type":"conversation.item.input_audio_transcription.completed","item_id":"i1","transcript":"hi.","usage":{"seconds":2}}`, `{"item_id":"i1","transcript":"hi.","type":"conversation.item.input_audio_transcription.completed"}`},
		{`{"type":"conversation.item.input_audio_transcription.failed","item_id":"i2","error":{"code":"x","message":"nope"}}`, `{"error":{"code":"x","message":"nope"},"item_id":"i2","type":"conversation.item.input_audio_transcription.failed"}`},
		{`{"type":"error","error":{"type":"invalid_request_error","code":"bad","message":"key sk-secret-12345678 rejected","param":"x"}}`, `{"error":{"code":"bad","message":"key *** rejected"},"type":"error"}`},
	}
	for _, c := range cases {
		got, ok := BrowserEvent([]byte(c.in), "sk-secret-12345678")
		if !ok || string(got) != c.want {
			t.Errorf("%s → %s %v", c.in, got, ok)
		}
	}
	for _, in := range []string{`{"type":"session.created","session":{"id":"s"}}`, `{"type":"rate_limits.updated"}`, `not json`} {
		if _, ok := BrowserEvent([]byte(in), "k"); ok {
			t.Errorf("relayed %s", in)
		}
	}
}
