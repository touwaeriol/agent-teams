package status

import (
	"context"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// facts loads the persisted facts and merges the in-memory activity.
func (e *Engine) facts(ctx context.Context, tx *core.Tx, emp core.Employee, a activity) (Facts, core.Task, error) {
	f := Facts{Claiming: a.claimTask != "", ToolCat: a.toolCat, ToolSub: a.toolSub, Turn: a.turn}
	sess, err := core.LiveSession(ctx, tx, emp.ID)
	switch {
	case err == nil:
		f.Broken = sess.State == "crashed"
		f.Queued = sess.State == "queued"
		f.Turn = f.Turn || sess.State == "turn_running"
		f.NodeOffline = sess.NodeID != nil && !e.online(*sess.NodeID)
	case err != core.ErrNotFound:
		return f, core.Task{}, err
	}
	hands, err := core.RaisedHands(ctx, tx, emp.ProjectID, emp.ThreadID, emp.Name)
	if err != nil {
		return f, core.Task{}, err
	}
	f.RaisedHand = hands > 0
	task, waiting, err := currentWork(ctx, tx, emp)
	f.Waiting = waiting
	if f.ToolCat == "platform" {
		f.ToolCat = ""
	}
	return f, task, err
}

// currentWork returns the employee's current task and whether it is waiting on others.
func currentWork(ctx context.Context, tx *core.Tx, emp core.Employee) (core.Task, bool, error) {
	if emp.ThreadID == nil {
		return core.Task{}, false, nil
	}
	task, err := core.CurrentTask(ctx, tx, *emp.ThreadID, emp.Name)
	if err == nil {
		ok, err := core.DepsSatisfied(ctx, tx, task)
		return task, !ok, err
	}
	if err != core.ErrNotFound {
		return core.Task{}, false, err
	}
	var n int
	err = store.QueryRow(ctx, tx, `SELECT count(*) FROM at_tasks WHERE thread_id = $1 AND claimed_by = $2
		AND status IN ('review','decision','merging')`, *emp.ThreadID, emp.Name).Scan(&n)
	return core.Task{}, n > 0, err
}
