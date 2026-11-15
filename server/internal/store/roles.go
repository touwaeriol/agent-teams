package store

import (
	"context"
	"time"
)

// Look is an employee's appearance; outfit/accessory are keys of the layout's looks.
type Look struct {
	Color     string  `json:"color"`
	Outfit    string  `json:"outfit"`
	Accessory string  `json:"accessory"`
	Hair      *string `json:"hair" nullable:"true"`
}

// Permission modes of a role (at_roles.permission_mode).
// bypass (the default) runs without permission prompts on any node; ask asks for every
// non-read tool. "auto" existed before 00097 and is read as bypass.
const (
	PermBypass     = "bypass"
	PermAsk        = "ask"
	PermLegacyAuto = "auto"
)

// Role is a role template (at_roles, user level). JSON tags are the RoleView of api.md.
type Role struct {
	ID              string   `json:"id"`
	Tier            string   `json:"tier" enum:"coordinator,lead,member"`
	Role            string   `json:"role"`
	Title           string   `json:"title"`
	Runtime         string   `json:"runtime" enum:"claude,codex"`
	Count           int      `json:"count"`
	NodeIDs         []string `json:"node_ids"`
	Description     string   `json:"description"`
	Instructions    string   `json:"instructions"`
	Model           string   `json:"model"`
	Effort          string   `json:"effort"`
	PermissionMode  string   `json:"permission_mode,omitempty" enum:"bypass,ask" doc:"bypass (default): members run without permission prompts on any node; ask: every non-read tool asks (lead or user, per the project's approval routing). Ignored for the coordinator, the lead and read-only roles"`
	CanDelegate     bool     `json:"can_delegate"`
	ReadOnly        *bool    `json:"read_only,omitempty" nullable:"true" doc:"Omitted: defaults to true for gate review/decision"`
	AllowedTools    []string `json:"allowed_tools"`
	DisallowedTools []string `json:"disallowed_tools"`
	Sandbox         string   `json:"sandbox"`
	MaxTurns        *int     `json:"max_turns" nullable:"true"`
	Gate            string   `json:"gate"`
	TaskKinds       []string `json:"task_kinds"`
	Look            Look     `json:"look"`
	Skills          []string `json:"skills" doc:"Skill names (built-in or uploaded) the role's sessions load"`
	Plugins         []string `json:"plugins" doc:"Plugins the role's sessions load: name (latest version) or name@N (pinned)"`
	Sort            int      `json:"sort" doc:"Display order; PUT uses the position in the list"`
}

const roleCols = `id, tier, role, title, runtime, count, node_ids, description, instructions, model, effort,
	permission_mode, can_delegate, read_only, allowed_tools, disallowed_tools, sandbox, max_turns, gate,
	task_kinds, look, sort, skills, plugins`

func scanRole(row interface{ Scan(...any) error }) (Role, error) {
	var r Role
	var ro bool
	err := row.Scan(&r.ID, &r.Tier, &r.Role, &r.Title, &r.Runtime, &r.Count, &r.NodeIDs, &r.Description,
		&r.Instructions, &r.Model, &r.Effort, &r.PermissionMode, &r.CanDelegate, &ro, &r.AllowedTools,
		&r.DisallowedTools, &r.Sandbox, &r.MaxTurns, &r.Gate, &r.TaskKinds, &r.Look, &r.Sort, &r.Skills, &r.Plugins)
	r.ReadOnly = &ro
	return r, mapErr(err)
}

// Roles returns the user's role templates (project_id IS NULL) in display order.
func (s *Store) Roles(ctx context.Context, userID string) ([]Role, error) {
	rows, err := s.db.Query(ctx, `SELECT `+roleCols+` FROM at_roles WHERE user_id = $1 AND project_id IS NULL
		ORDER BY CASE tier WHEN 'coordinator' THEN 0 WHEN 'lead' THEN 1 ELSE 2 END, sort, created_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Role{}
	for rows.Next() {
		r, err := scanRole(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ReplaceRoles saves the whole set: rows matched by id are updated (keeping
// at_employees.role_id links), missing ones deleted, new ones inserted. Call in a Tx.
func (s *Store) ReplaceRoles(ctx context.Context, userID string, roles []Role) error {
	keep := make([]string, 0, len(roles))
	for _, r := range roles {
		if r.ID != "" {
			keep = append(keep, r.ID)
		}
	}
	if _, err := s.db.Exec(ctx, `DELETE FROM at_roles WHERE user_id = $1 AND project_id IS NULL AND NOT (id = ANY($2))`, userID, keep); err != nil {
		return err
	}
	// Park kept rows on unique placeholder keys so renames and tier swaps cannot collide mid-way.
	if _, err := s.db.Exec(ctx, `UPDATE at_roles SET role = '~' || id, tier = 'member', runtime = 'claude', read_only = FALSE
		WHERE user_id = $1 AND project_id IS NULL AND id = ANY($2)`, userID, keep); err != nil {
		return err
	}
	for i := range roles {
		if err := s.saveRole(ctx, userID, &roles[i]); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) saveRole(ctx context.Context, userID string, r *Role) error {
	ro := r.ReadOnly != nil && *r.ReadOnly
	args := []any{r.Tier, r.Role, r.Title, r.Runtime, r.Count, nonNil(r.NodeIDs), r.Description, r.Instructions,
		r.Model, r.Effort, r.PermissionMode, r.CanDelegate, ro, nonNil(r.AllowedTools), nonNil(r.DisallowedTools),
		r.Sandbox, r.MaxTurns, r.Gate, nonNil(r.TaskKinds), r.Look, r.Sort, nonNil(r.Skills), nonNil(r.Plugins)}
	if r.ID != "" {
		return affected(s.db.Exec(ctx, `UPDATE at_roles SET tier=$3, role=$4, title=$5, runtime=$6, count=$7, node_ids=$8,
			description=$9, instructions=$10, model=$11, effort=$12, permission_mode=$13, can_delegate=$14, read_only=$15,
			allowed_tools=$16, disallowed_tools=$17, sandbox=$18, max_turns=$19, gate=$20, task_kinds=$21, look=$22,
			sort=$23, skills=$24, plugins=$25, updated_at=now() WHERE id = $1 AND user_id = $2 AND project_id IS NULL`,
			append([]any{r.ID, userID}, args...)...))
	}
	r.ID = NewID("ro")
	_, err := s.db.Exec(ctx, `INSERT INTO at_roles (id, user_id, tier, role, title, runtime, count, node_ids, description,
		instructions, model, effort, permission_mode, can_delegate, read_only, allowed_tools, disallowed_tools, sandbox,
		max_turns, gate, task_kinds, look, sort, skills, plugins)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25)`,
		append([]any{r.ID, userID}, args...)...)
	return mapErr(err)
}

func nonNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// Team returns the user's v1 YAML team (at_teams), if any.
func (s *Store) Team(ctx context.Context, userID string) (string, bool, error) {
	var config string
	err := mapErr(s.db.QueryRow(ctx, `SELECT config FROM at_teams WHERE user_id = $1`, userID).Scan(&config))
	if err == ErrNotFound {
		return "", false, nil
	}
	return config, err == nil, err
}

// OfficeLayoutRow is a database layout (at_office_layouts).
type OfficeLayoutRow struct {
	ID        string
	Version   int
	Doc       []byte
	UpdatedBy string
	UpdatedAt time.Time
}

// OfficeLayouts returns every global database layout.
func (s *Store) OfficeLayouts(ctx context.Context) ([]OfficeLayoutRow, error) {
	rows, err := s.db.Query(ctx, `SELECT id, version, doc, updated_by, updated_at FROM at_office_layouts WHERE owner_id IS NULL ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OfficeLayoutRow{}
	for rows.Next() {
		var r OfficeLayoutRow
		if err := rows.Scan(&r.ID, &r.Version, &r.Doc, &r.UpdatedBy, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SaveOfficeLayout upserts a global database layout and bumps its version.
func (s *Store) SaveOfficeLayout(ctx context.Context, id string, doc []byte, by string) (OfficeLayoutRow, error) {
	var r OfficeLayoutRow
	err := s.db.QueryRow(ctx, `INSERT INTO at_office_layouts (id, doc, updated_by) VALUES ($1, $2, $3)
		ON CONFLICT (id) DO UPDATE SET doc = EXCLUDED.doc, updated_by = EXCLUDED.updated_by,
		version = at_office_layouts.version + 1, updated_at = now()
		RETURNING id, version, doc, updated_by, updated_at`, id, doc, by).
		Scan(&r.ID, &r.Version, &r.Doc, &r.UpdatedBy, &r.UpdatedAt)
	return r, mapErr(err)
}

// DeleteOfficeLayout removes a database layout.
func (s *Store) DeleteOfficeLayout(ctx context.Context, id string) error {
	return affected(s.db.Exec(ctx, `DELETE FROM at_office_layouts WHERE id = $1 AND owner_id IS NULL`, id))
}
