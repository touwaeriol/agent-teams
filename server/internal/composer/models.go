package composer

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// Model is one entry of a model list (at_models): the platform's (Mine false) or the user's own.
type Model struct {
	ID      string `json:"id" readOnly:"true"`
	Runtime string `json:"runtime" enum:"claude,codex"`
	Model   string `json:"model" maxLength:"100" doc:"The id the runtime is given (claude-…, gpt-…)"`
	Label   string `json:"label,omitempty" maxLength:"40" doc:"Display name; empty shows the id"`
	Default bool   `json:"default,omitempty" doc:"Platform list only: what sessions of this runtime open with when nothing else names a model (one per runtime)"`
	Mine    bool   `json:"mine" readOnly:"true" doc:"The user's own entry"`
}

// MaxModels caps one owner's list.
const MaxModels = 50

var modelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@\[\]-]{0,99}$`)

// ValidModel reports whether id can be given to a runtime as a model.
func ValidModel(id string) bool { return modelPattern.MatchString(id) }

// NormalizeModels trims ids and labels; user lists never carry a default.
func NormalizeModels(list []Model, platform bool) []Model {
	out := make([]Model, len(list))
	for i, m := range list {
		m.Model, m.Label = strings.TrimSpace(m.Model), strings.TrimSpace(m.Label)
		if m.Runtime == "" {
			m.Runtime = "claude"
		}
		m.Default = platform && m.Default
		out[i] = m
	}
	return out
}

// ValidateModels checks a normalized list of one owner.
func ValidateModels(list []Model) []Problem {
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

// ListModels is the platform list (in order) followed by the user's own entries.
func ListModels(ctx context.Context, db store.DB, userID string) ([]Model, error) {
	rows, err := store.Query(ctx, db, `SELECT id, runtime, model, label, is_default, user_id IS NOT NULL FROM at_models
		WHERE user_id IS NULL OR user_id = $1 ORDER BY user_id IS NOT NULL, sort, created_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Model{}
	for rows.Next() {
		var m Model
		if err := rows.Scan(&m.ID, &m.Runtime, &m.Model, &m.Label, &m.Default, &m.Mine); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ReplaceModels saves an owner's whole list in order (userID "" = the platform list).
func ReplaceModels(ctx context.Context, db store.DB, userID string, list []Model) error {
	return store.InTx(ctx, db, func(tx store.DB) error {
		owner := any(nil)
		if userID != "" {
			owner = userID
		}
		if _, err := store.Exec(ctx, tx, `DELETE FROM at_models WHERE COALESCE(user_id, '') = $1`, userID); err != nil {
			return err
		}
		for i, m := range list {
			row := map[string]any{"id": store.NewID("mdl"), "user_id": owner, "runtime": m.Runtime, "model": m.Model,
				"label": m.Label, "sort": i, "is_default": m.Default}
			if err := tx.Gorm(ctx).Table("at_models").Create(row).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// DefaultModel is the platform default of a runtime ("" when the list has none).
func DefaultModel(ctx context.Context, db store.DB, runtime string) string {
	var id string
	if err := store.QueryRow(ctx, db, `SELECT model FROM at_models WHERE user_id IS NULL AND runtime = $1 AND is_default`, runtime).Scan(&id); err != nil {
		return ""
	}
	return id
}
