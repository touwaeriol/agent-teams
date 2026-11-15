// Package delivery is the path from a member's commits to the base branch
// (architecture.md §8): push jobs on the node, the per-thread merge queue on
// GitHub with trial branches, CI from the Checks API (polling; degrades to
// "off" when the App lacks checks:read), pull requests, acceptance and
// automatic merge, cleanup of worktrees and branches, deliverables and the
// narrowed GitHub credentials handed to nodes (cred.request).
package delivery

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	protocol "github.com/touwaeriol/agent-teams/protocol"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/fanout"
	"github.com/touwaeriol/agent-teams/server/internal/ghops"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw"
	"github.com/touwaeriol/agent-teams/server/internal/orchestrator"
	"github.com/touwaeriol/agent-teams/server/internal/scheduler"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// Errors for tools and REST.
var (
	ErrNoRepo          = ghops.ErrNoRepo
	ErrPRExists        = errors.New("这个讨论串已经开过 PR 了")
	ErrMergeInProgress = errors.New("合并队列里还有正在合并的条目，等它们处理完再开 PR")
	ErrPlanOpen        = errors.New("还有主任务没有完成")
	ErrNoPR            = errors.New("这个讨论串还没有 PR")
	ErrChecksPending   = errors.New("PR 的检查还没有全部通过")
	ErrTaskNotReady    = errors.New("任务还不能进合并队列（需要成员先 complete_task）")
	ErrBadURL          = errors.New("交付物链接必须是 http:// 或 https:// 开头的网址")
)

// mergeChannel relays merge queue hints between replicas.
const mergeChannel = "merge:all"

// Engine is the delivery engine.
type Engine struct {
	core  *core.Store
	orch  *orchestrator.Orchestrator
	sched *scheduler.Scheduler
	gh    *ghops.GitHub
	gw    *nodegw.Gateway
	hub   *fanout.Hub
	log   *slog.Logger
	now   func() time.Time

	// PollInterval is how often waiting CI is polled (§8.3: 15 s).
	PollInterval time.Duration
	// PushDone runs inside the transaction that recorded a push job's git.result.
	PushDone func(ctx context.Context, tx *core.Tx, meta map[string]any, r *protocol.GitResult) error
}

// New builds the engine.
func New(cs *core.Store, orch *orchestrator.Orchestrator, sched *scheduler.Scheduler, gh *ghops.GitHub, gw *nodegw.Gateway,
	hub *fanout.Hub, log *slog.Logger) *Engine {
	return &Engine{core: cs, orch: orch, sched: sched, gh: gh, gw: gw, hub: hub, log: log, now: time.Now,
		PollInterval: 15 * time.Second,
		PushDone:     func(context.Context, *core.Tx, map[string]any, *protocol.GitResult) error { return nil }}
}

// repoOf resolves a project's repository.
func (e *Engine) repoOf(ctx context.Context, projectID string) (store.Project, ghops.Repo, error) {
	p, err := e.orch.Project(ctx, projectID)
	if err != nil {
		return p, ghops.Repo{}, err
	}
	repo, err := e.gh.RepoOf(ctx, p)
	return p, repo, err
}

// Push queues git.push of a member's worktree to thread/<no>/<name> (§8.1); meta says what to do with the result.
func (e *Engine) Push(ctx context.Context, tx *core.Tx, emp core.Employee, t core.Thread, meta map[string]any) (string, error) {
	p, repo, err := e.repoOf(ctx, emp.ProjectID)
	if err != nil {
		return "", err
	}
	if emp.NodeID == nil {
		return "", orchestrator.ErrNoNode
	}
	job := core.NewID("g")
	paths := orchestrator.PathsOf(emp, &t)
	body := protocol.GitPush{JobID: protocol.JobID(job), ProjectID: p.ID, Repo: protocol.Repo(repo.Slug),
		Cwd: protocol.RelPath(paths.Cwd), Refspec: "HEAD:refs/heads/" + paths.Branch, Employee: emp.Name, ThreadNo: t.No}
	meta["purpose"] = "push"
	meta["employee_id"] = emp.ID
	_, err = e.gw.Enqueue(ctx, tx, *emp.NodeID, nodegw.Command{Type: protocol.TypeGitPush, Body: body, Meta: meta})
	return job, err
}

// JobResult records git.result and continues what the job was for.
func (e *Engine) JobResult(ctx context.Context, node store.Node, r *protocol.GitResult) error {
	return e.core.InTx(ctx, func(tx *core.Tx) error {
		typ, meta, err := jobOf(ctx, tx, node.ID, string(r.JobID))
		if err != nil {
			return ignoreMissing(err)
		}
		if err := nodegw.FinishCommand(ctx, tx, node.ID, typ+":"+string(r.JobID), r.Ok, r); err != nil {
			return err
		}
		return e.continueJob(ctx, tx, node, typ, meta, r)
	})
}

func jobOf(ctx context.Context, q core.DB, nodeID, jobID string) (string, map[string]any, error) {
	var typ string
	var meta map[string]any
	err := q.QueryRow(ctx, `SELECT type, meta FROM at_node_commands WHERE node_id = $1 AND type LIKE 'git.%'
		AND body->>'job_id' = $2`, nodeID, jobID).Scan(&typ, &meta)
	return typ, meta, core.MapErr(err)
}

func (e *Engine) continueJob(ctx context.Context, tx *core.Tx, node store.Node, typ string, meta map[string]any, r *protocol.GitResult) error {
	switch typ {
	case protocol.TypeGitPush:
		return e.PushDone(ctx, tx, meta, r)
	case protocol.TypeGitPrepare:
		return e.prepared(ctx, tx, meta, r)
	case protocol.TypeGitCleanup:
		return e.cleaned(ctx, tx, node, meta, r)
	}
	return nil
}

// prepared reports a failed worktree preparation in the employee's conversation.
func (e *Engine) prepared(ctx context.Context, tx *core.Tx, meta map[string]any, r *protocol.GitResult) error {
	if r.Ok {
		return nil
	}
	emp, err := core.EmployeeByID(ctx, tx, str(meta["employee_id"]))
	if err != nil {
		return ignoreMissing(err)
	}
	return e.orch.PostError(ctx, tx, emp, fmt.Sprintf("准备 %s 的工作目录失败（%s）：%s", emp.Name, code(r), tail(r)))
}

func code(r *protocol.GitResult) string {
	if r.ErrorCode == nil {
		return "error"
	}
	return string(*r.ErrorCode)
}

func tail(r *protocol.GitResult) string {
	switch {
	case r.Error != nil && *r.Error != "":
		return *r.Error
	case r.LogTail != nil:
		return clip(*r.LogTail, 500)
	}
	return ""
}

// PushError explains a failed push job to the member (used by toolsvc).
func PushError(r *protocol.GitResult) string {
	switch code(r) {
	case "workflow_permission":
		return "推送被拒绝：改动包含 .github/workflows，平台令牌没有 workflows 权限。请把 workflow 变更写进交付说明，由用户合并。"
	case "rejected_non_ff":
		return "推送被拒绝：远端分支有你本地没有的提交，请先 git pull --rebase 再完成。"
	case "refspec_not_allowed":
		return "推送被拒绝：只能推送到你自己的成员分支。"
	}
	return "推送失败（" + code(r) + "）：" + tail(r)
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func ignoreMissing(err error) error {
	if errors.Is(err, core.ErrNotFound) {
		return nil
	}
	return err
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// Publish registers a deliverable (publish_deliverable, REST).
func (e *Engine) Publish(ctx context.Context, tx *core.Tx, projectID string, threadID *string, kind, title, url, by string,
	detail map[string]any) (core.Deliverable, error) {
	if !WebURL(url) {
		return core.Deliverable{}, ErrBadURL
	}
	d, err := core.InsertDeliverable(ctx, tx, core.NewDeliverable{ProjectID: projectID, ThreadID: threadID,
		Kind: strings.TrimSpace(kind), Title: strings.TrimSpace(title), URL: strings.TrimSpace(url), Detail: detail, PublishedBy: by})
	if err != nil {
		return d, err
	}
	return d, tx.EmitDeliverable(ctx, d)
}

// WebURL reports whether a deliverable link is an absolute http(s) URL; anything else
// (javascript:, data:, relative paths) would run or navigate inside the web app when clicked.
func WebURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return false
	}
	return u.Scheme == "https" || u.Scheme == "http"
}

// Kick asks the merge loop to look at the queues now.
func (e *Engine) Kick(ctx context.Context) { e.hub.Publish(ctx, mergeChannel, "1") }
