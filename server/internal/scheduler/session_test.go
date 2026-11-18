package scheduler

import (
	"strings"
	"testing"

	"github.com/touwaeriol/agent-teams/server/internal/orchestrator"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

func n(v int) *int { return &v }

// TestDecideStep is the table of shared-members.md §5: percentages are of the role's own
// context limit (600K → 60% = 360K, even on a 1M model) capped at the window the runtime
// reported (600K on a 200K model: 60% of 200K), else of that window.
func TestDecideStep(t *testing.T) {
	dev := store.Role{ContextLimit: n(600000), CompactBetweenPct: n(60), CompactInTaskPct: n(80), ClearAfterTasks: n(8)}
	noLimit := store.Role{CompactBetweenPct: n(60), ClearAfterTasks: n(8)}
	gate := store.Role{CompactInTaskPct: n(80), ClearAfterTasks: n(1)}
	never := store.Role{ContextLimit: n(600000), ClearAfterTasks: n(0)}
	same := func(done, used, max int, compact bool) sessionFacts {
		return sessionFacts{sameThread: true, idle: true, tasksDone: done, used: n(used), max: n(max), canCompact: compact}
	}
	cases := []struct {
		name string
		role store.Role
		f    sessionFacts
		want step
	}{
		{"under 60% of the limit", dev, same(1, 359999, 1000000, true), stepContinue},
		{"at 60% of a 600K limit on a 1M model", dev, same(1, 360000, 1000000, true), stepCompact},
		{"full but the node cannot compact", dev, same(1, 500000, 1000000, false), stepClear},
		{"8 tasks done", dev, same(8, 1000, 1000000, true), stepClear},
		{"no limit: the runtime's window", noLimit, same(1, 120000, 200000, true), stepCompact},
		{"a limit above the model's window: 60% of the window", dev, same(1, 120000, 200000, true), stepCompact},
		{"a limit above the model's window, under 60% of it", dev, same(1, 119999, 200000, true), stepContinue},
		{"no limit, window unknown", noLimit, sessionFacts{sameThread: true, idle: true, used: n(150000), canCompact: true}, stepContinue},
		{"context not reported", dev, sessionFacts{sameThread: true, idle: true, tasksDone: 1, canCompact: true}, stepContinue},
		{"a gate clears after every task", gate, same(1, 1000, 200000, true), stepClear},
		{"a gate's first task", gate, same(0, 1000, 200000, true), stepContinue},
		{"clear_after 0 never clears, no between pct never compacts", never, same(99, 590000, 1000000, true), stepContinue},
		{"another thread's session (closed by the move)", dev, sessionFacts{idle: true, tasksDone: 9, used: n(590000), canCompact: true}, stepContinue},
		{"mid-turn or crashed: left alone", dev, sessionFacts{sameThread: true, tasksDone: 9, used: n(590000), canCompact: true}, stepContinue},
	}
	for _, c := range cases {
		if got := decideStep(c.role, c.f); got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
}

// TestCompactInstructions: the role's focus, then the note on the task that comes next, within
// session.compact's limit.
func TestCompactInstructions(t *testing.T) {
	if got := compactInstructions(store.Role{CompactFocus: "保留分支"}, "接下来要做任务 t7"); got != "保留分支\n接下来要做任务 t7" {
		t.Fatalf("instructions %q", got)
	}
	if got := compactInstructions(store.Role{}, "接下来要做任务 t7"); got != "接下来要做任务 t7" {
		t.Fatalf("without a focus: %q", got)
	}
	long := compactInstructions(store.Role{CompactFocus: strings.Repeat("重", 2000)}, strings.Repeat("长", 7500))
	if n := len([]rune(long)); n > orchestrator.AnchorMax+1 {
		t.Fatalf("instructions of %d characters", n)
	}
}
