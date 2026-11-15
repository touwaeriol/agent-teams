package toolsvc

import (
	"testing"

	protocol "github.com/touwaeriol/agent-teams/protocol"

	"github.com/touwaeriol/agent-teams/server/internal/delivery"
)

func TestPushedReposFromOutcomes(t *testing.T) {
	outcomes := []delivery.PushOutcome{{Repo: "o/api", Done: true, OK: true, Unchanged: true, SHA: "base"},
		{Repo: "o/web", Done: true, OK: true, SHA: "w1"}}
	pushed, first := pushedRepos(outcomes)
	if first != "w1" || len(pushed) != 1 || pushed["o/web"] != "w1" {
		t.Fatalf("pushed %v first %s", pushed, first)
	}
	none, first := pushedRepos(outcomes[:1])
	if first != "" || none == nil || len(none) != 0 {
		t.Fatalf("nothing pushed must record an empty set: %v %q", none, first)
	}
	legacy, first := pushedRepos([]delivery.PushOutcome{{Done: true, OK: true, SHA: "s"}})
	if legacy != nil || first != "s" {
		t.Fatalf("legacy single push %v %q", legacy, first)
	}
	if list := pushedList(map[string]string{"b/b": "2", "a/a": "1"}); len(list) != 2 || list[0].Repo != "a/a" {
		t.Fatalf("list %+v", list)
	}
}

func TestFirstFailureNamesRepo(t *testing.T) {
	code := protocol.GitResultErrorCode("rejected_non_ff")
	outcomes := []delivery.PushOutcome{{Repo: "o/api", OK: true}, {Repo: "o/web", Result: protocol.GitResult{ErrorCode: &code}}}
	f, ok := firstFailure(outcomes)
	if !ok || f.Repo != "o/web" {
		t.Fatalf("failure %+v", f)
	}
	if text := pushFailText(f, 2); text[:len("仓库 o/web：")] != "仓库 o/web：" {
		t.Fatalf("text %q", text)
	}
}
