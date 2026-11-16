package httpapi

import (
	"testing"
)

func TestAdminUsers(t *testing.T) {
	h := newHarness(t)
	admin, me := h.signUp("a@example.com")
	userClient, u := h.signUp("b@example.com")
	if r := userClient.do("GET", "/api/admin/users", nil); r.Status != 403 {
		t.Fatalf("non-admin = %d", r.Status)
	}
	var n createdNode
	userClient.must(201, "POST", "/api/nodes", map[string]string{"name": "n1"}, &n)
	var users []AdminUserView
	admin.must(200, "GET", "/api/admin/users", nil, &users)
	if len(users) != 2 || users[1].Nodes != 1 {
		t.Fatalf("users %+v", users)
	}
	if r := admin.do("PATCH", "/api/admin/users/"+me.ID, map[string]any{"active": false}); r.Status != 400 {
		t.Fatalf("self deactivate = %d", r.Status)
	}
	var updated UserView
	admin.must(200, "PATCH", "/api/admin/users/"+u.ID, map[string]any{"role": "admin", "max_running_threads": 5}, &updated)
	if updated.Role != "admin" {
		t.Fatalf("role %s", updated.Role)
	}
	admin.must(200, "PATCH", "/api/admin/users/"+u.ID, map[string]any{"active": false}, nil)
	if r := userClient.do("GET", "/api/me", nil); r.Status != 401 {
		t.Fatal("deactivated user still signed in")
	}
	if len(h.nodes.disconnected) != 1 || h.nodes.disconnected[0] != n.Node.ID {
		t.Fatalf("node links not dropped: %v", h.nodes.disconnected)
	}
	c := h.client()
	if r := c.do("POST", "/auth/login", Credentials{"b@example.com", "password123"}); r.Status != 403 {
		t.Fatalf("inactive login = %d", r.Status)
	}
	if r := admin.do("DELETE", "/api/admin/users/"+me.ID, nil); r.Status != 400 {
		t.Fatalf("self delete = %d", r.Status)
	}
	admin.must(204, "DELETE", "/api/admin/users/"+u.ID, nil, nil)
	if r := admin.do("DELETE", "/api/admin/users/"+u.ID, nil); r.Status != 404 {
		t.Fatalf("delete twice = %d", r.Status)
	}
	var audit struct {
		Items []map[string]any `json:"items"`
	}
	admin.must(200, "GET", "/api/admin/audit?action=role_change", nil, &audit)
	if len(audit.Items) != 1 {
		t.Fatalf("audit %+v", audit)
	}
}

func TestAdminSettings(t *testing.T) {
	h := newHarness(t)
	admin, _ := h.signUp("a@example.com")
	var s AdminSettings
	admin.must(200, "GET", "/api/admin/settings", nil, &s)
	if !s.RegistrationOpen || s.DefaultMaxNodes != 5 || s.MaxRunningThreads != 3 || s.OfficeDefault != "default" || s.NodeRelease != nil {
		t.Fatalf("defaults %+v", s)
	}
	s.DeliveryDefault.MergeMethod = "merge"
	s.MaxRunningThreads = 4
	admin.must(200, "PUT", "/api/admin/settings", s, &s)
	if s.DeliveryDefault.MergeMethod != "merge" || s.MaxRunningThreads != 4 {
		t.Fatalf("saved %+v", s)
	}
	var p ProjectView
	admin.must(201, "POST", "/api/projects", map[string]any{"name": "P"}, &p)
	if p.Delivery.MergeMethod != "merge" {
		t.Fatalf("new project ignores delivery default: %+v", p.Delivery)
	}
	s.OfficeDefault = "missing"
	if r := admin.do("PUT", "/api/admin/settings", s); r.Status != 422 {
		t.Fatalf("unknown layout = %d", r.Status)
	}
}
