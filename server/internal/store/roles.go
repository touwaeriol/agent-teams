package store

import (
	"context"
	"slices"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/touwaeriol/agent-teams/server/internal/runtimes"
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
	ID              string            `json:"id"`
	Tier            string            `json:"tier" enum:"coordinator,lead,member"`
	Role            string            `json:"role"`
	Title           string            `json:"title"`
	Runtime         string            `json:"runtime" enum:"claude,codex,antigravity" doc:"The role's first runtime (runtimes[0])"`
	Runtimes        []string          `json:"runtimes,omitempty" enum:"claude,codex,antigravity" gorm:"serializer:pgtext" doc:"Every runtime the role's employees may run on, in display order; each new session takes the (runtime, node) pair with the lowest load score (used quota plus live sessions), so sessions spread over them; a runtime whose quota is tight on a node is only used when every pair is. Empty: [runtime]"`
	Models          map[string]string `json:"models,omitempty" nullable:"true" gorm:"serializer:jsonb" doc:"The model of each further runtime (runtimes[1:]), by runtime id; model is the first runtime's"`
	Count           int               `json:"count"`
	NodeIDs         []string          `json:"node_ids" gorm:"column:node_ids;serializer:pgtext"`
	Description     string            `json:"description"`
	Instructions    string            `json:"instructions"`
	Model           string            `json:"model" doc:"The model of the role's sessions, typed by hand (any tier; '' = the project setting, then the built-in default). Below the project setting and, for members, /model"`
	Effort          string            `json:"effort"`
	PermissionMode  string            `json:"permission_mode,omitempty" enum:"bypass,ask" doc:"bypass (default): members run without permission prompts on any node; ask: every non-read tool asks (lead or user, per the project's approval routing). Ignored for the coordinator, the lead and read-only roles"`
	CanDelegate     bool              `json:"can_delegate"`
	ReadOnly        *bool             `json:"read_only,omitempty" nullable:"true" doc:"Omitted: defaults to true for gate review/decision"`
	AllowedTools    []string          `json:"allowed_tools" gorm:"serializer:pgtext"`
	DisallowedTools []string          `json:"disallowed_tools" gorm:"serializer:pgtext"`
	Sandbox         string            `json:"sandbox"`
	MaxTurns        *int              `json:"max_turns" nullable:"true"`
	Gate            string            `json:"gate"`
	TaskKinds       []string          `json:"task_kinds" gorm:"serializer:pgtext"`
	Look            Look              `json:"look" gorm:"serializer:jsonb"`
	Skills          []string          `json:"skills" gorm:"serializer:pgtext" doc:"Skill names (built-in or uploaded) the role's sessions load"`
	Plugins         []string          `json:"plugins" gorm:"serializer:pgtext" doc:"Plugins the role's sessions load: name (latest version) or name@N (pinned)"`
	HiddenCommands  []string          `json:"hidden_commands,omitempty" gorm:"serializer:pgtext" doc:"Platform commands (model, status…) the role's chats do not offer"`
	Commands        []CustomCommand   `json:"commands,omitempty" gorm:"serializer:jsonb" doc:"The role's own slash commands"`
	Sort            int               `json:"sort" doc:"Display order; PUT uses the position in the list"`
	// Session management (shared-members.md §5.1): percentages are of ContextLimit, not of the model's window.
	ContextLimit      *int   `json:"context_limit" nullable:"true" doc:"The role's context limit in tokens (e.g. 600000); the percentages below are of it. Null: the model's window"`
	CompactBetweenPct *int   `json:"compact_between_pct" nullable:"true" doc:"Members: claiming the next task with context ≥ limit × pct compacts the session first. Null: never"`
	CompactInTaskPct  *int   `json:"compact_in_task_pct" nullable:"true" doc:"The runtime compacts by itself at limit × pct during a turn. Null: the runtime's default"`
	ClearAfterTasks   *int   `json:"clear_after_tasks" nullable:"true" doc:"Members: a session that did this many tasks in a row is closed and the next task starts a fresh one (0: never). Null: not applicable"`
	CompactFocus      string `json:"compact_focus" doc:"What a compaction must keep, added to its instructions"`
	// Solo marks the employee of a 多仓库会话 (kind session): a thread's lead that writes code and
	// talks to the user directly (no members, no task pool). Only in employee specs and thread
	// role snapshots, never in at_roles nor the API.
	Solo bool `json:"solo,omitempty" gorm:"-" hidden:"true"`
}

// Custom command modes: prompt expands the template on the server before delivery; passthrough
// sends "/name args" to the session unchanged (Claude Code runs it; not for Codex).
const (
	CommandPrompt      = "prompt"
	CommandPassthrough = "passthrough"
)

// CustomCommand is a slash command of a role (at_roles.commands) or a platform built-in
// (at_settings.composer_commands).
type CustomCommand struct {
	Name        string `json:"name" maxLength:"48" doc:"Typed after the slash"`
	Description string `json:"description,omitempty" maxLength:"200"`
	Mode        string `json:"mode,omitempty" enum:"prompt,passthrough" doc:"Default prompt"`
	Prompt      string `json:"prompt,omitempty" maxLength:"8000" doc:"mode prompt: the instruction sent instead; {{args}} is what follows the name"`
}

// TableName maps Role to at_roles.
func (Role) TableName() string { return "at_roles" }

// Roles returns the user's role templates (project_id IS NULL) in display order.
func (s *Store) Roles(ctx context.Context, userID string) ([]Role, error) {
	out := []Role{}
	err := s.g(ctx).Where("user_id = ? AND project_id IS NULL", userID).
		Order("CASE tier WHEN 'coordinator' THEN 0 WHEN 'lead' THEN 1 ELSE 2 END, sort, created_at").Find(&out).Error
	for i := range out {
		if out[i].ReadOnly == nil {
			out[i].ReadOnly = new(bool)
		}
	}
	return out, err
}

// ReplaceRoles saves the whole set: rows matched by id are updated (keeping
// at_employees.role_id links), missing ones deleted, new ones inserted. Call in a Tx.
func (s *Store) ReplaceRoles(ctx context.Context, userID string, roles []Role) error {
	keep := TextArray{}
	for _, r := range roles {
		if r.ID != "" {
			keep = append(keep, r.ID)
		}
	}
	templates := func() *gorm.DB { return s.g(ctx).Table("at_roles").Where("user_id = ? AND project_id IS NULL", userID) }
	if err := s.g(ctx).Where("user_id = ? AND project_id IS NULL AND NOT (id = ANY(?))", userID, keep).Delete(&Role{}).Error; err != nil {
		return err
	}
	// Park kept rows on unique placeholder keys so renames and tier swaps cannot collide mid-way.
	park := map[string]any{"role": gorm.Expr("'~' || id"), "tier": "member", "runtime": runtimes.Claude, "read_only": false}
	if err := templates().Where("id = ANY(?)", keep).Updates(park).Error; err != nil {
		return err
	}
	for i := range roles {
		if err := s.saveRole(ctx, userID, &roles[i]); err != nil {
			return err
		}
	}
	return nil
}

func roleColumns(r *Role) map[string]any {
	return map[string]any{"tier": r.Tier, "role": r.Role, "title": r.Title, "runtime": r.Runtime, "runtimes": TextArray(r.Runtimes), "models": modelsJSON(r.Models), "count": r.Count,
		"node_ids": TextArray(r.NodeIDs), "description": r.Description, "instructions": r.Instructions,
		"model": r.Model, "effort": r.Effort, "permission_mode": r.PermissionMode, "can_delegate": r.CanDelegate,
		"read_only": r.ReadOnly != nil && *r.ReadOnly, "allowed_tools": TextArray(r.AllowedTools),
		"disallowed_tools": TextArray(r.DisallowedTools), "sandbox": r.Sandbox, "max_turns": r.MaxTurns, "gate": r.Gate,
		"task_kinds": TextArray(r.TaskKinds), "look": r.Look, "sort": r.Sort, "skills": TextArray(r.Skills),
		"plugins": TextArray(r.Plugins), "hidden_commands": TextArray(r.HiddenCommands), "commands": commandsJSON(r.Commands),
		"context_limit": r.ContextLimit, "compact_between_pct": r.CompactBetweenPct, "compact_in_task_pct": r.CompactInTaskPct,
		"clear_after_tasks": r.ClearAfterTasks, "compact_focus": r.CompactFocus}
}

func modelsJSON(m map[string]string) any {
	if m == nil {
		m = map[string]string{}
	}
	return Param(m)
}

// RuntimeList is the role's runtimes: Runtimes, or just Runtime for a role saved before roles had several.
func (r Role) RuntimeList() []string {
	if len(r.Runtimes) > 0 {
		return r.Runtimes
	}
	if r.Runtime == "" {
		return []string{runtimes.Default().ID}
	}
	return []string{r.Runtime}
}

// ModelFor is the role's model for one of its runtimes: Models[rt] for a further runtime, else
// Model (the first runtime's, and the only one of a snapshot taken before roles had several).
func (r Role) ModelFor(rt string) string {
	if list := r.RuntimeList(); rt != list[0] && slices.Contains(list, rt) {
		return r.Models[rt]
	}
	return r.Model
}

func commandsJSON(list []CustomCommand) any {
	if list == nil {
		list = []CustomCommand{}
	}
	return Param(list)
}

func (s *Store) saveRole(ctx context.Context, userID string, r *Role) error {
	cols := roleColumns(r)
	if r.ID != "" {
		cols["updated_at"] = gorm.Expr("now()")
		return Changed(s.g(ctx).Table("at_roles").Where("id = ? AND user_id = ? AND project_id IS NULL", r.ID, userID).
			Updates(cols))
	}
	r.ID = NewID("ro")
	cols["id"], cols["user_id"] = r.ID, userID
	return mapErr(s.g(ctx).Table("at_roles").Create(cols).Error)
}

// OfficeLayoutRow is a database layout (at_office_layouts).
type OfficeLayoutRow struct {
	ID        string
	Version   int `gorm:"default:1"`
	Doc       []byte
	UpdatedBy string
	UpdatedAt time.Time `gorm:"default:now()"`
}

// TableName maps OfficeLayoutRow to at_office_layouts.
func (OfficeLayoutRow) TableName() string { return "at_office_layouts" }

// OfficeLayouts returns every global database layout.
func (s *Store) OfficeLayouts(ctx context.Context) ([]OfficeLayoutRow, error) {
	out := []OfficeLayoutRow{}
	err := s.g(ctx).Where("owner_id IS NULL").Order("id").Find(&out).Error
	return out, err
}

// SaveOfficeLayout upserts a global database layout and bumps its version.
func (s *Store) SaveOfficeLayout(ctx context.Context, id string, doc []byte, by string) (OfficeLayoutRow, error) {
	r := OfficeLayoutRow{ID: id, Doc: doc, UpdatedBy: by}
	err := s.g(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "id"}},
		DoUpdates: clause.Set{
			{Column: clause.Column{Name: "doc"}, Value: gorm.Expr("EXCLUDED.doc")},
			{Column: clause.Column{Name: "updated_by"}, Value: gorm.Expr("EXCLUDED.updated_by")},
			{Column: clause.Column{Name: "version"}, Value: gorm.Expr("at_office_layouts.version + 1")},
			{Column: clause.Column{Name: "updated_at"}, Value: gorm.Expr("now()")},
		},
	}, clause.Returning{}).Create(&r).Error
	return r, mapErr(err)
}

// DeleteOfficeLayout removes a database layout.
func (s *Store) DeleteOfficeLayout(ctx context.Context, id string) error {
	return Changed(s.g(ctx).Where("id = ? AND owner_id IS NULL", id).Delete(&OfficeLayoutRow{}))
}
