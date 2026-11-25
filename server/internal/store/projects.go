package store

import (
	"github.com/touwaeriol/agent-teams/server/internal/blobstore"

	"context"
	"encoding/json"
	"errors"
	"time"

	"gorm.io/gorm"
)

// Project is a row of at_projects plus the sidebar counters.
type Project struct {
	ID               string
	UserID           string
	Name             string
	Description      string
	Context          string
	Repos            []ProjectRepo `gorm:"->;serializer:jsonb"`
	CreatedAt        time.Time
	UpdatedAt        time.Time
	Pinned           bool
	Archived         bool
	WorkMode         string
	AutoApprove      bool
	MergePolicy      string // pr | auto | manual (architecture.md §8.4)
	Delivery         json.RawMessage
	OfficeLayout     string
	Icon             string
	CoordinatorModel string
	ThreadModel      string
	ThreadCodexModel string
	ThreadEffort     string
	AutoResume       bool
	// Kind is project (default) or session (多仓库会话: one thread, its employee talks to the user
	// directly; see migrations/00420_project_kind.sql).
	Kind          string
	Waiting       int `gorm:"->"`
	ThreadsActive int `gorm:"->"`
	// SessionStatus is a session's state for the sidebar ('' for a project): see sessionStatusCol.
	SessionStatus string `gorm:"->"`
}

// TableName maps Project to at_projects.
func (Project) TableName() string { return "at_projects" }

// NewProject is what POST /api/projects stores.
type NewProject struct {
	Name, Description, Context string
	Repos                      []ProjectRepo
	WorkMode                   string
	AutoApprove                bool
	MergePolicy                string // '' = pr
	Delivery                   json.RawMessage
	// Model overrides ('' = each role's model): the coordinator, Claude leads and members, Codex
	// leads and members (orchestrator.Model).
	CoordinatorModel, ThreadModel, ThreadCodexModel string
	// Kind: '' = project; session for a 多仓库会话.
	Kind string
	// Attachments are ids of the user's attachments uploaded without a project (POST
	// /api/uploads): they join the new project in its transaction (AdoptAttachments).
	Attachments []string
	// AttachmentTTL is their new retention (a use); 0 = 24 hours.
	AttachmentTTL time.Duration
}

// mergePolicy is the policy a new project stores: pr unless one was chosen.
func mergePolicy(p string) string {
	if p == "" {
		return "pr"
	}
	return p
}

// Project kinds (at_projects.kind).
const (
	KindProject = "project"
	KindSession = "session"
)

// Session reports whether the project is a 多仓库会话.
func (p Project) Session() bool { return p.Kind == KindSession }

func projectKind(k string) string {
	if k == "" {
		return KindProject
	}
	return k
}

// ProjectRepo is one repository bound to a project (at_project_repos), in project order.
type ProjectRepo struct {
	Repo            string  `json:"repo"`
	GitConnectionID *string `json:"git_connection_id"`
	// GitRepoID names a standalone repository (at_git_repos, git-repos.md §3.4); then Repo is its
	// slug and GitConnectionID is nil.
	GitRepoID *string `json:"git_repo_id"`
}

// Standalone reports whether the repository is a standalone Git repository (no GitHub API).
func (r ProjectRepo) Standalone() bool { return r.GitRepoID != nil && *r.GitRepoID != "" }

const projectCols = `p.id, p.user_id, p.name, p.description, p.context,
	COALESCE((SELECT json_agg(json_build_object('repo', r.repo, 'git_connection_id', r.git_connection_id, 'git_repo_id', r.git_repo_id)
	  ORDER BY r.position, r.created_at) FROM at_project_repos r WHERE r.project_id = p.id), '[]'::json) AS repos,
	p.created_at, p.updated_at, p.pinned, p.archived, p.work_mode, p.auto_approve, p.merge_policy,
	p.delivery, p.office_layout, p.icon, p.coordinator_model, p.thread_model, p.thread_codex_model, p.thread_effort, p.auto_resume, p.kind,
	(SELECT count(*) FROM at_questions q WHERE q.project_id = p.id AND q.status = 'open' AND q.audience = 'user')
	  + (SELECT count(*) FROM at_threads t WHERE t.project_id = p.id AND t.status = 'review'
	    AND (p.merge_policy = 'manual' OR t.merge_error IS NOT NULL)) AS waiting,
	(SELECT count(*) FROM at_threads t WHERE t.project_id = p.id
	  AND t.status IN ('queued','running','waiting','review')) AS threads_active,
	` + sessionStatusCol

// sessionStatusCol is a session's (kind session) state for the sidebar, from its thread and its
// employee's (the lead's) state in at_employee_status: the same rule as the page (web phase.ts
// threadPhase with solo, core.PhaseOf). Open cards are counted in waiting (the sidebar shows the
// count). A running session is
//   - attention (等你处理) when the employee's node is offline, its session crashed, or no node can
//     run it (queued, no_runtime): nothing moves until the user looks;
//   - working (进行中) while the employee works (a turn runs, it is waking for a message, it uses a
//     tool…) or waits on others;
//   - queued (排队中) while its session waits for a free slot on a node;
//   - finished (已完成) otherwise: not in a turn and nothing for the user to answer, whether it
//     replied, the user stopped it or the round ended without a reply.
//
// A done thread is resolved (已解决, the user confirmed); other statuses are the thread's
// (queued, waiting, review, paused, failed). Empty for a project.
const sessionStatusCol = `CASE WHEN p.kind <> 'session' THEN '' ELSE COALESCE((SELECT CASE
	  WHEN t.status = 'done' THEN 'resolved'
	  WHEN t.status <> 'running' THEN t.status
	  WHEN st.state IN ('offline', 'broken') OR (st.state = 'queued' AND st.substate = 'no_runtime') THEN 'attention'
	  WHEN st.state IN ('typing', 'reading', 'running', 'thinking', 'claiming', 'walking_to_board', 'returning',
	    'waiting', 'raising_hand') THEN 'working'
	  WHEN EXISTS (SELECT 1 FROM at_agent_sessions s WHERE s.employee_id = e.id AND s.state <> 'closed' AND s.in_turn) THEN 'working'
	  WHEN st.state = 'queued' THEN 'queued'
	  ELSE 'finished' END
	FROM at_threads t
	LEFT JOIN at_employees e ON e.thread_id = t.id AND e.tier = 'lead'
	LEFT JOIN at_employee_status st ON st.employee_id = e.id
	WHERE t.project_id = p.id ORDER BY t.no LIMIT 1), '') END AS session_status`

func (s *Store) projects(ctx context.Context, userID string) *gorm.DB {
	return s.g(ctx).Table("at_projects p").Select(projectCols).Where("p.user_id = ?", userID)
}

// HasRepos reports whether the project works in at least one repository.
func (p Project) HasRepos() bool { return len(p.Repos) > 0 }

// ListProjects returns the user's projects, pinned first then most recent; q filters name/description.
func (s *Store) ListProjects(ctx context.Context, userID, q string) ([]Project, error) {
	out := []Project{}
	g := s.projects(ctx, userID)
	if q != "" {
		g = g.Where("p.name ILIKE '%' || ? || '%' OR p.description ILIKE '%' || ? || '%'", q, q)
	}
	err := g.Order("p.pinned DESC, p.updated_at DESC").Find(&out).Error
	return out, err
}

// ProjectOf loads one of the user's projects.
func (s *Store) ProjectOf(ctx context.Context, userID, id string) (Project, error) {
	var p Project
	err := s.projects(ctx, userID).Where("p.id = ?", id).Take(&p).Error
	return p, mapErr(err)
}

// CreateProject inserts a project; ErrConflict when the name is taken.
func (s *Store) CreateProject(ctx context.Context, userID string, n NewProject) (Project, error) {
	delivery := n.Delivery
	if len(delivery) == 0 {
		delivery = json.RawMessage(`{}`)
	}
	id := NewHexID()
	err := s.Tx(ctx, func(tx *Store) error {
		err := tx.g(ctx).Table("at_projects").Create(map[string]any{"id": id, "user_id": userID, "name": n.Name,
			"description": n.Description, "context": n.Context, "work_mode": n.WorkMode, "auto_approve": n.AutoApprove,
			"merge_policy": mergePolicy(n.MergePolicy), "delivery": delivery,
			"coordinator_model": n.CoordinatorModel, "thread_model": n.ThreadModel, "thread_codex_model": n.ThreadCodexModel,
			"kind": projectKind(n.Kind)}).Error
		if err != nil {
			return err
		}
		if err := tx.writeRepos(ctx, id, n.Repos); err != nil {
			return err
		}
		return tx.adoptAttachments(ctx, userID, id, n.Attachments, n.AttachmentTTL)
	})
	if err != nil {
		if errors.Is(err, ErrAttachmentExpired) || errors.Is(err, ErrAttachmentUnknown) {
			return Project{}, err
		}
		return Project{}, mapErr(err)
	}
	return s.ProjectOf(ctx, userID, id)
}

var (
	// ErrAttachmentExpired is an attachment given to a new project whose retention ran out.
	ErrAttachmentExpired = errors.New("附件已过期（超过保留期没有使用，文件已删除），请重新上传")
	// ErrAttachmentUnknown is an id given to a new project that is not one of the user's
	// attachments uploaded without a project, finished (ready) and not yet in a project.
	ErrAttachmentUnknown = errors.New("附件不存在、还没传完或已经用过了")
)

// adoptAttachments moves the user's attachments uploaded without a project into project id
// (their keys stay under users/<uid>/) and restarts their retention (a use). Every id must be
// ready, unexpired and still without a project.
func (s *Store) adoptAttachments(ctx context.Context, userID, id string, ids []string, ttl time.Duration) error {
	if len(ids) == 0 {
		return nil
	}
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	var moved []string
	if err := s.g(ctx).Raw(`UPDATE at_attachments SET project_id = ?,
		expires_at = GREATEST(expires_at, now() + make_interval(secs => ?))
		WHERE id IN ? AND user_id = ? AND project_id IS NULL AND state = 'ready' AND expires_at > now()
		RETURNING id`, id, ttl.Seconds(), ids, userID).Scan(&moved).Error; err != nil {
		return err
	}
	if len(moved) == len(dedupe(ids)) {
		return nil
	}
	var expired int64
	if err := s.g(ctx).Table("at_attachments").Where("id IN ? AND user_id = ? AND project_id IS NULL", ids, userID).
		Where("state = 'expired' OR (state = 'ready' AND expires_at <= now())").Count(&expired).Error; err != nil {
		return err
	}
	if expired > 0 {
		return ErrAttachmentExpired
	}
	return ErrAttachmentUnknown
}

func dedupe(ids []string) []string {
	seen := make(map[string]bool, len(ids))
	out := ids[:0:0]
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// SetProjectRepos replaces a project's repositories (order = position).
func (s *Store) SetProjectRepos(ctx context.Context, userID, id string, repos []ProjectRepo) (Project, error) {
	err := s.Tx(ctx, func(tx *Store) error {
		res := tx.g(ctx).Table("at_projects").Where("id = ? AND user_id = ?", id, userID).Update("updated_at", gorm.Expr("now()"))
		if err := Changed(res); err != nil {
			return err
		}
		if err := tx.g(ctx).Where("project_id = ?", id).Delete(&projectRepoRow{}).Error; err != nil {
			return err
		}
		return tx.writeRepos(ctx, id, repos)
	})
	if err != nil {
		return Project{}, mapErr(err)
	}
	return s.ProjectOf(ctx, userID, id)
}

// projectRepoRow is a row of at_project_repos.
type projectRepoRow struct {
	ProjectID       string
	Repo            string
	GitConnectionID *string
	GitRepoID       *string
	Position        int
}

func (projectRepoRow) TableName() string { return "at_project_repos" }

func (s *Store) writeRepos(ctx context.Context, projectID string, repos []ProjectRepo) error {
	if len(repos) == 0 {
		return nil
	}
	rows := make([]projectRepoRow, len(repos))
	for i, r := range repos {
		rows[i] = projectRepoRow{ProjectID: projectID, Repo: r.Repo, GitConnectionID: r.GitConnectionID, GitRepoID: r.GitRepoID, Position: i}
	}
	return s.g(ctx).Create(&rows).Error
}

// UpdateProject applies changes and bumps updated_at.
func (s *Store) UpdateProject(ctx context.Context, userID, id string, changes []Change) (Project, error) {
	set := changeMap(changes)
	set["updated_at"] = gorm.Expr("now()")
	if err := Changed(s.g(ctx).Table("at_projects").Where("id = ? AND user_id = ?", id, userID).Updates(set)); err != nil {
		return Project{}, err
	}
	return s.ProjectOf(ctx, userID, id)
}

// DeleteProject removes a project and (by cascade) its conversations, threads and memory; its
// objects in the object store (attachments, shared files) are queued for deletion in the same
// transaction (#58, blobgc).
func (s *Store) DeleteProject(ctx context.Context, userID, id string) error {
	return s.Tx(ctx, func(tx *Store) error {
		// Queued first: the attachments under users/ (uploaded before the project existed) are
		// found through their rows, which the cascade removes.
		if err := queueProjectObjects(ctx, tx, []string{id}); err != nil {
			return err
		}
		return Changed(tx.g(ctx).Where("id = ? AND user_id = ?", id, userID).Delete(&Project{}))
	})
}

// queueProjectObjects queues the object-store prefix of each project for deletion (the
// at_blob_deletions row blobgc.EnqueuePrefix writes; store cannot import blobgc), and the
// objects of its attachments kept outside it (users/<uid>/attachments/…, uploaded before the
// project existed). Call it before the rows go.
func queueProjectObjects(ctx context.Context, tx *Store, ids []string) error {
	for _, id := range ids {
		if _, err := Exec(ctx, tx.db, `INSERT INTO at_blob_deletions (key, prefix) VALUES ($1, true)
			ON CONFLICT (key) DO UPDATE SET prefix = true`, blobstore.ProjectPrefix(id)); err != nil {
			return err
		}
		if _, err := Exec(ctx, tx.db, `INSERT INTO at_blob_deletions (key) SELECT storage_key FROM at_attachments
			WHERE project_id = $1 AND storage_key LIKE 'users/%' ON CONFLICT (key) DO NOTHING`, id); err != nil {
			return err
		}
	}
	return nil
}

// ProjectIDsUsingLayout lists projects whose office uses layoutID ("" matches the global default).
func (s *Store) ProjectIDsUsingLayout(ctx context.Context, layoutID string, isDefault bool) ([]string, error) {
	out := []string{}
	err := s.g(ctx).Model(&Project{}).Where("office_layout = ? OR (? AND office_layout = '')", layoutID, isDefault).
		Pluck("id", &out).Error
	return out, err
}
