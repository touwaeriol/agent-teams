package delivery

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/ghops"
)

// acceptTimeout bounds one background merge after the user accepted (checks + merges + finish).
const acceptTimeout = 3 * time.Minute

// Accept answers at once: once the thread is the user's and has an open pull request it moves to
// pr_state "accepting" (clearing the last merge_error) and returns. With require_acceptance the
// merge loop merges the PRs once their checks pass; without it they are checked and merged now,
// in the background. A failure puts pr_state back to "open" with merge_error saying why; a thread
// already accepting is returned as it is (no second merge).
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
	if p.RequireAcceptance {
		e.Kick(ctx)
	} else {
		e.mergeInBackground(ctx, t.ID)
	}
	return t, nil
}

// startAccepting moves the thread to pr_state "accepting" and clears merge_error; false when it
// already is accepting (a repeated click, or another replica got there first).
func (e *Engine) startAccepting(ctx context.Context, threadID string) (bool, error) {
	started := false
	err := e.core.InTx(ctx, func(tx *core.Tx) error {
		t, err := core.LockThread(ctx, tx, threadID)
		if err != nil || t.PRState == "accepting" {
			return err
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
// (require_acceptance off). The merge loop leaves a thread alone while this runs.
func (e *Engine) mergeInBackground(ctx context.Context, threadID string) {
	if _, busy := e.accepting.LoadOrStore(threadID, struct{}{}); busy {
		return
	}
	bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), acceptTimeout)
	go func() {
		defer cancel()
		defer e.accepting.Delete(threadID)
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
		return e.mergeFailed(ctx, t.ID, mergeErrorText("读取 PR 状态失败", err))
	}
	if v != CIPass {
		return e.mergeFailed(ctx, t.ID, checksText(v, failed))
	}
	if t, err = core.ThreadByID(ctx, e.core.Pool, threadID); err != nil || t.PRState != "accepting" {
		return ignoreMissing(err) // rejected while the checks were read
	}
	if err := e.mergePRs(ctx, t); err != nil {
		return e.mergeFailed(ctx, t.ID, mergeErrorText("合并失败", err))
	}
	return nil
}

// mergeFailed puts an accepted thread's pull requests back to open with the reason in
// merge_error (unless they merged meanwhile), so the user sees why and can accept again.
func (e *Engine) mergeFailed(ctx context.Context, threadID, reason string) error {
	return e.core.InTx(ctx, func(tx *core.Tx) error {
		t, err := core.LockThread(ctx, tx, threadID)
		if err != nil || t.Status == "done" || t.PRState == "merged" || t.PRState == "closed" {
			return ignoreMissing(err)
		}
		if t.PRState == "open" && t.MergeError != nil && *t.MergeError == reason {
			return nil // the merge loop retrying the same failure: nothing new to show
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

// permanent errors (GitHub 4xx: permission, conflict, not mergeable, not found) are reported to
// the user by the merge loop; others (GitHub 5xx, the network, the database) are retried.
func permanent(err error) bool {
	s := ghops.Status(err)
	return s >= 400 && s < 500
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
		hint = fmt.Sprintf("GitHub 暂时不可用（%d），稍后重试", s)
	}
	msg := clip(strings.TrimSpace(err.Error()), 300)
	if hint == "" {
		return label + "：" + msg
	}
	return label + "：" + hint + "。详情：" + msg
}

// checksText says why the checks keep the pull requests from merging.
func checksText(v Verdict, failed []core.CheckView) string {
	switch v {
	case CIPending:
		return "PR 的检查还没有全部通过（仍在运行），等检查完成后再点验收"
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
