package store

import (
	"context"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// CasbinRule is a row of casbin_rule, the Casbin policy table (standard adapter layout). It
// holds the user → role grouping rules (ptype "g", v0 = user id, v1 = role); the route policy
// stays in the embedded authz/policy.csv. Only the Casbin adapter in httpapi uses these
// methods: every change goes through the enforcer.
type CasbinRule struct {
	ID    int64
	Ptype string
	V0    string
	V1    string
	V2    string
	V3    string
	V4    string
	V5    string
}

// TableName maps CasbinRule to casbin_rule.
func (CasbinRule) TableName() string { return "casbin_rule" }

// NewCasbinRule builds a row from a policy type and its values (at most six).
func NewCasbinRule(ptype string, values []string) CasbinRule {
	v := make([]string, 6)
	copy(v, values)
	return CasbinRule{Ptype: ptype, V0: v[0], V1: v[1], V2: v[2], V3: v[3], V4: v[4], V5: v[5]}
}

// Values returns the rule's values without the trailing empty ones.
func (r CasbinRule) Values() []string {
	v := []string{r.V0, r.V1, r.V2, r.V3, r.V4, r.V5}
	for len(v) > 0 && v[len(v)-1] == "" {
		v = v[:len(v)-1]
	}
	return v
}

var casbinCols = []string{"v0", "v1", "v2", "v3", "v4", "v5"}

// CasbinRules lists the rows of the given policy types, in insertion order.
func (s *Store) CasbinRules(ctx context.Context, ptypes ...string) ([]CasbinRule, error) {
	var out []CasbinRule
	err := s.g(ctx).Where("ptype IN ?", ptypes).Order("id").Find(&out).Error
	return out, err
}

// AddCasbinRules inserts rows; one that already exists is left alone.
func (s *Store) AddCasbinRules(ctx context.Context, rules []CasbinRule) error {
	if len(rules) == 0 {
		return nil
	}
	return s.g(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&rules).Error
}

// RemoveCasbinRules deletes exact rows.
func (s *Store) RemoveCasbinRules(ctx context.Context, rules []CasbinRule) error {
	return s.Tx(ctx, func(tx *Store) error {
		for _, r := range rules {
			if err := tx.casbinMatch(ctx, r.Ptype, 0, padded(r), true).Delete(&CasbinRule{}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// RemoveFilteredCasbinRules deletes the rows of ptype whose values from fieldIndex on equal
// values ("" matches anything, as in Casbin).
func (s *Store) RemoveFilteredCasbinRules(ctx context.Context, ptype string, fieldIndex int, values ...string) error {
	return s.casbinMatch(ctx, ptype, fieldIndex, values, false).Delete(&CasbinRule{}).Error
}

// UpdateCasbinRules replaces old rows with new ones in one transaction.
func (s *Store) UpdateCasbinRules(ctx context.Context, old, next []CasbinRule) error {
	return s.Tx(ctx, func(tx *Store) error {
		if err := tx.RemoveCasbinRules(ctx, old); err != nil {
			return err
		}
		return tx.AddCasbinRules(ctx, next)
	})
}

// casbinMatch filters rows of ptype by values from fieldIndex on; unless exact, "" matches anything.
func (s *Store) casbinMatch(ctx context.Context, ptype string, fieldIndex int, values []string, exact bool) *gorm.DB {
	q := s.g(ctx).Where("ptype = ?", ptype)
	for i, v := range values {
		if (exact || v != "") && fieldIndex+i < len(casbinCols) {
			q = q.Where(casbinCols[fieldIndex+i]+" = ?", v)
		}
	}
	return q
}

// padded lists all six values of an exact rule (empty ones too, so they must match).
func padded(r CasbinRule) []string { return []string{r.V0, r.V1, r.V2, r.V3, r.V4, r.V5} }
