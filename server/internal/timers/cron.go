// Package timers runs the control plane's clock-driven work (data-model.md §2.10): scheduled
// tasks (the user's recurring messages to the coordinator, and the wake-ups agents set with the
// schedule_* tools), run as River jobs (river.go), plus the 20 s loop that continues threads a
// Claude usage limit paused and fires thread reminders (runner.go).
package timers

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

// Cron is a parsed schedule: five fields (minute hour day-of-month month day-of-week) or a
// descriptor (@daily, @hourly, @every 30m…), read by robfig/cron's standard parser.
type Cron struct{ s cron.Schedule }

// ErrCron is a malformed cron expression.
var ErrCron = errors.New("bad cron expression")

var parser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

// ParseCron parses "m h dom mon dow" with *, numbers, a-b ranges, /n steps and comma lists, or a
// descriptor. Day-of-week 7 is Sunday like 0. When both day fields are restricted either may
// match. The zone comes from the schedule's timezone, so a CRON_TZ= prefix is refused.
func ParseCron(expr string) (Cron, error) {
	expr = strings.Join(strings.Fields(expr), " ")
	if strings.Contains(expr, "TZ=") {
		return Cron{}, fmt.Errorf("%w: put the zone in timezone", ErrCron)
	}
	if !strings.HasPrefix(expr, "@") {
		parts := strings.Fields(expr)
		if len(parts) != 5 {
			return Cron{}, fmt.Errorf("%w: need 5 fields, got %d", ErrCron, len(parts))
		}
		for _, p := range parts {
			if strings.Contains(","+p+",", ",,") {
				return Cron{}, fmt.Errorf("%w: empty list item in %q", ErrCron, p)
			}
		}
		parts[4] = sundaySeven(parts[4])
		expr = strings.Join(parts, " ")
	}
	s, err := parser.Parse(expr)
	if err != nil {
		return Cron{}, fmt.Errorf("%w: %v", ErrCron, err)
	}
	return Cron{s: s}, nil
}

// sundaySeven rewrites day-of-week 7 (Sunday, as in Vixie cron) to 0, which robfig/cron reads:
// "7" → "0" and "a-7" → "a-6,0".
func sundaySeven(field string) string {
	items := strings.Split(field, ",")
	for i, item := range items {
		switch lo, hi, isRange := strings.Cut(item, "-"); {
		case item == "7":
			items[i] = "0"
		case isRange && hi == "7":
			if n, err := strconv.Atoi(lo); err == nil && n <= 6 {
				items[i] = lo + "-6,0"
			}
		}
	}
	return strings.Join(items, ",")
}

// Next is the first run strictly after `after`, read in loc; zero when none exists within five
// years (e.g. 31 February).
func (c Cron) Next(after time.Time, loc *time.Location) time.Time {
	if c.s == nil {
		return time.Time{}
	}
	return c.s.Next(after.In(loc))
}

// MinGap is the shortest interval between two of the next few runs (agents' recurring
// schedules must not fire more often than MinAgentInterval).
func (c Cron) MinGap(from time.Time, loc *time.Location, runs int) time.Duration {
	gap := time.Duration(1<<63 - 1)
	prev := c.Next(from, loc)
	for i := 1; i < runs && !prev.IsZero(); i++ {
		next := c.Next(prev, loc)
		if next.IsZero() {
			break
		}
		gap = min(gap, next.Sub(prev))
		prev = next
	}
	return gap
}
