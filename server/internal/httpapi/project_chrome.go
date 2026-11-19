package httpapi

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/touwaeriol/agent-teams/server/internal/composer"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

var (
	iconColor = regexp.MustCompile(`^color:#[0-9A-Fa-f]{6}$`)
	efforts   = map[string]bool{"": true, "low": true, "medium": true, "high": true, "xhigh": true, "max": true}
)

// ValidIcon accepts ” (initial), color:#RRGGBB, or a short emoji/symbol without letters, digits or spaces.
func ValidIcon(icon string) bool {
	if icon == "" || iconColor.MatchString(icon) {
		return true
	}
	if strings.HasPrefix(icon, "color:") || len(icon) > 32 {
		return false
	}
	for _, r := range icon {
		if r < 128 && !unicode.IsPunct(r) && !unicode.IsSymbol(r) {
			return false
		}
	}
	return true
}

// chromeChanges validates and collects the icon and effort of a project update (models: modelChanges).
func chromeChanges(b ProjectUpdate) ([]store.Change, error) {
	var ch []store.Change
	var errs []FieldError
	if b.Icon != nil {
		icon := strings.TrimSpace(*b.Icon)
		if !ValidIcon(icon) {
			errs = append(errs, FieldError{"icon", "图标只能是一个表情，或 color:#RRGGBB"})
		}
		ch = append(ch, store.Change{Col: "icon", Val: icon})
	}
	if b.ThreadEffort != nil {
		if !efforts[*b.ThreadEffort] {
			errs = append(errs, FieldError{"thread_effort", "思考强度只能是 low、medium、high、xhigh 或 max"})
		}
		ch = append(ch, store.Change{Col: "thread_effort", Val: *b.ThreadEffort})
	}
	if len(errs) > 0 {
		return nil, invalid(errs, errs[0].Message)
	}
	return ch, nil
}

// projectModel is one model override of a project: field (= column), the runtime it is for, the
// new value (nil = unchanged) and the stored one.
type projectModel struct {
	field, runtime string
	val            *string
	saved          string
}

// modelChanges validates and collects the member model overrides of a project update.
// coordinator_model is no longer a setting (the coordinator runs the default model): ignored.
func (s *Server) modelChanges(ctx context.Context, userID string, cur store.Project, b ProjectUpdate) ([]store.Change, error) {
	models := []projectModel{
		{"thread_model", "claude", trimmed(b.ThreadModel), cur.ThreadModel},
		{"thread_codex_model", "codex", trimmed(b.ThreadCodexModel), cur.ThreadCodexModel},
	}
	if err := s.checkProjectModels(ctx, userID, models); err != nil {
		return nil, err
	}
	var ch []store.Change
	for _, m := range models {
		if m.val != nil {
			ch = append(ch, store.Change{Col: m.field, Val: *m.val})
		}
	}
	return ch, nil
}

func trimmed(p *string) *string {
	if p == nil {
		return nil
	}
	v := strings.TrimSpace(*p)
	return &v
}

// checkProjectModels checks the project's model overrides against the owner's model list
// (composer.ListModels; the owner is the user editing the project): ” follows each role; any
// other id must be an entry of the field's runtime (thread_model: Claude, thread_codex_model:
// Codex). A value equal to the stored one passes even when it has since left
// the list, so deleting a model does not lock the settings page (as roles.modelProblems).
func (s *Server) checkProjectModels(ctx context.Context, userID string, models []projectModel) error {
	var list []composer.Model
	var errs []FieldError
	for _, m := range models {
		if m.val == nil || *m.val == "" || *m.val == m.saved {
			continue
		}
		if list == nil {
			var err error
			if list, err = composer.ListModels(ctx, s.store.Pool, userID); err != nil {
				return err
			}
		}
		if msg := modelProblem(list, m.runtime, *m.val); msg != "" {
			errs = append(errs, FieldError{m.field, msg})
		}
	}
	if len(errs) > 0 {
		return invalid(errs, errs[0].Message)
	}
	return nil
}

var runtimeLabel = map[string]string{"claude": "Claude", "codex": "Codex"}

// modelProblem is why id cannot be used for runtime (” = it can).
func modelProblem(list []composer.Model, runtime, id string) string {
	other := ""
	for _, m := range list {
		if m.Model != id {
			continue
		}
		if m.Runtime == runtime {
			return ""
		}
		other = m.Runtime
	}
	if other != "" {
		return fmt.Sprintf("这里只能选 %s 模型，%s 是 %s 模型", runtimeLabel[runtime], id, runtimeLabel[other])
	}
	return "模型不在你的模型列表里：" + id
}
