package httpapi

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"testing"
)

// groupingRows lists casbin_rule's g rows as "user=role", sorted.
func groupingRows(t *testing.T, h *harness) []string {
	t.Helper()
	rows, err := h.db.CasbinRules(context.Background(), "g")
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = strings.Join(r.Values(), "=")
	}
	sort.Strings(out)
	return out
}

func sortedOf(xs ...string) []string { sort.Strings(xs); return xs }

// TestRegistrationWritesGroupingRules: the first user and AGENT_TEAMS_ADMIN_EMAILS become admin,
// everyone else user, each as one casbin_rule row written by the enforcer.
func TestRegistrationWritesGroupingRules(t *testing.T) {
	h := newHarness(t)
	_, first := h.signUp("first@example.com")
	_, second := h.signUp("second@example.com")
	_, boss := h.signUp("boss@example.com")
	want := sortedOf(first.ID+"=admin", second.ID+"=user", boss.ID+"=admin")
	if got := groupingRows(t, h); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("rows %v, want %v", got, want)
	}
	if first.Role != "admin" || second.Role != "user" || boss.Role != "admin" {
		t.Fatalf("roles %s %s %s", first.Role, second.Role, boss.Role)
	}
}

// TestRoleChangeTakesEffectAtOnce: an admin's PATCH promotes and demotes through the enforcer;
// the user's existing session gains and loses admin routes on its next request.
func TestRoleChangeTakesEffectAtOnce(t *testing.T) {
	h := newHarness(t)
	admin, _ := h.signUp("a@example.com")
	userClient, u := h.signUp("b@example.com")
	if r := userClient.do("GET", "/api/admin/users", nil); r.Status != http.StatusForbidden {
		t.Fatalf("user on admin route = %d", r.Status)
	}
	admin.must(200, "PATCH", "/api/admin/users/"+u.ID, map[string]any{"role": "admin"}, nil)
	var me UserView
	userClient.must(200, "GET", "/api/me", nil, &me)
	if me.Role != "admin" {
		t.Fatalf("me role %s", me.Role)
	}
	userClient.must(200, "GET", "/api/admin/users", nil, nil)
	if rows := groupingRows(t, h); strings.Count(strings.Join(rows, ","), u.ID) != 1 {
		t.Fatalf("one rule per user, got %v", rows)
	}
	admin.must(200, "PATCH", "/api/admin/users/"+u.ID, map[string]any{"role": "user"}, nil)
	if r := userClient.do("GET", "/api/admin/users", nil); r.Status != http.StatusForbidden {
		t.Fatalf("demoted user on admin route = %d", r.Status)
	}
	userClient.must(200, "GET", "/api/projects", nil, nil)
	if r := admin.do("PATCH", "/api/admin/users/nope", map[string]any{"role": "admin"}); r.Status != http.StatusNotFound {
		t.Fatalf("unknown user = %d", r.Status)
	}
	for _, row := range groupingRows(t, h) {
		if strings.HasPrefix(row, "nope=") {
			t.Fatal("rule written for an unknown user")
		}
	}
}

// TestDeleteUserRemovesGroupingRules: deleting an account removes its casbin_rule rows.
func TestDeleteUserRemovesGroupingRules(t *testing.T) {
	h := newHarness(t)
	admin, me := h.signUp("a@example.com")
	_, u := h.signUp("b@example.com")
	admin.must(204, "DELETE", "/api/admin/users/"+u.ID, nil, nil)
	if got := groupingRows(t, h); len(got) != 1 || got[0] != me.ID+"=admin" {
		t.Fatalf("rows after delete %v", got)
	}
}

// TestOtherReplicaReloads: a second console on the same database sees a role change after its
// periodic reload (RunAuthz), and atctl's SetUserRole goes through the same enforcer API.
func TestOtherReplicaReloads(t *testing.T) {
	h := newHarness(t)
	_, _ = h.signUp("a@example.com")
	_, u := h.signUp("b@example.com")
	other := mustAuthorizer(h.db)
	if other.role(u.ID) != roleUser {
		t.Fatal("replica did not load the rules")
	}
	if err := SetUserRole(h.db, u.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	if err := SetUserRole(h.db, u.ID, "root"); err == nil {
		t.Fatal("unknown role accepted")
	}
	if ok, _ := other.allowed(u.ID, "/api/admin/users", "GET"); ok {
		t.Fatal("replica changed before reloading")
	}
	if err := other.reload(); err != nil {
		t.Fatal(err)
	}
	if ok, _ := other.allowed(u.ID, "/api/admin/users", "GET"); !ok || other.role(u.ID) != roleAdmin {
		t.Fatal("replica did not pick up the change")
	}
	if ok, _ := other.allowed(u.ID, "/api/projects", "GET"); !ok {
		t.Fatal("admin lost user routes")
	}
	if ok, _ := other.allowed("unknown-user", "/api/projects", "GET"); ok {
		t.Fatal("a user id without a rule passed")
	}
}
