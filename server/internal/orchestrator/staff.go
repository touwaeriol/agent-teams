package orchestrator

import (
	"context"

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
	return core.EnsureStaff(ctx, tx, projectID, core.Staffing{Roles: roles, Nodes: nodes, Hair: o.hairFor})
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
