package delivery

import (
	"testing"

	protocol "github.com/touwaeriol/agent-teams/protocol"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

func TestHeadsByRepo(t *testing.T) {
	q := []core.MergeEntry{{ID: "1", Repo: "o/a"}, {ID: "2", Repo: "o/a"}, {ID: "3", Repo: "o/b"}, {ID: "4", Repo: "o/b"}}
	heads := headsByRepo(q)
	if len(heads) != 2 || heads[0].ID != "1" || heads[1].ID != "3" {
		t.Fatalf("heads %+v", heads)
	}
}

func TestWorseVerdict(t *testing.T) {
	cases := []struct{ a, b, want Verdict }{
		{CIPass, CIPass, CIPass}, {CIPass, CIPending, CIPending}, {CIPending, CIFail, CIFail},
		{CIFail, CIPending, CIFail}, {CIMissing, CIPass, CIMissing},
	}
	for _, c := range cases {
		if got := worse(c.a, c.b); got != c.want {
			t.Errorf("worse(%v, %v) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestBranchedWithoutPR(t *testing.T) {
	n := 3
	rows := []core.ThreadRepo{{Repo: "o/a", BranchCreated: true}, {Repo: "o/b", BranchCreated: true, PRNumber: &n},
		{Repo: "o/c", BaseBranch: "dev"}}
	pending, withPR := branchedWithoutPR(rows, "")
	if len(pending) != 1 || pending[0].Repo != "o/a" || !withPR {
		t.Fatalf("pending %+v withPR %v", pending, withPR)
	}
	if pending, withPR = branchedWithoutPR(rows, "O/B"); len(pending) != 0 || !withPR {
		t.Fatalf("only o/b: %+v %v", pending, withPR)
	}
}

func TestByShared(t *testing.T) {
	p := store.Project{ID: "p1", Repos: []store.ProjectRepo{{Repo: "o/api"}, {Repo: "o/web"}}}
	got := byShared(p, []string{"projects/p1/threads/1/dev-1/o__api", "projects/p1/threads/1/dev-1/o__web",
		"projects/p1/threads/1/lead/o__api", "projects/p1/threads/1/old"})
	if len(got["projects/p1/repos/o__api"]) != 2 || len(got["projects/p1/repos/o__web"]) != 1 || len(got["projects/p1/repo"]) != 1 {
		t.Fatalf("by shared %v", got)
	}
}

func TestOutcomeOf(t *testing.T) {
	o := outcomeOf(PushOutcome{Repo: "o/a"}, "done", []byte(`{"job_id":"g_1","ok":true,"unchanged":true,"head_sha":"abc"}`))
	if !o.Done || !o.OK || !o.Unchanged || o.SHA != "abc" {
		t.Fatalf("outcome %+v", o)
	}
	if o = outcomeOf(PushOutcome{Repo: "o/a"}, "sent", nil); o.Done {
		t.Fatal("pending job reported done")
	}
	fail := outcomeOf(PushOutcome{}, "failed", []byte(`{"job_id":"g_1","ok":false,"error_code":"not_a_repo","error":"x"}`))
	if !fail.Done || fail.OK || PushError(&fail.Result) == "" {
		t.Fatalf("failed outcome %+v", fail)
	}
	_ = protocol.GitResult{}
}

func TestPushedRepos(t *testing.T) {
	task := core.Task{Pushed: map[string]string{"o/web": "b", "o/api": "a"}}
	if got := pushedRepos(task); len(got) != 2 || got[0] != "o/api" {
		t.Fatalf("pushed %v", got)
	}
}
