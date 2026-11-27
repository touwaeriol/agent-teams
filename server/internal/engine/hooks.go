package engine

import (
	"context"
	"encoding/json"
	"github.com/touwaeriol/agent-teams/server/internal/userfs"
	"strings"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/orchestrator"
	"github.com/touwaeriol/agent-teams/server/internal/platform"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// Hooks implements platform.Hooks for the REST layer.
type Hooks struct{ e *Engine }

var _ platform.Hooks = (*Hooks)(nil)

// ProjectCreated delivers first_message to the (new) coordinator.
func (h *Hooks) ProjectCreated(ctx context.Context, p store.Project, firstMessage string) error {
	if strings.TrimSpace(firstMessage) == "" {
		return nil
	}
	_, conv, err := h.e.Orch.EnsureCoordinator(ctx, p.UserID, p.ID)
	if err != nil {
		return err
	}
	_, err = h.e.Orch.PostUserMessage(ctx, p.UserID, conv, firstMessage, false, nil)
	return err
}

// ProjectDeleting closes the project's sessions and purges its directories on the nodes.
func (h *Hooks) ProjectDeleting(ctx context.Context, _ string, projectID string) error {
	return h.e.Core.InTx(ctx, func(tx *core.Tx) error {
		if err := h.e.Orch.ProjectDeleting(ctx, tx, projectID); err != nil {
			return err
		}
		return h.e.Delivery.Purge(ctx, tx, projectID)
	})
}

// ProjectDeleted removes the deleted project's folder (projects/<pid>, its deliverable versions
// included) from the owner's shared folder; a failure only leaves clutter, so it is logged.
func (h *Hooks) ProjectDeleted(ctx context.Context, userID, projectID string) {
	if err := h.e.FS.RemoveProject(ctx, userID, projectID); err != nil {
		h.e.log.Warn("project folder not removed from the shared folder", "user_id", userID, "project_id", projectID, "err", err)
	}
}

// UserDeleting destroys the user's shared folder (in the background, from the volume as it stands
// now: the row goes with the user). Not having one is fine.
func (h *Hooks) UserDeleting(ctx context.Context, userID string) {
	if !h.e.FS.Enabled() {
		return
	}
	if err := h.e.FS.Disable(ctx, userID); err != nil && !userfs.IsNotFound(err) {
		h.e.log.Warn("shared folder of a deleted user not destroyed", "user_id", userID, "err", err)
	}
}

// ProjectArchived pauses the project's going threads and closes every session it has.
func (h *Hooks) ProjectArchived(ctx context.Context, _ string, projectID string) error {
	return h.e.Core.InTx(ctx, func(tx *core.Tx) error { return h.e.Orch.ProjectArchived(ctx, tx, projectID) })
}

// UserCutOff closes every session of the user and cancels the questions they wait on.
func (h *Hooks) UserCutOff(ctx context.Context, userID string) error {
	return h.e.Core.InTx(ctx, func(tx *core.Tx) error {
		live, err := core.UserLiveSessions(ctx, tx, userID)
		if err != nil {
			return err
		}
		for _, s := range live {
			if err := h.e.Orch.CloseSession(ctx, tx, s, "employee_removed"); err != nil {
				return err
			}
		}
		_, err = store.Exec(ctx, tx, `UPDATE at_questions q SET status = 'cancelled', decided_by = 'system:user_cutoff', answered_at = now()
			FROM at_projects p WHERE p.id = q.project_id AND p.user_id = $1 AND q.status = 'open'`, userID)
		return err
	})
}

// NodeDeleting moves the node's employees off it (fresh sessions with a handoff on the next message).
func (h *Hooks) NodeDeleting(ctx context.Context, _ string, nodeID string) error {
	h.e.Gateway.Disconnect(ctx, nodeID, "node deleted")
	return h.e.Core.InTx(ctx, func(tx *core.Tx) error { return h.e.Orch.NodeRemoved(ctx, tx, nodeID) })
}

// RolesSaved brings the user's staffed projects in line with the saved role templates.
func (h *Hooks) RolesSaved(ctx context.Context, userID string) error {
	return h.e.Orch.RolesSaved(ctx, userID)
}

// GitHubEvent forwards verified webhooks to delivery.
func (h *Hooks) GitHubEvent(ctx context.Context, event string, payload json.RawMessage) error {
	return h.e.Delivery.GitHubEvent(ctx, event, payload)
}

// SessionCreated opens the session's thread: its employee gets the first message once it starts.
func (h *Hooks) SessionCreated(ctx context.Context, p store.Project, s platform.SessionStart) (platform.SessionThread, error) {
	t, err := h.e.Orch.OpenSession(ctx, orchestrator.SessionRequest{UserID: p.UserID, ProjectID: p.ID, Title: s.Title,
		Message: s.Message, Role: s.Role, Runtime: s.Runtime, Model: s.Model, PermissionMode: s.PermissionMode, Effort: s.Effort, NodeID: s.NodeID, Branch: s.Branch, Repos: focusOf(s.Repos),
		Attachments: s.Attachments})
	return platform.SessionThread{ID: t.ID, No: t.No}, err
}

// SessionReposChanged checks out added repositories, drops removed ones and tells the employee.
func (h *Hooks) SessionReposChanged(ctx context.Context, projectID string, added []platform.SessionRepo, removed []string) error {
	return h.e.Orch.SessionReposChanged(ctx, projectID, focusOf(added), removed)
}

func focusOf(repos []platform.SessionRepo) []orchestrator.RepoFocus {
	out := make([]orchestrator.RepoFocus, len(repos))
	for i, r := range repos {
		out[i] = orchestrator.RepoFocus{Repo: r.Repo, Base: r.Branch}
	}
	return out
}
