package orchestrator

import (
	"context"
	"errors"
	"sync"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/core"
)

// hold keeps a session that no candidate node can run now (pickNode: the runtime is not
// installed or not logged in on any online node that reports its CLIs): the first message waits
// in at_session_holds with the reason, which the employee's status shows (queued / no_runtime),
// until ReleaseHolds finds a node.
func (o *Orchestrator) hold(ctx context.Context, tx *core.Tx, e core.Employee, first *protocol.FirstMessage,
	resume *protocol.ResumeRef, reason string) error {
	if first == nil {
		first = &protocol.FirstMessage{MsgID: protocol.MsgID(core.NewID("m"))}
	}
	if err := core.HoldSession(ctx, tx, e, *first, resume, reason); err != nil {
		return err
	}
	o.touch(tx, e.ID, "session.held")
	return nil
}

// appendHeld adds a message for an employee whose session is held to its held first message;
// false when the employee has no hold.
func (o *Orchestrator) appendHeld(ctx context.Context, tx *core.Tx, e core.Employee, first *protocol.FirstMessage) (bool, error) {
	h, err := core.LockHold(ctx, tx, e.ID)
	if errors.Is(err, core.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, o.hold(ctx, tx, e, first, nil, h.Reason)
}

// releaseMu serializes ReleaseHolds runs of this replica (node reports arrive per link).
var releaseMu sync.Mutex

// ReleaseHolds tries again to place the held sessions of the user's employees (a node came
// online or reported a runtime change): each one that a node can take now opens there with its
// held messages; the others keep waiting with an updated reason.
func (o *Orchestrator) ReleaseHolds(ctx context.Context, userID string) {
	releaseMu.Lock()
	defer releaseMu.Unlock()
	holds, err := core.UserHolds(ctx, o.core.Pool, userID)
	if err != nil {
		o.logErr("list held sessions", err)
		return
	}
	warmed := map[string]bool{}
	for _, h := range holds {
		if !warmed[h.ProjectID] {
			warmed[h.ProjectID] = true
			o.WarmProject(ctx, h.ProjectID) // no GitHub call inside the transaction
		}
		if err := o.core.InTx(ctx, func(tx *core.Tx) error { return o.releaseHold(ctx, tx, h.ProjectID, h.EmployeeID) }); err != nil {
			o.log.Warn("release held session", "employee_id", h.EmployeeID, "err", err)
		}
	}
}

// releaseHold opens the held session when a node can take it; when it must still wait, the hold
// stays as it was with the new reason.
func (o *Orchestrator) releaseHold(ctx context.Context, tx *core.Tx, projectID, employeeID string) error {
	if err := core.LockProject(ctx, tx, projectID); err != nil {
		return err
	}
	h, err := core.LockHold(ctx, tx, employeeID)
	if err != nil {
		return ignoreMissing(err)
	}
	e, err := core.EmployeeByID(ctx, tx, employeeID)
	if err != nil {
		return ignoreMissing(err)
	}
	if err := core.DropHold(ctx, tx, employeeID); err != nil {
		return err
	}
	o.touch(tx, e.ID, "session.released")
	if done, err := o.releaseElsewhere(ctx, tx, e, h); done || err != nil {
		return err
	}
	env, err := o.loadEnv(ctx, tx, e)
	if err != nil {
		return err
	}
	worked, err := o.workedHere(ctx, tx, env.emp)
	if err != nil {
		return err
	}
	pick, err := o.pickNode(ctx, tx, env.emp, env.project, h.Resume != nil || worked)
	if err != nil {
		return err
	}
	if pick.hold != "" {
		h.Reason = pick.hold
		return core.SaveHold(ctx, tx, h)
	}
	if _, err := o.openSession(ctx, tx, e, &h.First, h.Resume); err != nil {
		return err
	}
	return markHeldDelivered(ctx, tx, h.More)
}

// releaseElsewhere handles a hold whose employee no longer needs a new session: its thread ended
// (the messages stay in the conversation) or it got a live session meanwhile (they go to it).
func (o *Orchestrator) releaseElsewhere(ctx context.Context, tx *core.Tx, e core.Employee, h core.SessionHold) (bool, error) {
	awake, err := o.awake(ctx, tx, e)
	if err != nil || !awake {
		return true, err
	}
	sess, err := core.LiveSession(ctx, tx, e.ID)
	if errors.Is(err, core.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	d := Delivery{Text: h.First.Text, Images: imagesOf(h.First.Images), Anchor: h.First.Anchor, Kind: "system"}
	if err := o.send(ctx, tx, sess, string(h.First.MsgID), d); err != nil {
		return true, err
	}
	return true, markHeldDelivered(ctx, tx, h.More)
}

// markHeldDelivered stamps the messages merged into a held first message as delivered with it.
func markHeldDelivered(ctx context.Context, tx *core.Tx, msgIDs []string) error {
	for _, id := range msgIDs {
		if _, _, err := core.MarkDelivered(ctx, tx, id); err != nil {
			return err
		}
	}
	return nil
}

// imagesOf turns a first message's image refs back into attachment refs (session.send).
func imagesOf(refs []protocol.ImageRef) []core.AttachmentRef {
	out := make([]core.AttachmentRef, 0, len(refs))
	for _, r := range refs {
		a := core.AttachmentRef{ID: r.ID, ContentType: string(r.MediaType)}
		if r.Name != nil {
			a.Name = *r.Name
		}
		if r.Size != nil {
			a.Size = *r.Size
		}
		out = append(out, a)
	}
	return out
}
