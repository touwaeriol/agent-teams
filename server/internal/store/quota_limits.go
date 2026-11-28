package store

import (
	"context"
	"slices"
)

// Default scheduling thresholds (at_users.quota_*_pct NULL): a node whose 5-hour window has used
// DefaultQuotaFiveHourPct percent, or any weekly window DefaultQuotaWeeklyPct, gets no new session
// while another candidate is under them.
const (
	DefaultQuotaFiveHourPct = 85
	DefaultQuotaWeeklyPct   = 95
)

// QuotaLimits are a user's scheduling thresholds in percent (1–100), defaults applied.
type QuotaLimits struct {
	FiveHourPct int `json:"five_hour_pct" minimum:"1" maximum:"100" doc:"A node whose 5-hour window (Codex: windows up to a day) has used this many percent gets no new session while another candidate node is under the thresholds"`
	WeeklyPct   int `json:"weekly_pct" minimum:"1" maximum:"100" doc:"The same for any weekly window (Claude weekly and per-model weekly; Codex: windows longer than a day)"`
}

// DefaultQuotaLimits are the thresholds of a user who never set them.
func DefaultQuotaLimits() QuotaLimits {
	return QuotaLimits{FiveHourPct: DefaultQuotaFiveHourPct, WeeklyPct: DefaultQuotaWeeklyPct}
}

// UserQuotaLimits loads the user's scheduling thresholds (defaults for unset ones).
func (s *Store) UserQuotaLimits(ctx context.Context, userID string) (QuotaLimits, error) {
	var row struct {
		QuotaFiveHourPct *int
		QuotaWeeklyPct   *int
	}
	out := DefaultQuotaLimits()
	err := s.g(ctx).Table("at_users").Select("quota_five_hour_pct, quota_weekly_pct").Where("id = ?", userID).Take(&row).Error
	if err != nil {
		return out, mapErr(err)
	}
	if row.QuotaFiveHourPct != nil {
		out.FiveHourPct = *row.QuotaFiveHourPct
	}
	if row.QuotaWeeklyPct != nil {
		out.WeeklyPct = *row.QuotaWeeklyPct
	}
	return out, nil
}

// SaveUserQuotaLimits stores the user's scheduling thresholds (callers validate 1–100).
func (s *Store) SaveUserQuotaLimits(ctx context.Context, userID string, l QuotaLimits) error {
	return Changed(s.g(ctx).Table("at_users").Where("id = ?", userID).
		Updates(map[string]any{"quota_five_hour_pct": l.FiveHourPct, "quota_weekly_pct": l.WeeklyPct}))
}

// Locales are the interface languages a user can pick ("" = follow the browser).
var Locales = []string{"zh-CN", "en", "ko", "ja"}

// ValidLocale reports whether l is "" or one of Locales.
func ValidLocale(l string) bool { return l == "" || slices.Contains(Locales, l) }

// SaveUserLocale stores the user's interface language (callers validate with ValidLocale).
func (s *Store) SaveUserLocale(ctx context.Context, userID, locale string) error {
	return Changed(s.g(ctx).Table("at_users").Where("id = ?", userID).Update("locale", locale))
}
