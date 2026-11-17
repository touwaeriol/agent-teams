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

// newAcceptCase: status is the thread's status ("review" for a PR awaiting acceptance). Without
// require_acceptance the merge loop also merges the open PRs of a thread in review by itself, as
// in production: the tests below run Accept's background merge next to it.
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

func (c *acceptCase) set(fn func(gh *fakeGitHub)) {
	c.gh.mu.Lock()
	defer c.gh.mu.Unlock()
	fn(c.gh)
}

func (c *acceptCase) reject(t *testing.T) core.ThreadView {
	t.Helper()
	var v core.ThreadView
	c.b.must(200, "POST", "/api/threads/"+c.thread.ID+"/reject", map[string]string{"reason": "再改改"}, &v)
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
// checks and merges in the background (a slow GitHub no longer holds the request). The merge
// loop, which auto-merges open PRs of threads in review, runs next to it and repeated clicks
// arrive while it merges: the PR is still merged exactly once.
func TestAcceptAsync(t *testing.T) {
	c := newAcceptCase(t, false, "review")
	c.setLatency(500 * time.Millisecond) // the background merge makes ≥ 4 GitHub calls: ≥ 2 s
	var seen int
	c.set(func(gh *fakeGitHub) { seen = gh.requests })
	// Accept while a loop step is reading the PR: both reach the merge at about the same time.
	eventually(t, "merge loop reading the PR", func() bool {
		n := 0
		c.set(func(gh *fakeGitHub) { n = gh.requests })
		return n > seen
	})
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
	time.Sleep(time.Second) // a loop step that started before accept would have merged by now
	if n := c.mergeCalls(); n != 1 {
		t.Fatalf("PR merged %d times", n)
	}
	if pr, _ := c.gh.prOf("o/r", c.thread.Branch); !pr.merged {
		t.Fatal("PR not merged on GitHub")
	}
}

// TestAcceptNotInReview: only a thread in review can be accepted.
func TestAcceptNotInReview(t *testing.T) {
	c := newAcceptCase(t, false, "running")
	if code, raw := c.b.do("POST", "/api/threads/"+c.thread.ID+"/accept", nil); code != 409 || !strings.Contains(string(raw), "not_in_review") {
		t.Fatalf("accept outside review: %d %s", code, raw)
	}
	if v := c.view(t); v.PRState != "open" || c.mergeCalls() != 0 {
		t.Fatalf("thread changed: %q", v.PRState)
	}
}

// TestAcceptMergeFails: GitHub refusing the merge (405) puts the PR back to open with the reason
// in merge_error; accepting again clears it and, once GitHub allows it, merges.
func TestAcceptMergeFails(t *testing.T) {
	c := newAcceptCase(t, false, "review")
	key := "o/r#" + itoa(c.pr)
	c.set(func(gh *fakeGitHub) { gh.mergeFail[key] = 405 })
	c.accept(t)
	v := c.waitFor(t, "merge failed", func(v core.ThreadView) bool { return v.PRState == "open" })
	if !strings.Contains(mergeError(v), "405") || !strings.Contains(mergeError(v), "o/r#") || v.Repos[0].PRState != "open" {
		t.Fatalf("merge_error %q repos %+v", mergeError(v), v.Repos)
	}
	c.events.wait(t, "thread", `"merge_error":"合并失败`)

	c.set(func(gh *fakeGitHub) {
		delete(gh.mergeFail, key)
		gh.latency = 300 * time.Millisecond // the loop's own auto-merge needs ≥ 0.9 s: accept comes first
	})
	v, _ = c.accept(t)
	if v.PRState != "accepting" || v.MergeError != nil {
		t.Fatalf("retry: %q %v", v.PRState, v.MergeError)
	}
	v = c.waitFor(t, "merged on retry", func(v core.ThreadView) bool { return v.Status == "done" })
	if v.PRState != "merged" || v.MergeError != nil {
		t.Fatalf("after retry: %q %v", v.PRState, v.MergeError)
	}
}

// TestAcceptChecksPending: checks still running are reported (no 409); the PR merges by itself later.
func TestAcceptChecksPending(t *testing.T) {
	c := newAcceptCase(t, false, "review")
	c.set(func(gh *fakeGitHub) {
		gh.checks["sha-thread"] = []map[string]any{{"name": "verify", "status": "in_progress"}}
	})
	c.accept(t)
	v := c.waitFor(t, "pending reported", func(v core.ThreadView) bool { return v.PRState == "open" })
	if !strings.Contains(mergeError(v), "通过后会自动合并") || c.mergeCalls() != 0 {
		t.Fatalf("pending: %q", mergeError(v))
	}
}

// TestAcceptRateLimited: a rate-limited merge (429) is no failure: the thread stays accepting
// without merge_error, the merge loop retries and merges once GitHub lets it.
func TestAcceptRateLimited(t *testing.T) {
	c := newAcceptCase(t, false, "review")
	key := "o/r#" + itoa(c.pr)
	c.set(func(gh *fakeGitHub) { gh.mergeFail[key] = 429 })
	c.accept(t)
	eventually(t, "loop retries", func() bool { return c.mergeCalls() >= 3 })
	if v := c.view(t); v.PRState != "accepting" || v.MergeError != nil {
		t.Fatalf("rate limited: %q %q", v.PRState, mergeError(v))
	}
	c.set(func(gh *fakeGitHub) { delete(gh.mergeFail, key) })
	v := c.waitFor(t, "merged after the rate limit", func(v core.ThreadView) bool { return v.Status == "done" })
	if v.PRState != "merged" || v.MergeError != nil {
		t.Fatalf("after rate limit: %q %v", v.PRState, v.MergeError)
	}
}

// TestAcceptRejectStopsMerge: a reject while the background merge reads the checks stops it
// before anything is merged.
func TestAcceptRejectStopsMerge(t *testing.T) {
	c := newAcceptCase(t, false, "review")
	c.setLatency(400 * time.Millisecond)
	c.accept(t)
	if v := c.reject(t); v.Status != "running" || v.PRState != "open" {
		t.Fatalf("reject: %s %q", v.Status, v.PRState)
	}
	time.Sleep(3 * time.Second) // the background merge and a loop step in flight both get past their checks
	v := c.view(t)
	if n := c.mergeCalls(); n != 0 || v.Status != "running" || v.PRState != "open" || v.MergeError != nil {
		t.Fatalf("after reject: merges %d, %s %q %q", n, v.Status, v.PRState, mergeError(v))
	}
}

// TestAcceptRejectKeepsResult: a merge already in flight when the user rejects fails afterwards;
// its failure does not overwrite the reject (no merge_error, still running and open).
func TestAcceptRejectKeepsResult(t *testing.T) {
	c := newAcceptCase(t, false, "review")
	c.set(func(gh *fakeGitHub) {
		gh.mergeFail["o/r#"+itoa(c.pr)] = 405
		gh.mergeDelay = time.Second
	})
	c.accept(t)
	time.Sleep(200 * time.Millisecond)
	if v := c.reject(t); v.Status != "running" {
		t.Fatalf("reject: %s", v.Status)
	}
	time.Sleep(1500 * time.Millisecond)
	if v := c.view(t); v.Status != "running" || v.PRState != "open" || v.MergeError != nil {
		t.Fatalf("merge failure overwrote the reject: %s %q %q", v.Status, v.PRState, mergeError(v))
	}
}

// TestAcceptTimeout: a background merge that runs out of time says so in merge_error.
func TestAcceptTimeout(t *testing.T) {
	c := newAcceptCase(t, false, "review")
	c.w.e.Delivery.AcceptTimeout = 300 * time.Millisecond
	c.setLatency(time.Second)
	c.accept(t)
	v := c.waitFor(t, "timeout reported", func(v core.ThreadView) bool { return v.MergeError != nil })
	if mergeError(v) != "合并超时，请稍后重试" || v.PRState != "open" {
		t.Fatalf("timeout: %q %q", v.PRState, mergeError(v))
	}
}

// TestAcceptLoopMergeFails: with require_acceptance the merge loop merges; a merge GitHub
// refuses (403) is reported on the thread (open + merge_error) instead of retried silently, and
// rejecting clears it.
func TestAcceptLoopMergeFails(t *testing.T) {
	c := newAcceptCase(t, true, "review")
	c.set(func(gh *fakeGitHub) { gh.mergeFail["o/r#"+itoa(c.pr)] = 403 })
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
	v = c.reject(t)
	t.Logf("reject answered in %v", time.Since(start))
	if v.MergeError != nil || v.Status != "running" {
		t.Fatalf("reject: %s %v", v.Status, v.MergeError)
	}
}

// TestAcceptLoopChecksFail: with require_acceptance failed checks reach the user too.
func TestAcceptLoopChecksFail(t *testing.T) {
	c := newAcceptCase(t, true, "review")
	c.set(func(gh *fakeGitHub) {
		gh.checks["sha-thread"] = []map[string]any{{"name": "verify", "status": "completed", "conclusion": "failure"}}
	})
	c.accept(t)
	v := c.waitFor(t, "checks failure reported", func(v core.ThreadView) bool { return v.PRState == "open" })
	if !strings.Contains(mergeError(v), "verify：failure") || c.mergeCalls() != 0 {
		t.Fatalf("checks failure: %q", mergeError(v))
	}
}
