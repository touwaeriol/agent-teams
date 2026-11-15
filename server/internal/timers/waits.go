package timers

import (
	"context"
	"errors"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/orchestrator"
)

// MinWait separates a usage limit (resets in hours) from the SDK's own short API retries:
// only a reset at least this far away pauses the thread.
const MinWait = 2 * time.Minute

// PauseReason marks threads paused by a usage limit.
const PauseReason = "rate_limit"

// ResumeNote wakes an employee once its usage limit has reset.
const ResumeNote = "（平台）Claude 使用限额已重置，请从中断的地方继续未完成的工作。"

// IsUsageLimit reports whether a rate_limited event's reset is far enough away to wait for.
func IsUsageLimit(resetAt *time.Time, now time.Time) bool {
	return resetAt != nil && resetAt.Sub(now) >= MinWait
}

// RecordLimit remembers that the employee hit a usage limit and pauses its thread.
func RecordLimit(ctx context.Context, tx *core.Tx, orch *orchestrator.Orchestrator, e core.Employee, resetAt time.Time, detail string) error {
	if _, err := tx.Exec(ctx, `INSERT INTO at_rate_limit_waits (employee_id, project_id, thread_id, reset_at, detail)
		VALUES ($1,$2,$3,$4,$5) ON CONFLICT (employee_id) DO UPDATE SET reset_at = GREATEST(at_rate_limit_waits.reset_at, EXCLUDED.reset_at),
		detail = EXCLUDED.detail`, e.ID, e.ProjectID, e.ThreadID, resetAt, detail); err != nil {
		return err
	}
	if e.ThreadID == nil {
		return nil
	}
	t, err := core.LockThread(ctx, tx, *e.ThreadID)
	if err != nil {
		return ignoreMissing(err)
	}
	if t.Status != "running" && t.Status != "waiting" && t.Status != "queued" {
		return nil
	}
	return orch.Pause(ctx, tx, t, PauseReason)
}

// Waiting reports whether the employee is waiting for a usage limit to reset (no nudges then).
func Waiting(ctx context.Context, q core.DB, employeeID string) (bool, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT count(*) FROM at_rate_limit_waits WHERE employee_id = $1`, employeeID).Scan(&n)
	return n > 0, err
}

// LimitResetAt is the latest reset time among a thread's waiting employees (nil = none).
func LimitResetAt(ctx context.Context, q core.DB, threadID string) (*time.Time, error) {
	var at *time.Time
	err := q.QueryRow(ctx, `SELECT max(reset_at) FROM at_rate_limit_waits WHERE thread_id = $1`, threadID).Scan(&at)
	return at, err
}

type dueWait struct {
	employeeID string
	threadID   *string
	autoResume bool
}

// resumeDue handles one wait whose limit has reset: the row goes away; with auto_resume the
// thread it paused runs again and the employee gets a note that resumes its work.
func resumeDue(ctx context.Context, tx *core.Tx, orch *orchestrator.Orchestrator, w dueWait) error {
	if _, err := tx.Exec(ctx, `DELETE FROM at_rate_limit_waits WHERE employee_id = $1`, w.employeeID); err != nil {
		return err
	}
	if !w.autoResume {
		return nil
	}
	active, err := resumeThread(ctx, tx, orch, w.threadID)
	if err != nil || !active {
		return err
	}
	e, err := core.EmployeeByID(ctx, tx, w.employeeID)
	if err != nil {
		return ignoreMissing(err)
	}
	_, err = orch.Deliver(ctx, tx, e, orchestrator.Delivery{Text: ResumeNote, Kind: "system", Priority: "next"})
	return err
}

// resumeThread resumes a thread the limit paused; it reports whether the employee may work now.
func resumeThread(ctx context.Context, tx *core.Tx, orch *orchestrator.Orchestrator, threadID *string) (bool, error) {
	if threadID == nil {
		return true, nil
	}
	t, err := core.LockThread(ctx, tx, *threadID)
	if err != nil {
		return false, ignoreMissing(err)
	}
	if t.Status == "paused" && t.PauseReason == PauseReason {
		if err := orch.Resume(ctx, tx, t); err != nil {
			return false, err
		}
		if t, err = core.ThreadByID(ctx, tx, t.ID); err != nil {
			return false, err
		}
	}
	return t.Active(), nil
}

func ignoreMissing(err error) error {
	if errors.Is(err, core.ErrNotFound) {
		return nil
	}
	return err
}
