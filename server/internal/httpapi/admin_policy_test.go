package httpapi

import (
	"strings"
	"testing"
)

// TestAdminPolicyInvariants locks the shape of the admin console in authz/policy.csv: every
// rule under /api/admin belongs to admin alone, every admin rule lives under /api/admin/,
// and no role in the embedded hierarchy inherits admin (only a user id may, from casbin_rule).
// The web app's /admin/* pages rely on this: they are plain SPA routes, the API is the gate.
func TestAdminPolicyInvariants(t *testing.T) {
	rules, err := parsePolicy(authzPolicy)
	if err != nil {
		t.Fatal(err)
	}
	adminLines := 0
	for _, r := range rules {
		switch r[0] {
		case "p":
			sub, obj, act := r[1], r[2], r[3]
			if strings.HasPrefix(obj, "/api/admin") && sub != roleAdmin {
				t.Errorf("p, %s, %s, %s: an /api/admin route granted to %q, want admin only", sub, obj, act, sub)
			}
			if sub == roleAdmin {
				adminLines++
				if !strings.HasPrefix(obj, "/api/admin/") {
					t.Errorf("p, %s, %s, %s: an admin rule outside /api/admin/", sub, obj, act)
				}
			}
		case "g":
			if r[2] == roleAdmin {
				t.Errorf("g, %s, %s: the embedded hierarchy must not hand out admin", r[1], r[2])
			}
		}
	}
	if adminLines < 20 {
		t.Fatalf("only %d admin rules parsed", adminLines)
	}
}

// TestAdminRoutesDeniedBelowAdmin: the enforcer, as loaded, refuses every admin rule to the
// other subjects (anonymous, node, user) and grants it to admin.
func TestAdminRoutesDeniedBelowAdmin(t *testing.T) {
	a := mustAuthorizer(nil)
	rules, err := parsePolicy(authzPolicy)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rules {
		if r[0] != "p" || !strings.HasPrefix(r[2], "/api/admin") {
			continue
		}
		for _, sub := range []string{roleAnonymous, roleNode, roleUser} {
			if ok, err := a.allowed(sub, r[2], r[3]); err != nil || ok {
				t.Errorf("%s %s as %s: allowed=%v err=%v", r[3], r[2], sub, ok, err)
			}
		}
		if ok, err := a.allowed(roleAdmin, r[2], r[3]); err != nil || !ok {
			t.Errorf("%s %s as admin: allowed=%v err=%v", r[3], r[2], ok, err)
		}
	}
}
