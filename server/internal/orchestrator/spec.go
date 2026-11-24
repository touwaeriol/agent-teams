package orchestrator

import (
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"strings"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/ghops"
	"github.com/touwaeriol/agent-teams/server/internal/runtimes"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// Built-in tool sets (architecture.md §6.1).
var (
	leadTools   = []string{"Read", "Glob", "Grep", "Bash"}
	memberTools = []string{"Read", "Glob", "Grep", "Edit", "Write", "Bash"}
	// chatTools talk to the user through the chat (§6.6): TodoWrite is the live checklist,
	// AskUserQuestion becomes the platform question card. Every tier has them.
	chatTools = []string{"TodoWrite", "AskUserQuestion"}
	// planTools let a writing member plan first; the plan shows as a card (approval for ask roles).
	planTools         = []string{"EnterPlanMode", "ExitPlanMode"}
	writeTools        = []string{"Edit", "Write", "NotebookEdit", "MultiEdit"}
	coordinatorDeny   = []string{"Bash", "Edit", "Write", "MultiEdit", "NotebookEdit", "Read", "Glob", "Grep", "WebFetch", "WebSearch", "Task"}
	readOnlyBashAllow = []string{"git log", "git diff", "git show", "git status", "git rev-parse", "git ls-files", "git blame",
		"git merge-base", "git branch:list", "gh pr view", "gh pr diff", "gh pr checks", "gh pr list", "gh pr status",
		"gh pr create", "gh pr comment", "gh pr review", "gh pr edit", "gh issue", "gh run list", "gh run view",
		"gh workflow list", "gh workflow view", "gh repo view", "gh search", "gh release list", "gh release view", "gh auth status"}
	readOnlyDenyFlags = []string{"--output", "-o", "--ext-diff", "--textconv", "-c", "--exec-path", "--git-dir", "--work-tree"}
)

// ProtectedRefsEnv names the branches an employee may not push, space separated (read by the pre-push hook).
const ProtectedRefsEnv = "AT_PROTECTED_REFS"

// Paths are an employee's directory and branch on the node (relative to the node root).
// The directory holds one checkout per project repository, named owner__name (RepoCwd).
type Paths struct {
	Cwd    string
	Branch string
}

// PathsOf computes where an employee works (architecture.md §8.1).
func PathsOf(e core.Employee, t *core.Thread) Paths {
	root := path.Join("projects", e.ProjectID)
	switch {
	case e.Tier == "coordinator" || t == nil:
		return Paths{Cwd: path.Join(root, "coordinator")}
	case e.Tier == "lead":
		return Paths{Cwd: path.Join(root, "threads", fmt.Sprint(t.No), "lead"), Branch: t.Branch}
	}
	return Paths{Cwd: path.Join(root, "threads", fmt.Sprint(t.No), e.Name), Branch: MemberBranch(t.No, e.Name)}
}

// RepoCwd is the employee's checkout of one repository.
func (p Paths) RepoCwd(slug string) string { return path.Join(p.Cwd, ghops.Dir(slug)) }

// SharedDir is the node's shared clone of a repository that worktrees hang off (worktree mode).
func SharedDir(projectID, slug string) string {
	return path.Join("projects", projectID, "repos", ghops.Dir(slug))
}

// LegacySharedDir is the single shared clone of projects from before multi-repository support.
func LegacySharedDir(projectID string) string { return path.Join("projects", projectID, "repo") }

// sharedGitDirs are the .git directories of the shared clones a sandboxed member writes commits into.
func sharedGitDirs(projectID string, repos []string) []string {
	out := make([]string, 0, len(repos))
	for _, slug := range repos {
		out = append(out, path.Join(SharedDir(projectID, slug), ".git"))
	}
	return out
}

// MemberBranch is a member's branch thread/<no>/<name>.
func MemberBranch(threadNo int, name string) string {
	return fmt.Sprintf("thread/%d/%s", threadNo, name)
}

// readOnly reports whether the employee gets the lead's read-only policy. A session's employee
// (Spec.Solo) is a lead that writes.
func readOnly(e core.Employee) bool {
	return (e.Tier == "lead" && !e.Spec.Solo) || (e.Spec.ReadOnly != nil && *e.Spec.ReadOnly)
}

// writer reports whether the employee works like a member: a member, or a session's employee.
func writer(e core.Employee) bool { return e.Tier == "member" || (e.Tier == "lead" && e.Spec.Solo) }

// Bypass reports whether a member runs without permission prompts (§9): every writing
// member (or session employee) whose role is not ask (bypass, and the pre-00097 auto), on any node.
func Bypass(e core.Employee) bool {
	return writer(e) && e.Spec.PermissionMode != store.PermAsk && !readOnly(e)
}

// soloTools are the platform tools of a session's employee: talking to the user and publishing
// files. The team tools of a lead (tasks, merge queue, PRs, other threads, memory, schedules) do
// not exist in a session.
var soloTools = []string{"ask_user", "close_question", "tell_user", "set_status", "publish_file", "publish_deliverable",
	"read_deliverable", "list_deliverables"}

// platformTools are the platform tools an employee's session gets.
func platformTools(e core.Employee) ([]protocol.PlatformToolSpec, error) {
	flags := []string{}
	if e.Spec.CanDelegate {
		flags = append(flags, "can_delegate")
	}
	tools, err := protocol.PlatformTools(protocol.Tier(e.Tier), flags...)
	if err != nil || !e.Spec.Solo {
		return tools, err
	}
	return slices.DeleteFunc(tools, func(t protocol.PlatformToolSpec) bool { return !slices.Contains(soloTools, t.Name) }), nil
}

// ClaudeMode is the Claude permission mode a writing member opens in: bypassPermissions,
// or default for ask roles (every non-read tool goes through canUseTool).
func ClaudeMode(e core.Employee) string {
	if Bypass(e) {
		return "bypassPermissions"
	}
	return "default"
}

// sessionInput is everything buildOpen needs.
type sessionInput struct {
	emp      core.Employee
	thread   *core.Thread
	project  store.Project
	node     store.Node
	sess     core.AgentSession
	prepare  []string
	first    *protocol.FirstMessage
	resume   *protocol.ResumeRef
	system   string
	rules    []string
	hasRepo  bool
	repos    []string // repository slugs checked out under the employee's directory
	protRefs []string
	gitHosts []protocol.SessionOpenGitHostsElem // standalone repositories' hosts (git-repos.md §5)
	window   *int                               // the context window the employee's last session of the runtime reported
}

// buildOpen renders session.open for an employee.
func buildOpen(in sessionInput) (protocol.SessionOpen, error) {
	tools, err := platformTools(in.emp)
	if err != nil {
		return protocol.SessionOpen{}, err
	}
	gitCreds := in.hasRepo && in.emp.Tier != "coordinator"
	open := protocol.SessionOpen{SessionID: protocol.SessionID(in.sess.ID), OpenGen: protocol.OpenGen(in.sess.OpenGen),
		Employee: employeeRef(in.emp, in.thread), Runtime: protocol.Runtime(in.emp.Runtime), Cwd: protocol.RelPath(in.sess.Cwd),
		PlatformTools: tools, Env: protocol.SessionOpenEnv{"LANG": "C.UTF-8"}, GitCredentials: &gitCreds,
		FirstMessage: in.first, Resume: in.resume, Context: sessionContext(in.emp.Spec, in.window)}
	if gitCreds {
		open.GitHosts = in.gitHosts
	}
	if gitCreds && len(in.protRefs) > 0 {
		// The platform pre-push hook refuses these (architecture.md §8.1), whatever the runtime.
		open.Env[ProtectedRefsEnv] = strings.Join(in.protRefs, " ")
	}
	setPrepare(&open, in.prepare)
	runtimeOptions[runtimes.Of(in.emp.Runtime).ID](&open, in)
	return open, nil
}

// runtimeOptions fills session.open's options block of each runtime (one entry per runtime in
// the registry; TestRuntimeOptionsCoverRegistry).
var runtimeOptions = map[string]func(*protocol.SessionOpen, sessionInput){
	runtimes.Claude: func(open *protocol.SessionOpen, in sessionInput) { c := claudeOptions(in); open.Claude = &c },
	runtimes.Codex:  func(open *protocol.SessionOpen, in sessionInput) { c := codexOptions(in); open.Codex = &c },
	runtimes.Antigravity: func(open *protocol.SessionOpen, in sessionInput) {
		a := antigravityOptions(in)
		open.Antigravity = &a
	},
}

// setPrepare names the git.prepare jobs session.open waits for (first one in prepare for older daemons).
func setPrepare(open *protocol.SessionOpen, jobs []string) {
	if len(jobs) == 0 {
		return
	}
	first := protocol.JobID(jobs[0])
	open.Prepare = &first
	open.PrepareJobs = make([]protocol.JobID, len(jobs))
	for i, j := range jobs {
		open.PrepareJobs[i] = protocol.JobID(j)
	}
}

func employeeRef(e core.Employee, t *core.Thread) protocol.EmployeeRef {
	role := e.Role
	scope := protocol.EmployeeRefScopeThread
	if e.ThreadID == nil {
		scope = protocol.EmployeeRefScopeProject // the coordinator and the project members
	}
	ref := protocol.EmployeeRef{Name: e.Name, ProjectID: e.ProjectID, Role: &role, Tier: protocol.Tier(e.Tier), Scope: &scope}
	if t != nil {
		no := t.No
		ref.ThreadNo = &no
	}
	return ref
}

// Model is what the employee's sessions run: the first non-empty step of the fallback order
// below (modelSteps). The whole order lives there, one line per step:
//
//	member:      /model (e.Model) → project (thread_model / thread_codex_model) → role (Spec.Model) → builtin
//	lead:        project (thread_model / thread_codex_model) → role (Spec.Model) → builtin
//	coordinator: project (coordinator_model) → role (Spec.Model) → builtin
//
// The coordinator and leads have no /model (fixedModel): their model is set in their role or in
// the project. A session's employee (a lead with Spec.Solo) goes the member's way: the model its
// user switched to in the session (e.Model) comes first. A Codex employee never falls through to a Claude id: its built-in last resort is
// DefaultCodexModel (""), which leaves the choice to the Codex CLI.
func Model(e core.Employee, p store.Project) string {
	return firstNonEmpty(modelSteps(e, p)...)
}

// fixedModel reports whether the employee's tier cannot switch models with /model (coordinator,
// lead): their model comes from the project and the role only.
func fixedModel(tier string) bool { return tier == "coordinator" || tier == "lead" }

// modelSteps is Model's fallback order, highest precedence first.
func modelSteps(e core.Employee, p store.Project) []string {
	if fixedModel(e.Tier) && !e.Spec.Solo {
		return []string{
			projectModel(e, p),         // 1. the project override for the coordinator / the lead's runtime
			e.Spec.ModelFor(e.Runtime), // 2. the role's model for the runtime (the employee's spec)
			builtinModel(e.Runtime),    // 3. built in: DefaultModel / DefaultCodexModel
		}
	}
	return []string{
		e.Model,                    // 1. picked with /model, or in the session (at_employees.model)
		projectModel(e, p),         // 2. the project override for the employee's runtime
		e.Spec.ModelFor(e.Runtime), // 3. the role's model for the runtime (the employee's spec)
		builtinModel(e.Runtime),    // 4. built in: DefaultModel / DefaultCodexModel
	}
}

// projectModel is the project's model override for the employee: coordinator_model for the
// coordinator; by runtime for leads and members so an override only reaches roles of its own
// runtime: thread_codex_model for Codex (never thread_model, a Claude id), thread_model for
// Claude. "" falls through to the role's model.
func projectModel(e core.Employee, p store.Project) string {
	if e.Tier == "coordinator" {
		return p.CoordinatorModel
	}
	return threadModels[runtimes.Of(e.Runtime).ID](p)
}

// threadModels is the project's thread model override of each runtime (TestRuntimeOptionsCoverRegistry).
var threadModels = map[string]func(store.Project) string{
	runtimes.Claude: func(p store.Project) string { return p.ThreadModel },
	runtimes.Codex:  func(p store.Project) string { return p.ThreadCodexModel },
	// No project override for Antigravity yet: the role's model, else agy's own choice.
	runtimes.Antigravity: func(store.Project) string { return "" },
}

// effort is the project's thread effort for leads and members, else the role's; a session's
// employee runs the effort its user picked in the session first.
func effort(e core.Employee, p store.Project) string {
	if e.Tier == "coordinator" {
		return e.Spec.Effort
	}
	if e.Spec.Solo {
		return firstNonEmpty(e.SessionEffort, p.ThreadEffort, e.Spec.Effort)
	}
	return firstNonEmpty(p.ThreadEffort, e.Spec.Effort)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func claudeOptions(in sessionInput) protocol.ClaudeSessionOptions {
	e := in.emp
	system := in.system
	c := protocol.ClaudeSessionOptions{Model: Model(e, in.project), SystemAppend: &system, SettingSources: []string{},
		AllowedTools: []string{"mcp__platform__*"}}
	if eff := effort(e, in.project); eff != "" {
		c.Effort = &eff
	}
	if e.Spec.MaxTurns != nil {
		c.MaxTurns = e.Spec.MaxTurns
	}
	switch {
	case e.Tier == "coordinator":
		coordinatorClaude(&c)
	case readOnly(e):
		readOnlyClaude(&c, in)
	default:
		memberClaude(&c, in)
	}
	return c
}

func coordinatorClaude(c *protocol.ClaudeSessionOptions) {
	t := true
	c.Tools = slices.Clone(chatTools)
	c.DisallowedTools = coordinatorDeny
	c.PermissionMode = "dontAsk"
	c.DenyNonPlatformTools = &t
}

func readOnlyClaude(c *protocol.ClaudeSessionOptions, in sessionInput) {
	t := true
	c.Tools = mergeLists(leadTools, chatTools)
	c.DisallowedTools = mergeLists(writeTools, in.emp.Spec.DisallowedTools)
	c.PermissionMode = "dontAsk"
	c.ReadOnly = &t
	c.BashPolicy = &protocol.BashPolicy{Mode: "argv", Allow: readOnlyBashAllow, DenyFlags: readOnlyDenyFlags, DenyMetachar: &t}
	c.Sandbox = &protocol.SandboxPolicy{Enabled: in.node.Isolation != "none", WritePaths: []string{}}
	c.ProtectedRefs = in.protRefs
	// dontAsk denies whatever is not pre-approved: the read tools are allowed here,
	// Bash is narrowed further by bash_policy in the daemon's PreToolUse hook.
	c.AllowedTools = mergeLists(c.AllowedTools, leadTools, chatTools, in.emp.Spec.AllowedTools, in.rules)
}

func memberClaude(c *protocol.ClaudeSessionOptions, in sessionInput) {
	e := in.emp
	c.Tools = mergeLists(memberTools, chatTools, planTools)
	c.AllowedTools = mergeLists(c.AllowedTools, e.Spec.AllowedTools, in.rules)
	c.DisallowedTools = mergeLists(e.Spec.DisallowedTools)
	c.PermissionMode = protocol.PermissionMode(ClaudeMode(e))
	c.ProtectedRefs = in.protRefs
	if c.PermissionMode == "bypassPermissions" {
		t := true
		c.AllowDangerouslySkipPermissions = &t
	}
	if e.Spec.Solo {
		soloClaude(c, e)
	}
	if in.node.Isolation == "sandbox" {
		c.Sandbox = &protocol.SandboxPolicy{Enabled: true, WritePaths: []string{"."},
			ExtraWritePaths: sharedGitDirs(e.ProjectID, in.repos)}
	}
}

func codexOptions(in sessionInput) protocol.CodexSessionOptions {
	e := in.emp
	dev := in.system
	c := protocol.CodexSessionOptions{Model: Model(e, in.project), DeveloperInstructions: &dev, Sandbox: "read-only", ApprovalPolicy: "never"}
	switch {
	case readOnly(e):
	case Bypass(e):
		c.Sandbox = "danger-full-access"
	default:
		c.Sandbox, c.ApprovalPolicy = "workspace-write", "untrusted"
		cfg, _ := json.Marshal(map[string]any{"sandbox_workspace_write": map[string]any{
			"writable_roots": sharedGitDirs(e.ProjectID, in.repos), "network_access": true}})
		raw := json.RawMessage(cfg)
		c.Config = &raw
	}
	if e.Spec.Solo && !readOnly(e) {
		soloCodex(&c, e, writableRoots(e, in.repos))
	}
	return c
}

// writableRoots is the workspace-write config of a Codex employee: the shared clones' .git
// directories it commits into, with network access.
func writableRoots(e core.Employee, repos []string) json.RawMessage {
	cfg, _ := json.Marshal(map[string]any{"sandbox_workspace_write": map[string]any{
		"writable_roots": sharedGitDirs(e.ProjectID, repos), "network_access": true}})
	return cfg
}

// antigravityOptions: read-only roles get read_only (the daemon disables agy's writing tools and
// rejects writing permission requests) with the read-only shell allow list; bypass members have
// permission requests allowed by the daemon; ask members forward them as approval.request.
func antigravityOptions(in sessionInput) protocol.AntigravitySessionOptions {
	e := in.emp
	system := in.system
	a := protocol.AntigravitySessionOptions{Model: Model(e, in.project), SystemAppend: &system,
		Approval: protocol.AntigravitySessionOptionsApprovalAsk}
	switch {
	case readOnly(e):
		t := true
		a.ReadOnly = true
		a.BashPolicy = &protocol.BashPolicy{Mode: "argv", Allow: readOnlyBashAllow, DenyFlags: readOnlyDenyFlags, DenyMetachar: &t}
	case Bypass(e):
		a.Approval = protocol.AntigravitySessionOptionsApprovalAuto
	}
	if e.Spec.Solo && !readOnly(e) {
		soloAntigravity(&a, e)
	}
	return a
}

// textInstructions is the instructions field of a runtime without skill or plugin loading
// (Codex developer_instructions, Antigravity system_append): skills and plugin commands are
// appended there as text. nil for Claude.
func textInstructions(open *protocol.SessionOpen) **string {
	switch {
	case open.Codex != nil:
		return &open.Codex.DeveloperInstructions
	case open.Antigravity != nil:
		return &open.Antigravity.SystemAppend
	}
	return nil
}

func appendInstructions(field **string, text string) {
	cur := ""
	if *field != nil {
		cur = **field
	}
	cur += text
	*field = &cur
}

func mergeLists(lists ...[]string) []string {
	out := []string{}
	for _, l := range lists {
		for _, v := range l {
			if v != "" && !slices.Contains(out, v) {
				out = append(out, v)
			}
		}
	}
	return out
}

// ToolsAllowed checks the init tool list of a session against its role (§10.2 third line of defence).
func ToolsAllowed(e core.Employee, tools []string) (bool, string) {
	for _, t := range tools {
		if e.Tier == "coordinator" && !strings.HasPrefix(t, "mcp__platform__") && !slices.Contains(chatTools, t) {
			return false, t
		}
		if readOnly(e) && slices.Contains(writeTools, t) {
			return false, t
		}
	}
	return true, ""
}
