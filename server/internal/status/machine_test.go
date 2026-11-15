package status

import "testing"

func TestDecidePriority(t *testing.T) {
	cases := []struct {
		name string
		f    Facts
		want Decision
	}{
		{"idle", Facts{}, Decision{State: "idle"}},
		{"offline beats all", Facts{NodeOffline: true, Broken: true, RaisedHand: true, ToolCat: "edit"}, Decision{State: "offline"}},
		{"broken", Facts{Broken: true, RaisedHand: true}, Decision{State: "broken"}},
		{"raising hand over tool", Facts{RaisedHand: true, ToolCat: "shell"}, Decision{State: "raising_hand"}},
		{"queued", Facts{Queued: true, Turn: true}, Decision{State: "queued"}},
		{"claiming", Facts{Claiming: true, ToolCat: "read"}, Decision{State: "claiming"}},
		{"typing", Facts{ToolCat: "edit", Turn: true}, Decision{State: "typing"}},
		{"reading", Facts{ToolCat: "read"}, Decision{State: "reading"}},
		{"running test", Facts{ToolCat: "shell", ToolSub: "test"}, Decision{State: "running", Substate: "test"}},
		{"thinking", Facts{Turn: true, Waiting: true}, Decision{State: "thinking"}},
		{"waiting", Facts{Waiting: true}, Decision{State: "waiting"}},
		{"platform tool = thinking", Facts{ToolCat: "platform", Turn: true}, Decision{State: "thinking"}},
	}
	for _, c := range cases {
		if got := Decide(c.f); got != c.want {
			t.Errorf("%s: got %+v want %+v", c.name, got, c.want)
		}
	}
}

func TestCategory(t *testing.T) {
	for tool, want := range map[string]string{"Edit": "edit", "Write": "edit", "fileChange": "edit", "Grep": "read",
		"Bash": "shell", "commandExecution": "shell", "mcp__platform__set_status": "platform", "WebSearch": ""} {
		if got := Category(tool); got != want {
			t.Errorf("%s: %q want %q", tool, got, want)
		}
	}
}

func TestDefaultBubble(t *testing.T) {
	if b, k := defaultBubble(Decision{State: "claiming"}, "", "t8"); b != "领取 t8" || k != "plain" {
		t.Fatal(b, k)
	}
	if b, k := defaultBubble(Decision{State: "raising_hand"}, "", ""); b != "等你处理" || k != "ask" {
		t.Fatal(b, k)
	}
	long := "pytest -q tests/test_comms.py tests/test_tasks.py tests/test_more.py tests/test_even_more.py"
	if b, _ := defaultBubble(Decision{State: "running"}, long, ""); len([]rune(b)) != 61 {
		t.Fatalf("not clipped: %q", b)
	}
}
