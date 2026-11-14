package protocol

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const examplesDir = "../../examples"

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

func newValidator(t *testing.T) *Validator {
	t.Helper()
	v, err := NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func exampleFrames(t *testing.T) []json.RawMessage {
	var frames []json.RawMessage
	readJSON(t, filepath.Join(examplesDir, "messages.json"), &frames)
	return frames
}

func TestExampleFramesValidateAndDecode(t *testing.T) {
	v := newValidator(t)
	seen := map[string]bool{}
	for _, frame := range exampleFrames(t) {
		if err := v.ValidateFrame(frame); err != nil {
			t.Fatalf("validate %s: %v", frame, err)
		}
		env, body, err := Decode(frame)
		if err != nil {
			t.Fatalf("decode %s: %v", frame, err)
		}
		seen[env.Type] = true
		assertRoundTrip(t, v, env, body)
	}
	for typ := range Messages {
		if !seen[typ] {
			t.Errorf("no example frame for %s", typ)
		}
	}
}

// assertRoundTrip re-encodes the decoded body: the Go structs must not drop required data.
func assertRoundTrip(t *testing.T, v *Validator, env *Envelope, body any) {
	t.Helper()
	seq := 0
	if env.Seq != nil {
		seq = *env.Seq
	}
	again, err := NewEnvelope(env.Type, env.ID, seq, body)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(again)
	if err != nil {
		t.Fatal(err)
	}
	if err := v.ValidateFrame(raw); err != nil {
		t.Fatalf("round trip %s: %v\n%s", env.Type, err, raw)
	}
}

func TestInvalidFramesRejected(t *testing.T) {
	v := newValidator(t)
	var cases []struct {
		Why   string          `json:"why"`
		Frame json.RawMessage `json:"frame"`
	}
	readJSON(t, filepath.Join(examplesDir, "invalid-messages.json"), &cases)
	for _, c := range cases {
		if err := v.ValidateFrame(c.Frame); err == nil {
			t.Errorf("accepted: %s", c.Why)
		}
	}
}

type toolExamples struct {
	Valid   map[string][]json.RawMessage `json:"valid"`
	Invalid map[string][]json.RawMessage `json:"invalid"`
}

func TestToolInputs(t *testing.T) {
	v := newValidator(t)
	var ex toolExamples
	readJSON(t, filepath.Join(examplesDir, "tools.json"), &ex)
	for name := range Tools {
		if len(ex.Valid[name]) == 0 {
			t.Errorf("no example input for %s", name)
		}
	}
	for name, inputs := range ex.Valid {
		for _, in := range inputs {
			if err := v.ValidateToolInput(name, in); err != nil {
				t.Errorf("%s %s: %v", name, in, err)
			}
			if err := json.Unmarshal(in, Tools[name].NewInput()); err != nil {
				t.Errorf("decode %s %s: %v", name, in, err)
			}
		}
	}
	for name, inputs := range ex.Invalid {
		for _, in := range inputs {
			if err := v.ValidateToolInput(name, in); err == nil {
				t.Errorf("%s accepted %s", name, in)
			}
		}
	}
}

func TestToolInputSchemaIsSelfContained(t *testing.T) {
	for name := range Tools {
		schema, err := ToolInputSchema(name)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(schema), "$ref") || !json.Valid(schema) {
			t.Errorf("%s: input schema not self-contained", name)
		}
	}
	if _, err := ToolInputSchema("approve"); err == nil {
		t.Error("v1 approve tool must not exist")
	}
}

func TestPlatformToolsPerTier(t *testing.T) {
	member, err := PlatformTools(TierMember)
	if err != nil {
		t.Fatal(err)
	}
	delegating, _ := PlatformTools(TierMember, "can_delegate")
	if len(delegating) != len(member)+2 {
		t.Fatalf("can_delegate adds assign_subtask and wait_task: %d vs %d", len(delegating), len(member))
	}
	assertBlocking(t, member, "ask_user", true)
	lead, _ := PlatformTools(TierLead)
	assertBlocking(t, lead, "ask_user", false)
	coord, _ := PlatformTools(TierCoordinator)
	for _, spec := range coord {
		if spec.Name == "complete_task" {
			t.Error("coordinator must not get member tools")
		}
	}
}

func assertBlocking(t *testing.T, specs []PlatformToolSpec, name string, want bool) {
	t.Helper()
	for _, s := range specs {
		if s.Name == name {
			if s.Blocking != want {
				t.Errorf("%s blocking=%v, want %v", name, s.Blocking, want)
			}
			return
		}
	}
	t.Errorf("%s missing", name)
}

func TestNewEnvelopeSeqRules(t *testing.T) {
	body := SessionWatch{SessionID: "s_1", On: true}
	if _, err := NewEnvelope(TypeSessionWatch, "c_1", 0, body); err == nil {
		t.Error("reliable message without seq accepted")
	}
	if _, err := NewEnvelope(TypeStreamDelta, "n_1", 3, StreamDelta{SessionID: "s_1", Text: "x"}); err == nil {
		t.Error("unreliable message with seq accepted")
	}
	if _, err := NewEnvelope("nope", "c_1", 1, body); err == nil {
		t.Error("unknown type accepted")
	}
}

func TestOfficeExampleValidates(t *testing.T) {
	raw, err := os.ReadFile("../../../docs/v2/office.example.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := newValidator(t).ValidateOffice(raw); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeRejectsMissingRequiredField(t *testing.T) {
	frame := []byte(`{"v":2,"type":"session.send","id":"c_1","seq":1,"ts":"2026-11-14T12:00:00Z","body":{"session_id":"s_1","text":"x"}}`)
	if _, _, err := Decode(frame); err == nil {
		t.Error("missing msg_id decoded")
	}
}
