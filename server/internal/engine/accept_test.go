package engine_test

import (
	"strings"
	"testing"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw/nodetest"
)

// acceptCase is a thread in review with one open pull request in o/r (no node: the PR rows are
// written directly, the way open_pr records them).
type acceptCase struct {
	w      *world
	gh     *fakeGitHub
	b      *browser
	thread core.ThreadView
	pr     int
	events *sse
}

// newAcceptCase: status is the thread's status. Without require_acceptance the merge loop merges
// the open PRs of a thread in review by itself; "running" keeps it out of the way, so only Accept merges.
func newAcceptCase(t *testing.T, requireAcceptance bool, status string) *acceptCase {
	t.Helper()
	gh, ghSrv := newFakeGitHub(t)
	w := newWorld(t, ghSrv)
	w.signUp("admin-placeholder@example.com")
	b := w.signUp("accept@example.com")
	var node nodeCreated
	b.must(201, "POST", "/api/nodes", map[string]string{"name": "fake"}, &node)
	w.connect(node.Token, nodetest.Options{}) // the lead is told about rejects and failed checks
	var conn struct{ ID string }
	b.must(201, "POST", "/api/git/connections", map[string]string{"name": "gh", "token": "ghp_good"}, &conn)
	var project struct{ ID string }
	b.must(201, "POST", "/api/projects", map[string]any{"name": "acc", "require_acceptance": requireAcceptance,
		"repos": []map[string]any{{"repo": "o/r", "git_connection_id": conn.ID}}}, &project)
	c := &acceptCase{w: w, gh: gh, b: b}
	c.events = b.stream("/api/projects/"+project.ID+"/events?topics=thread", "")
	b.must(201, "POST", "/api/projects/"+project.ID+"/threads", map[string]any{"title": "Acc", "brief": "验收"}, &c.thread)
	gh.setRef(c.thread.Branch, "sha-thread")
	c.pr = gh.openPRByHand("o/r", c.thread.Branch, "main")
	url := prURL("o/r", c.pr)
	err := w.e.Core.InTx(w.ctx, func(tx *core.Tx) error {
		if err := core.MarkThreadBranch(w.ctx, tx, c.thread.ID, "o/r", "main"); err != nil {
			return err
		}
		if err := core.SetThreadRepoPR(w.ctx, tx, c.thread.ID, "o/r", c.pr, url); err != nil {
			return err
		}
		return core.UpdateThread(w.ctx, tx, c.thread.ID, map[string]any{"status": status, "pr_number": c.pr, "pr_url": url,
			"pr_state": "open"})
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (c *acceptCase) view(t *testing.T) core.ThreadView {
	t.Helper()
	var v core.ThreadView
	c.b.must(200, "GET", "/api/threads/"+c.thread.ID, nil, &v)
	return v
}

func (c *acceptCase) accept(t *testing.T) (core.ThreadView, time.Duration) {
	t.Helper()
	var v core.ThreadView
	start := time.Now()
	c.b.must(200, "POST", "/api/threads/"+c.thread.ID+"/accept", nil, &v)
	return v, time.Since(start)
}

func (c *acceptCase) setLatency(d time.Duration) {
	c.gh.mu.Lock()
	defer c.gh.mu.Unlock()
	c.gh.latency = d
}

func (c *acceptCase) mergeCalls() int {
	c.gh.mu.Lock()
	defer c.gh.mu.Unlock()
	return c.gh.mergeCalls
}

// waitFor polls the thread until cond holds.
func (c *acceptCase) waitFor(t *testing.T, what string, cond func(v core.ThreadView) bool) core.ThreadView {
	t.Helper()
	var v core.ThreadView
	eventually(t, what, func() bool { v = c.view(t); return cond(v) })
	return v
}

func mergeError(v core.ThreadView) string {
	if v.MergeError == nil {
		return ""
	}
	return *v.MergeError
}

// TestAcceptAsync: without require_acceptance, accept answers at once with pr_state accepting and
// checks and merges in the background (a slow GitHub no longer holds the request); repeated
// clicks while it runs start no second merge.
func TestAcceptAsync(t *testing.T) {
	c := newAcceptCase(t, false, "running")
	c.setLatency(500 * time.Millisecond) // the background merge makes ≥ 4 GitHub calls: ≥ 2 s
	v, took := c.accept(t)
	t.Logf("accept answered in %v with GitHub at 500 ms a request", took)
	if v.PRState != "accepting" || v.MergeError != nil || took > 400*time.Millisecond {
		t.Fatalf("accept: pr_state %q merge_error %v after %v", v.PRState, v.MergeError, took)
	}
	c.events.wait(t, "thread", `"pr_state":"accepting"`)
	again, took := c.accept(t)
	if again.PRState != "accepting" || took > 400*time.Millisecond {
		t.Fatalf("second accept: %q after %v", again.PRState, took)
	}
	v = c.waitFor(t, "merged", func(v core.ThreadView) bool { return v.Status == "done" })
	if v.PRState != "merged" || v.MergeError != nil || len(v.Repos) != 1 || v.Repos[0].PRState != "merged" {
		t.Fatalf("after merge: %s %q %v %+v", v.Status, v.PRState, v.MergeError, v.Repos)
	}
	if n := c.mergeCalls(); n != 1 {
		t.Fatalf("PR merged %d times", n)
	}
	if pr, _ := c.gh.prOf("o/r", c.thread.Branch); !pr.merged {
		t.Fatal("PR not merged on GitHub")
	}
}

// TestAcceptMergeFails: GitHub refusing the merge (405) puts the PR back to open with the reason
// in merge_error; accepting again clears it and, once GitHub allows it, merges.
func TestAcceptMergeFails(t *testing.T) {
	c := newAcceptCase(t, false, "running")
	key := "o/r#" + itoa(c.pr)
	c.gh.mu.Lock()
	c.gh.mergeFail[key] = 405
	c.gh.mu.Unlock()
	c.accept(t)
	v := c.waitFor(t, "merge failed", func(v core.ThreadView) bool { return v.PRState == "open" })
	if !strings.Contains(mergeError(v), "405") || !strings.Contains(mergeError(v), "o/r#") || v.Repos[0].PRState != "open" {
		t.Fatalf("merge_error %q repos %+v", mergeError(v), v.Repos)
	}
	c.events.wait(t, "thread", `"merge_error":"合并失败`)

	c.gh.mu.Lock()
	delete(c.gh.mergeFail, key)
	c.gh.latency = 300 * time.Millisecond
	c.gh.mu.Unlock()
	v, _ = c.accept(t)
	if v.PRState != "accepting" || v.MergeError != nil {
		t.Fatalf("retry: %q %v", v.PRState, v.MergeError)
	}
	v = c.waitFor(t, "merged on retry", func(v core.ThreadView) bool { return v.Status == "done" })
	if v.PRState != "merged" || v.MergeError != nil {
		t.Fatalf("after retry: %q %v", v.PRState, v.MergeError)
	}
}

// TestAcceptChecksNotPassed: checks still running or failed are reported, not a 409.
func TestAcceptChecksNotPassed(t *testing.T) {
	c := newAcceptCase(t, false, "running")
	c.gh.mu.Lock()
	c.gh.checks["sha-thread"] = []map[string]any{{"name": "verify", "status": "in_progress"}}
	c.gh.mu.Unlock()
	c.accept(t)
	v := c.waitFor(t, "pending reported", func(v core.ThreadView) bool { return v.PRState == "open" })
	if !strings.Contains(mergeError(v), "还没有全部通过") {
		t.Fatalf("pending: %q", mergeError(v))
	}
	c.gh.mu.Lock()
	c.gh.checks["sha-thread"] = []map[string]any{{"name": "verify", "status": "completed", "conclusion": "failure"}}
	c.gh.mu.Unlock()
	c.accept(t)
	v = c.waitFor(t, "failure reported", func(v core.ThreadView) bool {
		return v.PRState == "open" && strings.Contains(mergeError(v), "没有通过")
	})
	if !strings.Contains(mergeError(v), "o/r · verify：failure") || c.mergeCalls() != 0 {
		t.Fatalf("failure: %q merges %d", mergeError(v), c.mergeCalls())
	}
}

// TestAcceptLoopMergeFails: with require_acceptance the merge loop merges; a merge GitHub
// refuses (403) is reported on the thread (open + merge_error) instead of retried silently, and
// rejecting clears it.
func TestAcceptLoopMergeFails(t *testing.T) {
	c := newAcceptCase(t, true, "review")
	c.gh.mu.Lock()
	c.gh.mergeFail["o/r#"+itoa(c.pr)] = 403
	c.gh.mu.Unlock()
	v, _ := c.accept(t)
	if v.PRState != "accepting" {
		t.Fatalf("accept: %q", v.PRState)
	}
	v = c.waitFor(t, "loop failure reported", func(v core.ThreadView) bool { return v.PRState == "open" })
	if !strings.Contains(mergeError(v), "403") || v.Status != "review" {
		t.Fatalf("loop failure: %s %q", v.Status, mergeError(v))
	}
	time.Sleep(600 * time.Millisecond) // a few loop ticks: an open PR is not merged again
	if n := c.mergeCalls(); n != 1 {
		t.Fatalf("merge retried %d times", n)
	}
	start := time.Now()
	c.b.must(200, "POST", "/api/threads/"+c.thread.ID+"/reject", map[string]string{"reason": "再改改"}, &v)
	t.Logf("reject answered in %v", time.Since(start))
	if v.MergeError != nil || v.Status != "running" {
		t.Fatalf("reject: %s %v", v.Status, v.MergeError)
	}
}

// TestAcceptLoopChecksFail: with require_acceptance failed checks reach the user too.
func TestAcceptLoopChecksFail(t *testing.T) {
	c := newAcceptCase(t, true, "review")
	c.gh.mu.Lock()
	c.gh.checks["sha-thread"] = []map[string]any{{"name": "verify", "status": "completed", "conclusion": "failure"}}
	c.gh.mu.Unlock()
	c.accept(t)
	v := c.waitFor(t, "checks failure reported", func(v core.ThreadView) bool { return v.PRState == "open" })
	if !strings.Contains(mergeError(v), "verify：failure") || c.mergeCalls() != 0 {
		t.Fatalf("checks failure: %q", mergeError(v))
	}
}
