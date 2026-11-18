package orchestrator

import (
	"context"
	"errors"

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
	if err := core.EnsureStaff(ctx, tx, projectID, core.Staffing{Roles: roles, Nodes: nodes, Hair: o.hairFor}); err != nil {
		return err
	}
	return o.closeRetired(ctx, tx, projectID)
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

// RolesSaved re-staffs the user's projects that have members after the role templates changed;
// a project without members gets them when its next thread opens.
func (o *Orchestrator) RolesSaved(ctx context.Context, userID string) error {
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
