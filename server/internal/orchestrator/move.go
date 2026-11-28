package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw"
	"github.com/touwaeriol/agent-teams/server/internal/runtimes"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// Session moves (protocol.md §4.11, architecture.md §6.2): when an employee's session hits its
// node's subscription limit and another candidate node of the same runtime still has quota, the
// session is closed on the old node, which packs the conversation and a snapshot of every checkout
// (session.export → PUT /nodes/moves/{id} → session.exported); meanwhile the employee's messages
// wait in a hold. Then the session opens on the new node with session.open.import and resumes the
// same conversation there. Any failure falls back to what happens without moves: the hold is
// released as usual (a recap on another node, or the old node and its usage-limit pause).

// FeatureSessionMove is the hello feature of daemons that export and import sessions.
const FeatureSessionMove = "session_move"

// CloseNodeMove closes a session that moves to another node.
const CloseNodeMove = "node_move"

// moveExportTimeout: an export not answered by then is given up (the hold is released as usual).
const moveExportTimeout = 10 * time.Minute

// moveImportTimeout: a moved session that has not answered session.open by then drops its archive.
const moveImportTimeout = time.Hour

// moveRetryAfter: after a failed move the employee waits for its limit as before for this long.
const moveRetryAfter = 30 * time.Minute

// moveKeep is how long finished move rows are kept (they tell moveTarget which nodes are out of quota).
const moveKeep = 7 * 24 * time.Hour

// movableRuntimes can resume a conversation from a copied transcript.
var movableRuntimes = []string{runtimes.Claude, runtimes.Codex}

// MoveOnLimit starts moving the session to another node after a usage limit that resets at
// resetAt; false when it cannot (the caller pauses for the limit as before): the runtime cannot
// move, the session has no conversation id, either daemon lacks session_move, a move of the
// employee failed a short while ago, or no other candidate node has quota.
func (o *Orchestrator) MoveOnLimit(ctx context.Context, tx *core.Tx, sc core.SessionContext, resetAt time.Time) (bool, error) {
	s := sc.Session
	resume := resumeRef(s)
	if sc.Employee.Spec.Local {
		return false, nil // a session's local folder stays on its node (pinLocal)
	}
	if s.NodeID == nil || s.State == "closed" || resume == nil || !movable(s.Runtime) {
		return false, nil
	}
	if ok, err := core.NodeHasFeature(ctx, tx, *s.NodeID, FeatureSessionMove); err != nil || !ok {
		return false, err
	}
	if err := core.LockProject(ctx, tx, sc.Employee.ProjectID); err != nil {
		return false, err
	}
	pending, blocked, err := moveState(ctx, tx, sc.Employee.ID)
	if err != nil || pending || blocked {
		return pending, err // on its way already: no pause; a recent failure: pause as before
	}
	env, err := o.loadEnv(ctx, tx, sc.Employee)
	if err != nil {
		return false, err
	}
	target, ok, err := o.moveTarget(ctx, tx, env, *s.NodeID)
	if err != nil || !ok {
		return false, err
	}
	return true, o.startMove(ctx, tx, env, s, target, resume, resetAt)
}

func movable(runtime string) bool {
	id := runtimes.Of(runtime).ID
	for _, r := range movableRuntimes {
		if r == id {
			return true
		}
	}
	return false
}

// moveState: pending = a move of the employee is in progress (another limit event of the same
// session needs no pause, the session is on its way); blocked = one failed within moveRetryAfter.
func moveState(ctx context.Context, q core.DB, employeeID string) (pending, blocked bool, err error) {
	var row struct{ Pending, Blocked bool }
	err = q.Gorm(ctx).Raw(`SELECT
		EXISTS (SELECT 1 FROM at_session_moves WHERE employee_id = ? AND status IN ('exporting', 'importing')) AS pending,
		EXISTS (SELECT 1 FROM at_session_moves WHERE employee_id = ? AND status = 'failed' AND updated_at > ?) AS blocked`,
		employeeID, employeeID, time.Now().Add(-moveRetryAfter)).Scan(&row).Error
	return row.Pending, row.Blocked, err
}

// moveTarget is the best other candidate node for the employee's runtime (pickRank): online, the
// runtime logged in there, a free slot and quota under QuotaHardFull (not the user's thresholds:
// a moved session ran out, so any node with quota left beats pausing; pickRank still prefers a node under them), a session_move daemon, and not a node a
// move left because of a limit that has not reset yet.
func (o *Orchestrator) moveTarget(ctx context.Context, tx *core.Tx, env sessionEnv, from string) (store.Node, bool, error) {
	e := env.emp
	nodes, err := o.store.On(tx).ListNodes(ctx, env.project.UserID)
	if err != nil {
		return store.Node{}, false, err
	}
	reports, err := o.store.On(tx).NodeRuntimes(ctx, nodeIDs(nodes))
	if err != nil {
		return store.Node{}, false, err
	}
	limited, err := limitedNodes(ctx, tx, e.Runtime)
	if err != nil {
		return store.Node{}, false, err
	}
	limits, err := o.quotaLimits(ctx, tx, env.project.UserID)
	if err != nil {
		return store.Node{}, false, err
	}
	states := o.nodeStates(ctx, tx, core.NodeCandidates(e, nodes), e.Runtime, Model(e, env.project), reports, limits)
	best := -1
	for i, c := range states {
		// The hard limit, not the user's thresholds: the session is moved because its node ran out,
		// so any node with quota left beats pausing; pickRank still prefers one that is not tight.
		if c.id == from || !c.usable || c.hardFull() || limited[c.id] {
			continue
		}
		if ok, err := core.NodeHasFeature(ctx, tx, c.id, FeatureSessionMove); err != nil || !ok {
			if err != nil {
				return store.Node{}, false, err
			}
			continue
		}
		if best < 0 || pickRank(c, false, states[best], false) < 0 {
			best = i
		}
	}
	if best < 0 {
		return store.Node{}, false, nil
	}
	for _, n := range nodes {
		if n.ID == states[best].id {
			return n, true, nil
		}
	}
	return store.Node{}, false, nil
}

// limitedNodes are the nodes a move of this runtime left whose limit has not reset yet (the usage
// report may not show it yet).
func limitedNodes(ctx context.Context, q core.DB, runtime string) (map[string]bool, error) {
	var ids []string
	err := q.Gorm(ctx).Table("at_session_moves").Where("runtime = ? AND reset_at > ?", runtimes.Of(runtime).ID, time.Now()).
		Distinct().Pluck("from_node_id", &ids).Error
	out := map[string]bool{}
	for _, id := range ids {
		out[id] = true
	}
	return out, err
}

// MovedNote opens the first message of a moved session.
func MovedNote(runtime, node string) string {
	return fmt.Sprintf("（平台）原节点的 %s 使用额度用完了，平台已把这个会话连同工作区（本地提交和未提交的改动）迁移到节点 %s，"+
		"请从中断的地方继续未完成的工作。", runtimes.Of(runtime).Title, node)
}

// moveFallbackNote replaces MovedNote when the move did not happen.
const moveFallbackNote = "（平台）请从中断的地方继续未完成的工作。"

// startMove closes the session, holds the employee's messages (the ones still on their way to the
// old session first, after the note) and asks the old node to export.
func (o *Orchestrator) startMove(ctx context.Context, tx *core.Tx, env sessionEnv, s core.AgentSession, target store.Node,
	resume *protocol.ResumeRef, resetAt time.Time) error {
	e := env.emp
	first, err := o.movedFirst(ctx, tx, s, MovedNote(e.Runtime, target.Name))
	if err != nil {
		return err
	}
	if err := o.closeSession(ctx, tx, s, CloseNodeMove); err != nil {
		return err
	}
	id := core.NewID("mv")
	err = tx.Gorm(ctx).Table("at_session_moves").Create(map[string]any{"id": id, "employee_id": e.ID, "project_id": e.ProjectID,
		"from_session_id": s.ID, "from_node_id": *s.NodeID, "to_node_id": target.ID, "runtime": runtimes.Of(e.Runtime).ID,
		"reset_at": resetAt}).Error
	if err != nil {
		return err
	}
	if err := o.hold(ctx, tx, e, &first, resume, fmt.Sprintf("额度用完，正在把会话迁移到节点 %s", target.Name)); err != nil {
		return err
	}
	var repos []protocol.RelPath
	if len(env.slugs()) > 0 {
		for _, p := range o.checkoutPaths(ctx, tx, PathsOf(e, env.thread), env) {
			repos = append(repos, protocol.RelPath(p))
		}
	}
	body := protocol.SessionExport{ExportID: protocol.ExportID(id), SessionID: protocol.SessionID(s.ID),
		Runtime: protocol.SessionExportRuntime(runtimes.Of(e.Runtime).ID), Resume: *resume, Cwd: protocol.RelPath(s.Cwd), Repos: repos}
	if _, err := o.gw.Enqueue(ctx, tx, *s.NodeID, nodegw.Command{Type: protocol.TypeSessionExport, Body: body,
		Meta: map[string]any{"purpose": "move", "move_id": id}}); err != nil {
		return err
	}
	o.log.Info("moving session after usage limit", "employee", e.Name, "session_id", s.ID, "to", target.Name, "move_id", id)
	return tx.EmitActivity(ctx, e.ProjectID, e.WorkThreadID(), e.Name, "额度用完，正在把会话迁移到节点 "+target.Name)
}

// movedFirst is the moved session's first message: the note, then the messages the old session
// never got (folded in like a handoff: the first keeps its msg id, the others count as delivered).
func (o *Orchestrator) movedFirst(ctx context.Context, tx *core.Tx, s core.AgentSession, note string) (protocol.FirstMessage, error) {
	first := protocol.FirstMessage{MsgID: protocol.MsgID(core.NewID("m")), Text: note}
	queued, err := o.inFlight(ctx, tx, s, nil)
	if err != nil || len(queued) == 0 {
		return first, err
	}
	first.MsgID = queued[0].MsgID
	for _, q := range queued {
		first.Text += "\n\n---\n" + q.Text
		joinRefs(&first, q)
	}
	return first, markForwarded(ctx, tx, queued[1:])
}

// sessionMove is a row of at_session_moves.
type sessionMove struct {
	ID            string
	EmployeeID    string
	ProjectID     string
	FromSessionID string
	FromNodeID    string
	ToNodeID      string
	ToSessionID   *string
	Runtime       string
	Status        string
	Size          *int64
	SHA256        *string `gorm:"column:sha256"`
	UpdatedAt     time.Time
}

func lockMove(ctx context.Context, tx *core.Tx, id string) (sessionMove, error) {
	var mv sessionMove
	err := core.MapErr(tx.Gorm(ctx).Table("at_session_moves").Clauses(core.ForUpdate).
		Select("id, employee_id, project_id, from_session_id, from_node_id, to_node_id, to_session_id, runtime, status, size, sha256, updated_at").
		Where("id = ?", id).Take(&mv).Error)
	return mv, err
}

func setMove(ctx context.Context, tx *core.Tx, id string, fields map[string]any) error {
	fields["updated_at"] = time.Now()
	return tx.Gorm(ctx).Table("at_session_moves").Where("id = ?", id).Updates(fields).Error
}

// failMove records why a move did not happen and drops its archive.
func failMove(ctx context.Context, tx *core.Tx, id, why string) error {
	return setMove(ctx, tx, id, map[string]any{"status": "failed", "error": clip(why, 2000), "archive": nil})
}

// HandleExported takes the old node's answer: the session opens on the target with the archive
// it uploaded, or (failed, or the upload is missing) the hold is released as usual.
func (o *Orchestrator) HandleExported(ctx context.Context, node store.Node, m *protocol.SessionExported) error {
	return o.core.InTx(ctx, func(tx *core.Tx) error {
		mv, err := lockMove(ctx, tx, string(m.ExportID))
		if err != nil || mv.FromNodeID != node.ID || mv.Status != "exporting" {
			return ignoreMissing(err)
		}
		if why := exportProblem(mv, m); why != "" {
			o.log.Warn("session export failed", "move_id", mv.ID, "err", why)
			if err := failMove(ctx, tx, mv.ID, why); err != nil {
				return err
			}
			return o.releaseMove(ctx, tx, mv, nil)
		}
		if err := setMove(ctx, tx, mv.ID, map[string]any{"status": "importing"}); err != nil {
			return err
		}
		imp := &protocol.SessionImport{ExportID: m.ExportID, Size: int(*mv.Size), SHA256: *mv.SHA256}
		return o.releaseMove(ctx, tx, mv, imp)
	})
}

// exportProblem is why an export cannot be imported ("" when it can).
func exportProblem(mv sessionMove, m *protocol.SessionExported) string {
	switch {
	case !m.Ok:
		if m.Error != nil {
			return *m.Error
		}
		return "export failed"
	case mv.Size == nil || mv.SHA256 == nil:
		return "the archive was not uploaded"
	case m.Size == nil || m.SHA256 == nil || int64(*m.Size) != *mv.Size || *m.SHA256 != *mv.SHA256:
		return "the uploaded archive is not the exported one"
	}
	return ""
}

// releaseMove ends the hold a move made: with imp the session opens on the move's target and
// resumes from the archive; without, or when the target cannot take it now, the hold is released
// like any other (releaseHold) with the note saying nothing about a move.
func (o *Orchestrator) releaseMove(ctx context.Context, tx *core.Tx, mv sessionMove, imp *protocol.SessionImport) error {
	if err := core.LockProject(ctx, tx, mv.ProjectID); err != nil {
		return err
	}
	h, err := core.LockHold(ctx, tx, mv.EmployeeID)
	if errors.Is(err, core.ErrNotFound) {
		return failMove(ctx, tx, mv.ID, "the employee's messages were no longer held (thread ended or moved)")
	}
	if err != nil {
		return err
	}
	e, err := core.EmployeeByID(ctx, tx, mv.EmployeeID)
	if err != nil {
		return ignoreMissing(err)
	}
	if imp != nil {
		opened, err := o.openMoved(ctx, tx, e, h, mv, imp)
		if err != nil || opened {
			return err
		}
		if err := failMove(ctx, tx, mv.ID, "the new node cannot take the session now"); err != nil {
			return err
		}
	}
	note := MovedNote(mv.Runtime, o.nodeName(ctx, tx, e.ProjectID, mv.ToNodeID))
	h.First.Text = strings.Replace(h.First.Text, note, moveFallbackNote, 1)
	if err := core.SaveHold(ctx, tx, h); err != nil {
		return err
	}
	return o.releaseHold(ctx, tx, mv.ProjectID, mv.EmployeeID)
}

// openMoved opens the held session on the move's target with the archive; false (nothing done)
// when the target is offline or gone, or the employee no longer needs a session there.
func (o *Orchestrator) openMoved(ctx context.Context, tx *core.Tx, e core.Employee, h core.SessionHold, mv sessionMove,
	imp *protocol.SessionImport) (bool, error) {
	env, err := o.loadEnv(ctx, tx, e)
	if err != nil {
		return false, err
	}
	e = env.emp
	if !h.InThread(e.WorkThreadID()) || runtimes.Of(e.Runtime).ID != mv.Runtime || !o.gw.Online(mv.ToNodeID) {
		return false, nil
	}
	if _, err := core.LiveSession(ctx, tx, e.ID); !errors.Is(err, core.ErrNotFound) {
		return false, err // it got a live session meanwhile (err nil): releaseHold sends it the messages
	}
	node, err := o.store.On(tx).NodeOf(ctx, env.project.UserID, mv.ToNodeID)
	if err != nil {
		return false, ignoreMissing(err)
	}
	if err := core.DropHold(ctx, tx, e.ID); err != nil {
		return false, err
	}
	if err := o.pinThread(ctx, tx, e); err != nil {
		return false, err
	}
	sess, err := o.openOn(ctx, tx, env, node, &h.First, h.Resume, imp)
	if err != nil {
		return false, err
	}
	if err := setMove(ctx, tx, mv.ID, map[string]any{"to_session_id": sess.ID}); err != nil {
		return false, err
	}
	o.touch(tx, e.ID, "session.moved")
	return true, markHeldDelivered(ctx, tx, h.More)
}

func (o *Orchestrator) nodeName(ctx context.Context, q *core.Tx, projectID, nodeID string) string {
	var name string
	_ = q.Gorm(ctx).Table("at_nodes n").Select("n.name").Joins("JOIN at_projects p ON p.user_id = n.user_id").
		Where("n.id = ? AND p.id = ?", nodeID, projectID).Row().Scan(&name)
	return name
}

// settleMove records how a moved session's open ended (HandleOpened) and drops the archive.
func settleMove(ctx context.Context, tx *core.Tx, sessionID string, failed bool, why string) error {
	fields := map[string]any{"status": "done", "archive": nil, "updated_at": time.Now()}
	if failed {
		fields["status"], fields["error"] = "failed", clip(why, 2000)
	}
	return tx.Gorm(ctx).Table("at_session_moves").Where("to_session_id = ? AND status = 'importing'", sessionID).Updates(fields).Error
}

// movedPending: a hold that belongs to a move in progress is released by the move, not by ReleaseHolds.
func movedPending(ctx context.Context, q core.DB, employeeID string) (bool, error) {
	var n int64
	err := q.Gorm(ctx).Table("at_session_moves").Where("employee_id = ? AND status = 'exporting'", employeeID).Count(&n).Error
	return n > 0, err
}

// SweepMoves gives up exports nobody answered (the hold is released as usual), drops the archive
// of imports that never opened, and forgets old moves.
func (o *Orchestrator) SweepMoves(ctx context.Context, now time.Time) error {
	var stale []string
	err := o.core.Pool.Gorm(ctx).Table("at_session_moves").Where("status = 'exporting' AND updated_at < ?", now.Add(-moveExportTimeout)).
		Limit(50).Pluck("id", &stale).Error
	if err != nil {
		return err
	}
	for _, id := range stale {
		err := o.core.InTx(ctx, func(tx *core.Tx) error {
			mv, err := lockMove(ctx, tx, id)
			if err != nil || mv.Status != "exporting" {
				return ignoreMissing(err)
			}
			if err := failMove(ctx, tx, mv.ID, "the old node did not export the session in time"); err != nil {
				return err
			}
			return o.releaseMove(ctx, tx, mv, nil)
		})
		if err != nil {
			return err
		}
	}
	return o.core.InTx(ctx, func(tx *core.Tx) error {
		if err := tx.Gorm(ctx).Table("at_session_moves").Where("status = 'importing' AND updated_at < ?", now.Add(-moveImportTimeout)).
			Updates(map[string]any{"status": "failed", "error": "the new node did not open the session", "archive": nil, "updated_at": now}).Error; err != nil {
			return err
		}
		return tx.Gorm(ctx).Exec(`DELETE FROM at_session_moves WHERE status IN ('done', 'failed') AND updated_at < ?
			AND (reset_at IS NULL OR reset_at < ?)`, now.Add(-moveKeep), now).Error
	})
}
