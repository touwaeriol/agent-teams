package delivery

import (
	"context"
	"fmt"
	"strings"

	protocol "github.com/touwaeriol/agent-teams/protocol"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw"
	"github.com/touwaeriol/agent-teams/server/internal/orchestrator"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// ThreadEnded cleans up after done/archived/failed (§8.4): git.cleanup on every node the
// thread used (worktrees with unpushed work are kept and reported) and the member branches.
func (e *Engine) ThreadEnded(ctx context.Context, threadID string) {
	t, err := core.ThreadByID(ctx, e.core.Pool, threadID)
	if err != nil {
		return
	}
	if err := e.queueCleanup(ctx, t); err != nil {
		e.log.Warn("queue cleanup", "thread_id", threadID, "err", err)
	}
	e.deleteMemberBranches(ctx, t)
}

func (e *Engine) queueCleanup(ctx context.Context, t core.Thread) error {
	rows, err := e.core.Pool.Query(ctx, `SELECT node_id, paths FROM at_thread_nodes WHERE thread_id = $1 AND cleaned_at IS NULL`, t.ID)
	if err != nil {
		return err
	}
	type place struct {
		node  string
		paths []string
	}
	var places []place
	for rows.Next() {
		var p place
		if rows.Scan(&p.node, &p.paths) == nil {
			places = append(places, p)
		}
	}
	rows.Close()
	shared := fmt.Sprintf("projects/%s/repo", t.ProjectID)
	return e.core.InTx(ctx, func(tx *core.Tx) error {
		for _, p := range places {
			if err := e.cleanupNode(ctx, tx, t, p.node, shared, p.paths); err != nil {
				return err
			}
		}
		return nil
	})
}

func (e *Engine) cleanupNode(ctx context.Context, tx *core.Tx, t core.Thread, nodeID, shared string, paths []string) error {
	rel := make([]protocol.RelPath, len(paths))
	for i, p := range paths {
		rel[i] = protocol.RelPath(p)
	}
	body := protocol.GitCleanup{JobID: protocol.JobID(core.NewID("g")), Shared: protocol.RelPath(shared), Paths: rel,
		KeepUnpushed: true, DeleteLocalBranches: true}
	_, err := e.gw.Enqueue(ctx, tx, nodeID, nodegw.Command{Type: protocol.TypeGitCleanup, Body: body,
		Meta: map[string]any{"purpose": "cleanup", "thread_id": t.ID}})
	return err
}

func (e *Engine) deleteMemberBranches(ctx context.Context, t core.Thread) {
	_, repo, err := e.repoOf(ctx, t.ProjectID)
	if err != nil {
		return
	}
	emps, err := core.ThreadEmployees(ctx, e.core.Pool, t.ID)
	if err != nil {
		return
	}
	for _, m := range emps {
		if m.Tier != "member" {
			continue
		}
		if err := e.gh.DeleteRef(ctx, repo, orchestrator.MemberBranch(t.No, m.Name)); err != nil {
			e.log.Warn("delete member branch", "thread_id", t.ID, "member", m.Name, "err", err)
		}
	}
}

// cleaned records a cleanup result: the node is done, kept worktrees are reported.
func (e *Engine) cleaned(ctx context.Context, tx *core.Tx, node store.Node, meta map[string]any, r *protocol.GitResult) error {
	threadID := str(meta["thread_id"])
	if threadID == "" {
		return nil
	}
	if _, err := tx.Exec(ctx, `UPDATE at_thread_nodes SET cleaned_at = now() WHERE thread_id = $1 AND node_id = $2`,
		threadID, node.ID); err != nil {
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
	rows, err := tx.Query(ctx, `SELECT DISTINCT n.node_id FROM at_thread_nodes n JOIN at_threads t ON t.id = n.thread_id
		WHERE t.project_id = $1 UNION SELECT DISTINCT node_id FROM at_employees WHERE project_id = $1 AND node_id IS NOT NULL`, projectID)
	if err != nil {
		return err
	}
	var nodes []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			nodes = append(nodes, id)
		}
	}
	rows.Close()
	for _, id := range nodes {
		body := protocol.GitPurge{JobID: protocol.JobID(core.NewID("g")), Path: "projects/" + projectID}
		if _, err := e.gw.Enqueue(ctx, tx, id, nodegw.Command{Type: protocol.TypeGitPurge, Body: body}); err != nil {
			return err
		}
	}
	return nil
}
