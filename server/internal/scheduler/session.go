package scheduler

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/orchestrator"
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

// limitOf is the role's context limit capped at the window the runtime reported (a limit above
// the model's window would never be reached), else that window (0: unknown).
func limitOf(r store.Role, max *int) int {
	switch {
	case r.ContextLimit != nil && max != nil && *max > 0:
		return min(*r.ContextLimit, *max)
	case r.ContextLimit != nil:
		return *r.ContextLimit
	case max != nil:
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
		return s.compactFirst(ctx, tx, t, task, m, sess)
	case stepClear:
		if err := s.orch.ClearSession(ctx, tx, sess); err != nil {
			return err
		}
	}
	return s.syncThenBrief(ctx, tx, t, task, m)
}

// compactFirst queues session.compact; the task stays claimed with its compact_id.
func (s *Scheduler) compactFirst(ctx context.Context, tx *core.Tx, t core.Thread, task core.Task, m core.Employee, sess core.AgentSession) error {
	next, err := orchestrator.NextTaskNote(ctx, tx, t, task)
	if err != nil {
		return err
	}
	id := core.NewID("cp")
	if err := s.orch.Compact(ctx, tx, sess, id, compactInstructions(m.Spec, next)); err != nil {
		return err
	}
	_, err = core.UpdateTask(ctx, tx, task, map[string]any{"compact_id": id, "wait_since": time.Now().UTC()})
	return err
}

// compactInstructions: the role's focus and the note on the task that comes next (the anchor's
// content), within session.compact's limit.
func compactInstructions(r store.Role, next string) string {
	return clip(strings.TrimSpace(r.CompactFocus+"\n"+next), orchestrator.AnchorMax)
}

// Compacted handles session.compacted from nodeID: the context is recorded and the task that
// waited for it goes on to the pre-task sync and the brief. A failed compaction goes on all the
// same. A report about a session the node does not run (any more) is ignored.
func (s *Scheduler) Compacted(ctx context.Context, nodeID string, m *protocol.SessionCompacted) error {
	return s.core.InTx(ctx, func(tx *core.Tx) error {
		sess, err := core.SessionByID(ctx, tx, string(m.SessionID))
		if err != nil || sess.NodeID == nil || *sess.NodeID != nodeID {
			return ignoreMissing(err)
		}
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
	if sc.Employee.Spec.Solo && sc.Session.ThreadID != nil {
		// A session's page shows the context bar (ThreadView.session).
		if err := tx.EmitThread(ctx, *sc.Session.ThreadID); err != nil {
			return err
		}
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
