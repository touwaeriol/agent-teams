package ghops

import (
	"testing"

	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// TestEmployeeScope: employee tokens are limited to the one repository and may push and open
// PRs/issues; workflows only when the project allows workflow edits; read stays read-only.
func TestEmployeeScope(t *testing.T) {
	r := Repo{Slug: "o/web", conn: store.GitConnection{Kind: "app", Permissions: map[string]string{"checks": "read"}}}
	emp := request(r, ScopeEmployee)
	if len(emp.Repositories) != 1 || emp.Repositories[0] != "web" {
		t.Fatalf("repositories %v", emp.Repositories)
	}
	want := map[string]string{"contents": "write", "pull_requests": "write", "issues": "write", "metadata": "read", "checks": "read"}
	if len(emp.Permissions) != len(want) {
		t.Fatalf("employee permissions %v", emp.Permissions)
	}
	for k, v := range want {
		if emp.Permissions[k] != v {
			t.Fatalf("employee permissions %v", emp.Permissions)
		}
	}
	if flows := request(r, ScopeEmployeeFlows); flows.Permissions["workflows"] != "write" {
		t.Fatalf("flows %v", flows.Permissions)
	}
	if read := request(r, ScopeRead); read.Permissions["contents"] != "read" || read.Permissions["pull_requests"] != "read" {
		t.Fatalf("read %v", read.Permissions)
	}
}
