// Package status is the employee state machine (architecture.md §11.3): facts
// from session events, the task board, approvals and node links become one
// state per employee, written to at_status_events / at_employee_status with
// the project's event seq and pushed as SSE "status" events.
package status

import (
	"strings"

	"github.com/touwaeriol/agent-teams/server/internal/msg"
)

// Facts are what the state is derived from.
type Facts struct {
	NodeOffline bool
	Broken      bool
	RaisedHand  bool
	Queued      bool
	Held        string // why the employee's session waits for a node (no candidate has its runtime ready)
	Claiming    bool
	ToolCat     string // edit | read | web | shell | platform | other | "" (no tool running)
	ToolSub     string // shell: test | git_commit | git_push | ci | install
	Turn        bool
	Waking      bool // a message is on its way, or the session is (re)opening
	Waiting     bool
}

// Decision is the derived state.
type Decision struct {
	State    string
	Substate string
	Note     string // the bubble of a held session: why it waits
}

// SubstateNoRuntime is the queued substate of an employee whose session waits because no
// candidate node has its runtime (CLI) installed and logged in; the bubble says why.
const SubstateNoRuntime = "no_runtime"

// Decide applies the priority table: the first condition that holds wins.
func Decide(f Facts) Decision {
	switch {
	case f.NodeOffline:
		return Decision{State: "offline"}
	case f.Broken:
		return Decision{State: "broken"}
	case f.RaisedHand:
		return Decision{State: "raising_hand"}
	case f.Held != "":
		return Decision{State: "queued", Substate: SubstateNoRuntime, Note: f.Held}
	case f.Queued:
		return Decision{State: "queued"}
	case f.Claiming:
		return Decision{State: "claiming"}
	}
	if d, ok := toolState(f); ok {
		return d
	}
	switch {
	case f.Turn:
		return Decision{State: "thinking"}
	case f.Waking:
		return Decision{State: "thinking", Substate: "waking"}
	case f.Waiting:
		return Decision{State: "waiting"}
	}
	return Decision{State: "idle"}
}

func toolState(f Facts) (Decision, bool) {
	switch f.ToolCat {
	case "edit":
		return Decision{State: "typing"}, true
	case "read", "web":
		return Decision{State: "reading"}, true
	case "shell":
		return Decision{State: "running", Substate: f.ToolSub}, true
	}
	return Decision{}, false
}

// Category maps a tool name to its category when the daemon did not tag it.
func Category(tool string) string {
	switch {
	case strings.HasPrefix(tool, "mcp__platform__"):
		return "platform"
	case tool == "Edit" || tool == "Write" || tool == "NotebookEdit" || tool == "MultiEdit" || tool == "fileChange":
		return "edit"
	case tool == "Read" || tool == "Glob" || tool == "Grep" || tool == "WebFetch":
		return "read"
	case tool == "Bash" || tool == "commandExecution":
		return "shell"
	}
	return ""
}

// defaultBubble is the platform-generated bubble when the employee did not set one.
func defaultBubble(d Decision, summary, taskID string) (string, string) {
	switch d.State {
	case "raising_hand":
		return msg.K("等你处理"), "ask"
	case "broken":
		return msg.K("出错了"), "error"
	case "queued":
		if d.Substate == SubstateNoRuntime {
			return d.Note, "ask"
		}
		return msg.K("排队等工位"), "plain"
	case "claiming":
		return "领取 " + taskID, "plain"
	case "typing", "reading", "running":
		return clip(summary, 60), "plain"
	}
	return "", "plain"
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
