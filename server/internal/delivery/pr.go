package delivery

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/ghops"
)

// PRRequest opens a thread's pull requests (open_pr tool, "开 PR" button).
type PRRequest struct {
	Title string
	Body  string
	Force bool   // skip "all plan steps done"
	By    string // employee name or "user"
	Repo  string // only this repository ("" = every repository the thread changed)
	Base  string // PR base for Repo (or for the only repository to open)
}

// OpenPR checks the preconditions (§8.4) and opens one pull request per changed repository
// (thread branch → that repository's base), then moves the thread to review.
func (e *Engine) OpenPR(ctx context.Context, threadID string, req PRRequest) (core.Thread, error) {
	t, err := core.ThreadByID(ctx, e.core.Pool, threadID)
	if err != nil {
		return t, err
	}
	if err := e.prReady(ctx, t, req.Force); err != nil {
		return t, err
	}
	targets, err := e.prTargets(ctx, t, req)
	if err != nil {
		return t, err
	}
	title, body, err := e.prText(ctx, t, req)
	if err != nil {
		return t, err
	}
	opened := 0
	for _, target := range targets {
		ok, err := e.openOne(ctx, t, target, req, title, body)
		if err != nil {
			return t, err
		}
		if ok {
			opened++
		}
	}
	if opened == 0 {
		return t, ErrNothingToPR
	}
	return e.toReview(ctx, t)
}

// prTarget is one repository to open a pull request in.
type prTarget struct {
	repo ghops.Repo
	base string
}

// prTargets are the repositories whose thread branch exists and that have no pull request yet.
func (e *Engine) prTargets(ctx context.Context, t core.Thread, req PRRequest) ([]prTarget, error) {
	rows, err := core.ThreadRepos(ctx, e.core.Pool, t.ID)
	if err != nil {
		return nil, err
	}
	pending, withPR := branchedWithoutPR(rows, req.Repo)
	if len(pending) == 0 && withPR {
		return nil, ErrPRExists
	}
	if len(pending) == 0 {
		return nil, ErrNothingToPR
	}
	if req.Base != "" && req.Repo == "" && len(pending) > 1 {
		return nil, ErrBaseNeedsRepo
	}
	out := make([]prTarget, 0, len(pending))
	for _, tr := range pending {
		repo, err := e.repoIn(ctx, t.ProjectID, tr.Repo)
		if err != nil {
			return nil, err
		}
		out = append(out, prTarget{repo: repo, base: orDefault(strings.TrimSpace(req.Base), e.orch.BaseFor(ctx, e.core.Pool, t, repo))})
	}
	return out, nil
}

func branchedWithoutPR(rows []core.ThreadRepo, only string) ([]core.ThreadRepo, bool) {
	var pending []core.ThreadRepo
	withPR := false
	for _, tr := range rows {
		if only != "" && !strings.EqualFold(tr.Repo, only) {
			continue
		}
		withPR = withPR || tr.PRNumber != nil
		if tr.BranchCreated && tr.PRNumber == nil {
			pending = append(pending, tr)
		}
	}
	return pending, withPR
}

// openOne opens a repository's pull request; false when GitHub finds nothing to merge there.
func (e *Engine) openOne(ctx context.Context, t core.Thread, target prTarget, req PRRequest, title, body string) (bool, error) {
	pr, err := e.gh.CreatePR(ctx, target.repo, t.Branch, target.base, title, body)
	if ghops.Status(err) == 422 && strings.Contains(err.Error(), "No commits between") {
		return false, nil
	}
	if ghops.Status(err) == 422 && strings.Contains(err.Error(), "already exists") {
		pr, err = e.adoptPR(ctx, t, target, err)
	}
	if err != nil {
		return false, fmt.Errorf("%s：%w", target.repo.Slug, err)
	}
	return true, e.recordPR(ctx, t, target.repo.Slug, pr, req.By)
}

// adoptPR takes over the open pull request someone already opened from the thread branch
// (an employee with gh, architecture.md §8.4), so acceptance and merging track it.
func (e *Engine) adoptPR(ctx context.Context, t core.Thread, target prTarget, createErr error) (ghops.PR, error) {
	pr, ok, err := e.gh.OpenPRFor(ctx, target.repo, t.Branch, target.base)
	if err != nil {
		return pr, err
	}
	if !ok {
		return pr, createErr
	}
	return pr, nil
}

func (e *Engine) prReady(ctx context.Context, t core.Thread, force bool) error {
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

// recordPR stores one repository's pull request and its deliverable; the thread keeps the first
// pull request in pr_number/pr_url for older clients.
func (e *Engine) recordPR(ctx context.Context, t core.Thread, repo string, pr ghops.PR, by string) error {
	return e.core.InTx(ctx, func(tx *core.Tx) error {
		if err := core.SetThreadRepoPR(ctx, tx, t.ID, repo, pr.Number, pr.HTMLURL); err != nil {
			return err
		}
		set := map[string]any{"pr_state": "open"}
		if cur, err := core.LockThread(ctx, tx, t.ID); err == nil && cur.PRNumber == nil {
			set["pr_number"], set["pr_url"] = pr.Number, pr.HTMLURL
		}
		if err := core.UpdateThread(ctx, tx, t.ID, set); err != nil {
			return err
		}
		detail := map[string]any{"number": pr.Number, "state": "open", "repo": repo, "additions": pr.Additions, "deletions": pr.Deletions}
		_, err := e.Publish(ctx, tx, t.ProjectID, &t.ID, "pr", fmt.Sprintf("%s#%d %s", repo, pr.Number, t.Title), pr.HTMLURL,
			orDefault(by, "platform"), detail)
		return err
	})
}

// toReview moves the thread to review once its pull requests are open.
func (e *Engine) toReview(ctx context.Context, t core.Thread) (core.Thread, error) {
	err := e.core.InTx(ctx, func(tx *core.Tx) error {
		cur, err := core.LockThread(ctx, tx, t.ID)
		if err != nil {
			return err
		}
		if err := tx.EmitThread(ctx, t.ID); err != nil {
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

// openPRs are the thread's pull requests that are still open, with their repositories.
func (e *Engine) openPRs(ctx context.Context, t core.Thread) ([]core.ThreadRepo, []ghops.Repo, error) {
	rows, err := core.ThreadRepos(ctx, e.core.Pool, t.ID)
	if err != nil {
		return nil, nil, err
	}
	var prs []core.ThreadRepo
	var repos []ghops.Repo
	for _, tr := range rows {
		if !tr.HasOpenPR() {
			continue
		}
		repo, err := e.repoIn(ctx, t.ProjectID, tr.Repo)
		if err != nil {
			return nil, nil, err
		}
		prs, repos = append(prs, tr), append(repos, repo)
	}
	return prs, repos, nil
}

// Accept: with require_acceptance the PRs merge once their checks pass; without it, merge now
// (409 checks_pending when they have not all passed).
func (e *Engine) Accept(ctx context.Context, userID, threadID string) (core.Thread, error) {
	t, err := core.ThreadOfUser(ctx, e.core.Pool, userID, threadID)
	if err != nil {
		return t, err
	}
	prs, _, err := e.openPRs(ctx, t)
	if err != nil {
		return t, err
	}
	if len(prs) == 0 {
		return t, ErrNoPR
	}
	p, err := e.orch.Project(ctx, t.ProjectID)
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
	verdict, _, err := e.prCheck(ctx, t)
	if err != nil {
		return t, err
	}
	if verdict != CIPass {
		return t, ErrChecksPending
	}
	return t, e.mergePRs(ctx, t)
}

func audit(ctx context.Context, cs *core.Store, userID, action, target string) {
	_ = cs.Pool.Gorm(ctx).Table("at_audit_log").Create(map[string]any{"user_id": userID, "actor": "user:" + userID,
		"action": action, "target_type": "thread", "target_id": target}).Error
}

func (e *Engine) setPRState(ctx context.Context, t core.Thread, state string) error {
	return e.core.InTx(ctx, func(tx *core.Tx) error {
		if err := core.UpdateThread(ctx, tx, t.ID, map[string]any{"pr_state": state}); err != nil {
			return err
		}
		if err := core.SetThreadPRStates(ctx, tx, t.ID, state); err != nil {
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
			if err := core.SetThreadPRStates(ctx, tx, t.ID, "open"); err != nil {
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

// prCheck evaluates the CI rules on every open pull request of the thread: pass when all pass,
// fail (with every failed check, named by repository) when one fails, else pending.
func (e *Engine) prCheck(ctx context.Context, t core.Thread) (Verdict, []core.CheckView, error) {
	prs, repos, err := e.openPRs(ctx, t)
	if err != nil {
		return CIPending, nil, err
	}
	verdict, failed := CIPass, []core.CheckView{}
	for i, tr := range prs {
		v, f, err := e.prCheckOne(ctx, t, repos[i], tr)
		if err != nil {
			return CIPending, nil, err
		}
		verdict = worse(verdict, v)
		for _, c := range f {
			c.Name = tr.Repo + " · " + c.Name
			failed = append(failed, c)
		}
	}
	return verdict, failed, nil
}

// worse combines two verdicts: fail/missing beat pending, pending beats pass.
func worse(a, b Verdict) Verdict {
	rank := map[Verdict]int{CIPass: 0, CIPending: 1, CIMissing: 2, CIFail: 3}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

func (e *Engine) prCheckOne(ctx context.Context, t core.Thread, repo ghops.Repo, tr core.ThreadRepo) (Verdict, []core.CheckView, error) {
	pr, err := e.gh.GetPR(ctx, repo, *tr.PRNumber)
	if err != nil {
		return CIPending, nil, err
	}
	if pr.Merged {
		return CIPass, nil, nil
	}
	since := tr.UpdatedAt
	if d, err := core.PRDeliverableByURL(ctx, e.core.Pool, t.ID, deref(tr.PRURL)); err == nil {
		since = d.CreatedAt
	}
	v, failed, err := e.ciVerdict(ctx, t, repo, pr.Head.SHA, nil, since)
	if err == nil && v == CIPass && pr.Mergeable != nil && !*pr.Mergeable {
		return CIFail, []core.CheckView{{Name: "mergeable", Status: "completed", Conclusion: "conflict",
			Summary: "PR 与基准分支有冲突"}}, nil
	}
	return v, failed, err
}

// mergePRs merges every open pull request with the project's merge method, then finishes the thread.
func (e *Engine) mergePRs(ctx context.Context, t core.Thread) error {
	d, err := core.DeliveryOf(ctx, e.core.Pool, t.ProjectID)
	if err != nil {
		return err
	}
	prs, repos, err := e.openPRs(ctx, t)
	if err != nil {
		return err
	}
	for i, tr := range prs {
		if err := e.mergeOne(ctx, t, repos[i], tr, orDefault(d.MergeMethod, "squash")); err != nil {
			return err
		}
	}
	return e.Merged(ctx, t.ID)
}

func (e *Engine) mergeOne(ctx context.Context, t core.Thread, repo ghops.Repo, tr core.ThreadRepo, method string) error {
	pr, err := e.gh.GetPR(ctx, repo, *tr.PRNumber)
	if err != nil {
		return err
	}
	if !pr.Merged {
		if err := e.gh.MergePR(ctx, repo, *tr.PRNumber, method, pr.Head.SHA); err != nil {
			return fmt.Errorf("%s：%w", tr.Repo, err)
		}
	}
	return e.core.InTx(ctx, func(tx *core.Tx) error {
		if err := core.SetThreadRepoPRState(ctx, tx, t.ID, tr.Repo, "merged"); err != nil {
			return err
		}
		return e.markPRDeliverable(ctx, tx, t.ID, deref(tr.PRURL), "merged")
	})
}

// Merged records that the thread's pull requests are merged: thread done once none is open.
func (e *Engine) Merged(ctx context.Context, threadID string) error {
	return e.core.InTx(ctx, func(tx *core.Tx) error {
		t, err := core.LockThread(ctx, tx, threadID)
		if err != nil || t.Status == "done" {
			return ignoreMissing(err)
		}
		if open, err := anyOpenPR(ctx, tx, t.ID); err != nil || open {
			return err
		}
		if err := core.UpdateThread(ctx, tx, t.ID, map[string]any{"pr_state": "merged"}); err != nil {
			return err
		}
		return e.orch.SetStatus(ctx, tx, t, "done", "")
	})
}

func anyOpenPR(ctx context.Context, q core.DB, threadID string) (bool, error) {
	rows, err := core.ThreadRepos(ctx, q, threadID)
	for _, tr := range rows {
		if tr.HasOpenPR() {
			return true, err
		}
	}
	return false, err
}

// markPRDeliverable sets the state of the pr deliverable with this URL.
func (e *Engine) markPRDeliverable(ctx context.Context, tx *core.Tx, threadID, url, state string) error {
	d, err := core.PRDeliverableByURL(ctx, tx, threadID, url)
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
	var ids []string
	err := e.core.Pool.Gorm(ctx).Table("at_threads t").Joins("JOIN at_projects p ON p.id = t.project_id").
		Where(`t.status = 'review' AND t.pr_number IS NOT NULL
			AND (t.pr_state = 'accepting' OR (t.pr_state = 'open' AND NOT p.require_acceptance))`).Pluck("t.id", &ids).Error
	if err != nil {
		e.log.Warn("pr tick", "err", err)
		return
	}
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
	v, failed, err := e.prCheck(ctx, t)
	switch {
	case err != nil:
		return err
	case v == CIPass:
		return e.mergePRs(ctx, t)
	case v == CIFail || v == CIMissing:
		return e.prFailed(ctx, t, failed)
	}
	return nil
}

func (e *Engine) prFailed(ctx context.Context, t core.Thread, failed []core.CheckView) error {
	var b strings.Builder
	fmt.Fprintf(&b, "[Thread #%d · CI 失败] PR 的检查没有通过，请安排修复：\n", t.No)
	for _, c := range failed {
		fmt.Fprintf(&b, "- %s：%s %s\n", c.Name, orDefault(c.Conclusion, c.Status), c.URL)
	}
	return e.core.InTx(ctx, func(tx *core.Tx) error {
		if err := core.UpdateThread(ctx, tx, t.ID, map[string]any{"pr_state": "open"}); err != nil {
			return err
		}
		if err := core.SetThreadPRStates(ctx, tx, t.ID, "open"); err != nil {
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
	tr, err := core.ThreadRepoByPR(ctx, e.core.Pool, ev.PullRequest.HTMLURL)
	if err != nil {
		return ignoreMissing(err)
	}
	state := map[bool]string{true: "merged", false: "closed"}[ev.PullRequest.Merged]
	err = e.core.InTx(ctx, func(tx *core.Tx) error {
		if err := core.SetThreadRepoPRState(ctx, tx, tr.ThreadID, tr.Repo, state); err != nil {
			return err
		}
		if err := e.markPRDeliverable(ctx, tx, tr.ThreadID, ev.PullRequest.HTMLURL, state); err != nil {
			return err
		}
		if state == "merged" {
			return nil
		}
		if err := core.UpdateThread(ctx, tx, tr.ThreadID, map[string]any{"pr_state": "closed"}); err != nil {
			return err
		}
		return tx.EmitThread(ctx, tr.ThreadID)
	})
	if err != nil || state != "merged" {
		return err
	}
	return e.Merged(ctx, tr.ThreadID)
}
