package delivery

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/ghops"
)

// defaultAcceptTimeout bounds one background merge after the user accepted (checks + merges + finish).
const defaultAcceptTimeout = 3 * time.Minute

// finishTimeout bounds the write that records how a background merge ended.
const finishTimeout = 10 * time.Second

// Reasons written to merge_error by the background merge itself.
const (
	reasonTimeout  = "合并超时，请稍后重试"
	reasonInternal = "合并时出现内部错误，请重试"
)

// errStopped: the thread left the state a merge was started for (rejected, merged elsewhere).
var errStopped = errors.New("merge stopped: thread no longer accepting")

// Accept starts the merge of the thread's pull requests and returns at once, without calling
// GitHub. It returns the thread as loaded before the change (callers reload it for the view) and:
//   - core.ErrNotFound when the thread is not the user's;
//   - ErrNoPR when it has no open pull request;
//   - ErrNotInReview (409) when it is not in review or its PRs are merged / closed.
//
// Otherwise pr_state becomes "accepting" and merge_error is cleared (thread event). A thread already
// accepting is left as it is: repeated clicks start no second merge. Under merge_policy manual the
// merge loop merges once the checks pass; under pr and auto ("merge now, don't wait") the PRs are
// checked and merged now, in the background (mergeInBackground). When that cannot merge, pr_state goes back to "open" and
// merge_error says why; the user accepts again to retry.
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
	started, err := e.startAccepting(ctx, t.ID)
	if err != nil || !started {
		return t, err
	}
	audit(ctx, e.core, userID, "thread_accept", t.ID)
	if p.MergePolicy == "manual" {
		e.Kick(ctx)
	} else {
		e.mergeInBackground(ctx, t.ID)
	}
	return t, nil
}

// startAccepting moves a thread in review to pr_state "accepting" and clears merge_error; false
// when it already is accepting (a repeated click, or another replica got there first: the row
// lock makes this the cross-replica guard against a second merge).
func (e *Engine) startAccepting(ctx context.Context, threadID string) (bool, error) {
	started := false
	err := e.core.InTx(ctx, func(tx *core.Tx) error {
		t, err := core.LockThread(ctx, tx, threadID)
		switch {
		case err != nil:
			return err
		case t.Status != "review" || t.PRState == "merged" || t.PRState == "closed":
			return ErrNotInReview
		case t.PRState == "accepting":
			return nil
		}
		started = true
		if err := core.UpdateThread(ctx, tx, t.ID, map[string]any{"pr_state": "accepting", "merge_error": nil}); err != nil {
			return err
		}
		if err := core.SetThreadPRStates(ctx, tx, t.ID, "accepting"); err != nil {
			return err
		}
		return tx.EmitThread(ctx, t.ID)
	})
	return started, err
}

// mergeInBackground checks and merges an accepted thread's pull requests outside the request
// (merge_policy pr or auto), on a context of its own (AcceptTimeout).
//
// e.accepting only deduplicates within this process: the merge loop here skips the thread while
// the merge runs. Across replicas startAccepting's row lock lets one Accept start a merge, the
// loop's mergeOne re-checks pr_state under the lock before merging (stillMerging), a merge that
// lost the race is no failure (mergedMeanwhile), and Merged is idempotent (a done thread is left
// as it is, so SetStatus("done") runs once).
func (e *Engine) mergeInBackground(ctx context.Context, threadID string) {
	if _, busy := e.accepting.LoadOrStore(threadID, struct{}{}); busy {
		return
	}
	bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), e.AcceptTimeout)
	go func() {
		defer cancel()
		defer e.accepting.Delete(threadID)
		defer func() {
			if r := recover(); r != nil {
				e.log.Error("accept merge panic", "thread_id", threadID, "panic", r)
				if err := e.mergeFailed(bg, threadID, reasonInternal); err != nil {
					e.log.Warn("accept merge", "thread_id", threadID, "err", err)
				}
			}
		}()
		start := e.now()
		err := e.acceptNow(bg, threadID)
		if err != nil {
			e.log.Warn("accept merge", "thread_id", threadID, "took", e.now().Sub(start), "err", err)
			return
		}
		e.log.Info("accept merge", "thread_id", threadID, "took", e.now().Sub(start))
	}()
}

// acceptNow merges right away when every check passed; anything else is recorded on the thread.
//
// Error handling (merge_policy pr or auto): GitHub rate limits (429, a 403 saying "rate limit")
// leave the thread accepting and end the background merge; the merge loop picks accepting
// threads up on its next tick and retries (merging once the checks pass). Every other error — a
// 4xx, and also a GitHub 5xx or a network error — ends the attempt: pr_state back to "open"
// with merge_error, and the user accepts again to retry. (Under auto the loop also keeps
// merging an open PR of a thread in review once its checks pass.)
func (e *Engine) acceptNow(ctx context.Context, threadID string) error {
	t, err := core.ThreadByID(ctx, e.core.Pool, threadID)
	if err != nil {
		return ignoreMissing(err)
	}
	if t.PRState != "accepting" {
		return nil // rejected (or merged by a webhook) meanwhile
	}
	v, failed, err := e.prCheck(ctx, t)
	if err != nil {
		return e.acceptFailed(ctx, t.ID, "读取 PR 状态失败", err)
	}
	if v != CIPass {
		return e.mergeFailed(ctx, t.ID, checksText(v, failed, false))
	}
	if t, err = core.ThreadByID(ctx, e.core.Pool, threadID); err != nil || t.PRState != "accepting" {
		return ignoreMissing(err) // rejected while the checks were read
	}
	err = e.mergePRs(ctx, t, "accepting")
	if errors.Is(err, errStopped) {
		return nil
	}
	return e.acceptFailed(ctx, t.ID, "合并失败", err)
}

// acceptFailed records a background merge error, except rate limits (left to the merge loop).
func (e *Engine) acceptFailed(ctx context.Context, threadID, label string, err error) error {
	switch {
	case err == nil:
		return nil
	case rateLimited(err):
		return err
	case ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded):
		return errors.Join(err, e.mergeFailed(ctx, threadID, reasonTimeout))
	}
	return errors.Join(err, e.mergeFailed(ctx, threadID, mergeErrorText(label, err)))
}

// mergeFailed puts an accepted thread's pull requests back to open with the reason in
// merge_error, so the user sees why and can accept again. Only a thread still in review and
// accepting is touched: a reject (running, open) or a merge that finished meanwhile wins. It runs
// on a context of its own, so a merge that timed out can still be recorded.
func (e *Engine) mergeFailed(ctx context.Context, threadID, reason string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), finishTimeout)
	defer cancel()
	return e.core.InTx(ctx, func(tx *core.Tx) error {
		t, err := core.LockThread(ctx, tx, threadID)
		if err != nil || t.Status != "review" || t.PRState != "accepting" {
			return ignoreMissing(err)
		}
		if err := core.UpdateThread(ctx, tx, t.ID, map[string]any{"pr_state": "open", "merge_error": reason}); err != nil {
			return err
		}
		if err := core.SetThreadPRStates(ctx, tx, t.ID, "open"); err != nil {
			return err
		}
		return tx.EmitThread(ctx, t.ID)
	})
}

// stillMerging re-reads the thread under its row lock right before a pull request is merged: it
// must still be in review with the pr_state the merge started from, else errStopped (a reject in
// between stops the merge instead of racing it).
func (e *Engine) stillMerging(ctx context.Context, threadID, want string) error {
	return e.core.InTx(ctx, func(tx *core.Tx) error {
		t, err := core.LockThread(ctx, tx, threadID)
		if err != nil {
			return err
		}
		if t.Status != "review" || t.PRState != want {
			return errStopped
		}
		return nil
	})
}

// rateLimited: GitHub asks to slow down (429, or a 403 for the secondary rate limit). Retried.
func rateLimited(err error) bool {
	s := ghops.Status(err)
	return s == 429 || (s == 403 && strings.Contains(strings.ToLower(err.Error()), "rate limit"))
}

// permanent errors (GitHub 4xx: permission, conflict, not mergeable, not found) are reported to
// the user by the merge loop; others (rate limits, GitHub 5xx, the network, the database) are
// retried on its next tick.
func permanent(err error) bool {
	s := ghops.Status(err)
	return s >= 400 && s < 500 && !rateLimited(err)
}

// mergeErrorText explains a failed GitHub call in words the user can act on.
func mergeErrorText(label string, err error) string {
	var hint string
	switch s := ghops.Status(err); {
	case s == 403:
		hint = "没有权限（403）：平台使用的 GitHub 凭据没有这个仓库的合并权限，或被分支保护规则拦下"
	case s == 404:
		hint = "找不到 PR 或仓库（404）：PR 可能已被删除，或凭据看不到这个仓库"
	case s == 405:
		hint = "GitHub 拒绝合并（405）：PR 现在不可合并（有冲突、分支保护要求的检查或审查未满足，或不允许这种合并方式）"
	case s == 409:
		hint = "合并冲突（409）：PR 的分支在合并时变了，或与基准分支有冲突"
	case s == 422:
		hint = "GitHub 拒绝了合并请求（422）"
	case s >= 500:
		hint = fmt.Sprintf("GitHub 暂时不可用（%d），请稍后重试", s)
	case s == 0:
		hint = "连不上 GitHub，请稍后重试"
	}
	msg := clip(strings.TrimSpace(err.Error()), 300)
	if hint == "" {
		return label + "：" + msg
	}
	return label + "：" + hint + "。详情：" + msg
}

// checksText says why the checks keep the pull requests from merging. requireAcceptance picks
// the wording for checks still running: without it an open PR merges by itself once they pass.
func checksText(v Verdict, failed []core.CheckView, requireAcceptance bool) string {
	switch v {
	case CIPending:
		if requireAcceptance {
			return "PR 的检查还没有全部通过（仍在运行），等检查完成后再点验收"
		}
		return "PR 的检查还没通过，通过后会自动合并；也可以稍后再点验收"
	case CIMissing:
		return "PR 上没有出现项目要求的 CI 检查，无法合并"
	}
	parts := make([]string, 0, len(failed))
	for _, c := range failed {
		s := c.Name + "：" + orDefault(c.Conclusion, c.Status)
		if c.Summary != "" {
			s += "（" + c.Summary + "）"
		}
		parts = append(parts, s)
	}
	if len(parts) == 0 {
		return "PR 的检查没有通过，无法合并"
	}
	return "PR 的检查没有通过，无法合并：" + strings.Join(parts, "；")
}
