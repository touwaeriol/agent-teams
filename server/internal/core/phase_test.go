package core

import (
	"testing"
	"time"
)

// TestPhaseOf: the coordinator's state line for every case the page tells apart (web
// src/lib/phase.ts threadPhase), plus a merged thread waiting on its tasks or merge queue.
func TestPhaseOf(t *testing.T) {
	str := func(s string) *string { return &s }
	num := func(n int) *int { return &n }
	now := time.Now()
	lead := func(state string) EmployeeBrief {
		return EmployeeBrief{Name: "lead-1", Tier: "lead", Status: &EmployeeStatusView{State: state}}
	}
	member := func(state string, elsewhere bool) EmployeeBrief {
		return EmployeeBrief{Name: "dev-1", Tier: "member", Status: &EmployeeStatusView{State: state}, BusyElsewhere: elsewhere}
	}
	replied := &LeadLast{Text: "做完了"}
	running := func(f func(v *ThreadView)) ThreadView {
		v := ThreadView{Status: "running", MergePolicy: "pr", Members: []EmployeeBrief{lead("idle")}, LeadLast: replied}
		f(&v)
		return v
	}
	cases := []struct {
		name   string
		v      ThreadView
		merges int
		want   string
	}{
		{"archived", ThreadView{Status: "archived"}, 0, "已归档"},
		{"reminded", running(func(v *ThreadView) { v.RemindedAt = &now }), 0, "等你处理：提醒时间到了，等用户处理"},
		{"done", ThreadView{Status: "done"}, 0, "已完成"},
		{"failed", ThreadView{Status: "failed"}, 0, "失败"},
		{"manual acceptance", ThreadView{Status: "review", MergePolicy: "manual"}, 0, "待验收：等用户验收"},
		{"merge error", ThreadView{Status: "review", MergePolicy: "pr", MergeError: str("PR 上没有出现项目要求的 CI 检查，无法合并")}, 0,
			"待验收：合并没有完成，等用户处理：PR 上没有出现项目要求的 CI 检查，无法合并"},
		{"asking", running(func(v *ThreadView) { v.Status = "waiting" }), 0, "等你处理：有给用户的问题或审批卡片，等用户回答"},
		{"asking card", running(func(v *ThreadView) { v.Counts.Waiting = 1 }), 0, "等你处理：有给用户的问题或审批卡片，等用户回答"},
		{"queued", ThreadView{Status: "queued", QueuePosition: num(2)}, 0, "排队中：前面还有 2 个讨论串"},
		{"paused", ThreadView{Status: "paused", PauseReason: "rate_limit"}, 0, "已暂停：Claude 使用限额用完，已暂停"},
		{"checks pr", ThreadView{Status: "review", MergePolicy: "pr"}, 0, "进行中：等 PR 检查通过（通过后讨论串结束，PR 留给人合并）"},
		{"checks auto", ThreadView{Status: "review", MergePolicy: "auto"}, 0, "进行中：等 PR 检查通过，通过后平台自动合并"},
		{"merged tasks", running(func(v *ThreadView) { v.PRState, v.Counts.TasksOpen = "merged", 1 }), 0, "进行中：PR 已合并，等 1 个未完成的小任务结束后结束"},
		{"merged queue", running(func(v *ThreadView) { v.PRState = "merged" }), 1, "进行中：PR 已合并，等合并队列清空后结束"},
		{"merged both", running(func(v *ThreadView) { v.PRState, v.Counts.TasksOpen = "merged", 2 }), 1, "进行中：PR 已合并，等 2 个未完成的小任务和合并队列清空后结束"},
		{"merged ending", running(func(v *ThreadView) { v.PRState = "merged" }), 0, "进行中：PR 已合并，等组长这一回合结束（最终汇报）后结束"},
		{"working", running(func(v *ThreadView) { v.Members = append(v.Members, member("typing", false)) }), 0, "进行中：有人在干活（dev-1）"},
		{"lead working", running(func(v *ThreadView) { v.Members = []EmployeeBrief{lead("thinking")} }), 0, "进行中：有人在干活（lead-1）"},
		{"tasks", running(func(v *ThreadView) { v.Counts.TasksOpen = 3; v.Members = append(v.Members, member("typing", true)) }), 0,
			"进行中：等成员处理任务（3 个未完成）"},
		{"lead offline", running(func(v *ThreadView) { v.Members = []EmployeeBrief{lead("offline")} }), 0, "等你处理：组长离线：节点不在线，节点上线后才能继续"},
		{"lead broken", running(func(v *ThreadView) { v.Members = []EmployeeBrief{lead("broken")} }), 0, "等你处理：组长的会话出错了，需要用户看一下"},
		{"lead queued", running(func(v *ThreadView) { v.Members = []EmployeeBrief{lead("queued")} }), 0, "进行中：等组长上线（节点工位已满，排队中）"},
		{"lead waiting", running(func(v *ThreadView) { v.Members = []EmployeeBrief{lead("raising_hand")} }), 0, "进行中：组长在等别人回复"},
		{"reading", running(func(v *ThreadView) { v.LeadLast = nil }), 0, "进行中：组长正在阅读任务"},
		{"replied", running(func(*ThreadView) {}), 0, "等你处理：组长已回复，等用户回复"},
	}
	for _, c := range cases {
		if got := PhaseOf(c.v, c.merges).Text(); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}
