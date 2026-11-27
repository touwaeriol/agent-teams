package userfs

import (
	"context"

	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// DeliverablesDir is the folder of a project's deliverable versions inside the project's folder.
const DeliverablesDir = ".deliverables"

// DeliverableFolder is the owner's volume and the deliverable's folder in it
// (projects/<pid>/.deliverables/<id>); ok false when the owner's shared folder is not ready
// (artifacts.Volume). q is the caller's handle, so a deliverable inserted in its transaction is seen.
func (s *Service) DeliverableFolder(ctx context.Context, q store.DB, deliverableID string) (Files, string, bool, error) {
	var row struct{ ProjectID, UserID string }
	err := store.QueryRow(ctx, q, `SELECT d.project_id, p.user_id FROM at_deliverables d JOIN at_projects p ON p.id = d.project_id
		WHERE d.id = $1`, deliverableID).Scan(&row.ProjectID, &row.UserID)
	if err != nil {
		return nil, "", false, err
	}
	f, err := s.Files(ctx, row.UserID)
	if Unavailable(err) {
		return nil, "", false, nil
	}
	if err != nil {
		return nil, "", false, err
	}
	return f, ProjectDir(row.ProjectID) + "/" + DeliverablesDir + "/" + deliverableID, true, nil
}

// RemoveProject deletes a deleted project's folder (projects/<pid>) from its owner's volume
// (Remove: a failure is retried by the shared-folder sweep).
func (s *Service) RemoveProject(ctx context.Context, userID, projectID string) error {
	return s.Remove(ctx, userID, ProjectDir(projectID))
}
