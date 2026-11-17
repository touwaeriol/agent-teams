package composer

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// Model is one entry of the user's model list (at_models).
type Model struct {
	ID      string `json:"id" readOnly:"true"`
	Runtime string `json:"runtime" enum:"claude,codex"`
	Model   string `json:"model" maxLength:"100" doc:"The id the runtime is given (claude-…, gpt-…)"`
	Label   string `json:"label,omitempty" maxLength:"40" doc:"Display name; empty shows the id"`
	Default bool   `json:"default,omitempty" doc:"What the user's sessions of this runtime open with when no employee, project or role names a model (at most one per runtime)"`
	Mine    bool   `json:"mine" readOnly:"true" doc:"Always true: every entry is the user's own (kept for older clients)"`
}

// MaxModels caps one user's list.
const MaxModels = 50

// StarterModels is the list a user starts with: Opus 5.5, the Claude default.
var StarterModels = []Model{{Runtime: "claude", Model: "claude-opus-5-5", Label: "Opus 5.5", Default: true}}

var modelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@\[\]-]{0,99}$`)

// ValidModel reports whether id can be given to a runtime as a model.
func ValidModel(id string) bool { return modelPattern.MatchString(id) }

// NormalizeModels trims ids and labels; an empty runtime is Claude.
func NormalizeModels(list []Model) []Model {
	out := make([]Model, len(list))
	for i, m := range list {
		m.Model, m.Label = strings.TrimSpace(m.Model), strings.TrimSpace(m.Label)
		if m.Runtime == "" {
			m.Runtime = "claude"
		}
		out[i] = m
	}
	return out
}

// ValidateModels checks a normalized list of one user: at least one model, no duplicates, at
// most one default per runtime.
func ValidateModels(list []Model) []Problem {
	if len(list) == 0 {
		return []Problem{{"models", "至少保留一个模型"}}
	}
	if len(list) > MaxModels {
		return []Problem{{"models", fmt.Sprintf("最多 %d 个模型", MaxModels)}}
	}
	var out []Problem
	seen := map[string]bool{}
	defaults := map[string]bool{}
	for i, m := range list {
		p := fmt.Sprintf("models[%d]", i)
		key := m.Runtime + "/" + m.Model
		switch {
		case m.Runtime != "claude" && m.Runtime != "codex":
			out = append(out, Problem{p + ".runtime", "类型只能是 claude 或 codex"})
		case !ValidModel(m.Model):
			out = append(out, Problem{p + ".model", "模型 ID 只能用字母、数字和 . _ - : / @ [ ]，最多 100 个字符"})
		case seen[key]:
			out = append(out, Problem{p + ".model", "模型重复：" + m.Model})
		case len([]rune(m.Label)) > 40:
			out = append(out, Problem{p + ".label", "显示名最多 40 个字"})
		case m.Default && defaults[m.Runtime]:
			out = append(out, Problem{p + ".default", "每种类型只能有一个默认模型"})
		}
		seen[key] = true
		defaults[m.Runtime] = defaults[m.Runtime] || m.Default
	}
	return out
}

// ListModels is the user's list in order. A user without any entry (a new account) starts with
// StarterModels, saved before it is returned.
func ListModels(ctx context.Context, db store.DB, userID string) ([]Model, error) {
	out, err := listModels(ctx, db, userID)
	if err != nil || len(out) > 0 {
		return out, err
	}
	for i, m := range StarterModels {
		// ON CONFLICT: a concurrent first read seeds the same rows.
		if _, err := store.Exec(ctx, db, `INSERT INTO at_models (id, user_id, runtime, model, label, sort, is_default)
			VALUES ($1, $2, $3, $4, $5, $6, $7) ON CONFLICT DO NOTHING`,
			store.NewID("mdl"), userID, m.Runtime, m.Model, m.Label, i, m.Default); err != nil {
			return nil, err
		}
	}
	return listModels(ctx, db, userID)
}

func listModels(ctx context.Context, db store.DB, userID string) ([]Model, error) {
	rows, err := store.Query(ctx, db, `SELECT id, runtime, model, label, is_default FROM at_models
		WHERE user_id = $1 ORDER BY sort, created_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Model{}
	for rows.Next() {
		m := Model{Mine: true}
		if err := rows.Scan(&m.ID, &m.Runtime, &m.Model, &m.Label, &m.Default); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ReplaceModels saves the user's whole list in order.
func ReplaceModels(ctx context.Context, db store.DB, userID string, list []Model) error {
	return store.InTx(ctx, db, func(tx store.DB) error {
		if _, err := store.Exec(ctx, tx, `DELETE FROM at_models WHERE user_id = $1`, userID); err != nil {
			return err
		}
		for i, m := range list {
			row := map[string]any{"id": store.NewID("mdl"), "user_id": userID, "runtime": m.Runtime, "model": m.Model,
				"label": m.Label, "sort": i, "is_default": m.Default}
			if err := tx.Gorm(ctx).Table("at_models").Create(row).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// DefaultModel is the user's default of a runtime ("" when the user's list has none). Sessions
// pass the owner of their project: a session belongs to a project, a project to a user.
func DefaultModel(ctx context.Context, db store.DB, userID, runtime string) string {
	var id string
	if err := store.QueryRow(ctx, db, `SELECT model FROM at_models WHERE user_id = $1 AND runtime = $2 AND is_default`, userID, runtime).Scan(&id); err != nil {
		return ""
	}
	return id
}
