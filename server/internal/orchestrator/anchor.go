package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/core"
)

// AnchorMax keeps the anchor inside session.send's limit (8000 characters).
const AnchorMax = 7500

// NotesDir is where members keep the working notes of long tasks (git-ignored by the daemon).
const NotesDir = ".agent-notes/"

// Heads of a task note: the anchor put back after the runtime compacted during the task, and the
// next task named in the instructions of a compaction between tasks.
const (
	anchorHead = "[任务锚点] 你正在做讨论串 #%d 的任务 %s（%s）。上下文刚被压缩过；如果这个任务已经 complete_task，忽略这段。\n"
	nextHead   = "接下来要做讨论串 #%d 的任务 %s（%s），压缩时保留和它有关的内容。\n"
)

// TaskAnchor is the short reminder of the task the daemon puts back after the runtime compacts
// the session during it (shared-members.md §5.2): id, instruction, branch, hand-off notes of the
// dependencies, the latest progress and where the working notes are.
func TaskAnchor(ctx context.Context, q core.DB, t core.Thread, task core.Task) (string, error) {
	return taskNote(ctx, q, t, task, anchorHead)
}

// NextTaskNote is the same note about the task a compaction between tasks makes room for.
func NextTaskNote(ctx context.Context, q core.DB, t core.Thread, task core.Task) (string, error) {
	return taskNote(ctx, q, t, task, nextHead)
}

func taskNote(ctx context.Context, q core.DB, t core.Thread, task core.Task, head string) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, head, t.No, task.ID, task.Kind)
	fmt.Fprintf(&b, "原始说明：%s\n你的分支：%s\n", clip(task.Instruction, 3000), task.Branch)
	for _, dep := range task.DependsOn {
		d, err := core.TaskByID(ctx, q, task.ConversationID, dep)
		if errors.Is(err, core.ErrNotFound) || (err == nil && d.Handoff == "") {
			continue
		}
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "依赖 %s（%s）的交接说明：%s\n", d.ID, d.ClaimedBy, clip(d.Handoff, 800))
	}
	if task.Progress != "" {
		fmt.Fprintf(&b, "最近报告的进度：%s\n", clip(task.Progress, 1000))
	}
	fmt.Fprintf(&b, "工作笔记：%s%s.md（先读它，再接着做）", NotesDir, task.ID)
	return clip(b.String(), AnchorMax), nil
}

// currentAnchor is the anchor of the task a project member holds (claimed or running);
// ok=false when it holds none.
func currentAnchor(ctx context.Context, q core.DB, e core.Employee) (string, bool, error) {
	if e.Tier != "member" {
		return "", false, nil
	}
	task, err := core.CurrentTaskOf(ctx, q, e)
	if errors.Is(err, core.ErrNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	t, err := core.ThreadByID(ctx, q, task.ConversationID)
	if err != nil {
		return "", false, err
	}
	anchor, err := TaskAnchor(ctx, q, t, task)
	return anchor, err == nil, err
}

// sendAnchor is the anchor a session.send to a running session carries: the delivery's own (a
// task brief); else "" when the member holds no task, so a finished task's anchor never comes
// back after a compaction; else none (the daemon keeps the brief's).
func sendAnchor(ctx context.Context, q core.DB, e core.Employee, d Delivery) (*string, error) {
	if d.Anchor != nil || e.Tier != "member" {
		return d.Anchor, nil
	}
	_, holds, err := currentAnchor(ctx, q, e)
	if err != nil || holds {
		return nil, err
	}
	none := ""
	return &none, nil
}

// openAnchor is session.open's anchor: the anchor of the task a member holds when its session
// (re)opens without a first message that carries one (a crash reopen, a reopen after a daemon
// restart, a hand-off, a cleared session opened by mail); the new process starts without the
// old one's anchor.
func openAnchor(ctx context.Context, q core.DB, e core.Employee, first *protocol.FirstMessage) (*string, error) {
	if first != nil && first.Anchor != nil {
		return nil, nil
	}
	anchor, holds, err := currentAnchor(ctx, q, e)
	if err != nil || !holds {
		return nil, err
	}
	return &anchor, nil
}
