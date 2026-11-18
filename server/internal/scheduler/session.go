package scheduler

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// CompactTimeout is how long a claimed task waits for session.compacted before it goes on.
const CompactTimeout = 10 * time.Minute

// SplitHintAfter is how many in-task compactions make the platform suggest splitting the task.
const SplitHintAfter = 2

// step is how a member's session starts the task it claimed (shared-members.md §5).
type step int

const (
	stepContinue step = iota // brief in the current session (or the one the brief opens)
	stepCompact              // compact first, brief on session.compacted
	stepClear                // close; the brief opens a fresh session with a thread digest
)

// sessionFacts is what the decision needs to know about the member's live session.
type sessionFacts struct {
	sameThread bool // the session is in the task's thread
	idle       bool // active or parked between turns (mid-turn, starting or crashed: leave it alone)
	tasksDone  int  // task briefs the session got so far
	used, max  *int // context used at the last turn end, and the runtime's window (tokens)
	canCompact bool // the node runs session.compact
}

// decideStep is the table of shared-members.md §5: clear after clear_after_tasks tasks; compact
// when the context reached context_limit × compact_between_pct (the runtime's window when the
// role has no limit), or clear when the node cannot compact; otherwise continue.
func decideStep(r store.Role, f sessionFacts) step {
	if !f.sameThread || !f.idle {
		return stepContinue
	}
	if r.ClearAfterTasks != nil && *r.ClearAfterTasks > 0 && f.tasksDone >= *r.ClearAfterTasks {
		return stepClear
	}
	if !overBetween(r, f) {
		return stepContinue
	}
	if f.canCompact {
		return stepCompact
	}
	return stepClear
}

func overBetween(r store.Role, f sessionFacts) bool {
	if r.CompactBetweenPct == nil || f.used == nil {
		return false
	}
	limit := limitOf(r, f.max)
	return limit > 0 && *f.used*100 >= limit**r.CompactBetweenPct
}

// limitOf is the role's context limit, else the window the runtime reported (0: unknown).
func limitOf(r store.Role, max *int) int {
	if r.ContextLimit != nil {
		return *r.ContextLimit
	}
	if max != nil {
		return *max
	}
	return 0
}

// sessionStep looks at the member's live session and decides the step.
func (s *Scheduler) sessionStep(ctx context.Context, tx *core.Tx, m core.Employee, threadID string) (core.AgentSession, step, error) {
	sess, err := core.LiveSession(ctx, tx, m.ID)
	if errors.Is(err, core.ErrNotFound) {
		return sess, stepContinue, nil
	}
	if err != nil {
		return sess, stepContinue, err
	}
	can, err := s.orch.CanCompact(ctx, tx, sess)
	if err != nil {
		return sess, stepContinue, err
	}
	idle := (sess.State == "active" || sess.State == "parked") && !sess.InTurn
	f := sessionFacts{sameThread: sess.ThreadID != nil && *sess.ThreadID == threadID, idle: idle,
		tasksDone: sess.TasksDone, used: sess.ContextUsed, max: sess.ContextMax, canCompact: can}
	return sess, decideStep(m.Spec, f), nil
}

// startTask manages the member's session, then syncs its worktree and sends the brief: a
// compaction comes first and the rest waits for session.compacted (or CompactTimeout).
func (s *Scheduler) startTask(ctx context.Context, tx *core.Tx, t core.Thread, task core.Task, m core.Employee) error {
	sess, st, err := s.sessionStep(ctx, tx, m, t.ID)
	if err != nil {
		return err
	}
	switch st {
	case stepCompact:
		return s.compactFirst(ctx, tx, task, m, sess)
	case stepClear:
		if err := s.orch.ClearSession(ctx, tx, sess); err != nil {
			return err
		}
	}
	return s.syncThenBrief(ctx, tx, t, task, m)
}

// compactFirst queues session.compact; the task stays claimed with its compact_id.
func (s *Scheduler) compactFirst(ctx context.Context, tx *core.Tx, task core.Task, m core.Employee, sess core.AgentSession) error {
	id := core.NewID("cp")
	if err := s.orch.Compact(ctx, tx, sess, id, compactInstructions(m.Spec, task)); err != nil {
		return err
	}
	_, err := core.UpdateTask(ctx, tx, task, map[string]any{"compact_id": id, "wait_since": time.Now().UTC()})
	return err
}

// compactInstructions: the role's focus and the task that comes next.
func compactInstructions(r store.Role, task core.Task) string {
	next := fmt.Sprintf("接下来要做任务 %s：%s", task.ID, clip(task.Instruction, 1000))
	return strings.TrimSpace(r.CompactFocus + "\n" + next)
}

// Compacted handles session.compacted: the context is recorded and the task that waited for it
// goes on to the pre-task sync and the brief. A failed compaction goes on all the same.
func (s *Scheduler) Compacted(ctx context.Context, m *protocol.SessionCompacted) error {
	return s.core.InTx(ctx, func(tx *core.Tx) error {
		if m.Context != nil {
			if err := core.RecordContext(ctx, tx, string(m.SessionID), m.Context.UsedTokens, m.Context.MaxTokens); err != nil {
				return err
			}
		}
		task, err := core.LockTaskByCompact(ctx, tx, m.CompactID)
		if err != nil {
			return ignoreMissing(err)
		}
		return s.afterCompact(ctx, tx, task)
	})
}

// afterCompact moves a task past its compaction wait.
func (s *Scheduler) afterCompact(ctx context.Context, tx *core.Tx, task core.Task) error {
	task, err := core.UpdateTask(ctx, tx, task, map[string]any{"compact_id": ""})
	if err != nil {
		return err
	}
	t, err := core.ThreadByID(ctx, tx, task.ConversationID)
	if err != nil {
		return err
	}
	m, err := core.EmployeeByName(ctx, tx, t.ProjectID, &t.ID, task.ClaimedBy)
	if err != nil {
		return ignoreMissing(err)
	}
	return s.syncThenBrief(ctx, tx, t, task, m)
}

// releaseStaleCompactions lets the tasks whose session.compacted did not come in time go on.
func (s *Scheduler) releaseStaleCompactions(ctx context.Context) {
	var stale []core.Task
	err := s.core.Pool.Gorm(ctx).Model(&core.Task{}).Where("status = 'claimed' AND compact_id <> '' AND wait_since < ?",
		time.Now().Add(-CompactTimeout)).Find(&stale).Error
	if err != nil {
		s.log.Warn("stale compactions", "err", err)
		return
	}
	for _, st := range stale {
		err := s.core.InTx(ctx, func(tx *core.Tx) error {
			task, err := core.LockTaskByCompact(ctx, tx, st.CompactID)
			if err != nil {
				return ignoreMissing(err)
			}
			return s.afterCompact(ctx, tx, task)
		})
		if err != nil {
			s.log.Warn("release stale compaction", "task_id", st.ID, "err", err)
		}
	}
}

// briefDelivered counts the brief in the session and records the context the task starts with.
func briefDelivered(ctx context.Context, tx *core.Tx, task core.Task, sessionID string) error {
	sess, err := core.SessionByID(ctx, tx, sessionID)
	if err != nil {
		return ignoreMissing(err)
	}
	if err := core.CountTaskBrief(ctx, tx, sess.ID); err != nil {
		return err
	}
	_, err = core.UpdateTask(ctx, tx, task, map[string]any{"ctx_start": sess.ContextUsed})
	return err
}

// ContextReported records a turn's context use on the session and, for the member's running
// task, as the context the task ends with so far.
func (s *Scheduler) ContextReported(ctx context.Context, tx *core.Tx, sc core.SessionContext, c protocol.ContextUsage) error {
	if err := core.RecordContext(ctx, tx, sc.Session.ID, c.UsedTokens, c.MaxTokens); err != nil {
		return err
	}
	task, err := runningTask(ctx, tx, sc)
	if err != nil {
		return ignoreMissing(err)
	}
	_, err = core.UpdateTask(ctx, tx, task, map[string]any{"ctx_end": c.UsedTokens})
	return err
}

// CompactedInTask counts a compaction the runtime did during the member's running task; from
// the SplitHintAfter-th one the lead hears, once, that the task may be too big (§5.2).
func (s *Scheduler) CompactedInTask(ctx context.Context, tx *core.Tx, sc core.SessionContext) error {
	task, err := runningTask(ctx, tx, sc)
	if err != nil {
		return ignoreMissing(err)
	}
	task, err = core.UpdateTask(ctx, tx, task, map[string]any{"compactions": task.Compactions + 1})
	if err != nil || task.Compactions < SplitHintAfter || task.SplitHinted {
		return err
	}
	if task, err = core.UpdateTask(ctx, tx, task, map[string]any{"split_hinted": true}); err != nil {
		return err
	}
	th, err := core.ThreadByID(ctx, tx, task.ConversationID)
	if err != nil {
		return err
	}
	return s.notifyLead(ctx, tx, th, fmt.Sprintf("[Task %s · %s] 任务很长，已压缩 %d 次，考虑拆小", task.ID, sc.Employee.Name, task.Compactions))
}

func runningTask(ctx context.Context, tx *core.Tx, sc core.SessionContext) (core.Task, error) {
	if sc.Employee.Tier != "member" {
		return core.Task{}, core.ErrNotFound
	}
	task, err := core.SessionTask(ctx, tx, sc)
	if err == nil && task.Status != "running" {
		return task, core.ErrNotFound
	}
	return task, err
}

// AnchorMax keeps the anchor inside session.send's limit (8000 characters).
const AnchorMax = 7500

// taskAnchor is the short reminder of the task the daemon puts back after the runtime compacts
// the session during it (shared-members.md §5.2): id, instruction, branch, hand-off notes of the
// dependencies, the latest progress and where the working notes are.
func taskAnchor(ctx context.Context, tx *core.Tx, t core.Thread, task core.Task) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "[任务锚点] 你正在做讨论串 #%d 的任务 %s（%s）。上下文刚被压缩过；如果这个任务已经 complete_task，忽略这段。\n",
		t.No, task.ID, task.Kind)
	fmt.Fprintf(&b, "原始说明：%s\n你的分支：%s\n", clip(task.Instruction, 3000), task.Branch)
	for _, dep := range task.DependsOn {
		d, err := core.TaskByID(ctx, tx, task.ConversationID, dep)
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

// NotesDir is where members keep the working notes of long tasks (git-ignored by the daemon).
const NotesDir = ".agent-notes/"
