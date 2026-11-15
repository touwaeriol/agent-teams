package delivery

import (
	"slices"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/core"
)

// Verdict is the CI state of one commit.
type Verdict int

// Verdicts.
const (
	CIPending Verdict = iota
	CIPass
	CIFail
	CIMissing // no check appeared in time and on_missing = block
)

// CIRules are the project's CI settings (§8.3).
type CIRules struct {
	Mode          string
	Required      []string
	AppearTimeout time.Duration
	Timeout       time.Duration
	OnMissing     string
}

// RulesOf extracts CI rules from delivery settings.
func RulesOf(d core.Delivery) CIRules {
	return CIRules{Mode: d.CI.Mode, Required: d.CI.Required, AppearTimeout: time.Duration(d.CI.AppearTimeoutS) * time.Second,
		Timeout: time.Duration(d.CI.TimeoutS) * time.Second, OnMissing: d.CI.OnMissing}
}

// Evaluate decides CI for a commit from its checks and how long we have waited.
// required empty = every check must pass; a required check that never appears keeps it pending
// until the timeout, which then fails it.
func Evaluate(checks []core.CheckView, r CIRules, waited time.Duration) (Verdict, []core.CheckView) {
	if r.Mode == "off" {
		return CIPass, nil
	}
	relevant := relevantChecks(checks, r.Required)
	if len(checks) == 0 {
		return missing(r, waited), nil
	}
	if failed := failedChecks(relevant); len(failed) > 0 {
		return CIFail, failed
	}
	if allPassed(relevant, r.Required) {
		return CIPass, nil
	}
	if r.Timeout > 0 && waited >= r.Timeout {
		return CIFail, pendingChecks(relevant)
	}
	return CIPending, nil
}

func missing(r CIRules, waited time.Duration) Verdict {
	if waited < r.AppearTimeout {
		return CIPending
	}
	if r.OnMissing == "block" || r.OnMissing == "fail" {
		return CIMissing
	}
	return CIPass
}

func relevantChecks(checks []core.CheckView, required []string) []core.CheckView {
	if len(required) == 0 {
		return checks
	}
	var out []core.CheckView
	for _, c := range checks {
		if slices.Contains(required, c.Name) {
			out = append(out, c)
		}
	}
	return out
}

var passing = []string{"success", "neutral", "skipped"}

func failedChecks(checks []core.CheckView) []core.CheckView {
	var out []core.CheckView
	for _, c := range checks {
		if c.Status == "completed" && !slices.Contains(passing, c.Conclusion) {
			out = append(out, c)
		}
	}
	return out
}

func pendingChecks(checks []core.CheckView) []core.CheckView {
	var out []core.CheckView
	for _, c := range checks {
		if c.Status != "completed" {
			out = append(out, c)
		}
	}
	return out
}

func allPassed(checks []core.CheckView, required []string) bool {
	for _, name := range required {
		if !slices.ContainsFunc(checks, func(c core.CheckView) bool { return c.Name == name }) {
			return false
		}
	}
	for _, c := range checks {
		if c.Status != "completed" || !slices.Contains(passing, c.Conclusion) {
			return false
		}
	}
	return true
}
