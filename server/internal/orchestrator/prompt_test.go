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

// TestMemberNotesPrompt: members are asked to keep working notes of long tasks in .agent-notes/
// and to report progress (shared-members.md §5.2); the lead is not.
func TestMemberNotesPrompt(t *testing.T) {
	th := core.Thread{No: 3, Title: "登录", Branch: "thread/3-x"}
	g := guide(core.Employee{Tier: "member", Name: "dev-1", Role: "dev"}, &th)
	for _, want := range []string{".agent-notes/<任务id>.md", "report_progress", "任务锚点"} {
		if !strings.Contains(g, want) {
			t.Fatalf("member guide lacks %q", want)
		}
	}
	if strings.Contains(guide(core.Employee{Tier: "lead"}, &th), ".agent-notes") {
		t.Fatal("lead guide asks for notes")
	}
}

// TestReplyLanguagePrompt: every tier gets the platform's reply-language rule; the project
// instructions come right after the guide (before the role spec), the rule right after them,
// and without instructions the rule still sets the default language. Leads and coordinators are
// told that their end-of-turn text is shown to the user.
func TestReplyLanguagePrompt(t *testing.T) {
	th := core.Thread{No: 3, Title: "登录", Branch: "thread/3-x"}
	spec := store.Role{Instructions: "你是工程师。"}
	cases := []struct {
		name string
		e    core.Employee
		t    *core.Thread
	}{
		{"coordinator", core.Employee{Tier: "coordinator", Spec: spec}, nil},
		{"lead", core.Employee{Tier: "lead", Spec: spec}, &th},
		{"member", core.Employee{Tier: "member", Name: "dev-1", Role: "dev", Spec: spec}, &th},
	}
	for _, c := range cases {
		with := strings.Join(leadingSections(c.e, c.t, "使用中文进行沟通"), "\n\n")
		ins, lang, role := strings.Index(with, "## 项目指令"), strings.Index(with, "## 回复语言"), strings.Index(with, "## 岗位说明")
		if ins < 0 || lang < ins || role < lang {
			t.Fatalf("%s: order 项目指令=%d 回复语言=%d 岗位说明=%d:\n%s", c.name, ins, lang, role, with)
		}
		without := strings.Join(leadingSections(c.e, c.t, ""), "\n\n")
		if strings.Contains(without, "## 项目指令") || !strings.Contains(without, "## 回复语言") || !strings.Contains(without, "平台默认中文") {
			t.Fatalf("%s: no default language rule:\n%s", c.name, without)
		}
		for _, want := range []string{"回合结尾", "tell_user", "complete_task", "send_message", "ask_lead", "不代表要用英文回复"} {
			if !strings.Contains(without, want) {
				t.Fatalf("%s: language rule lacks %q", c.name, want)
			}
		}
		if lead := strings.Contains(without, "不是控制台日志"); lead != (c.e.Tier != "member") {
			t.Fatalf("%s: end-of-turn emphasis %v", c.name, lead)
		}
	}
}
