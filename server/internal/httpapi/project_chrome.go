package httpapi

import (
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
			errs = append(errs, FieldError{Path: "icon", Message: "图标只能是一个表情，或 color:#RRGGBB"})
		}
		ch = append(ch, store.Change{Col: "icon", Val: icon})
	}
	if b.ThreadEffort != nil {
		if !efforts[*b.ThreadEffort] {
			errs = append(errs, FieldError{Path: "thread_effort", Message: "思考强度只能是 low、medium、high、xhigh 或 max"})
		}
		ch = append(ch, store.Change{Col: "thread_effort", Val: *b.ThreadEffort})
	}
	if len(errs) > 0 {
		return nil, invalid(errs, errs[0].Message)
	}
	return ch, nil
}

// projectModel is one model override of a project: field (= column) and the new value (nil =
// unchanged).
type projectModel struct {
	field string
	val   *string
}

// modelChanges validates and collects the model overrides of a project update: the
// coordinator's, the Claude threads' (leads and members) and the Codex threads'.
func modelChanges(b ProjectUpdate) ([]store.Change, error) {
	models := []projectModel{
		{"coordinator_model", trimmed(b.CoordinatorModel)},
		{"thread_model", trimmed(b.ThreadModel)},
		{"thread_codex_model", trimmed(b.ThreadCodexModel)},
	}
	if err := checkProjectModels(models); err != nil {
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

// checkProjectModels checks the format of the project's model overrides (composer.ValidModel):
// ” follows each role's model; models are typed by hand, there is no list to check them against.
func checkProjectModels(models []projectModel) error {
	var errs []FieldError
	for _, m := range models {
		if m.val == nil || *m.val == "" || composer.ValidModel(*m.val) {
			continue
		}
		errs = append(errs, FieldError{Path: m.field, Message: composer.ModelFormatProblem})
	}
	if len(errs) > 0 {
		return invalid(errs, errs[0].Message)
	}
	return nil
}
