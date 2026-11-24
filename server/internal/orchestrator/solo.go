package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/ghops"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// Sessions (多仓库会话): a project of kind session holds one thread whose lead is the session's
// employee (store.Role.Solo): it writes code in a directory holding every repository of the
// project, side by side, and talks to the user directly. There is no coordinator, no member and
// no task pool; the branch the user picked for each repository is the thread's base there.

// SessionRequest opens a session's thread.
type SessionRequest struct {
	UserID, ProjectID string
	Title             string
	Message           string      // the user's first message, delivered once the thread starts
	Role              store.Role  // the member role template the employee is made from
	Model             string      // '' = the role's model
	PermissionMode    string      // '' = the initial mode (soloMode); kept as the employee's session_mode
	Effort            string      // '' = project / role effort; kept as the employee's session_effort
	NodeID            string      // '' = scheduled like any employee (lowest load score, pickNode)
	Branch            string      // the work branch's name the user typed ('' = from the title or the repositories)
	Repos             []RepoFocus // every repository with the branch picked for it ('' = default)
}

// SessionRole turns a member role template into a session employee's role: a writing lead
// (Solo) on the role's runtimes, pinned to nodeID when given. A model picked for the session
// applies to the role's first runtime, which the session then keeps to.
//
// The role is only a template (prompt, runtimes, model, permissions): the employee is not one of
// the role's staff (Count, project members) and does not take the nodes the role's members are
// placed on: the role's own node_ids are dropped, so a session runs on the node the user picked or
// on any of the user's nodes (nodepick). Only the node's physical cap (max_active_sessions) and
// quota still apply, as for every live session.
func SessionRole(r store.Role, model, nodeID string) store.Role {
	r.ID, r.Tier, r.Solo, r.Count = "", "lead", true, 1
	r.ReadOnly, r.Gate, r.TaskKinds, r.CanDelegate = nil, "", nil, false
	r.CompactBetweenPct, r.ClearAfterTasks = nil, nil
	if model = strings.TrimSpace(model); model != "" {
		r.Model, r.Runtimes, r.Models = model, nil, nil
	}
	r.NodeIDs = []string{}
	if nodeID != "" {
		r.NodeIDs = []string{nodeID}
	}
	return r
}

// SessionBranch is the branch a session's employee works on in every repository. It carries the
// project id so that sessions on the same repository never share a branch. Its name is the one the
// user typed, else the title's; a title with no Latin letters or digits (a Chinese one) names it
// after the repositories instead, so that it still says what it is about.
func SessionBranch(projectID, name, title string, repos []RepoFocus) string {
	id := projectID
	if len(id) > 8 {
		id = id[:8]
	}
	slug := ""
	for _, s := range []string{name, title, repoWords(repos)} {
		if slug = core.Slug(s); slug != "work" {
			break
		}
	}
	return fmt.Sprintf("session/%s-%s", id, slug)
}

// repoWords is the repositories' own names (no owner), e.g. "web api".
func repoWords(repos []RepoFocus) string {
	words := make([]string, 0, len(repos))
	for _, r := range repos {
		words = append(words, r.Repo[strings.LastIndex(r.Repo, "/")+1:])
	}
	return strings.Join(words, " ")
}

// OpenSession opens the session's thread and starts it (within the user's running-thread
// limit): the employee then gets the user's first message.
func (o *Orchestrator) OpenSession(ctx context.Context, req SessionRequest) (core.Thread, error) {
	role := SessionRole(req.Role, req.Model, req.NodeID)
	return o.OpenThread(ctx, ThreadRequest{UserID: req.UserID, ProjectID: req.ProjectID, Title: req.Title,
		Brief: req.Message, OpenedBy: "user", Repos: req.Repos, Solo: &role, SoloMode: req.PermissionMode, SoloEffort: req.Effort, Branch: SessionBranch(req.ProjectID, req.Branch, req.Title, req.Repos)})
}

// startSolo delivers a session's first message: the brief is the user's own words, shown in the
// conversation as their message (the repositories are in the employee's prompt).
func (o *Orchestrator) startSolo(ctx context.Context, tx *core.Tx, t core.Thread, lead core.Employee) error {
	rec := &core.NewMessage{ConversationID: t.ID, Role: "user", Author: "user", Kind: "chat", Content: t.Brief}
	_, err := o.Deliver(ctx, tx, lead, Delivery{Text: t.Brief, Kind: "user", Priority: "next", Record: rec})
	return err
}

// SessionThread is a session's thread (its first and only one).
func SessionThread(ctx context.Context, q core.DB, projectID string) (core.Thread, error) {
	threads, err := core.Threads(ctx, q, projectID, nil)
	if err != nil {
		return core.Thread{}, err
	}
	for _, t := range threads {
		if t.Solo() {
			return t, nil
		}
	}
	return core.Thread{}, core.ErrNotFound
}

// SessionReposChanged follows the repositories of a session after the user added or removed some
// (the project's repositories are already saved): added ones get the branch picked for them as
// the thread's base and are checked out on the node of the employee's live session; removed
// ones are taken off the employee's node (unpushed work is kept); the employee is told either way.
func (o *Orchestrator) SessionReposChanged(ctx context.Context, projectID string, added []RepoFocus, removed []string) error {
	if len(added) == 0 && len(removed) == 0 {
		return nil
	}
	p, err := o.Project(ctx, projectID)
	if err != nil {
		return err
	}
	o.WarmRepos(ctx, p)
	return o.core.InTx(ctx, func(tx *core.Tx) error {
		t, err := SessionThread(ctx, tx, projectID)
		if errors.Is(err, core.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if _, err := applyFocus(ctx, tx, p, t.ID, added); err != nil {
			return err
		}
		lead, err := core.Lead(ctx, tx, t.ID)
		if err != nil {
			return err
		}
		if err := o.dropCheckouts(ctx, tx, lead, t, removed); err != nil {
			return err
		}
		if core.ThreadEnded(t.Status) {
			return nil // the next message reopens it and its session checks out every repository
		}
		if err := o.checkoutAdded(ctx, tx, lead, added); err != nil {
			return err
		}
		note := reposNote(p, t, added, removed)
		_, err = o.Deliver(ctx, tx, lead, Delivery{Text: note, Kind: "system", Priority: "later"})
		return err
	})
}

// checkoutAdded queues git.prepare of each added repository on the node of the employee's live
// session (a session opened later prepares every repository anyway).
func (o *Orchestrator) checkoutAdded(ctx context.Context, tx *core.Tx, lead core.Employee, added []RepoFocus) error {
	s, err := core.LiveSession(ctx, tx, lead.ID)
	if errors.Is(err, core.ErrNotFound) || (err == nil && s.NodeID == nil) {
		return nil
	}
	if err != nil {
		return err
	}
	env, err := o.loadEnv(ctx, tx, lead)
	if err != nil {
		return err
	}
	var paths []string
	for _, r := range env.repos {
		if !slices.ContainsFunc(added, func(f RepoFocus) bool { return strings.EqualFold(f.Repo, r.Slug) }) {
			continue
		}
		if _, err := o.prepareRepo(ctx, tx, env, *s.NodeID, r, prepareOpts{}); err != nil {
			return err
		}
		paths = append(paths, PathsOf(lead, env.thread).RepoCwd(r.Slug))
	}
	if len(paths) == 0 {
		return nil
	}
	return o.placeEmployee(ctx, tx, lead, *s.NodeID, paths)
}

// dropCheckouts removes the employee's checkouts of removed repositories from its node, keeping
// unpushed work, and forgets them in the thread's node paths (thread cleanup would no longer
// find their shared clone).
func (o *Orchestrator) dropCheckouts(ctx context.Context, tx *core.Tx, lead core.Employee, t core.Thread, removed []string) error {
	if len(removed) == 0 || lead.NodeID == nil {
		return nil
	}
	cwd := PathsOf(lead, &t)
	for _, slug := range removed {
		path := cwd.RepoCwd(slug)
		body := protocol.GitCleanup{JobID: protocol.JobID(core.NewID("g")), Shared: protocol.RelPath(SharedDir(t.ProjectID, slug)),
			Paths: []protocol.RelPath{protocol.RelPath(path)}, KeepUnpushed: true, DeleteLocalBranches: true}
		if _, err := o.gw.Enqueue(ctx, tx, *lead.NodeID, nodegw.Command{Type: protocol.TypeGitCleanup, Body: body,
			Meta: map[string]any{"purpose": "cleanup", "thread_id": t.ID}}); err != nil {
			return err
		}
		if _, err := store.Exec(ctx, tx, `UPDATE at_thread_nodes SET paths = array_remove(paths, $2) WHERE thread_id = $1`,
			t.ID, path); err != nil {
			return err
		}
	}
	return nil
}

// reposNote tells the employee how its directory changed.
func reposNote(p store.Project, t core.Thread, added []RepoFocus, removed []string) string {
	var b strings.Builder
	b.WriteString("（平台）用户修改了这个会话的仓库。")
	for _, f := range added {
		base := strings.TrimSpace(f.Base)
		if base == "" {
			base = "默认分支"
		}
		fmt.Fprintf(&b, "\n- 新增 %s：正在拉到工作目录下的 ./%s（从 %s 切出你的分支 %s），稍等片刻目录就会出现。", f.Repo, ghops.Dir(f.Repo), base, t.Branch)
	}
	for _, slug := range removed {
		fmt.Fprintf(&b, "\n- 移除 %s：目录 ./%s 会被删掉（还没推送的提交会保留），不要再改它。", slug, ghops.Dir(slug))
	}
	fmt.Fprintf(&b, "\n现在的仓库：%s。", strings.Join(repoNames(p), "、"))
	return b.String()
}
