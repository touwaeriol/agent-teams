package core

import "fmt"

// ThreadPhase is a thread's state as the page shows it (web/src/lib/phase.ts threadPhase, the one
// place the panel, the open thread and the coordinator cards read): the coordinator's
// thread_status and @-references say the same. Label is the state's name, Note what it waits on.
type ThreadPhase struct {
	ID    string
	Label string
	Note  string
}

// Text is "label：note" (the label alone when there is no note).
func (p ThreadPhase) Text() string {
	if p.Note == "" {
		return p.Label
	}
	return p.Label + "：" + p.Note
}

// workingStates are the employee states in which someone is actively working (web live.ts WORKING).
var workingStates = map[string]bool{"typing": true, "reading": true, "running": true, "thinking": true, "claiming": true,
	"walking_to_board": true, "returning": true}

var reviewNotes = map[string]string{"pr": "等 PR 检查通过（通过后讨论串结束，PR 留给人合并）", "auto": "等 PR 检查通过，通过后平台自动合并",
	"manual": "等用户验收"}

var pauseNotes = map[string]string{"user": "用户暂停了", "rate_limit": "Claude 使用限额用完，已暂停"}

// PhaseOf works out a thread's phase in threadPhase's order. mergesActive is the number of active
// merge queue entries (not in the view): a thread whose PRs are merged but that waits for its
// tasks or its merge queue says so, which the page shows only as 等成员处理任务.
func PhaseOf(v ThreadView, mergesActive int) ThreadPhase {
	ph := func(id, label, note string) ThreadPhase { return ThreadPhase{ID: id, Label: label, Note: note} }
	switch v.Status {
	case "archived":
		return ph("archived", "已归档", "")
	}
	if v.RemindedAt != nil {
		return ph("reminded", "等你处理", "提醒时间到了，等用户处理")
	}
	switch v.Status {
	case "done":
		return ph("done", "已完成", "")
	case "failed":
		return ph("failed", "失败", "")
	}
	if v.Status == "review" && (v.MergePolicy == "manual" || v.MergeError != nil) {
		if v.MergeError != nil {
			return ph("acceptance", "待验收", "合并没有完成，等用户处理："+*v.MergeError)
		}
		return ph("acceptance", "待验收", "等用户验收")
	}
	if v.Status == "waiting" || v.Counts.Waiting > 0 {
		return ph("asking", "等你处理", "有给用户的问题或审批卡片，等用户回答")
	}
	switch v.Status {
	case "queued":
		if v.QueuePosition != nil && *v.QueuePosition > 0 {
			return ph("queued", "排队中", fmt.Sprintf("前面还有 %d 个讨论串", *v.QueuePosition))
		}
		return ph("queued", "排队中", "等节点空出来")
	case "paused":
		note, ok := pauseNotes[v.PauseReason]
		if !ok && v.PauseReason != "" {
			note = v.PauseReason
		}
		return ph("paused", "已暂停", note)
	case "review":
		note, ok := reviewNotes[v.MergePolicy]
		if !ok {
			note = reviewNotes["pr"]
		}
		return ph("checks", "进行中", note)
	}
	if v.PRState == "merged" {
		switch {
		case v.Counts.TasksOpen > 0 && mergesActive > 0:
			return ph("merged_waiting", "进行中", fmt.Sprintf("PR 已合并，等 %d 个未完成的小任务和合并队列清空后结束", v.Counts.TasksOpen))
		case v.Counts.TasksOpen > 0:
			return ph("merged_waiting", "进行中", fmt.Sprintf("PR 已合并，等 %d 个未完成的小任务结束后结束", v.Counts.TasksOpen))
		case mergesActive > 0:
			return ph("merged_waiting", "进行中", "PR 已合并，等合并队列清空后结束")
		}
		return ph("merged_waiting", "进行中", "PR 已合并，等组长这一回合结束（最终汇报）后结束")
	}
	var lead *EmployeeBrief
	for i, m := range v.Members {
		if !m.BusyElsewhere && m.Status != nil && workingStates[m.Status.State] {
			return ph("working", "进行中", "有人在干活（"+m.Name+"）")
		}
		if m.Tier == "lead" {
			lead = &v.Members[i]
		}
	}
	if v.Counts.TasksOpen > 0 {
		return ph("tasks", "进行中", fmt.Sprintf("等成员处理任务（%d 个未完成）", v.Counts.TasksOpen))
	}
	if lead != nil && lead.Status != nil {
		switch lead.Status.State {
		case "offline":
			return ph("lead_down", "等你处理", "组长离线：节点不在线，节点上线后才能继续")
		case "broken":
			return ph("lead_down", "等你处理", "组长的会话出错了，需要用户看一下")
		case "queued":
			return ph("lead_queued", "进行中", "等组长上线（节点工位已满，排队中）")
		case "waiting", "raising_hand":
			return ph("lead_waiting", "进行中", "组长在等别人回复")
		}
	}
	if v.LeadLast == nil {
		return ph("reading", "进行中", "组长正在阅读任务")
	}
	return ph("replied", "等你处理", "组长已回复，等用户回复")
}
