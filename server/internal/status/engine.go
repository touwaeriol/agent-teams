package status

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/core"
)

// BubbleTTL is how long a set_status bubble stays (or until the next one / task change).
const BubbleTTL = 10 * time.Minute

// Kind of an observed fact.
type Kind int

// Observation kinds.
const (
	Touch Kind = iota // something persisted changed (approval, question, task, session state, node)
	TurnStarted
	TurnEnded
	ToolStarted
	ToolEnded
	SetStatus
	Claimed
	Woken // a message is on its way to the employee (its session may be parked or closed)
)

// WakeTTL is how long a woken employee shows as busy without its turn starting.
const WakeTTL = 3 * time.Minute

// Observation is one fact about an employee.
type Observation struct {
	Kind     Kind
	Cause    string
	Category string
	Sub      string
	Summary  string
	Text     string // SetStatus bubble
	BubbleKd string
	TaskID   string
	TaskKind string
}

// activity is what only lives in memory: the running turn and tool, the set_status bubble.
type activity struct {
	turn        bool
	toolCat     string
	toolSub     string
	summary     string
	bubble      string
	bubbleKind  string
	bubbleUntil time.Time
	claimTask   string
	claimKind   string
	cause       string
	wakeUntil   time.Time
}

// Engine derives and records employee states.
type Engine struct {
	core     *core.Store
	online   func(nodeID string) bool
	log      *slog.Logger
	Debounce time.Duration

	mu     sync.Mutex
	act    map[string]*activity
	timers map[string]*time.Timer
}

// New returns an engine with the 800 ms debounce of §11.2.
func New(cs *core.Store, online func(string) bool, log *slog.Logger) *Engine {
	return &Engine{core: cs, online: online, log: log, Debounce: 800 * time.Millisecond, act: map[string]*activity{},
		timers: map[string]*time.Timer{}}
}

// Observe applies a fact and (re)computes the employee's state.
func (e *Engine) Observe(ctx context.Context, employeeID string, o Observation) {
	claim := e.apply(employeeID, o)
	if claim || e.Debounce <= 0 {
		e.flush(context.WithoutCancel(ctx), employeeID)
		return
	}
	e.schedule(employeeID)
}

func (e *Engine) apply(employeeID string, o Observation) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	a := e.act[employeeID]
	if a == nil {
		a = &activity{}
		e.act[employeeID] = a
	}
	a.cause = o.Cause
	switch o.Kind {
	case TurnStarted:
		a.turn, a.wakeUntil = true, time.Time{}
	case TurnEnded:
		a.turn, a.toolCat, a.toolSub, a.summary, a.wakeUntil = false, "", "", "", time.Time{}
	case ToolStarted:
		a.turn, a.wakeUntil = true, time.Time{}
		a.toolCat, a.toolSub, a.summary = o.Category, o.Sub, o.Summary
	case Woken:
		a.wakeUntil = time.Now().Add(WakeTTL)
		time.AfterFunc(WakeTTL+time.Second, func() { e.flush(context.Background(), employeeID) })
	case ToolEnded:
		a.toolCat, a.toolSub = "", ""
	case SetStatus:
		a.bubble, a.bubbleKind, a.bubbleUntil = o.Text, o.BubbleKd, time.Now().Add(BubbleTTL)
	case Claimed:
		a.claimTask, a.claimKind = o.TaskID, o.TaskKind
		a.bubble = ""
		return true
	}
	return false
}

func (e *Engine) schedule(employeeID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if t, ok := e.timers[employeeID]; ok {
		t.Reset(e.Debounce)
		return
	}
	e.timers[employeeID] = time.AfterFunc(e.Debounce, func() {
		e.mu.Lock()
		delete(e.timers, employeeID)
		e.mu.Unlock()
		e.flush(context.Background(), employeeID)
	})
}

// Forget drops the in-memory activity (session closed or crashed).
func (e *Engine) Forget(employeeID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.act, employeeID)
}

func (e *Engine) snapshot(employeeID string) activity {
	e.mu.Lock()
	defer e.mu.Unlock()
	a := e.act[employeeID]
	if a == nil {
		return activity{}
	}
	out := *a
	a.claimTask, a.claimKind = "", "" // claiming is a one-shot state
	return out
}

func (e *Engine) flush(ctx context.Context, employeeID string) {
	err := e.core.InTx(ctx, func(tx *core.Tx) error { return e.write(ctx, tx, employeeID) })
	if err != nil && err != core.ErrNotFound {
		e.log.Warn("status write failed", "employee_id", employeeID, "err", err)
	}
}

// Refresh recomputes and writes inside an existing transaction (no debounce).
func (e *Engine) Refresh(ctx context.Context, tx *core.Tx, employeeID string) error {
	return e.write(ctx, tx, employeeID)
}

func (e *Engine) write(ctx context.Context, tx *core.Tx, employeeID string) error {
	emp, err := core.EmployeeByID(ctx, tx, employeeID)
	if err != nil {
		return err
	}
	a := e.snapshot(employeeID)
	f, task, err := e.facts(ctx, tx, emp, a)
	if err != nil {
		return err
	}
	d := Decide(f)
	row := e.row(emp, d, a, task)
	same, err := unchanged(ctx, tx, row)
	if err != nil || same {
		return err
	}
	_, err = tx.EmitStatus(ctx, row)
	return err
}

func (e *Engine) row(emp core.Employee, d Decision, a activity, task core.Task) core.StatusRow {
	row := core.StatusRow{ProjectID: emp.ProjectID, ThreadID: emp.WorkThreadID(), EmployeeID: emp.ID, Tier: emp.Tier,
		Role: emp.Role, State: d.State, Substate: d.Substate, TaskID: task.ID, TaskKind: task.Kind, Cause: a.cause}
	if a.claimTask != "" {
		row.TaskID, row.TaskKind = a.claimTask, a.claimKind
	}
	row.Bubble, row.BubbleKind = defaultBubble(d, a.summary, row.TaskID)
	if a.bubble != "" && time.Now().Before(a.bubbleUntil) && d.State != "raising_hand" {
		until := a.bubbleUntil
		row.Bubble, row.BubbleKind, row.BubbleUntil = a.bubble, first(a.bubbleKind, "plain"), &until
	}
	return row
}

func first(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// unchanged reports whether the employee's latest status already says the same.
func unchanged(ctx context.Context, tx *core.Tx, r core.StatusRow) (bool, error) {
	var cur struct{ State, Substate, Bubble, TaskID string }
	err := core.MapErr(tx.Gorm(ctx).Table("at_employee_status").Select("state, substate, bubble, task_id").
		Where("employee_id = ?", r.EmployeeID).Take(&cur).Error)
	if err == core.ErrNotFound {
		return false, nil
	}
	return err == nil && cur.State == r.State && cur.Substate == r.Substate && cur.Bubble == r.Bubble &&
		cur.TaskID == r.TaskID, err
}
