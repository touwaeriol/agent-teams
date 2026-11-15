// Package timers runs the control plane's clock-driven work (data-model.md §2.10):
// scheduled messages to a project's coordinator and continuing threads that a Claude
// usage limit paused once the limit resets. One process runs the loop (advisory lock);
// rows are claimed with FOR UPDATE SKIP LOCKED so nothing fires twice.
package timers

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Cron is a parsed five-field cron expression: minute hour day-of-month month day-of-week.
type Cron struct {
	minute, hour, dom, month, dow []bool
	domAny, dowAny                bool
}

// ErrCron is a malformed cron expression.
var ErrCron = errors.New("bad cron expression")

type fieldRange struct{ min, max int }

var fieldRanges = [5]fieldRange{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 7}}

// ParseCron parses "m h dom mon dow" with *, numbers, a-b ranges, /n steps and comma lists.
// Day-of-week 7 is Sunday like 0. When both day fields are restricted either may match.
func ParseCron(expr string) (Cron, error) {
	parts := strings.Fields(expr)
	if len(parts) != 5 {
		return Cron{}, fmt.Errorf("%w: need 5 fields, got %d", ErrCron, len(parts))
	}
	sets := make([][]bool, 5)
	for i, p := range parts {
		set, err := parseField(p, fieldRanges[i])
		if err != nil {
			return Cron{}, err
		}
		sets[i] = set
	}
	sets[4][0] = sets[4][0] || sets[4][7]
	return Cron{minute: sets[0], hour: sets[1], dom: sets[2], month: sets[3], dow: sets[4],
		domAny: parts[2] == "*", dowAny: parts[4] == "*"}, nil
}

func parseField(field string, r fieldRange) ([]bool, error) {
	set := make([]bool, r.max+1)
	for _, item := range strings.Split(field, ",") {
		if err := markItem(set, item, r); err != nil {
			return nil, err
		}
	}
	return set, nil
}

func markItem(set []bool, item string, r fieldRange) error {
	body, stepText, hasStep := strings.Cut(item, "/")
	step := 1
	if hasStep {
		n, err := strconv.Atoi(stepText)
		if err != nil || n < 1 {
			return fmt.Errorf("%w: step %q", ErrCron, item)
		}
		step = n
	}
	lo, hi, err := itemBounds(body, r, hasStep)
	if err != nil {
		return err
	}
	for v := lo; v <= hi; v += step {
		set[v] = true
	}
	return nil
}

// itemBounds reads "*", "a" or "a-b"; "a/n" runs from a to the field's maximum.
func itemBounds(body string, r fieldRange, hasStep bool) (int, int, error) {
	if body == "*" {
		return r.min, r.max, nil
	}
	loText, hiText, isRange := strings.Cut(body, "-")
	lo, err := bounded(loText, r)
	if err != nil {
		return 0, 0, err
	}
	switch {
	case isRange:
		hi, err := bounded(hiText, r)
		if err != nil || hi < lo {
			return 0, 0, fmt.Errorf("%w: range %q", ErrCron, body)
		}
		return lo, hi, nil
	case hasStep:
		return lo, r.max, nil
	}
	return lo, lo, nil
}

func bounded(text string, r fieldRange) (int, error) {
	n, err := strconv.Atoi(text)
	if err != nil || n < r.min || n > r.max {
		return 0, fmt.Errorf("%w: %q not in %d-%d", ErrCron, text, r.min, r.max)
	}
	return n, nil
}

// dayMatches applies cron's rule: with both day fields restricted, either one matching is enough.
func (c Cron) dayMatches(t time.Time) bool {
	dom, dow := c.dom[t.Day()], c.dow[int(t.Weekday())]
	switch {
	case c.domAny && c.dowAny:
		return true
	case c.domAny:
		return dow
	case c.dowAny:
		return dom
	}
	return dom || dow
}

// Next is the first matching minute strictly after `after`, read in loc; zero when none
// exists within five years (e.g. 31 February).
func (c Cron) Next(after time.Time, loc *time.Location) time.Time {
	t := after.In(loc).Truncate(time.Minute).Add(time.Minute)
	limit := t.AddDate(5, 0, 0)
	for t.Before(limit) {
		y, m, d := t.Date()
		switch {
		case !c.month[int(m)]:
			t = time.Date(y, m+1, 1, 0, 0, 0, 0, loc)
		case !c.dayMatches(t):
			t = time.Date(y, m, d+1, 0, 0, 0, 0, loc)
		case !c.hour[t.Hour()]:
			t = time.Date(y, m, d, t.Hour()+1, 0, 0, 0, loc)
		case !c.minute[t.Minute()]:
			t = t.Add(time.Minute)
		default:
			return t
		}
	}
	return time.Time{}
}
