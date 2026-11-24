package delivery

import (
	"context"
	"fmt"
	pathpkg "path"
	"strings"

	"gorm.io/gorm"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/ghops"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw"
	"github.com/touwaeriol/agent-teams/server/internal/orchestrator"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// ThreadEnded cleans up after done/archived/failed (§8.4): git.cleanup on every node the
// thread used (worktrees with unpushed work are kept and reported) and the branches of the
// members that worked there (core.ThreadWorkers: project members too; the members stay).
func (e *Engine) ThreadEnded(ctx context.Context, threadID string) {
	t, err := core.ThreadByID(ctx, e.core.Pool, threadID)
	if err != nil {
		return
	}
	if localSession(ctx, e.core.Pool, t) {
		return // 已解决 keeps a session's local folder (and what was checked out in it) until it is deleted
	}
	if err := e.queueCleanup(ctx, t); err != nil {
		e.log.Warn("queue cleanup", "thread_id", threadID, "err", err)
	}
	e.deleteMemberBranches(ctx, t)
}

// localSession reports whether t is a session whose employee works in a local folder (Spec.Local).
func localSession(ctx context.Context, q core.DB, t core.Thread) bool {
	if !t.Solo() {
		return false
	}
	lead, err := core.Lead(ctx, q, t.ID)
	return err == nil && lead.Spec.Local
}

func (e *Engine) queueCleanup(ctx context.Context, t core.Thread) error {
	return e.core.InTx(ctx, func(tx *core.Tx) error { return e.queueCleanupTx(ctx, tx, t, true) })
}

// PurgeThread queues the removal of every checkout of a deleted thread, unpushed work
// included, and deletes the branches of the members that worked there after the transaction
// commits. Project members are not deleted with the thread.
func (e *Engine) PurgeThread(ctx context.Context, tx *core.Tx, t core.Thread) error {
	if err := e.queueCleanupTx(ctx, tx, t, false); err != nil {
		return err
	}
	emps, err := core.ThreadWorkers(ctx, tx, t.ID)
	if err != nil {
		return err
	}
	if err := e.purgeThreadDirs(ctx, tx, t, emps); err != nil {
		return err
	}
	tx.AfterCommit(func(ctx context.Context) { e.deleteBranches(context.WithoutCancel(ctx), t, emps) })
	return nil
}

// purgeThreadDirs removes projects/<pid>/threads/<no> (lead notes, members' checkouts,
// leftovers) on every node the thread used: the nodes it recorded, its lead's and the nodes of
// the members that worked there.
func (e *Engine) purgeThreadDirs(ctx context.Context, tx *core.Tx, t core.Thread, workers []core.Employee) error {
	ids := make([]string, 0, len(workers))
	for _, w := range workers {
		ids = append(ids, w.ID)
	}
	nodes, err := store.Strings(ctx, tx, `SELECT node_id FROM at_thread_nodes WHERE thread_id = $1
		UNION SELECT node_id FROM at_employees WHERE (thread_id = $1 OR id = ANY($2)) AND node_id IS NOT NULL`, t.ID, ids)
	if err != nil {
		return err
	}
	dir := fmt.Sprintf("projects/%s/threads/%d", t.ProjectID, t.No)
	for _, id := range nodes {
		body := protocol.GitPurge{JobID: protocol.JobID(core.NewID("g")), Path: dir}
		if _, err := e.gw.Enqueue(ctx, tx, id, nodegw.Command{Type: protocol.TypeGitPurge, Body: body}); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) queueCleanupTx(ctx context.Context, tx *core.Tx, t core.Thread, keepUnpushed bool) error {
	var places []struct {
		NodeID string
		Paths  []string `gorm:"serializer:pgtext"`
	}
	err := tx.Gorm(ctx).Table("at_thread_nodes").Select("node_id, paths").Where("thread_id = ? AND cleaned_at IS NULL", t.ID).
		Find(&places).Error
	if err != nil {
		return err
	}
	p, err := e.orch.Project(ctx, t.ProjectID)
	if err != nil {
		return err
	}
	for _, pl := range places {
		for shared, paths := range byShared(p, pl.Paths) {
			if err := e.cleanupNode(ctx, tx, t, pl.NodeID, shared, paths, keepUnpushed); err != nil {
				return err
			}
		}
	}
	return nil
}

// byShared groups checkout paths by the shared clone they belong to: a path ending in a
// repository's directory (owner__name) hangs off that repository's clone, anything else off the
// legacy single clone.
func byShared(p store.Project, paths []string) map[string][]string {
	dirs := map[string]string{}
	for _, r := range p.Repos {
		dirs[ghops.Dir(r.Repo)] = orchestrator.SharedDir(p.ID, r.Repo)
	}
	out := map[string][]string{}
	for _, path := range paths {
		shared, ok := dirs[pathpkg.Base(path)]
		if !ok {
			shared = orchestrator.LegacySharedDir(p.ID)
		}
		out[shared] = append(out[shared], path)
	}
	return out
}

func (e *Engine) cleanupNode(ctx context.Context, tx *core.Tx, t core.Thread, nodeID, shared string, paths []string, keepUnpushed bool) error {
	rel := make([]protocol.RelPath, len(paths))
	for i, p := range paths {
		rel[i] = protocol.RelPath(p)
	}
	body := protocol.GitCleanup{JobID: protocol.JobID(core.NewID("g")), Shared: protocol.RelPath(shared), Paths: rel,
		KeepUnpushed: keepUnpushed, DeleteLocalBranches: true}
	_, err := e.gw.Enqueue(ctx, tx, nodeID, nodegw.Command{Type: protocol.TypeGitCleanup, Body: body,
		Meta: map[string]any{"purpose": "cleanup", "thread_id": t.ID}})
	return err
}

// deleteMemberBranches deletes the branches of the members that worked in the thread in every
// repository of the project.
func (e *Engine) deleteMemberBranches(ctx context.Context, t core.Thread) {
	_, repos, err := e.reposOf(ctx, t.ProjectID)
	if err != nil {
		return
	}
	emps, err := core.ThreadWorkers(ctx, e.core.Pool, t.ID)
	if err != nil {
		return
	}
	e.deleteBranchesIn(ctx, t, emps, repos)
}

// deleteBranches deletes the members' branches of a thread whose rows may already be gone.
func (e *Engine) deleteBranches(ctx context.Context, t core.Thread, emps []core.Employee) {
	_, repos, err := e.reposOf(ctx, t.ProjectID)
	if err == nil {
		e.deleteBranchesIn(ctx, t, emps, repos)
	}
}

func (e *Engine) deleteBranchesIn(ctx context.Context, t core.Thread, emps []core.Employee, repos []ghops.Repo) {
	for _, repo := range repos {
		done := map[string]bool{}
		for _, m := range emps {
			if m.Tier != "member" || done[m.Name] {
				continue
			}
			done[m.Name] = true
			if err := e.gh.DeleteRef(ctx, repo, orchestrator.MemberBranch(t.No, m.Name)); err != nil {
				e.log.Warn("delete member branch", "thread_id", t.ID, "repo", repo.Slug, "member", m.Name, "err", err)
			}
		}
	}
}

// cleaned records a cleanup result: the node is done, kept worktrees are reported.
func (e *Engine) cleaned(ctx context.Context, tx *core.Tx, node store.Node, meta map[string]any, r *protocol.GitResult) error {
	threadID := str(meta["thread_id"])
	if threadID == "" {
		return nil
	}
	if err := tx.Gorm(ctx).Table("at_thread_nodes").Where("thread_id = ? AND node_id = ?", threadID, node.ID).
		Update("cleaned_at", gorm.Expr("now()")).Error; err != nil {
		return err
	}
	if len(r.Kept) == 0 {
		return nil
	}
	t, err := core.ThreadByID(ctx, tx, threadID)
	if err != nil {
		return ignoreMissing(err)
	}
	var kept []string
	for _, k := range r.Kept {
		kept = append(kept, k.Path+"（"+k.Reason+"）")
	}
	return tx.EmitActivity(ctx, t.ProjectID, &t.ID, "platform",
		fmt.Sprintf("节点 %s 上保留了有未推送改动的工作目录：%s", node.Name, strings.Join(kept, "、")))
}

// Purge removes a project's directory on every node it used (project deletion).
func (e *Engine) Purge(ctx context.Context, tx *core.Tx, projectID string) error {
	nodes, err := store.Strings(ctx, tx, `SELECT DISTINCT n.node_id FROM at_thread_nodes n JOIN at_threads t ON t.id = n.thread_id
		WHERE t.project_id = $1 UNION SELECT DISTINCT node_id FROM at_employees WHERE project_id = $1 AND node_id IS NOT NULL`,
		projectID)
	if err != nil {
		return err
	}
	for _, id := range nodes {
		body := protocol.GitPurge{JobID: protocol.JobID(core.NewID("g")), Path: "projects/" + projectID}
		if _, err := e.gw.Enqueue(ctx, tx, id, nodegw.Command{Type: protocol.TypeGitPurge, Body: body}); err != nil {
			return err
		}
	}
	return nil
}
