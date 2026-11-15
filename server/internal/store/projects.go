package store

import (
	"context"
	"encoding/json"
	"time"
)

// Project is a row of at_projects plus the sidebar counters.
type Project struct {
	ID                string
	UserID            string
	Name              string
	Description       string
	Context           string
	Repos             []ProjectRepo
	CreatedAt         time.Time
	UpdatedAt         time.Time
	Pinned            bool
	Archived          bool
	WorkMode          string
	AutoApprove       bool
	RequireAcceptance bool
	Delivery          json.RawMessage
	OfficeLayout      string
	Waiting           int
	ThreadsActive     int
}

// NewProject is what POST /api/projects stores.
type NewProject struct {
	Name, Description, Context string
	Repos                      []ProjectRepo
	WorkMode                   string
	AutoApprove                bool
	RequireAcceptance          bool
	Delivery                   json.RawMessage
}

// ProjectRepo is one repository bound to a project (at_project_repos), in project order.
type ProjectRepo struct {
	Repo            string  `json:"repo"`
	GitConnectionID *string `json:"git_connection_id"`
}

const projectCols = `p.id, p.user_id, p.name, p.description, p.context,
	COALESCE((SELECT json_agg(json_build_object('repo', r.repo, 'git_connection_id', r.git_connection_id)
	  ORDER BY r.position, r.created_at) FROM at_project_repos r WHERE r.project_id = p.id), '[]'::json),
	p.created_at, p.updated_at, p.pinned, p.archived, p.work_mode, p.auto_approve, p.require_acceptance,
	p.delivery, p.office_layout,
	(SELECT count(*) FROM at_questions q WHERE q.project_id = p.id AND q.status = 'open' AND q.audience = 'user')
	  + (SELECT count(*) FROM at_threads t WHERE t.project_id = p.id AND t.status = 'review'),
	(SELECT count(*) FROM at_threads t WHERE t.project_id = p.id AND t.status IN ('queued','running','waiting','review'))`

func scanProject(row interface{ Scan(...any) error }) (Project, error) {
	var p Project
	var repos []byte
	err := row.Scan(&p.ID, &p.UserID, &p.Name, &p.Description, &p.Context, &repos, &p.CreatedAt, &p.UpdatedAt, &p.Pinned, &p.Archived, &p.WorkMode,
		&p.AutoApprove, &p.RequireAcceptance, &p.Delivery, &p.OfficeLayout, &p.Waiting, &p.ThreadsActive)
	if err == nil {
		err = json.Unmarshal(repos, &p.Repos)
	}
	return p, mapErr(err)
}

// HasRepos reports whether the project works in at least one repository.
func (p Project) HasRepos() bool { return len(p.Repos) > 0 }

// ListProjects returns the user's projects, pinned first then most recent; q filters name/description.
func (s *Store) ListProjects(ctx context.Context, userID, q string) ([]Project, error) {
	rows, err := s.db.Query(ctx, `SELECT `+projectCols+` FROM at_projects p WHERE p.user_id = $1
		AND ($2 = '' OR p.name ILIKE '%' || $2 || '%' OR p.description ILIKE '%' || $2 || '%')
		ORDER BY p.pinned DESC, p.updated_at DESC`, userID, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Project{}
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ProjectOf loads one of the user's projects.
func (s *Store) ProjectOf(ctx context.Context, userID, id string) (Project, error) {
	return scanProject(s.db.QueryRow(ctx, `SELECT `+projectCols+` FROM at_projects p WHERE p.id = $1 AND p.user_id = $2`, id, userID))
}

// CreateProject inserts a project; ErrConflict when the name is taken.
func (s *Store) CreateProject(ctx context.Context, userID string, n NewProject) (Project, error) {
	delivery := n.Delivery
	if len(delivery) == 0 {
		delivery = json.RawMessage(`{}`)
	}
	id := NewHexID()
	err := s.Tx(ctx, func(tx *Store) error {
		_, err := tx.db.Exec(ctx, `INSERT INTO at_projects (id, user_id, name, description, context,
			work_mode, auto_approve, require_acceptance, delivery) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			id, userID, n.Name, n.Description, n.Context, n.WorkMode, n.AutoApprove, n.RequireAcceptance, delivery)
		if err != nil {
			return err
		}
		return tx.writeRepos(ctx, id, n.Repos)
	})
	if err != nil {
		return Project{}, mapErr(err)
	}
	return s.ProjectOf(ctx, userID, id)
}

// SetProjectRepos replaces a project's repositories (order = position).
func (s *Store) SetProjectRepos(ctx context.Context, userID, id string, repos []ProjectRepo) (Project, error) {
	err := s.Tx(ctx, func(tx *Store) error {
		if err := affected(tx.db.Exec(ctx, `UPDATE at_projects SET updated_at = now() WHERE id = $1 AND user_id = $2`, id, userID)); err != nil {
			return err
		}
		if _, err := tx.db.Exec(ctx, `DELETE FROM at_project_repos WHERE project_id = $1`, id); err != nil {
			return err
		}
		return tx.writeRepos(ctx, id, repos)
	})
	if err != nil {
		return Project{}, mapErr(err)
	}
	return s.ProjectOf(ctx, userID, id)
}

func (s *Store) writeRepos(ctx context.Context, projectID string, repos []ProjectRepo) error {
	for i, r := range repos {
		if _, err := s.db.Exec(ctx, `INSERT INTO at_project_repos (project_id, repo, git_connection_id, position)
			VALUES ($1, $2, $3, $4)`, projectID, r.Repo, r.GitConnectionID, i); err != nil {
			return err
		}
	}
	return nil
}

// UpdateProject applies changes and bumps updated_at.
func (s *Store) UpdateProject(ctx context.Context, userID, id string, changes []Change) (Project, error) {
	set, args := setClause(changes, 3)
	if set != "" {
		set += ", "
	}
	err := affected(s.db.Exec(ctx, `UPDATE at_projects SET `+set+`updated_at = now() WHERE id = $1 AND user_id = $2`,
		append([]any{id, userID}, args...)...))
	if err != nil {
		return Project{}, err
	}
	return s.ProjectOf(ctx, userID, id)
}

// DeleteProject removes a project and (by cascade) its conversations, threads and memory.
func (s *Store) DeleteProject(ctx context.Context, userID, id string) error {
	return affected(s.db.Exec(ctx, `DELETE FROM at_projects WHERE id = $1 AND user_id = $2`, id, userID))
}

// ProjectIDsUsingLayout lists projects whose office uses layoutID ("" matches the global default).
func (s *Store) ProjectIDsUsingLayout(ctx context.Context, layoutID string, isDefault bool) ([]string, error) {
	rows, err := s.db.Query(ctx, `SELECT id FROM at_projects WHERE office_layout = $1 OR ($2 AND office_layout = '')`,
		layoutID, isDefault)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
