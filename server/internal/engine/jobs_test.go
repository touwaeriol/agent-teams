package engine_test

import (
	"testing"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/orchapi"
)

// TestAdminJobs: the background jobs are listed for administrators only; one can be run now,
// paused and given another interval, and its runs are listed.
func TestAdminJobs(t *testing.T) {
	tm := startTeam(t) // tm.b registered first: an administrator
	user := tm.w.signUp("jobs-user@example.com")
	if code, _ := user.do("GET", "/api/admin/jobs", nil); code != 403 {
		t.Fatalf("non-admin list = %d", code)
	}
	var list []orchapi.JobView
	tm.b.must(200, "GET", "/api/admin/jobs", nil, &list)
	names := map[string]orchapi.JobView{}
	for _, j := range list {
		names[j.Name] = j
	}
	for _, n := range []string{"scheduler", "merge-queue", "timers", "approvals", "blobs"} {
		if names[n].Title == "" {
			t.Fatalf("job %s missing from %+v", n, list)
		}
	}
	if names["approvals"].Scope != "every" || names["scheduler"].Scope != "single" {
		t.Fatalf("scopes %+v", names)
	}

	var j orchapi.JobView
	tm.b.must(200, "PATCH", "/api/admin/jobs/blobs", map[string]any{"paused": true, "every_seconds": 300}, &j)
	if !j.Paused || j.IntervalSeconds != 300 || !j.Override || j.DefaultSeconds != 60 || j.NextRunAt != nil {
		t.Fatalf("patched %+v", j)
	}
	tm.b.must(200, "POST", "/api/admin/jobs/blobs/run", nil, &j)
	if !j.RunRequested && j.LastStartedAt == nil {
		t.Fatalf("run not asked for %+v", j)
	}
	var runs []map[string]any
	deadline := time.Now().Add(10 * time.Second)
	for len(runs) == 0 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		tm.b.must(200, "GET", "/api/admin/jobs/blobs/runs", nil, &runs)
	}
	if len(runs) == 0 || runs[0]["trigger"] != "manual" {
		t.Fatalf("runs %v", runs)
	}
	tm.b.must(200, "PATCH", "/api/admin/jobs/blobs", map[string]any{"paused": false, "every_seconds": 0}, &j)
	if j.Paused || j.Override || j.IntervalSeconds != 60 {
		t.Fatalf("reset %+v", j)
	}
	tm.b.must(404, "POST", "/api/admin/jobs/nope/run", nil, nil)
	if code, _ := user.do("POST", "/api/admin/jobs/blobs/run", nil); code != 403 {
		t.Fatalf("non-admin run = %d", code)
	}
}
