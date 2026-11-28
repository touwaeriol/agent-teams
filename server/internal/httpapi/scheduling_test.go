package httpapi

import (
	"testing"

	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// TestSchedulingSettings: the defaults until set, a round trip, per-user storage, thresholds out
// of 1–100 refused (422), and no access without signing in.
func TestSchedulingSettings(t *testing.T) {
	h := newHarness(t)
	a, _ := h.signUp("a@example.com")
	b, _ := h.signUp("b@example.com")
	def := store.DefaultQuotaLimits()
	var got SchedulingSettings
	a.must(200, "GET", "/api/me/scheduling", nil, &got)
	if got.QuotaLimits != def || got.Defaults != def || def.FiveHourPct != 85 || def.WeeklyPct != 95 {
		t.Fatalf("defaults %+v", got)
	}
	a.must(200, "PUT", "/api/me/scheduling", store.QuotaLimits{FiveHourPct: 70, WeeklyPct: 100}, &got)
	if got.FiveHourPct != 70 || got.WeeklyPct != 100 {
		t.Fatalf("put %+v", got)
	}
	a.must(200, "GET", "/api/me/scheduling", nil, &got)
	if got.FiveHourPct != 70 || got.WeeklyPct != 100 || got.Defaults != def {
		t.Fatalf("after put %+v", got)
	}
	b.must(200, "GET", "/api/me/scheduling", nil, &got)
	if got.QuotaLimits != def {
		t.Fatalf("another user's settings changed: %+v", got)
	}
	for _, bad := range []any{
		store.QuotaLimits{FiveHourPct: 0, WeeklyPct: 95},
		store.QuotaLimits{FiveHourPct: 85, WeeklyPct: 101},
		map[string]int{"five_hour_pct": 85},
	} {
		if r := a.do("PUT", "/api/me/scheduling", bad); r.Status != 422 {
			t.Fatalf("PUT %+v = %d %s", bad, r.Status, r.Body)
		}
	}
	a.must(200, "GET", "/api/me/scheduling", nil, &got)
	if got.FiveHourPct != 70 || got.WeeklyPct != 100 {
		t.Fatalf("a refused PUT changed the settings: %+v", got)
	}
	if r := h.client().do("GET", "/api/me/scheduling", nil); r.Status != 401 {
		t.Fatalf("anonymous = %d", r.Status)
	}
}

// TestMyLocale: empty until set, a round trip through PUT and /api/me, per-user storage, unknown
// languages refused (422), and no access without signing in.
func TestMyLocale(t *testing.T) {
	h := newHarness(t)
	a, _ := h.signUp("a@example.com")
	b, _ := h.signUp("b@example.com")
	var me UserView
	a.must(200, "GET", "/api/me", nil, &me)
	if me.Locale != "" {
		t.Fatalf("default %q", me.Locale)
	}
	a.must(200, "PUT", "/api/me/locale", map[string]string{"locale": "ja"}, &me)
	if me.Locale != "ja" {
		t.Fatalf("put %q", me.Locale)
	}
	a.must(200, "GET", "/api/me", nil, &me)
	if me.Locale != "ja" {
		t.Fatalf("after put %q", me.Locale)
	}
	b.must(200, "GET", "/api/me", nil, &me)
	if me.Locale != "" {
		t.Fatalf("another user's locale changed: %q", me.Locale)
	}
	if r := a.do("PUT", "/api/me/locale", map[string]string{"locale": "fr"}); r.Status != 422 {
		t.Fatalf("fr = %d %s", r.Status, r.Body)
	}
	a.must(200, "PUT", "/api/me/locale", map[string]string{"locale": ""}, &me)
	if me.Locale != "" {
		t.Fatalf("reset %q", me.Locale)
	}
	if r := h.client().do("PUT", "/api/me/locale", map[string]string{"locale": "en"}); r.Status != 401 {
		t.Fatalf("anonymous = %d", r.Status)
	}
}
