package timers

import (
	"errors"
	"testing"
	"time"
)

func TestParseCronRejects(t *testing.T) {
	for _, expr := range []string{"", "* * * *", "60 * * * *", "* 24 * * *", "* * 0 * *", "* * * 13 *", "* * * * 8",
		"*/0 * * * *", "5-1 * * * *", "a * * * *", "1,,2 * * * *", "CRON_TZ=UTC 0 9 * * *", "@yearly-ish"} {
		if _, err := ParseCron(expr); !errors.Is(err, ErrCron) {
			t.Errorf("%q: err %v", expr, err)
		}
	}
}

func TestCronNext(t *testing.T) {
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 11, 15, 9, 30, 0, 0, shanghai) // Sunday
	cases := []struct {
		expr string
		want time.Time
	}{
		{"0 9 * * *", time.Date(2026, 11, 16, 9, 0, 0, 0, shanghai)},
		{"0 * * * *", time.Date(2026, 11, 15, 10, 0, 0, 0, shanghai)},
		{"0 9 * * 1", time.Date(2026, 11, 16, 9, 0, 0, 0, shanghai)},
		{"*/20 * * * *", time.Date(2026, 11, 15, 9, 40, 0, 0, shanghai)},
		{"30 9 * * 0,7", time.Date(2026, 11, 22, 9, 30, 0, 0, shanghai)},
		{"0 0 1 1 *", time.Date(2027, 1, 1, 0, 0, 0, 0, shanghai)},
		{"15 10-12/2 * * *", time.Date(2026, 11, 15, 10, 15, 0, 0, shanghai)},
		{"0 8 1 * 5", time.Date(2026, 11, 20, 8, 0, 0, 0, shanghai)}, // Friday comes before the 1st
		{"0 9 * * 5-7", time.Date(2026, 11, 20, 9, 0, 0, 0, shanghai)},
		{"@daily", time.Date(2026, 11, 16, 0, 0, 0, 0, shanghai)},
		{"@every 2h", time.Date(2026, 11, 15, 11, 30, 0, 0, shanghai)},
	}
	for _, c := range cases {
		cron, err := ParseCron(c.expr)
		if err != nil {
			t.Fatalf("%q: %v", c.expr, err)
		}
		if got := cron.Next(from, shanghai); !got.Equal(c.want) {
			t.Errorf("%q: next %v, want %v", c.expr, got, c.want)
		}
	}
	never, _ := ParseCron("0 0 31 2 *")
	if got := never.Next(from, shanghai); !got.IsZero() {
		t.Errorf("31 Feb: %v", got)
	}
}

func TestCronMinGap(t *testing.T) {
	from := time.Date(2026, 11, 15, 9, 30, 0, 0, time.UTC)
	for expr, want := range map[string]time.Duration{"*/5 * * * *": 5 * time.Minute, "0 9,10 * * *": time.Hour,
		"* * * * *": time.Minute, "@every 30m": 30 * time.Minute} {
		c, err := ParseCron(expr)
		if err != nil {
			t.Fatal(err)
		}
		if got := c.MinGap(from, time.UTC, 6); got != want {
			t.Errorf("%q: gap %v, want %v", expr, got, want)
		}
	}
}
