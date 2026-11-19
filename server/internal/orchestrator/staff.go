package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/touwaeriol/agent-teams/server/internal/core"
)

// EnsureStaff brings the project's members (shared by every thread) in line with the owner's
// role templates and nodes (core.EnsureStaff). Opening a thread, saving the role templates and
// the scheduler call it; it is idempotent.
func (o *Orchestrator) EnsureStaff(ctx context.Context, tx *core.Tx, projectID string) error {
	owner, err := core.ProjectOwner(ctx, tx, projectID)
	if err != nil {
		return err
	}
	roles, err := o.userRoles(ctx, owner)
	if err != nil {
		return err
	}
	nodes, err := o.store.ListNodes(ctx, owner)
	if err != nil {
		return err
	}
	released, err := core.EnsureStaff(ctx, tx, projectID, core.Staffing{Roles: roles, Nodes: nodes, Hair: o.hairFor})
	if err != nil {
		return err
	}
	if err := o.closeRetired(ctx, tx, projectID); err != nil {
		return err
	}
	if len(released) > 0 {
		tx.AfterCommit(func(ctx context.Context) { o.Kick(ctx, projectID) })
	}
	return o.tellReleased(ctx, tx, released)
}

// tellReleased emits the tasks a retired member was named for (now tasks of its role) and tells
// each thread's lead.
func (o *Orchestrator) tellReleased(ctx context.Context, tx *core.Tx, released []core.Task) error {
	byThread := map[string][]core.Task{}
	var order []string
	for _, t := range released {
		if err := tx.EmitTask(ctx, t); err != nil {
			return err
		}
		if byThread[t.ConversationID] == nil {
			order = append(order, t.ConversationID)
		}
		byThread[t.ConversationID] = append(byThread[t.ConversationID], t)
	}
	for _, th := range order {
		if err := o.tellLeadReleased(ctx, tx, th, byThread[th]); err != nil {
			return err
		}
	}
	return nil
}

// tellLeadReleased tells a started thread's lead (a queued thread's lead has not begun yet).
func (o *Orchestrator) tellLeadReleased(ctx context.Context, tx *core.Tx, threadID string, tasks []core.Task) error {
	t, err := core.ThreadByID(ctx, tx, threadID)
	if err != nil || t.Status == "queued" {
		return ignoreMissing(err)
	}
	lead, err := core.Lead(ctx, tx, threadID)
	if err != nil {
		return ignoreMissing(err)
	}
	lines := make([]string, len(tasks))
	for i, t := range tasks {
		lines[i] = fmt.Sprintf("%s → %s", t.ID, t.Role)
	}
	text := "[平台] 岗位编制缩减，指名给已停用成员的待领任务改由同岗位任一成员领取：" + strings.Join(lines, "、")
	_, err = o.Deliver(ctx, tx, lead, Delivery{Text: text, Kind: "system", Priority: "later"})
	return err
}

// closeRetired closes the live sessions of the project's deactivated members that hold no task.
func (o *Orchestrator) closeRetired(ctx context.Context, tx *core.Tx, projectID string) error {
	var retired []core.Employee
	if err := tx.Gorm(ctx).Where("project_id = ? AND thread_id IS NULL AND tier = 'member' AND NOT active", projectID).
		Find(&retired).Error; err != nil {
		return err
	}
	for _, e := range retired {
		if err := o.closeIdle(ctx, tx, e); err != nil {
			return err
		}
	}
	return nil
}

func (o *Orchestrator) closeIdle(ctx context.Context, tx *core.Tx, e core.Employee) error {
	if _, err := core.CurrentTaskOf(ctx, tx, e); !errors.Is(err, core.ErrNotFound) {
		return err
	}
	s, err := core.LiveSession(ctx, tx, e.ID)
	if errors.Is(err, core.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return o.closeAll(ctx, tx, []core.AgentSession{s}, "employee_removed")
}

// Released runs after a project member's task left claimed / running: a member the role
// templates no longer count (kept while it was busy) is retired now and its session closed.
func (o *Orchestrator) Released(ctx context.Context, e core.Employee) {
	err := o.core.InTx(ctx, func(tx *core.Tx) error {
		owner, err := core.ProjectOwner(ctx, tx, e.ProjectID)
		if err != nil {
			return err
		}
		roles, err := o.userRoles(ctx, owner)
		if err != nil || !core.Surplus(e, roles) {
			return err
		}
		return o.EnsureStaff(ctx, tx, e.ProjectID)
	})
	o.logErr("retire member", err)
}

// RolesSaved re-staffs the user's projects that have members after the role templates changed
// (a project without members gets them when its next thread opens), and brings the model of the
// coordinators and of the leads of unfinished threads in line with their template (syncRoleModels).
func (o *Orchestrator) RolesSaved(ctx context.Context, userID string) error {
	if err := o.syncRoleModels(ctx, userID); err != nil {
		return err
	}
	projects, err := core.StaffedProjects(ctx, o.core.Pool, userID)
	if err != nil {
		return err
	}
	for _, id := range projects {
		if err := o.core.InTx(ctx, func(tx *core.Tx) error { return o.EnsureStaff(ctx, tx, id) }); err != nil {
			return err
		}
	}
	return nil
}

// syncRoleModels copies the coordinator and lead templates' model into the role copy (spec.model)
// of the user's coordinators and of the leads of unfinished threads, and sends the resulting model
// to their live sessions (ApplyModel). Members get their whole copy refreshed by EnsureStaff; the
// coordinator and leads keep the copy they were created with except for the model, which they
// cannot switch with /model and so must follow the role setting.
func (o *Orchestrator) syncRoleModels(ctx context.Context, userID string) error {
	list, err := o.userRoles(ctx, userID)
	if err != nil {
		return err
	}
	return o.core.InTx(ctx, func(tx *core.Tx) error {
		for _, r := range list {
			if !fixedModel(r.Tier) {
				continue
			}
			stale, err := core.StaleRoleModels(ctx, tx, userID, r.Tier, r.Model)
			if err != nil {
				return err
			}
			for _, e := range stale {
				if err := core.SetSpecModel(ctx, tx, e.ID, r.Model); err != nil {
					return err
				}
				e.Spec.Model = r.Model
				p, err := o.project(ctx, tx, e.ProjectID)
				if err != nil {
					return err
				}
				// The new model is saved either way: a session that misses it picks it up when it reopens.
				_, err = o.ApplyModel(ctx, tx, e, p)
				o.logErr("send role model to "+e.Name, err)
			}
		}
		return nil
	})
}
