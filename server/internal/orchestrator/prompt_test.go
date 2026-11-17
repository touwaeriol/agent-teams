package orchestrator

import (
	"strings"
	"testing"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// TestLeadReceiptPrompt: the lead's guide and the brief both ask for a short tell_user receipt
// (understanding, split, decisions) before the work, without ending the turn; the brief carries
// the user's own words when the thread has a source message.
func TestLeadReceiptPrompt(t *testing.T) {
	th := core.Thread{No: 3, Title: "登录", Brief: "修登录", Branch: "thread/3-x"}
	g := guide(core.Employee{Tier: "lead"}, &th)
	for _, want := range []string{"tell_user", "回执", "3～8 行", "怎么理解", "怎么拆分", "需要用户决定", "不要结束本轮"} {
		if !strings.Contains(g, want) {
			t.Fatalf("lead guide lacks %q", want)
		}
	}
	if strings.Contains(guide(core.Employee{Tier: "member"}, &th), "tell_user") || strings.Contains(guide(core.Employee{Tier: "coordinator"}, nil), "tell_user") {
		t.Fatal("tell_user offered outside the lead")
	}
	b := briefText(th, store.Project{}, nil)
	if !strings.Contains(b, "tell_user") || strings.Contains(b, "用户原话") {
		t.Fatalf("brief %q", b)
	}
	b = briefText(th, store.Project{}, &core.Message{Content: "按截图修"})
	if !strings.Contains(b, "触发这个讨论串的用户原话") || !strings.Contains(b, "按截图修") {
		t.Fatalf("brief with source %q", b)
	}
}
