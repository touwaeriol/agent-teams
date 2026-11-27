package orchestrator

import (
	"errors"
	"slices"
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
	if ok, bad := ToolsAllowed(emp("coordinator", "coordinator", store.Role{}), []string{"mcp__platform__ask_user", "TodoWrite", "AskUserQuestion"}, false); !ok {
		t.Fatalf("coordinator chat tools refused: %s", bad)
	}
	if ok, _ := ToolsAllowed(emp("coordinator", "coordinator", store.Role{}), []string{"Bash"}, false); ok {
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
	coordinator, lead := emp("coordinator", "coordinator", store.Role{}), emp("lead", "lead", store.Role{})
	if ok, bad := ToolsAllowed(coordinator, []string{"mcp__platform__open_thread", "Read", "Edit", "Bash"}, true); ok || bad != "Bash" {
		t.Fatal("coordinator may only have platform tools and the memory file tools")
	}
	if ok, bad := ToolsAllowed(coordinator, []string{"mcp__platform__open_thread", "Read"}, false); ok || bad != "Read" {
		t.Fatal("without memory_files the coordinator has no file tools")
	}
	if ok, _ := ToolsAllowed(lead, []string{"Read", "Bash", "mcp__platform__assign_task"}, false); !ok {
		t.Fatal("lead read tools are fine")
	}
	if ok, bad := ToolsAllowed(lead, []string{"Edit", "Write", "NotebookEdit"}, true); ok || bad != "NotebookEdit" {
		t.Fatal("lead may only have the memory file tools among the write tools")
	}
	if ok, bad := ToolsAllowed(lead, []string{"Edit"}, false); ok || bad != "Edit" {
		t.Fatal("without memory_files the lead writes nothing")
	}
	if ok, _ := ToolsAllowed(emp("member", "dev", store.Role{}), []string{"Edit", "Write"}, false); !ok {
		t.Fatal("members edit")
	}
}

// TestAllowMemoryFiles: a coordinator or read-only Claude session gets the file tools for the
// memory directories (and claude.memory_files); a member's options stay as they are.
func TestAllowMemoryFiles(t *testing.T) {
	coord := emp("coordinator", "coordinator", store.Role{})
	o, _ := buildOpen(open(t, coord, store.Node{}))
	allowMemoryFiles(coord, o.Claude)
	c := o.Claude
	if c.MemoryFiles == nil || !*c.MemoryFiles || !slices.Contains(c.Tools, "Edit") || !slices.Contains(c.AllowedTools, "Write") ||
		slices.Contains(c.DisallowedTools, "Read") || !slices.Contains(c.DisallowedTools, "Bash") || c.DenyNonPlatformTools == nil {
		t.Fatalf("coordinator %+v", c)
	}
	lead := emp("lead", "lead", store.Role{})
	o, _ = buildOpen(open(t, lead, store.Node{}))
	allowMemoryFiles(lead, o.Claude)
	if o.Claude.MemoryFiles == nil || slices.Contains(o.Claude.DisallowedTools, "Edit") || !slices.Contains(o.Claude.DisallowedTools, "NotebookEdit") {
		t.Fatalf("lead %+v", o.Claude)
	}
	strict := emp("lead", "lead", store.Role{DisallowedTools: []string{"Write"}})
	o, _ = buildOpen(open(t, strict, store.Node{}))
	allowMemoryFiles(strict, o.Claude)
	if !slices.Contains(o.Claude.DisallowedTools, "Write") || slices.Contains(o.Claude.DisallowedTools, "Edit") {
		t.Fatalf("a tool the role disallows was lifted: %v", o.Claude.DisallowedTools)
	}
	member := emp("member", "dev", store.Role{})
	o, _ = buildOpen(open(t, member, store.Node{}))
	allowMemoryFiles(member, o.Claude)
	if o.Claude.MemoryFiles != nil {
		t.Fatal("a member needs no memory_files")
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

// TestProjectModelOverrides: the project's models and thread effort override the roles; ”
// keeps the role's. The coordinator follows coordinator_model (and keeps its role's effort), leads
// and members thread_model.
func TestProjectModelOverrides(t *testing.T) {
	p := store.Project{CoordinatorModel: "claude-coord-y", ThreadModel: "claude-thread-y", ThreadEffort: "high"}
	in := open(t, emp("coordinator", "coordinator", store.Role{Model: "role-model", Effort: "low"}), store.Node{})
	in.project = p
	c := claudeOptions(in)
	if c.Model != "claude-coord-y" || c.Effort == nil || *c.Effort != "low" {
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
	in = open(t, emp("lead", "lead", store.Role{Model: "role-model"}), store.Node{})
	in.project = p
	if l := claudeOptions(in); l.Model != "claude-thread-y" {
		t.Fatalf("lead %q", l.Model)
	}
}

// TestFixedModels: the coordinator and leads (Claude, and a Codex lead as far as Model goes) run
// project → role → built in; /model (at_employees.model) never reaches them.
func TestFixedModels(t *testing.T) {
	full := store.Project{CoordinatorModel: "coord-m", ThreadModel: "thread-m", ThreadCodexModel: "gpt-project"}
	cases := []struct {
		name, tier, runtime, roleModel string
		p                              store.Project
		want                           string
	}{
		{"coordinator project", "coordinator", "claude", "role-m", full, "coord-m"},
		{"coordinator ignores thread_model", "coordinator", "claude", "role-m", store.Project{ThreadModel: "thread-m"}, "role-m"},
		{"coordinator role", "coordinator", "claude", "role-m", store.Project{}, "role-m"},
		{"coordinator built in", "coordinator", "claude", "", store.Project{}, DefaultModel},
		{"lead project", "lead", "claude", "role-m", full, "thread-m"},
		{"lead ignores coordinator_model", "lead", "claude", "role-m", store.Project{CoordinatorModel: "coord-m"}, "role-m"},
		{"lead role", "lead", "claude", "role-m", store.Project{}, "role-m"},
		{"lead built in", "lead", "claude", "", store.Project{}, DefaultModel},
		{"codex lead project", "lead", "codex", "gpt-role", full, "gpt-project"},
		{"codex lead role", "lead", "codex", "gpt-role", store.Project{ThreadModel: "thread-m"}, "gpt-role"},
		{"codex lead built in", "lead", "codex", "", store.Project{ThreadModel: "thread-m"}, DefaultCodexModel},
	}
	for _, c := range cases {
		e := emp(c.tier, c.tier, store.Role{Model: c.roleModel})
		e.Runtime, e.Model = c.runtime, "picked"
		if got := Model(e, c.p); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	// session.open carries the same.
	lead := emp("lead", "lead", store.Role{Model: "role-m"})
	lead.Model = "picked"
	in := open(t, lead, store.Node{})
	in.project = store.Project{}
	if o, _ := buildOpen(in); o.Claude.Model != "role-m" {
		t.Fatalf("lead session.open model %q", o.Claude.Model)
	}
}

// TestProjectModelOverride: a project without an override runs the role's model; setting one
// switches the members of its runtime to it; setting it back to ” returns to the role's model.
// Codex members follow thread_codex_model only, Claude members thread_model only.
func TestProjectModelOverride(t *testing.T) {
	claudeDev := emp("member", "dev", store.Role{Model: "claude-role"})
	codexDev := emp("member", "dev", store.Role{Model: "gpt-role"})
	codexDev.Runtime = "codex"
	steps := []struct {
		name               string
		p                  store.Project
		wantClaude, wantCx string
	}{
		{"not set", store.Project{}, "claude-role", "gpt-role"},
		{"thread_model set", store.Project{ThreadModel: "claude-proj"}, "claude-proj", "gpt-role"},
		{"thread_codex_model set", store.Project{ThreadCodexModel: "gpt-proj"}, "claude-role", "gpt-proj"},
		{"both set", store.Project{ThreadModel: "claude-proj", ThreadCodexModel: "gpt-proj"}, "claude-proj", "gpt-proj"},
		{"back to ''", store.Project{}, "claude-role", "gpt-role"},
	}
	for _, s := range steps {
		if got := Model(claudeDev, s.p); got != s.wantClaude {
			t.Errorf("%s: claude %q, want %q", s.name, got, s.wantClaude)
		}
		if got := Model(codexDev, s.p); got != s.wantCx {
			t.Errorf("%s: codex %q, want %q", s.name, got, s.wantCx)
		}
	}
}

// TestModelSteps: every level of a member's fallback order (/model → project → role → built in),
// one step at a time from the top, for each runtime; a Codex member never ends on a Claude id.
func TestModelSteps(t *testing.T) {
	member := func(runtime, picked, roleModel string) core.Employee {
		e := emp("member", "dev", store.Role{Model: roleModel})
		e.Runtime, e.Model = runtime, picked
		return e
	}
	full := store.Project{CoordinatorModel: "coord-m", ThreadModel: "thread-m", ThreadCodexModel: "gpt-project"}
	claudeOnly := store.Project{ThreadModel: "thread-m"}
	cases := []struct {
		name                string
		runtime, picked, rm string
		p                   store.Project
		want                string
	}{
		{"claude /model", "claude", "picked", "role-m", full, "picked"},
		{"claude project", "claude", "", "role-m", full, "thread-m"},
		{"claude role", "claude", "", "role-m", store.Project{}, "role-m"},
		{"claude built in", "claude", "", "", store.Project{}, DefaultModel},
		{"claude ignores coordinator_model", "claude", "", "", store.Project{CoordinatorModel: "coord-m"}, DefaultModel},
		{"claude ignores thread_codex_model", "claude", "", "role-m", store.Project{ThreadCodexModel: "gpt-project"}, "role-m"},
		{"codex /model", "codex", "gpt-picked", "gpt-role", full, "gpt-picked"},
		{"codex project", "codex", "", "gpt-role", full, "gpt-project"},
		{"codex role", "codex", "", "gpt-role", store.Project{}, "gpt-role"},
		{"codex ignores thread_model", "codex", "", "gpt-role", claudeOnly, "gpt-role"},
		{"codex built in is empty", "codex", "", "", claudeOnly, DefaultCodexModel},
	}
	for _, c := range cases {
		got := Model(member(c.runtime, c.picked, c.rm), c.p)
		if got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
		if c.runtime == "codex" && strings.HasPrefix(got, "claude") {
			t.Errorf("%s: codex runs a Claude model %q", c.name, got)
		}
	}
	if DefaultCodexModel != "" {
		t.Fatalf("DefaultCodexModel %q: the Codex CLI should pick its own default", DefaultCodexModel)
	}
	// A Codex session.open with nothing configured carries an empty model, never a Claude id.
	cx := member("codex", "", "")
	in := open(t, cx, store.Node{})
	in.project = claudeOnly
	if o := codexOptions(in); o.Model != "" {
		t.Fatalf("codex session.open model %q", o.Model)
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

// TestAntigravityOptions: ask members forward permission requests, bypass members have them
// allowed by the daemon, read-only roles get read_only with the read-only shell allow list; agy
// picks the model when no level names one, and the project's Claude/Codex overrides never apply.
func TestAntigravityOptions(t *testing.T) {
	e := emp("member", "qa", store.Role{PermissionMode: store.PermAsk})
	e.Runtime = "antigravity"
	o, err := buildOpen(open(t, e, store.Node{Isolation: "agent_user"}))
	if err != nil {
		t.Fatal(err)
	}
	a := o.Antigravity
	if a == nil || o.Claude != nil || o.Codex != nil || a.Approval != "ask" || a.ReadOnly || a.BashPolicy != nil ||
		a.Model != "" || a.SystemAppend == nil || *a.SystemAppend != "sys" || string(o.Runtime) != "antigravity" {
		t.Fatalf("ask antigravity %+v", a)
	}
	e.Spec.PermissionMode = store.PermBypass
	if o, _ = buildOpen(open(t, e, store.Node{Isolation: "agent_user"})); o.Antigravity.Approval != "auto" || o.Antigravity.ReadOnly {
		t.Fatalf("bypass antigravity %+v", o.Antigravity)
	}
	ro := true
	e.Spec.ReadOnly = &ro
	o, _ = buildOpen(open(t, e, store.Node{Isolation: "agent_user"}))
	if a := o.Antigravity; !a.ReadOnly || a.Approval != "ask" || a.BashPolicy == nil || !contains(a.BashPolicy.Allow, "git diff") {
		t.Fatalf("read-only antigravity %+v", a)
	}
	e.Spec.Model = "gemini-4-neon-high"
	if got := Model(e, store.Project{ThreadModel: "claude-x", ThreadCodexModel: "gpt-x"}); got != "gemini-4-neon-high" {
		t.Fatalf("antigravity model %q", got)
	}
	e.Spec.Model = ""
	if got := Model(e, store.Project{ThreadModel: "claude-x", ThreadCodexModel: "gpt-x"}); got != "" {
		t.Fatalf("antigravity built-in model %q", got)
	}
}
