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
		"git merge-base", "git branch:list", "gh pr view", "gh pr diff", "gh pr checks"}
	readOnlyDenyFlags = []string{"--output", "-o", "--ext-diff", "--textconv", "-c", "--exec-path", "--git-dir", "--work-tree"}
)

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

// readOnly reports whether the employee gets the lead's read-only policy.
func readOnly(e core.Employee) bool {
	return e.Tier == "lead" || (e.Spec.ReadOnly != nil && *e.Spec.ReadOnly)
}

// Bypass reports whether a member runs without permission prompts (§9): every writing
// member whose role is not ask (bypass, and the pre-00097 auto), on any node.
func Bypass(e core.Employee) bool {
	return e.Tier == "member" && e.Spec.PermissionMode != store.PermAsk && !readOnly(e)
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
}

// buildOpen renders session.open for an employee.
func buildOpen(in sessionInput) (protocol.SessionOpen, error) {
	flags := []string{}
	if in.emp.Spec.CanDelegate {
		flags = append(flags, "can_delegate")
	}
	tools, err := protocol.PlatformTools(protocol.Tier(in.emp.Tier), flags...)
	if err != nil {
		return protocol.SessionOpen{}, err
	}
	gitCreds := in.hasRepo && in.emp.Tier != "coordinator"
	open := protocol.SessionOpen{SessionID: protocol.SessionID(in.sess.ID), OpenGen: protocol.OpenGen(in.sess.OpenGen),
		Employee: employeeRef(in.emp, in.thread), Runtime: protocol.Runtime(in.emp.Runtime), Cwd: protocol.RelPath(in.sess.Cwd),
		PlatformTools: tools, Env: protocol.SessionOpenEnv{"LANG": "C.UTF-8"}, GitCredentials: &gitCreds,
		FirstMessage: in.first, Resume: in.resume}
	setPrepare(&open, in.prepare)
	if in.emp.Runtime == "codex" {
		c := codexOptions(in)
		open.Codex = &c
		return open, nil
	}
	c := claudeOptions(in)
	open.Claude = &c
	return open, nil
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
	ref := protocol.EmployeeRef{Name: e.Name, ProjectID: e.ProjectID, Role: &role, Tier: protocol.Tier(e.Tier)}
	if t != nil {
		no := t.No
		ref.ThreadNo = &no
	}
	return ref
}

// model is the project override for the employee's tier, else the role's model, else the default.
func model(e core.Employee, p store.Project) string {
	override := p.ThreadModel
	if e.Tier == "coordinator" {
		override = p.CoordinatorModel
	}
	return firstNonEmpty(override, e.Spec.Model, DefaultModel)
}

// effort is the project's thread effort for leads and members, else the role's.
func effort(e core.Employee, p store.Project) string {
	if e.Tier == "coordinator" {
		return e.Spec.Effort
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
	c := protocol.ClaudeSessionOptions{Model: model(e, in.project), SystemAppend: &system, SettingSources: []string{},
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
	if in.node.Isolation == "sandbox" {
		c.Sandbox = &protocol.SandboxPolicy{Enabled: true, WritePaths: []string{"."},
			ExtraWritePaths: sharedGitDirs(e.ProjectID, in.repos)}
	}
}

func codexOptions(in sessionInput) protocol.CodexSessionOptions {
	e := in.emp
	dev := in.system
	c := protocol.CodexSessionOptions{Model: firstNonEmpty(e.Spec.Model, DefaultModel), DeveloperInstructions: &dev, Sandbox: "read-only", ApprovalPolicy: "never"}
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
	return c
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
