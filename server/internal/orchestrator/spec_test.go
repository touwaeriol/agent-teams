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
	if strings.Join(c.Tools, ",") != "TodoWrite,AskUserQuestion" || c.PermissionMode != "dontAsk" || !*c.DenyNonPlatformTools || *coord.GitCredentials ||
		coord.Cwd != "projects/p1/coordinator" || len(c.SettingSources) != 0 || c.Model != DefaultModel {
		t.Fatalf("coordinator %+v %+v", coord, c)
	}
	lead, _ := buildOpen(open(t, emp("lead", "lead", store.Role{}), store.Node{Isolation: "sandbox"}))
	l := lead.Claude
	if !*l.ReadOnly || l.Sandbox == nil || len(l.Sandbox.WritePaths) != 0 || !contains(l.DisallowedTools, "Write") ||
		!contains(l.AllowedTools, "Read") || lead.Cwd != "projects/p1/threads/3/lead" || l.PermissionMode != "dontAsk" {
		t.Fatalf("lead %+v", l)
	}
	dev, _ := buildOpen(open(t, emp("member", "dev", store.Role{PermissionMode: store.PermBypass, CanDelegate: true}), store.Node{Isolation: "sandbox"}))
	d := dev.Claude
	if d.PermissionMode != "bypassPermissions" || !*d.AllowDangerouslySkipPermissions || !contains(d.AllowedTools, "Bash(pnpm add:*)") || dev.Cwd != "projects/p1/threads/3/dev-1" ||
		d.Sandbox == nil || len(d.Sandbox.ExtraWritePaths) != 2 || d.Sandbox.ExtraWritePaths[1] != "projects/p1/repos/o__web/.git" {
		t.Fatalf("dev on a sandbox node: %+v", d)
	}
	names := []string{}
	for _, p := range dev.PlatformTools {
		names = append(names, p.Name)
	}
	if !contains(names, "assign_subtask") || !contains(names, "complete_task") || contains(names, "open_thread") {
		t.Fatalf("member tools %v", names)
	}
	plain, _ := buildOpen(open(t, emp("member", "dev", store.Role{}), store.Node{Isolation: "none"}))
	if plain.Claude.PermissionMode != "bypassPermissions" || !*plain.Claude.AllowDangerouslySkipPermissions {
		t.Fatalf("bypass on a plain node: %+v", plain.Claude)
	}
	if strings.Join(d.Tools, ",") != "Read,Glob,Grep,Edit,Write,Bash,TodoWrite,AskUserQuestion,EnterPlanMode,ExitPlanMode" ||
		strings.Join(l.Tools, ",") != "Read,Glob,Grep,Bash,TodoWrite,AskUserQuestion" || !contains(l.AllowedTools, "AskUserQuestion") {
		t.Fatalf("chat tools: member %v, lead %v / %v", d.Tools, l.Tools, l.AllowedTools)
	}
	if ok, bad := ToolsAllowed(emp("coordinator", "coordinator", store.Role{}), []string{"mcp__platform__ask_user", "TodoWrite", "AskUserQuestion"}); !ok {
		t.Fatalf("coordinator chat tools refused: %s", bad)
	}
	if ok, _ := ToolsAllowed(emp("coordinator", "coordinator", store.Role{}), []string{"Bash"}); ok {
		t.Fatal("coordinator Bash must be refused")
	}
}

// TestClaudeModes: bypass (also the empty and legacy auto mode) on any node, ask is default;
// read-only members, the lead and the coordinator stay dontAsk whatever the mode.
func TestClaudeModes(t *testing.T) {
	ro := true
	cases := []struct {
		spec store.Role
		node store.Node
		want string
	}{
		{store.Role{PermissionMode: store.PermBypass}, store.Node{Isolation: "none"}, "bypassPermissions"},
		{store.Role{PermissionMode: store.PermBypass}, store.Node{Isolation: "sandbox"}, "bypassPermissions"},
		{store.Role{PermissionMode: store.PermLegacyAuto}, store.Node{Isolation: "none"}, "bypassPermissions"},
		{store.Role{}, store.Node{Isolation: "none"}, "bypassPermissions"},
		{store.Role{PermissionMode: store.PermAsk}, store.Node{Isolation: "agent_user"}, "default"},
		{store.Role{PermissionMode: store.PermBypass, ReadOnly: &ro}, store.Node{Isolation: "none"}, "dontAsk"},
	}
	for i, c := range cases {
		o, _ := buildOpen(open(t, emp("member", "dev", c.spec), c.node))
		if string(o.Claude.PermissionMode) != c.want {
			t.Errorf("case %d: mode %q, want %q", i, o.Claude.PermissionMode, c.want)
		}
	}
	lead, _ := buildOpen(open(t, emp("lead", "lead", store.Role{PermissionMode: store.PermBypass}), store.Node{Isolated: true}))
	if lead.Claude.PermissionMode != "dontAsk" {
		t.Fatalf("lead %q", lead.Claude.PermissionMode)
	}
}

func TestCodexOptions(t *testing.T) {
	e := emp("member", "qa", store.Role{PermissionMode: store.PermAsk})
	e.Runtime = "codex"
	o, _ := buildOpen(open(t, e, store.Node{Isolation: "agent_user"}))
	if o.Codex == nil || o.Codex.Sandbox != "workspace-write" || o.Codex.ApprovalPolicy != "untrusted" ||
		!strings.Contains(string(*o.Codex.Config), "projects/p1/repos/o__web/.git") {
		t.Fatalf("ask codex %+v", o.Codex)
	}
	e.Spec.PermissionMode = store.PermBypass
	for _, iso := range []string{"none", "sandbox", "agent_user"} {
		if o, _ = buildOpen(open(t, e, store.Node{Isolation: iso})); o.Codex.Sandbox != "danger-full-access" || o.Codex.ApprovalPolicy != "never" {
			t.Fatalf("bypass codex on %s %+v", iso, o.Codex)
		}
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
	c := codexOptions(open(t, emp("member", "dev", store.Role{Runtime: "codex", PermissionMode: store.PermAsk}), store.Node{Isolation: "sandbox"}))
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

// TestProjectModelOverrides: the project's coordinator model applies to the coordinator only;
// its thread model and effort override leads' and members' roles; ” keeps the role's.
func TestProjectModelOverrides(t *testing.T) {
	p := store.Project{CoordinatorModel: "claude-coord-x", ThreadModel: "claude-thread-y", ThreadEffort: "high"}
	in := open(t, emp("coordinator", "coordinator", store.Role{Model: "role-model", Effort: "low"}), store.Node{})
	in.project = p
	c := claudeOptions(in)
	if c.Model != "claude-coord-x" || c.Effort == nil || *c.Effort != "low" {
		t.Fatalf("coordinator %q %v", c.Model, c.Effort)
	}
	in = open(t, emp("member", "dev", store.Role{Model: "role-model", Effort: "low"}), store.Node{})
	in.project = p
	d := claudeOptions(in)
	if d.Model != "claude-thread-y" || d.Effort == nil || *d.Effort != "high" {
		t.Fatalf("member %q %v", d.Model, d.Effort)
	}
	in.project = store.Project{}
	d = claudeOptions(in)
	if d.Model != "role-model" || *d.Effort != "low" {
		t.Fatalf("role defaults %q %v", d.Model, *d.Effort)
	}
	in = open(t, emp("lead", "lead", store.Role{}), store.Node{})
	if l := claudeOptions(in); l.Model != DefaultModel || l.Effort != nil {
		t.Fatalf("lead default %q %v", l.Model, l.Effort)
	}
}

// TestModelPrecedence: /model on the employee, then the project override for its tier (Claude
// only), the role, the default of the runtime in the project owner's model list, DefaultModel.
func TestModelPrecedence(t *testing.T) {
	lead := emp("lead", "lead", store.Role{Model: "role-m"})
	p := store.Project{ThreadModel: "project-m", CoordinatorModel: "coord-m"}
	cases := []struct {
		e    core.Employee
		p    store.Project
		def  string
		want string
	}{
		{lead, p, "def", "project-m"},
		{func() core.Employee { e := lead; e.Model = "picked"; return e }(), p, "def", "picked"},
		{lead, store.Project{}, "def", "role-m"},
		{emp("lead", "lead", store.Role{}), store.Project{}, "def", "def"},
		{emp("lead", "lead", store.Role{}), store.Project{}, "", DefaultModel},
		{emp("coordinator", "coordinator", store.Role{}), p, "", "coord-m"},
		{func() core.Employee { e := emp("member", "dev", store.Role{}); e.Runtime = "codex"; return e }(), p, "gpt-def", "gpt-def"},
	}
	for i, c := range cases {
		if got := Model(c.e, c.p, c.def); got != c.want {
			t.Fatalf("case %d: %q, want %q", i, got, c.want)
		}
	}
	o, _ := buildOpen(func() sessionInput {
		in := open(t, lead, store.Node{})
		in.defModel = "def"
		in.project = store.Project{}
		return in
	}())
	if o.Claude.Model != "role-m" {
		t.Fatalf("session.open model %q", o.Claude.Model)
	}
}

// TestProtectedRefsEnv: employees with credentials learn the protected branches (pre-push hook),
// the coordinator gets neither credentials nor the list; the lead may use gh for PRs and issues.
func TestProtectedRefsEnv(t *testing.T) {
	in := open(t, emp("member", "dev", store.Role{}), store.Node{Isolation: "none"})
	in.protRefs = []string{"thread/3-x", "main", "develop"}
	dev, _ := buildOpen(in)
	if dev.Env[ProtectedRefsEnv] != "thread/3-x main develop" {
		t.Fatalf("member env %+v", dev.Env)
	}
	cin := open(t, emp("coordinator", "coordinator", store.Role{}), store.Node{Isolation: "none"})
	cin.protRefs = []string{"main"}
	coord, _ := buildOpen(cin)
	if _, ok := coord.Env[ProtectedRefsEnv]; ok || *coord.GitCredentials {
		t.Fatalf("coordinator env %+v", coord.Env)
	}
	lead, _ := buildOpen(open(t, emp("lead", "lead", store.Role{}), store.Node{Isolation: "none"}))
	allow := lead.Claude.BashPolicy.Allow
	if !contains(allow, "gh pr create") || !contains(allow, "gh issue") || contains(allow, "gh api") || contains(allow, "gh pr merge") {
		t.Fatalf("lead gh allow list %v", allow)
	}
}
