package orchestrator

import (
	"testing"

	"github.com/touwaeriol/agent-teams/server/internal/core"
)

// TestMovedThread: a project member's last session counts as another thread's when it was opened
// elsewhere or its thread was deleted (thread_id NULL), never for a lead.
func TestMovedThread(t *testing.T) {
	a, b := "th_a", "th_b"
	member := core.Employee{Tier: "member", CurrentThreadID: &a}
	lead := core.Employee{Tier: "lead", ThreadID: &a}
	cases := []struct {
		name string
		e    core.Employee
		last *string
		want bool
	}{
		{"same thread", member, &a, false},
		{"other thread", member, &b, true},
		{"thread deleted", member, nil, true},
		{"no current thread", core.Employee{Tier: "member"}, &a, false},
		{"lead", lead, nil, false},
	}
	for _, c := range cases {
		if got := movedThread(c.e, core.AgentSession{ThreadID: c.last}); got != c.want {
			t.Errorf("%s: moved %v, want %v", c.name, got, c.want)
		}
	}
}

func TestElsewhereHeader(t *testing.T) {
	user := Delivery{Kind: "user", Record: &core.NewMessage{Author: "user"}}
	answer := Delivery{Kind: "answer", Record: &core.NewMessage{Author: "platform"}}
	if got := elsewhereHeader(2, user); got != "[#2 · 来自 用户 的留言]" {
		t.Errorf("user: %s", got)
	}
	if got := elsewhereHeader(3, answer); got != "[#3 · 来自 平台 的回答]" {
		t.Errorf("answer: %s", got)
	}
}
