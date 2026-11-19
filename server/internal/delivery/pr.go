package delivery

import (
	"context"
	"encoding/json"
	"errors"
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
	t, _, err := e.OpenOrResubmit(ctx, threadID, req)
	return t, err
}

// OpenOrResubmit is OpenPR; a thread back in running whose pull requests are all open already
// (failed checks, a reject) is resubmitted for review instead of refused with ErrPRExists
// (resubmitted = true).
func (e *Engine) OpenOrResubmit(ctx context.Context, threadID string, req PRRequest) (core.Thread, bool, error) {
	t, err := core.ThreadByID(ctx, e.core.Pool, threadID)
	if err != nil {
		return t, false, err
	}
	if err := e.prReady(ctx, t, req.Force); err != nil {
		return t, false, err
	}
	targets, err := e.prTargets(ctx, t, req)
	if errors.Is(err, ErrPRExists) {
		again, rerr := e.resubmit(ctx, t.ID, req.Force)
		if rerr != nil || !again {
			return t, false, errors.Join(err, rerr)
		}
		t, err = core.ThreadByID(ctx, e.core.Pool, t.ID)
		return t, true, err
	}
	if err != nil {
		return t, false, err
	}
	title, body, err := e.prText(ctx, t, req)
	if err != nil {
		return t, false, err
	}
	opened := 0
	for _, target := range targets {
		ok, err := e.openOne(ctx, t, target, req, title, body)
		if err != nil {
			return t, false, err
		}
		if ok {
			opened++
		}
	}
	if opened == 0 {
		return t, false, ErrNothingToPR
	}
	t, err = e.toReview(ctx, t)
	return t, false, err
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
	return true, e.recordPR(ctx, t, target.repo.Slug, pr)
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
	return e.prReadyIn(ctx, e.core.Pool, t, force)
}

func (e *Engine) prReadyIn(ctx context.Context, q core.DB, t core.Thread, force bool) error {
	active, err := core.MergeQueue(ctx, q, t.ID, true)
	if err != nil {
		return err
	}
	if len(active) > 0 {
		return ErrMergeInProgress
	}
	if force {
		return nil
	}
	plan, err := core.MainTasks(ctx, q, t.ID)
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

// recordPR stores one repository's pull request (a pull request is tracked on the thread, not
// published as a deliverable); the thread keeps the first
// pull request in pr_number/pr_url for older clients.
func (e *Engine) recordPR(ctx context.Context, t core.Thread, repo string, pr ghops.PR) error {
	return e.core.InTx(ctx, func(tx *core.Tx) error {
		if err := core.SetThreadRepoPR(ctx, tx, t.ID, repo, pr.Number, pr.HTMLURL); err != nil {
			return err
		}
		set := map[string]any{"pr_state": "open", "merge_error": nil}
		if cur, err := core.LockThread(ctx, tx, t.ID); err == nil && cur.PRNumber == nil {
			set["pr_number"], set["pr_url"] = pr.Number, pr.HTMLURL
		}
		return core.UpdateThread(ctx, tx, t.ID, set)
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

func audit(ctx context.Context, cs *core.Store, userID, action, target string) {
	_ = cs.Pool.Gorm(ctx).Table("at_audit_log").Create(map[string]any{"user_id": userID, "actor": "user:" + userID,
		"action": action, "target_type": "thread", "target_id": target}).Error
}

// Reject sends the reason to the lead and puts the thread back to running.
func (e *Engine) Reject(ctx context.Context, userID, threadID, reason string) (core.Thread, error) {
	t, err := core.ThreadOfUser(ctx, e.core.Pool, userID, threadID)
	if err != nil {
		return t, err
	}
	if t.Status != "review" {
		return t, ErrNotInReview
	}
	audit(ctx, e.core, userID, "thread_reject", t.ID)
	err = e.core.InTx(ctx, func(tx *core.Tx) error {
		if err := core.UpdateThread(ctx, tx, t.ID, map[string]any{"merge_error": nil, "done_pending_at": nil}); err != nil {
			return err
		}
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
	if tr.PROpenedAt != nil {
		since = *tr.PROpenedAt
	}
	v, failed, err := e.ciVerdict(ctx, t, repo, pr.Head.SHA, nil, since)
	if err == nil && v == CIPass && pr.Mergeable != nil && !*pr.Mergeable {
		return CIFail, []core.CheckView{{Name: "mergeable", Status: "completed", Conclusion: "conflict",
			Summary: "PR 与基准分支有冲突"}}, nil
	}
	return v, failed, err
}

// mergePRs merges every open pull request with the project's merge method, then finishes the
// thread. want is the pr_state the merge started from ("accepting", or "open" for the automatic
// merge under merge_policy auto): errStopped once the thread left it.
func (e *Engine) mergePRs(ctx context.Context, t core.Thread, want string) error {
	d, err := core.DeliveryOf(ctx, e.core.Pool, t.ProjectID)
	if err != nil {
		return err
	}
	prs, repos, err := e.openPRs(ctx, t)
	if err != nil {
		return err
	}
	for i, tr := range prs {
		if err := e.mergeOne(ctx, t, repos[i], tr, orDefault(d.MergeMethod, "squash"), want); err != nil {
			return err
		}
	}
	return e.Merged(ctx, t.ID)
}

func (e *Engine) mergeOne(ctx context.Context, t core.Thread, repo ghops.Repo, tr core.ThreadRepo, method, want string) error {
	pr, err := e.gh.GetPR(ctx, repo, *tr.PRNumber)
	if err != nil {
		return fmt.Errorf("%s#%d：%w", tr.Repo, *tr.PRNumber, err)
	}
	if !pr.Merged {
		if err := e.stillMerging(ctx, t.ID, want); err != nil {
			return err
		}
		if err := e.gh.MergePR(ctx, repo, *tr.PRNumber, method, pr.Head.SHA); err != nil && !e.mergedMeanwhile(ctx, repo, tr) {
			return fmt.Errorf("%s#%d：%w", tr.Repo, *tr.PRNumber, err)
		}
	}
	return e.core.InTx(ctx, func(tx *core.Tx) error {
		if err := core.SetThreadRepoPRState(ctx, tx, t.ID, tr.Repo, "merged"); err != nil {
			return err
		}
		return tx.EmitThread(ctx, t.ID) // repos[].pr_state changes before the whole thread is done
	})
}

// mergedMeanwhile reports a pull request someone else merged while the platform tried to
// (the merge loop on another replica, a person on GitHub): its merge failing is no failure.
func (e *Engine) mergedMeanwhile(ctx context.Context, repo ghops.Repo, tr core.ThreadRepo) bool {
	pr, err := e.gh.GetPR(ctx, repo, *tr.PRNumber)
	return err == nil && pr.Merged
}

// Merged records that the thread's pull requests are merged (pr_state merged once none is
// open) and ends the thread through endThread: not while a task is unfinished or the merge queue
// is busy (the merge loop's doneTick ends it once they clear, or the lead's finish does), and
// not in the middle of the lead's turn. Idempotent under the thread's row lock. A thread already
// done (merge_policy pr: it finished with its PRs open and a person merged them later) only gets
// pr_state merged.
func (e *Engine) Merged(ctx context.Context, threadID string) error {
	return e.core.InTx(ctx, func(tx *core.Tx) error {
		t, err := core.LockThread(ctx, tx, threadID)
		if err != nil || (t.Status == "done" && t.PRState == "merged") {
			return ignoreMissing(err)
		}
		if open, err := anyOpenPR(ctx, tx, t.ID); err != nil || open {
			return err
		}
		if err := core.UpdateThread(ctx, tx, t.ID, map[string]any{"pr_state": "merged", "merge_error": nil}); err != nil {
			return err
		}
		t.PRState, t.MergeError = "merged", nil
		if t.Status != "done" {
			if done, err := e.endThread(ctx, tx, t, endChecked, false); err != nil || done {
				return err
			}
		}
		return tx.EmitThread(ctx, t.ID)
	})
}

// policy is the thread's project merge_policy; "manual" when the project cannot be read, so
// nothing finishes or merges without the user.
func (e *Engine) policy(ctx context.Context, t core.Thread) string {
	p, err := e.orch.Project(ctx, t.ProjectID)
	if err != nil {
		return "manual"
	}
	return p.MergePolicy
}

// finishWithOpenPRs finishes a thread whose pull requests passed their checks under merge_policy pr: the
// PRs stay open for a person to merge (Merged records it later); nobody has to accept. The end
// goes through endThread: it waits for unfinished tasks, the merge queue and the lead's turn.
func (e *Engine) finishWithOpenPRs(ctx context.Context, threadID string) error {
	return e.core.InTx(ctx, func(tx *core.Tx) error {
		t, err := core.LockThread(ctx, tx, threadID)
		if err != nil || t.Status != "review" || t.PRState != "open" {
			return ignoreMissing(err)
		}
		_, err = e.endThread(ctx, tx, t, endChecked, true)
		return err
	})
}

// checksNoLongerPass drops a pending end that relied on passing checks (new commits made them
// run again): the end waits for the next pass.
func (e *Engine) checksNoLongerPass(ctx context.Context, t core.Thread) error {
	if t.DonePendingAt == nil {
		return nil
	}
	return e.core.InTx(ctx, func(tx *core.Tx) error {
		return core.UpdateThread(ctx, tx, t.ID, map[string]any{"done_pending_at": nil})
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

// prTick acts on PRs whose checks passed — merges accepted ones and those of auto projects,
// finishes the threads of pr projects (prStep) — and reports failures to the lead.
func (e *Engine) prTick(ctx context.Context) {
	var ids []string
	err := e.core.Pool.Gorm(ctx).Table("at_threads t").Joins("JOIN at_projects p ON p.id = t.project_id").
		Where(`t.status = 'review' AND t.pr_number IS NOT NULL
			AND (t.pr_state = 'accepting' OR (t.pr_state = 'open' AND p.merge_policy <> 'manual'))`).Pluck("t.id", &ids).Error
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
	if _, busy := e.accepting.Load(threadID); busy {
		return nil // Accept is merging it in the background
	}
	t, err := core.ThreadByID(ctx, e.core.Pool, threadID)
	if err != nil {
		return err
	}
	v, failed, err := e.prCheck(ctx, t)
	switch {
	case err != nil:
		return e.loopFailed(ctx, t, "读取 PR 状态失败", err)
	case v == CIPass && t.PRState == "open" && e.policy(ctx, t) == "pr":
		return e.finishWithOpenPRs(ctx, t.ID)
	case v == CIPass:
		if err := e.mergePRs(ctx, t, t.PRState); !errors.Is(err, errStopped) {
			return e.loopFailed(ctx, t, "合并失败", err)
		}
	case v == CIMissing:
		return e.ciMissing(ctx, t.ID)
	case v == CIFail:
		return e.prFailed(ctx, t, v, failed)
	case v == CIPending && t.PRState == "open":
		return e.checksNoLongerPass(ctx, t)
	}
	return nil
}

// loopFailed reports a merge-loop error the user has to act on (permission, conflict, not
// mergeable) on an accepted thread (merge_error, back to open); others (rate limits, GitHub 5xx,
// the network) are retried on the next tick. An open PR merged automatically (merge_policy auto)
// that fails is only logged and retried: there is no acceptance to report it on.
func (e *Engine) loopFailed(ctx context.Context, t core.Thread, label string, err error) error {
	if err == nil || !permanent(err) {
		return err
	}
	e.log.Warn("pr merge", "thread_id", t.ID, "err", err)
	return e.mergeFailed(ctx, t.ID, mergeErrorText(label, err))
}

// ciMissing keeps a thread whose pull requests never got the checks the project requires
// (on_missing = block) in review with merge_error saying so: the lead cannot fix a missing CI, so
// the user decides (it counts as waiting for the user); nothing goes to the lead.
func (e *Engine) ciMissing(ctx context.Context, threadID string) error {
	reason := checksText(CIMissing, nil, false)
	return e.core.InTx(ctx, func(tx *core.Tx) error {
		t, err := core.LockThread(ctx, tx, threadID)
		if err != nil || t.Status != "review" {
			return ignoreMissing(err)
		}
		if t.PRState == "open" && deref(t.MergeError) == reason && t.DonePendingAt == nil {
			return nil // already recorded: no event every tick
		}
		if err := core.UpdateThread(ctx, tx, t.ID, map[string]any{"pr_state": "open", "merge_error": reason, "done_pending_at": nil}); err != nil {
			return err
		}
		if err := core.SetThreadPRStates(ctx, tx, t.ID, "open"); err != nil {
			return err
		}
		return tx.EmitThread(ctx, t.ID)
	})
}

func (e *Engine) prFailed(ctx context.Context, t core.Thread, v Verdict, failed []core.CheckView) error {
	var b strings.Builder
	fmt.Fprintf(&b, "[Thread #%d · CI 失败] PR 的检查没有通过，请安排修复：\n", t.No)
	for _, c := range failed {
		fmt.Fprintf(&b, "- %s：%s %s\n", c.Name, orDefault(c.Conclusion, c.Status), c.URL)
	}
	return e.core.InTx(ctx, func(tx *core.Tx) error {
		if err := core.UpdateThread(ctx, tx, t.ID, map[string]any{"pr_state": "open", "merge_error": checksText(v, failed, true),
			"done_pending_at": nil}); err != nil {
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
		if state == "closed" {
			if err := core.UpdateThread(ctx, tx, tr.ThreadID, map[string]any{"pr_state": "closed"}); err != nil {
				return err
			}
		}
		return tx.EmitThread(ctx, tr.ThreadID)
	})
	if err != nil || state != "merged" {
		return err
	}
	return e.Merged(ctx, tr.ThreadID)
}
