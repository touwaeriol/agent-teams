package delivery

import (
	"testing"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/core"
)

func check(name, status, conclusion string) core.CheckView {
	return core.CheckView{Name: name, Status: status, Conclusion: conclusion}
}

func TestEvaluate(t *testing.T) {
	rules := CIRules{Mode: "checks", AppearTimeout: 5 * time.Minute, Timeout: time.Hour, OnMissing: "pass"}
	cases := []struct {
		name   string
		checks []core.CheckView
		rules  CIRules
		waited time.Duration
		want   Verdict
	}{
		{"off", nil, CIRules{Mode: "off"}, 0, CIPass},
		{"none yet", nil, rules, time.Minute, CIPending},
		{"none ever, pass", nil, rules, 10 * time.Minute, CIPass},
		{"none ever, block", nil, CIRules{Mode: "checks", AppearTimeout: time.Minute, OnMissing: "block"}, 2 * time.Minute, CIMissing},
		{"all green", []core.CheckView{check("verify", "completed", "success"), check("lint", "completed", "skipped")}, rules, 0, CIPass},
		{"one red", []core.CheckView{check("verify", "completed", "failure"), check("lint", "in_progress", "")}, rules, 0, CIFail},
		{"running", []core.CheckView{check("verify", "in_progress", "")}, rules, time.Minute, CIPending},
		{"timeout", []core.CheckView{check("verify", "queued", "")}, rules, 2 * time.Hour, CIFail},
	}
	for _, c := range cases {
		if got, _ := Evaluate(c.checks, c.rules, c.waited); got != c.want {
			t.Errorf("%s: got %d want %d", c.name, got, c.want)
		}
	}
}

func TestEvaluateRequired(t *testing.T) {
	rules := CIRules{Mode: "checks", Required: []string{"verify"}, AppearTimeout: time.Minute, Timeout: time.Hour}
	optionalRed := []core.CheckView{check("verify", "completed", "success"), check("flaky", "completed", "failure")}
	if v, _ := Evaluate(optionalRed, rules, 0); v != CIPass {
		t.Fatalf("a failing optional check must not block: %d", v)
	}
	missingRequired := []core.CheckView{check("lint", "completed", "success")}
	if v, _ := Evaluate(missingRequired, rules, 10*time.Minute); v != CIPending {
		t.Fatalf("required check not reported yet: %d", v)
	}
	if v, failed := Evaluate([]core.CheckView{check("verify", "completed", "timed_out")}, rules, 0); v != CIFail || len(failed) != 1 {
		t.Fatalf("required red: %d %v", v, failed)
	}
}

func TestGlobAndCoverage(t *testing.T) {
	cases := map[[2]string]bool{
		{"thread/**", "thread/12/dev-1"}: true, {"thread/*", "thread/12/dev-1"}: false, {"thread/*", "thread/12-x"}: true,
		{"mq/**", "mq/12/mq_a"}: true, {"main", "main"}: true, {"**", "anything/at/all"}: true, {"release/*", "main"}: false,
	}
	for in, want := range cases {
		if got := globMatch(in[0], in[1]); got != want {
			t.Errorf("glob %q %q = %v", in[0], in[1], got)
		}
	}
	w := parseWorkflow("ci.yml", "on:\n  push:\n    branches: [main, 'thread/**']\n  pull_request: {}\njobs: {}\n")
	if !covers(w, "thread/12-x", "thread/12/dev-1") || covers(w, "mq/1/x") {
		t.Fatalf("coverage %+v", w)
	}
	all := parseWorkflow("all.yml", "on: [push]\n")
	if !covers(all, "mq/1/x") {
		t.Fatalf("push without filters covers every branch: %+v", all)
	}
}

func TestAddPushBranches(t *testing.T) {
	out, ok := addPushBranches("name: ci\non:\n  push:\n    branches:\n      - main\njobs:\n  t: {runs-on: ubuntu-latest}\n")
	if !ok || !covers(parseWorkflow("x", out), "thread/1-a", "thread/1/dev-1", "mq/1/x") {
		t.Fatalf("not extended:\n%s", out)
	}
	if _, ok := addPushBranches("on: [push]\n"); ok {
		t.Fatal("a workflow without a branch filter needs no change")
	}
}
