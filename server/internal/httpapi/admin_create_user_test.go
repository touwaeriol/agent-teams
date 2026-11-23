package httpapi

import (
	"strings"
	"testing"
)

// TestAdminCreateUser: an administrator creates accounts with a generated or a chosen
// password; only administrators may, emails are unique after normalising, and neither the
// answer nor the audit trail repeats a password the administrator typed.
func TestAdminCreateUser(t *testing.T) {
	h := newHarness(t)
	admin, _ := h.signUp("a@example.com")
	userClient, _ := h.signUp("b@example.com")

	body := map[string]any{"email": "c@example.com"}
	if r := h.client().do("POST", "/api/admin/users", body); r.Status != 401 {
		t.Fatalf("anonymous = %d %s", r.Status, r.Body)
	}
	if r := userClient.do("POST", "/api/admin/users", body); r.Status != 403 {
		t.Fatalf("non-admin = %d %s", r.Status, r.Body)
	}

	// Registration closed does not stop an administrator.
	var s AdminSettings
	admin.must(200, "GET", "/api/admin/settings", nil, &s)
	s.RegistrationOpen = false
	admin.must(200, "PUT", "/api/admin/settings", s, nil)

	// Generated password: answered once, must be changed after signing in.
	var gen AdminCreatedUser
	admin.must(201, "POST", "/api/admin/users", map[string]any{"email": "  New@Example.COM ", "display_name": "  新同事 "}, &gen)
	if gen.User.Email != "new@example.com" || gen.User.DisplayName != "新同事" || gen.User.Role != "user" ||
		!gen.User.MustChangePassword || !gen.User.Active || gen.TempPassword == "" || gen.User.ID == "" {
		t.Fatalf("generated %+v", gen)
	}
	newbie := h.client()
	var me UserView
	newbie.must(200, "POST", "/auth/login", Credentials{"new@example.com", gen.TempPassword}, &me)
	if !me.MustChangePassword {
		t.Fatalf("login %+v", me)
	}
	if r := newbie.do("GET", "/api/projects", nil); r.Status != 403 || !strings.Contains(string(r.Body), "password_change_required") {
		t.Fatalf("temp password not enforced: %d %s", r.Status, r.Body)
	}
	newbie.must(204, "POST", "/api/me/password", map[string]string{"current": gen.TempPassword, "new": "newpassword1"}, nil)
	newbie.must(200, "GET", "/api/projects", nil, nil)

	// Chosen password without forced change: usable at once, never echoed.
	r := admin.must(201, "POST", "/api/admin/users", map[string]any{"email": "pw@example.com", "password": "chosen-pass-1",
		"must_change_password": false}, nil)
	if strings.Contains(string(r.Body), "temp_password") || strings.Contains(string(r.Body), "chosen-pass-1") {
		t.Fatalf("chosen password echoed: %s", r.Body)
	}
	var chosen AdminCreatedUser
	r.json(t, &chosen)
	if chosen.User.MustChangePassword || chosen.User.Role != "user" {
		t.Fatalf("chosen %+v", chosen)
	}
	pw := h.client()
	pw.must(200, "POST", "/auth/login", Credentials{"pw@example.com", "chosen-pass-1"}, nil)
	pw.must(200, "GET", "/api/projects", nil, nil)

	// A chosen password still has to be changed by default.
	var forced AdminCreatedUser
	admin.must(201, "POST", "/api/admin/users", map[string]any{"email": "forced@example.com", "password": "chosen-pass-2"}, &forced)
	if !forced.User.MustChangePassword || forced.TempPassword != "" {
		t.Fatalf("forced %+v", forced)
	}

	// Another administrator.
	var adm AdminCreatedUser
	admin.must(201, "POST", "/api/admin/users", map[string]any{"email": "boss@example.com", "role": "admin"}, &adm)
	if adm.User.Role != "admin" {
		t.Fatalf("admin role %+v", adm)
	}
	var users []AdminUserView
	admin.must(200, "GET", "/api/admin/users", nil, &users)
	roles := map[string]string{}
	for _, u := range users {
		roles[u.Email] = u.Role
	}
	if len(users) != 6 || roles["boss@example.com"] != "admin" || roles["new@example.com"] != "user" {
		t.Fatalf("users %+v", roles)
	}

	for _, tc := range []struct {
		body map[string]any
		code string
		want int
	}{
		{map[string]any{"email": "NEW@example.com "}, "email_taken", 409},
		{map[string]any{"email": "b@example.com"}, "email_taken", 409},
		{map[string]any{"email": "not-an-email"}, "invalid_email", 422},
		{map[string]any{"email": "   "}, "invalid_email", 422},
		{map[string]any{"email": "weak@example.com", "password": "short"}, "weak_password", 422},
		{map[string]any{"email": "role@example.com", "role": "owner"}, "", 422},
		{map[string]any{"email": "long@example.com", "display_name": strings.Repeat("x", 101)}, "", 422},
	} {
		r := admin.do("POST", "/api/admin/users", tc.body)
		if r.Status != tc.want || !strings.Contains(string(r.Body), tc.code) {
			t.Errorf("%v = %d %s", tc.body, r.Status, r.Body)
		}
	}

	// Audit: who, which role, how the password was set; never the password.
	var audit struct {
		Items []struct {
			TargetID string         `json:"target_id"`
			Detail   map[string]any `json:"detail"`
		} `json:"items"`
	}
	ar := admin.must(200, "GET", "/api/admin/audit?action=user_create", nil, &audit)
	if len(audit.Items) != 4 {
		t.Fatalf("audit %s", ar.Body)
	}
	for _, secret := range []string{gen.TempPassword, adm.TempPassword, "chosen-pass-1", "chosen-pass-2", "scrypt$"} {
		if strings.Contains(string(ar.Body), secret) {
			t.Fatalf("audit leaks %q: %s", secret, ar.Body)
		}
	}
	byID := map[string]map[string]any{}
	for _, it := range audit.Items {
		byID[it.TargetID] = it.Detail
	}
	if d := byID[gen.User.ID]; d["email"] != "new@example.com" || d["role"] != "user" || d["generated_password"] != true || d["must_change_password"] != true {
		t.Fatalf("generated audit %v", d)
	}
	if d := byID[chosen.User.ID]; d["generated_password"] != false || d["must_change_password"] != false {
		t.Fatalf("chosen audit %v", d)
	}
	if d := byID[adm.User.ID]; d["role"] != "admin" {
		t.Fatalf("admin audit %v", d)
	}

	// Deactivating the created account signs it out and blocks logging in.
	admin.must(200, "PATCH", "/api/admin/users/"+chosen.User.ID, map[string]any{"active": false}, nil)
	if r := pw.do("GET", "/api/me", nil); r.Status != 401 {
		t.Fatalf("deactivated session = %d", r.Status)
	}
	if r := h.client().do("POST", "/auth/login", Credentials{"pw@example.com", "chosen-pass-1"}); r.Status != 403 ||
		!strings.Contains(string(r.Body), "inactive") {
		t.Fatalf("inactive login = %d %s", r.Status, r.Body)
	}
}

// TestAdminCannotDemoteSelf: an administrator can neither deactivate nor demote themself,
// even when another administrator exists.
func TestAdminCannotDemoteSelf(t *testing.T) {
	h := newHarness(t)
	admin, me := h.signUp("a@example.com")
	admin.must(201, "POST", "/api/admin/users", map[string]any{"email": "boss@example.com", "role": "admin"}, nil)
	for _, patch := range []map[string]any{{"active": false}, {"role": "user"}} {
		if r := admin.do("PATCH", "/api/admin/users/"+me.ID, patch); r.Status != 400 || !strings.Contains(string(r.Body), "self_demote") {
			t.Fatalf("self %v = %d %s", patch, r.Status, r.Body)
		}
	}
	var after UserView
	admin.must(200, "GET", "/api/me", nil, &after)
	if after.Role != "admin" || !after.Active {
		t.Fatalf("self changed %+v", after)
	}
}
