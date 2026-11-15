package httpapi

import (
	"regexp"
	"strings"
	"unicode"

	"github.com/touwaeriol/agent-teams/server/internal/store"
)

var (
	iconColor    = regexp.MustCompile(`^color:#[0-9A-Fa-f]{6}$`)
	modelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@\[\]-]{0,99}$`)
	efforts      = map[string]bool{"": true, "low": true, "medium": true, "high": true, "xhigh": true, "max": true}
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

// ValidModel accepts ” (follow the role) or a model id.
func ValidModel(m string) bool { return m == "" || modelPattern.MatchString(m) }

// chromeChanges validates and collects the icon and model overrides of a project update.
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
	for _, m := range []struct {
		field, col string
		val        *string
	}{{"coordinator_model", "coordinator_model", b.CoordinatorModel}, {"thread_model", "thread_model", b.ThreadModel}} {
		if m.val == nil {
			continue
		}
		v := strings.TrimSpace(*m.val)
		if !ValidModel(v) {
			errs = append(errs, FieldError{m.field, "模型名称不对"})
		}
		ch = append(ch, store.Change{Col: m.col, Val: v})
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
