package orchestrator

import (
	"context"
	"fmt"
	"strings"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/ghops"
)

// Reopen puts a finished (done or failed) thread back to work. An archived thread is refused
// (ErrArchived): it is unarchived first, which keeps it finished, then reopened. No session
// starts here: the lead resumes its old conversation (SDK session id, recap when the node lost
// it) with the next message, and members with the lead's next message to them.
//
// The status goes back to running (queued when the user's running-thread limit is reached),
// the old summary moves into a note in the thread chat and is cleared, finished_at is cleared
// and the stalled note is re-armed (afterStatus). Pull requests that are merged or closed are
// history: their rows lose the PR so open_pr opens a new one and finish works again. A
// repository whose PR merged also forgets its thread branch, and that branch is deleted on the
// host after the commit (its work is in the base): new work cuts the thread branch again from
// the updated base. A closed (unmerged) PR keeps its branch and its commits.
// The coordinator is told "[Thread #n · 标题 · running] 用户重新打开了讨论串 #n。".
func (o *Orchestrator) Reopen(ctx context.Context, tx *core.Tx, t core.Thread, cause string) error {
	switch t.Status {
	case "archived":
		return ErrArchived
	case "done", "failed":
	default:
		return nil // still going: nothing to reopen
	}
	merged, err := o.forgetFinishedPRs(ctx, tx, t)
	if err != nil {
		return err
	}
	if err := o.reopenNote(ctx, tx, t, cause); err != nil {
		return err
	}
	if err := core.UpdateThread(ctx, tx, t.ID, map[string]any{"summary": "", "finished_at": nil}); err != nil {
		return err
	}
	next := "running"
	if free, err := o.threadSlotFree(ctx, tx, t.UserID); err != nil {
		return err
	} else if !free {
		next = "queued"
	}
	if err := o.setStatus(ctx, tx, t, next, ""); err != nil {
		return err
	}
	line := fmt.Sprintf("用户重新打开了讨论串 #%d。", t.No)
	if err := tx.Savepoint(ctx, func(sub *core.Tx) error { return o.notifyCoordinator(ctx, sub, t, next, line) }); err != nil {
		o.log.Warn("notify coordinator", "thread_id", t.ID, "status", next, "err", err)
	}
	if len(merged) > 0 {
		tx.AfterCommit(func(ctx context.Context) { o.deleteThreadBranches(context.WithoutCancel(ctx), t, merged) })
	}
	return nil
}

// forgetFinishedPRs clears the merged and closed pull requests of a thread (rows and the
// thread's first-PR columns) and returns the repositories whose PR merged: their thread branch
// is forgotten too, so the next delivery cuts it again from the updated base.
func (o *Orchestrator) forgetFinishedPRs(ctx context.Context, tx *core.Tx, t core.Thread) ([]string, error) {
	repos, err := core.ThreadRepos(ctx, tx, t.ID)
	if err != nil {
		return nil, err
	}
	var merged []string
	for _, r := range repos {
		if r.PRNumber == nil || (r.PRState != "merged" && r.PRState != "closed") {
			continue
		}
		set := map[string]any{"pr_number": nil, "pr_url": nil, "pr_state": ""}
		if r.PRState == "merged" {
			set["branch_created"] = false
			merged = append(merged, r.Repo)
		}
		if err := core.UpdateThreadRepo(ctx, tx, t.ID, r.Repo, set); err != nil {
			return nil, err
		}
	}
	if t.PRState == "merged" || t.PRState == "closed" || t.PRNumber != nil {
		left, err := core.ThreadRepos(ctx, tx, t.ID)
		if err != nil {
			return nil, err
		}
		set := map[string]any{"pr_number": nil, "pr_url": nil, "pr_state": ""}
		for _, r := range left {
			if r.PRNumber != nil { // an open PR survives (failed thread): it stays the thread's first PR
				set = map[string]any{"pr_number": *r.PRNumber, "pr_url": r.PRURL, "pr_state": r.PRState}
				break
			}
		}
		if err := core.UpdateThread(ctx, tx, t.ID, set); err != nil {
			return nil, err
		}
	}
	return merged, nil
}

// reopenNote records the reopening in the thread chat, keeping the old summary there.
func (o *Orchestrator) reopenNote(ctx context.Context, tx *core.Tx, t core.Thread, cause string) error {
	text := "用户重新打开了讨论串。"
	if cause == "message" {
		text = "用户发来新消息，讨论串重新打开。"
	}
	meta := map[string]any{"reopened": true, "from": t.Status}
	if s := strings.TrimSpace(t.Summary); s != "" {
		text += "\n\n之前的总结：\n" + s
		meta["previous_summary"] = s
	}
	m, err := core.InsertMessage(ctx, tx, core.NewMessage{ConversationID: t.ID, Role: "system", Author: "platform", Kind: "note",
		Content: text, Meta: meta})
	if err != nil {
		return err
	}
	return tx.EmitMessage(ctx, t.ProjectID, &t.ID, m)
}

// deleteThreadBranches removes a reopened thread's merged branches on the host, so the next
// delivery creates them again from the updated base. Failures are logged: a branch left behind
// would be reused by EnsureThreadBranch, so the warning names it.
func (o *Orchestrator) deleteThreadBranches(ctx context.Context, t core.Thread, slugs []string) {
	if o.gh == nil {
		return
	}
	p, err := o.Project(ctx, t.ProjectID)
	if err != nil {
		return
	}
	repos := o.Repos(ctx, p)
	for _, slug := range slugs {
		r, ok := ghops.Find(repos, slug)
		if !ok {
			continue
		}
		if err := o.gh.DeleteRef(ctx, r, t.Branch); err != nil {
			o.log.Warn("delete merged thread branch", "thread_id", t.ID, "repo", slug, "branch", t.Branch, "err", err)
		}
	}
}
