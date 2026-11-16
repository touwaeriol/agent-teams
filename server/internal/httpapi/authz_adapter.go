package httpapi

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/casbin/casbin/v2/model"
	"github.com/casbin/casbin/v2/persist"

	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// authzAdapter is the Casbin adapter: the route policy (p lines and the role hierarchy) comes
// from the embedded policy.csv, the users' grouping rules (g, <user id>, <role>) from the
// casbin_rule table through the store's GORM handle. The table is created by the goose
// migration 00110, never auto-migrated. Only g rules are written (auto-save); the embedded
// lines cannot change at run time, so a write of a p rule is refused. Without a store
// (OpenAPI generation, route tests) only the embedded policy is loaded.
type authzAdapter struct {
	policy string
	db     *store.Store
}

var (
	_ persist.Adapter          = (*authzAdapter)(nil)
	_ persist.UpdatableAdapter = (*authzAdapter)(nil)
	_ persist.BatchAdapter     = (*authzAdapter)(nil)
)

const adapterTimeout = 10 * time.Second

var errNoRoleStore = errors.New("authz: no database for role assignments")

// LoadPolicy loads the embedded lines, then every grouping row of casbin_rule.
func (a *authzAdapter) LoadPolicy(m model.Model) error {
	rules, err := parsePolicy(a.policy)
	if err != nil {
		return err
	}
	if a.db != nil {
		ctx, cancel := context.WithTimeout(context.Background(), adapterTimeout)
		defer cancel()
		rows, err := a.db.CasbinRules(ctx, "g")
		if err != nil {
			return fmt.Errorf("authz: load casbin_rule: %w", err)
		}
		for _, r := range rows {
			rules = append(rules, append([]string{r.Ptype}, r.Values()...))
		}
	}
	for _, r := range rules {
		if err := persist.LoadPolicyArray(r, m); err != nil {
			return err
		}
	}
	return nil
}

// SavePolicy is not supported: the route policy is code and grouping rules are saved one by one.
func (a *authzAdapter) SavePolicy(model.Model) error {
	return errors.New("authz: SavePolicy is not supported")
}

func (a *authzAdapter) AddPolicy(sec, ptype string, rule []string) error {
	return a.AddPolicies(sec, ptype, [][]string{rule})
}

func (a *authzAdapter) AddPolicies(sec, ptype string, rules [][]string) error {
	return a.write(sec, func(ctx context.Context) error { return a.db.AddCasbinRules(ctx, rows(ptype, rules)) })
}

func (a *authzAdapter) RemovePolicy(sec, ptype string, rule []string) error {
	return a.RemovePolicies(sec, ptype, [][]string{rule})
}

func (a *authzAdapter) RemovePolicies(sec, ptype string, rules [][]string) error {
	return a.write(sec, func(ctx context.Context) error { return a.db.RemoveCasbinRules(ctx, rows(ptype, rules)) })
}

func (a *authzAdapter) RemoveFilteredPolicy(sec, ptype string, fieldIndex int, fieldValues ...string) error {
	return a.write(sec, func(ctx context.Context) error {
		return a.db.RemoveFilteredCasbinRules(ctx, ptype, fieldIndex, fieldValues...)
	})
}

func (a *authzAdapter) UpdatePolicy(sec, ptype string, oldRule, newRule []string) error {
	return a.UpdatePolicies(sec, ptype, [][]string{oldRule}, [][]string{newRule})
}

func (a *authzAdapter) UpdatePolicies(sec, ptype string, oldRules, newRules [][]string) error {
	return a.write(sec, func(ctx context.Context) error {
		return a.db.UpdateCasbinRules(ctx, rows(ptype, oldRules), rows(ptype, newRules))
	})
}

// UpdateFilteredPolicies is not used by the console.
func (a *authzAdapter) UpdateFilteredPolicies(string, string, [][]string, int, ...string) ([][]string, error) {
	return nil, errors.New("authz: UpdateFilteredPolicies is not supported")
}

// write runs a change of grouping rules; p rules are code and cannot be written.
func (a *authzAdapter) write(sec string, fn func(context.Context) error) error {
	if sec != "g" {
		return fmt.Errorf("authz: %s rules live in policy.csv and cannot be changed at run time", sec)
	}
	if a.db == nil {
		return errNoRoleStore
	}
	ctx, cancel := context.WithTimeout(context.Background(), adapterTimeout)
	defer cancel()
	return fn(ctx)
}

func rows(ptype string, rules [][]string) []store.CasbinRule {
	out := make([]store.CasbinRule, len(rules))
	for i, r := range rules {
		out[i] = store.NewCasbinRule(ptype, r)
	}
	return out
}

// parsePolicy reads policy.csv: "p, sub, obj, act" and "g, a, b" lines; blank lines and #
// comments are skipped. Each rule is returned with its ptype first.
func parsePolicy(text string) ([][]string, error) {
	var out [][]string
	for n, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, ",")
		for i := range fields {
			fields[i] = strings.TrimSpace(fields[i])
		}
		if !(fields[0] == "p" && len(fields) == 4) && !(fields[0] == "g" && len(fields) == 3) {
			return nil, fmt.Errorf("authz policy line %d: unrecognised rule %q", n+1, line)
		}
		out = append(out, fields)
	}
	return out, nil
}
