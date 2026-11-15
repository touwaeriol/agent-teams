package delivery

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/ghops"
)

// PRRequest opens a thread's pull request (open_pr tool, "开 PR" button).
type PRRequest struct {
	Title string
	Body  string
	Force bool   // skip "all plan steps done"
	By    string // employee name or "user"
}

// OpenPR checks the preconditions (§8.4), opens head thread branch → base and moves the thread to review.
func (e *Engine) OpenPR(ctx context.Context, threadID string, req PRRequest) (core.Thread, error) {
	t, err := core.ThreadByID(ctx, e.core.Pool, threadID)
	if err != nil {
		return t, err
	}
	if err := e.prReady(ctx, t, req.Force); err != nil {
		return t, err
	}
	_, repo, err := e.repoOf(ctx, t.ProjectID)
	if err != nil {
		return t, err
	}
	title, body, err := e.prText(ctx, t, req)
	if err != nil {
		return t, err
	}
	pr, err := e.gh.CreatePR(ctx, repo, t.Branch, t.BaseBranch, title, body)
	if err != nil {
		return t, err
	}
	return e.recordPR(ctx, t, pr, req.By)
}

func (e *Engine) prReady(ctx context.Context, t core.Thread, force bool) error {
	if t.PRNumber != nil {
		return ErrPRExists
	}
	active, err := core.MergeQueue(ctx, e.core.Pool, t.ID, true)
	if err != nil {
		return err
	}
	if len(active) > 0 {
		return ErrMergeInProgress
	}
	if force {
		return nil
	}
	plan, err := core.MainTasks(ctx, e.core.Pool, t.ID)
	if err != nil {
		return err
	}
	var open []string
	for _, m := range plan {
		if m.Status != "done" {
			open = append(open, m.Title)
		}
	}
	if len(open) > 0 {
		return fmt.Errorf("%w：%s", ErrPlanOpen, strings.Join(open, "、"))
	}
	return nil
}

// prText fills a missing title/body from the thread title, plan and task summaries.
func (e *Engine) prText(ctx context.Context, t core.Thread, req PRRequest) (string, string, error) {
	title := orDefault(strings.TrimSpace(req.Title), fmt.Sprintf("#%d %s", t.No, t.Title))
	if strings.TrimSpace(req.Body) != "" {
		return title, req.Body, nil
	}
	plan, err := core.MainTasks(ctx, e.core.Pool, t.ID)
	if err != nil {
		return "", "", err
	}
	tasks, err := core.ThreadTasks(ctx, e.core.Pool, t.ID, core.TaskFilter{Status: []string{"done"}})
	if err != nil {
		return "", "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n## 主任务\n", t.Brief)
	for _, m := range plan {
		fmt.Fprintf(&b, "- [%s] %s\n", map[bool]string{true: "x", false: " "}[m.Status == "done"], m.Title)
	}
	b.WriteString("\n## 小任务\n")
	for _, task := range tasks {
		fmt.Fprintf(&b, "- %s（%s）：%s\n", task.ID, task.ClaimedBy, clip(task.Summary, 300))
	}
	b.WriteString("\n— 由 Agent Teams 讨论串生成")
	return title, b.String(), nil
}

func (e *Engine) recordPR(ctx context.Context, t core.Thread, pr ghops.PR, by string) (core.Thread, error) {
	err := e.core.InTx(ctx, func(tx *core.Tx) error {
		if err := core.UpdateThread(ctx, tx, t.ID, map[string]any{"pr_number": pr.Number, "pr_url": pr.HTMLURL, "pr_state": "open"}); err != nil {
			return err
		}
		detail := map[string]any{"number": pr.Number, "state": "open", "additions": pr.Additions, "deletions": pr.Deletions}
		if _, err := e.Publish(ctx, tx, t.ProjectID, &t.ID, "pr", fmt.Sprintf("#%d %s", pr.Number, t.Title), pr.HTMLURL,
			orDefault(by, "platform"), detail); err != nil {
			return err
		}
		cur, err := core.LockThread(ctx, tx, t.ID)
		if err != nil {
			return err
		}
		return e.orch.SetStatus(ctx, tx, cur, "review", "")
	})
	if err != nil {
		return t, err
	}
	e.Kick(ctx)
	return core.ThreadByID(ctx, e.core.Pool, t.ID)
}

// Accept: with require_acceptance the PR merges once its checks pass; without it, merge now
// (409 checks_pending when they have not passed).
func (e *Engine) Accept(ctx context.Context, userID, threadID string) (core.Thread, error) {
	t, err := core.ThreadOfUser(ctx, e.core.Pool, userID, threadID)
	if err != nil {
		return t, err
	}
	if t.PRNumber == nil || t.PRState == "merged" {
		return t, ErrNoPR
	}
	p, repo, err := e.repoOf(ctx, t.ProjectID)
	if err != nil {
		return t, err
	}
	audit(ctx, e.core, userID, "thread_accept", t.ID)
	if p.RequireAcceptance {
		if err := e.setPRState(ctx, t, "accepting"); err != nil {
			return t, err
		}
		e.Kick(ctx)
		return core.ThreadByID(ctx, e.core.Pool, t.ID)
	}
	verdict, err := e.prVerdict(ctx, t, repo)
	if err != nil {
		return t, err
	}
	if verdict != CIPass {
		return t, ErrChecksPending
	}
	return t, e.mergePR(ctx, t, repo)
}

func audit(ctx context.Context, cs *core.Store, userID, action, target string) {
	_, _ = cs.Pool.Exec(ctx, `INSERT INTO at_audit_log (user_id, actor, action, target_type, target_id) VALUES ($1, $2, $3, 'thread', $4)`,
		userID, "user:"+userID, action, target)
}

func (e *Engine) setPRState(ctx context.Context, t core.Thread, state string) error {
	return e.core.InTx(ctx, func(tx *core.Tx) error {
		if err := core.UpdateThread(ctx, tx, t.ID, map[string]any{"pr_state": state}); err != nil {
			return err
		}
		return tx.EmitThread(ctx, t.ID)
	})
}

// Reject sends the reason to the lead and puts the thread back to running.
func (e *Engine) Reject(ctx context.Context, userID, threadID, reason string) (core.Thread, error) {
	t, err := core.ThreadOfUser(ctx, e.core.Pool, userID, threadID)
	if err != nil {
		return t, err
	}
	if t.Status != "review" {
		return t, fmt.Errorf("讨论串不在验收中")
	}
	audit(ctx, e.core, userID, "thread_reject", t.ID)
	err = e.core.InTx(ctx, func(tx *core.Tx) error {
		if t.PRState == "accepting" {
			if err := core.UpdateThread(ctx, tx, t.ID, map[string]any{"pr_state": "open"}); err != nil {
				return err
			}
		}
		if err := e.sched.NotifyLead(ctx, tx, t, fmt.Sprintf("[Thread #%d · 验收未通过] %s", t.No, reason)); err != nil {
			return err
		}
		return e.orch.SetStatus(ctx, tx, t, "running", "")
	})
	if err != nil {
		return t, err
	}
	return core.ThreadByID(ctx, e.core.Pool, t.ID)
}

// prVerdict evaluates the CI rules on the PR head (waited since the PR was opened).
func (e *Engine) prVerdict(ctx context.Context, t core.Thread, repo ghops.Repo) (Verdict, error) {
	v, _, err := e.prCheck(ctx, t, repo)
	return v, err
}

func (e *Engine) prCheck(ctx context.Context, t core.Thread, repo ghops.Repo) (Verdict, []core.CheckView, error) {
	pr, err := e.gh.GetPR(ctx, repo, *t.PRNumber)
	if err != nil {
		return CIPending, nil, err
	}
	if pr.Merged {
		return CIPass, nil, nil
	}
	since := t.UpdatedAt
	if d, err := core.PRDeliverable(ctx, e.core.Pool, t.ID); err == nil {
		since = d.CreatedAt
	}
	v, failed, err := e.ciVerdict(ctx, t, repo, pr.Head.SHA, nil, since)
	if err == nil && v == CIPass && pr.Mergeable != nil && !*pr.Mergeable {
		return CIFail, []core.CheckView{{Name: "mergeable", Status: "completed", Conclusion: "conflict",
			Summary: "PR 与基准分支有冲突"}}, nil
	}
	return v, failed, err
}

// mergePR merges with the project's merge method and finishes the thread.
func (e *Engine) mergePR(ctx context.Context, t core.Thread, repo ghops.Repo) error {
	d, err := core.DeliveryOf(ctx, e.core.Pool, t.ProjectID)
	if err != nil {
		return err
	}
	pr, err := e.gh.GetPR(ctx, repo, *t.PRNumber)
	if err != nil {
		return err
	}
	if !pr.Merged {
		if err := e.gh.MergePR(ctx, repo, *t.PRNumber, orDefault(d.MergeMethod, "squash"), pr.Head.SHA); err != nil {
			return err
		}
	}
	return e.Merged(ctx, t.ID)
}

// Merged records that the thread's PR is merged: deliverable state, thread done.
func (e *Engine) Merged(ctx context.Context, threadID string) error {
	return e.core.InTx(ctx, func(tx *core.Tx) error {
		t, err := core.LockThread(ctx, tx, threadID)
		if err != nil || t.Status == "done" {
			return ignoreMissing(err)
		}
		if err := core.UpdateThread(ctx, tx, t.ID, map[string]any{"pr_state": "merged"}); err != nil {
			return err
		}
		if err := e.markPRDeliverable(ctx, tx, t.ID, "merged"); err != nil {
			return err
		}
		return e.orch.SetStatus(ctx, tx, t, "done", "")
	})
}

func (e *Engine) markPRDeliverable(ctx context.Context, tx *core.Tx, threadID, state string) error {
	d, err := core.PRDeliverable(ctx, tx, threadID)
	if err != nil {
		return ignoreMissing(err)
	}
	d.Detail["state"] = state
	d, err = core.UpdateDeliverable(ctx, tx, d.ID, nil, d.Detail)
	if err != nil {
		return err
	}
	return tx.EmitDeliverable(ctx, d)
}

// prTick merges PRs whose checks passed (auto-merge, or accepted) and reports failures to the lead.
func (e *Engine) prTick(ctx context.Context) {
	rows, err := e.core.Pool.Query(ctx, `SELECT t.id FROM at_threads t JOIN at_projects p ON p.id = t.project_id
		WHERE t.status = 'review' AND t.pr_number IS NOT NULL
		AND (t.pr_state = 'accepting' OR (t.pr_state = 'open' AND NOT p.require_acceptance))`)
	if err != nil {
		e.log.Warn("pr tick", "err", err)
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		if err := e.prStep(ctx, id); err != nil {
			e.log.Warn("pr step", "thread_id", id, "err", err)
		}
	}
}

func (e *Engine) prStep(ctx context.Context, threadID string) error {
	t, err := core.ThreadByID(ctx, e.core.Pool, threadID)
	if err != nil {
		return err
	}
	_, repo, err := e.repoOf(ctx, t.ProjectID)
	if err != nil {
		return err
	}
	v, failed, err := e.prCheck(ctx, t, repo)
	switch {
	case err != nil:
		return err
	case v == CIPass:
		return e.mergePR(ctx, t, repo)
	case v == CIFail || v == CIMissing:
		return e.prFailed(ctx, t, failed)
	}
	return nil
}

func (e *Engine) prFailed(ctx context.Context, t core.Thread, failed []core.CheckView) error {
	var b strings.Builder
	fmt.Fprintf(&b, "[Thread #%d · CI 失败] PR %s 的检查没有通过，请安排修复：\n", t.No, deref(t.PRURL))
	for _, c := range failed {
		fmt.Fprintf(&b, "- %s：%s %s\n", c.Name, orDefault(c.Conclusion, c.Status), c.URL)
	}
	return e.core.InTx(ctx, func(tx *core.Tx) error {
		if err := core.UpdateThread(ctx, tx, t.ID, map[string]any{"pr_state": "open"}); err != nil {
			return err
		}
		if err := e.sched.NotifyLead(ctx, tx, t, b.String()); err != nil {
			return err
		}
		return e.orch.SetStatus(ctx, tx, t, "running", "")
	})
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// GitHubEvent reacts to verified webhooks: check results wake the loop, a merged PR finishes its thread.
func (e *Engine) GitHubEvent(ctx context.Context, event string, payload json.RawMessage) error {
	switch event {
	case "check_run", "check_suite", "status":
		e.Kick(ctx)
	case "pull_request":
		return e.pullRequestEvent(ctx, payload)
	}
	return nil
}

func (e *Engine) pullRequestEvent(ctx context.Context, payload json.RawMessage) error {
	var ev struct {
		Action      string `json:"action"`
		PullRequest struct {
			HTMLURL string `json:"html_url"`
			Merged  bool   `json:"merged"`
		} `json:"pull_request"`
	}
	if err := json.Unmarshal(payload, &ev); err != nil || ev.Action != "closed" {
		return err
	}
	var id string
	err := e.core.Pool.QueryRow(ctx, `SELECT id FROM at_threads WHERE pr_url = $1`, ev.PullRequest.HTMLURL).Scan(&id)
	if err != nil {
		return ignoreMissing(core.MapErr(err))
	}
	if ev.PullRequest.Merged {
		return e.Merged(ctx, id)
	}
	return e.core.InTx(ctx, func(tx *core.Tx) error {
		if err := core.UpdateThread(ctx, tx, id, map[string]any{"pr_state": "closed"}); err != nil {
			return err
		}
		if err := e.markPRDeliverable(ctx, tx, id, "closed"); err != nil {
			return err
		}
		return tx.EmitThread(ctx, id)
	})
}
