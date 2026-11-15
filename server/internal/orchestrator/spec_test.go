package orchestrator

import (
	"errors"
	"strings"
	"testing"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

func emp(tier, role string, spec store.Role) core.Employee {
	tid := "th1"
	e := core.Employee{ID: "em_1", ProjectID: "p1", Name: role + "-1", Tier: tier, Role: role, Runtime: "claude", Spec: spec}
	if tier != "coordinator" {
		e.ThreadID = &tid
	}
	if tier == "lead" {
		e.Name = "lead-3"
	}
	return e
}

func open(t *testing.T, e core.Employee, n store.Node) sessionInput {
	t.Helper()
	th := &core.Thread{ID: "th1", No: 3, Branch: "thread/3-x"}
	if e.Tier == "coordinator" {
		th = nil
	}
	return sessionInput{emp: e, thread: th, node: n, sess: core.AgentSession{ID: "s_1", OpenGen: 1, Cwd: PathsOf(e, th).Cwd},
		system: "sys", hasRepo: true, repos: []string{"o/api", "o/web"}, rules: []string{"Bash(pnpm add:*)"},
		prepare: []string{"g_1", "g_2"}}
}

func TestBuildOpenPerTier(t *testing.T) {
	coord, err := buildOpen(open(t, emp("coordinator", "coordinator", store.Role{}), store.Node{Isolation: "sandbox"}))
	if err != nil {
		t.Fatal(err)
	}
	c := coord.Claude
	if len(c.Tools) != 0 || c.PermissionMode != "dontAsk" || !*c.DenyNonPlatformTools || *coord.GitCredentials ||
		coord.Cwd != "projects/p1/coordinator" || len(c.SettingSources) != 0 || c.Model != DefaultModel {
		t.Fatalf("coordinator %+v %+v", coord, c)
	}
	lead, _ := buildOpen(open(t, emp("lead", "lead", store.Role{}), store.Node{Isolation: "sandbox"}))
	l := lead.Claude
	if !*l.ReadOnly || l.Sandbox == nil || len(l.Sandbox.WritePaths) != 0 || !contains(l.DisallowedTools, "Write") ||
		!contains(l.AllowedTools, "Read") || lead.Cwd != "projects/p1/threads/3/lead" || l.PermissionMode != "dontAsk" {
		t.Fatalf("lead %+v", l)
	}
	dev, _ := buildOpen(open(t, emp("member", "dev", store.Role{SkipPermissions: true, CanDelegate: true}), store.Node{Isolation: "sandbox"}))
	d := dev.Claude
	if d.PermissionMode != "default" || !contains(d.AllowedTools, "Bash(pnpm add:*)") || dev.Cwd != "projects/p1/threads/3/dev-1" ||
		d.Sandbox == nil || len(d.Sandbox.ExtraWritePaths) != 2 || d.Sandbox.ExtraWritePaths[1] != "projects/p1/repos/o__web/.git" {
		t.Fatalf("dev on a sandbox node must not bypass permissions: %+v", d)
	}
	names := []string{}
	for _, p := range dev.PlatformTools {
		names = append(names, p.Name)
	}
	if !contains(names, "assign_subtask") || !contains(names, "complete_task") || contains(names, "open_thread") {
		t.Fatalf("member tools %v", names)
	}
	isolated, _ := buildOpen(open(t, emp("member", "dev", store.Role{SkipPermissions: true}), store.Node{Isolation: "agent_user"}))
	if isolated.Claude.PermissionMode != "bypassPermissions" || !*isolated.Claude.AllowDangerouslySkipPermissions {
		t.Fatalf("skip permissions on agent_user node: %+v", isolated.Claude)
	}
}

func TestCodexOptions(t *testing.T) {
	e := emp("member", "qa", store.Role{})
	e.Runtime = "codex"
	o, _ := buildOpen(open(t, e, store.Node{Isolation: "agent_user"}))
	if o.Codex == nil || o.Codex.Sandbox != "workspace-write" || o.Codex.ApprovalPolicy != "on-request" ||
		!strings.Contains(string(*o.Codex.Config), "projects/p1/repos/o__web/.git") {
		t.Fatalf("codex member %+v", o.Codex)
	}
	ro := true
	e.Spec.ReadOnly = &ro
	o, _ = buildOpen(open(t, e, store.Node{Isolation: "agent_user"}))
	if o.Codex.Sandbox != "read-only" || o.Codex.ApprovalPolicy != "never" {
		t.Fatalf("read-only codex %+v", o.Codex)
	}
}

func TestToolsAllowed(t *testing.T) {
	if ok, bad := ToolsAllowed(emp("coordinator", "coordinator", store.Role{}), []string{"mcp__platform__open_thread", "Read"}); ok || bad != "Read" {
		t.Fatal("coordinator may only have platform tools")
	}
	if ok, _ := ToolsAllowed(emp("lead", "lead", store.Role{}), []string{"Read", "Bash", "mcp__platform__assign_task"}); !ok {
		t.Fatal("lead read tools are fine")
	}
	if ok, bad := ToolsAllowed(emp("lead", "lead", store.Role{}), []string{"Edit"}); ok || bad != "Edit" {
		t.Fatal("lead must not edit")
	}
	if ok, _ := ToolsAllowed(emp("member", "dev", store.Role{}), []string{"Edit", "Write"}); !ok {
		t.Fatal("members edit")
	}
}

func TestPaths(t *testing.T) {
	th := &core.Thread{No: 12, Branch: "thread/12-comms"}
	p := PathsOf(emp("member", "dev", store.Role{}), th)
	if p.Branch != "thread/12/dev-1" || p.Cwd != "projects/p1/threads/12/dev-1" || p.RepoCwd("o/web") != "projects/p1/threads/12/dev-1/o__web" {
		t.Fatalf("paths %+v", p)
	}
	if SharedDir("p1", "o/web") != "projects/p1/repos/o__web" || LegacySharedDir("p1") != "projects/p1/repo" {
		t.Fatal("shared clone directories")
	}
	lead := PathsOf(emp("lead", "lead", store.Role{}), th)
	if lead.Branch != "thread/12-comms" || lead.RepoCwd("o/api") != "projects/p1/threads/12/lead/o__api" {
		t.Fatalf("lead paths %+v", lead)
	}
}

func TestPrepareJobsInOpen(t *testing.T) {
	o, err := buildOpen(open(t, emp("member", "dev", store.Role{}), store.Node{Isolation: "sandbox"}))
	if err != nil {
		t.Fatal(err)
	}
	if o.Prepare == nil || *o.Prepare != "g_1" || len(o.PrepareJobs) != 2 || o.PrepareJobs[1] != "g_2" {
		t.Fatalf("prepare %v %v", o.Prepare, o.PrepareJobs)
	}
	c := codexOptions(open(t, emp("member", "dev", store.Role{Runtime: "codex"}), store.Node{Isolation: "sandbox"}))
	if c.Config == nil || !strings.Contains(string(*c.Config), "projects/p1/repos/o__api/.git") {
		t.Fatalf("codex writable roots %v", c.Config)
	}
}

func TestMatchRepo(t *testing.T) {
	p := store.Project{Repos: []store.ProjectRepo{{Repo: "Org/API"}, {Repo: "org/web"}}}
	if got, err := matchRepo(p, " org/api.git "); err != nil || got != "Org/API" {
		t.Fatalf("match %q %v", got, err)
	}
	if _, err := matchRepo(p, "org/other"); !errors.Is(err, ErrUnknownRepo) {
		t.Fatalf("unknown repo %v", err)
	}
	if focusText("org/web", " dev ") != "org/web（基准分支 dev）" || focusText("org/web", "") != "org/web" {
		t.Fatal("focus text")
	}
}
