// Package timers runs the control plane's clock-driven work (data-model.md §2.10): scheduled
// tasks (the user's recurring messages to the coordinator, and the wake-ups agents set with the
// schedule_* tools), run as River jobs (river.go), plus the 20 s loop that continues threads a
// Claude usage limit paused and fires thread reminders (runner.go).
package timers

import "github.com/touwaeriol/agent-teams/server/internal/cronx"

// Cron is a parsed schedule (cronx).
type Cron = cronx.Cron

// ErrCron is a malformed cron expression.
var ErrCron = cronx.ErrCron

// ParseCron parses a cron expression (cronx.Parse).
func ParseCron(expr string) (Cron, error) { return cronx.Parse(expr) }
