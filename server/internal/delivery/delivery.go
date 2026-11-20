// Package delivery is the path from a member's commits to the base branch
// (architecture.md §8): push jobs on the node, the per-thread merge queue on
// GitHub with trial branches, CI from the Checks API (polling; degrades to
// "off" when the App lacks checks:read), pull requests, acceptance and
// automatic merge, cleanup of worktrees and branches, deliverables and the
// narrowed GitHub credentials handed to nodes (cred.request).
package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/url"
	"strings"
	"sync"
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
	ErrNothingToPR     = errors.New("讨论串还没有把改动合并进任何仓库的讨论串分支")
	ErrStandaloneNoPR  = errors.New("改动所在的是独立 Git 仓库，没有 PR：改动已经在讨论串分支上，直接调用 finish(summary) 结束讨论串")
	ErrBaseNeedsRepo   = errors.New("项目有多个仓库要开 PR，指定 base 时请同时写 repo")
	ErrMergeInProgress = errors.New("合并队列里还有正在合并的条目，等它们处理完再开 PR")
	ErrPlanOpen        = errors.New("还有主任务没有完成")
	ErrNoPR            = errors.New("这个讨论串还没有 PR")
	ErrNotInReview     = errors.New("这个讨论串现在不在等待验收（PR 可能已经合并或关闭，或者讨论串已经回到进行中），请刷新后再看")
	ErrTaskNotReady    = errors.New("任务还不能进合并队列（需要成员先 complete_task）")
	ErrNotPushed       = errors.New("这个仓库没有可合并的改动")
	ErrBadURL          = errors.New("交付物链接必须是 http:// 或 https:// 开头的网址")
	ErrPRKind          = errors.New("PR 由平台在讨论串上自动跟踪，不是交付物，不能用 kind=pr 发布")
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
	// AcceptTimeout bounds a background merge after accept (merge_policy pr or auto).
	AcceptTimeout time.Duration
	// DoneGrace bounds how long a thread whose end is due waits on its lead (DefaultDoneGrace).
	DoneGrace time.Duration
	// accepting holds the threads Accept is merging in the background (merge_policy pr or auto).
	accepting sync.Map
	// held is the unfinished tasks the lead was last told hold each thread's end (endHeld).
	held sync.Map

	// PushDone runs inside the transaction that recorded a push job's git.result.
	PushDone func(ctx context.Context, tx *core.Tx, meta map[string]any, r *protocol.GitResult) error
}

// New builds the engine.
func New(cs *core.Store, orch *orchestrator.Orchestrator, sched *scheduler.Scheduler, gh *ghops.GitHub, gw *nodegw.Gateway,
	hub *fanout.Hub, log *slog.Logger) *Engine {
	return &Engine{core: cs, orch: orch, sched: sched, gh: gh, gw: gw, hub: hub, log: log, now: time.Now,
		PollInterval: 15 * time.Second, AcceptTimeout: defaultAcceptTimeout, DoneGrace: DefaultDoneGrace,
		PushDone: func(context.Context, *core.Tx, map[string]any, *protocol.GitResult) error { return nil }}
}

// reposOf resolves a project's repositories.
func (e *Engine) reposOf(ctx context.Context, projectID string) (store.Project, []ghops.Repo, error) {
	p, err := e.orch.Project(ctx, projectID)
	if err != nil {
		return p, nil, err
	}
	repos, err := e.gh.ReposOf(ctx, p)
	return p, repos, err
}

// repoIn resolves one repository of a project by name.
func (e *Engine) repoIn(ctx context.Context, projectID, slug string) (ghops.Repo, error) {
	_, repos, err := e.reposOf(ctx, projectID)
	if err != nil {
		return ghops.Repo{}, err
	}
	if r, ok := ghops.Find(repos, slug); ok {
		return r, nil
	}
	return ghops.Repo{}, fmt.Errorf("%w：%s", orchestrator.ErrUnknownRepo, slug)
}

// Push queues one git.push per project repository: the member's checkout of that repository to
// thread/<no>/<name> (§8.1). The daemon skips a repository without commits beyond the thread
// branch and the base (unchanged). meta says what to do with the results; it returns the push group.
func (e *Engine) Push(ctx context.Context, tx *core.Tx, emp core.Employee, t core.Thread, meta map[string]any) (string, error) {
	p, repos, err := e.reposOf(ctx, emp.ProjectID)
	if err != nil {
		return "", err
	}
	if emp.NodeID == nil {
		return "", orchestrator.ErrNoNode
	}
	group := core.NewID("pg")
	for _, r := range repos {
		if err := e.pushRepo(ctx, tx, p, emp, t, r, len(repos), withGroup(meta, emp, group, r.Slug)); err != nil {
			return "", err
		}
	}
	return group, nil
}

func withGroup(meta map[string]any, emp core.Employee, group, repo string) map[string]any {
	m := maps.Clone(meta)
	m["purpose"], m["employee_id"], m["push_group"], m["repo"], m["node_id"] = "push", emp.ID, group, repo, *emp.NodeID
	return m
}

func (e *Engine) pushRepo(ctx context.Context, tx *core.Tx, p store.Project, emp core.Employee, t core.Thread, r ghops.Repo,
	nRepos int, meta map[string]any) error {
	paths := orchestrator.PathsOf(emp, &t)
	cwd := e.orch.CheckoutCwd(ctx, tx, emp, t, r.Slug, nRepos)
	body := protocol.GitPush{JobID: protocol.JobID(core.NewID("g")), ProjectID: p.ID, Repo: protocol.Repo(r.NodeRepo()), RemoteURL: e.gh.NodeRemote(r),
		Cwd: protocol.RelPath(cwd), Refspec: "HEAD:refs/heads/" + paths.Branch, Employee: emp.Name, ThreadNo: t.No,
		UnchangedAgainst: e.unchangedRefs(ctx, tx, t, r)}
	_, err := e.gw.Enqueue(ctx, tx, *emp.NodeID, nodegw.Command{Type: protocol.TypeGitPush, Body: body, Meta: meta})
	return err
}

// unchangedRefs are the branches whose commits do not count as the member's work in a repository.
func (e *Engine) unchangedRefs(ctx context.Context, q core.DB, t core.Thread, r ghops.Repo) []string {
	refs := []string{t.Branch}
	if base := e.orch.BaseFor(ctx, q, t, r); base != t.Branch {
		refs = append(refs, base)
	}
	return refs
}

// PushOutcome is one repository's result in a push group.
type PushOutcome struct {
	Repo      string
	Done      bool // the job reported back
	OK        bool
	Unchanged bool // no commits to deliver in this repository
	SHA       string
	Result    protocol.GitResult
}

// PushGroup reads the results of a push group so far (one per repository, in repository order).
func PushGroup(ctx context.Context, q core.DB, nodeID, group string) ([]PushOutcome, error) {
	rows, err := store.Query(ctx, q, `SELECT meta->>'repo', status, result FROM at_node_commands
		WHERE node_id = $1 AND type = $2 AND meta->>'push_group' = $3 ORDER BY seq`, nodeID, protocol.TypeGitPush, group)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PushOutcome
	for rows.Next() {
		var o PushOutcome
		var status string
		var raw []byte
		if err := rows.Scan(&o.Repo, &status, &raw); err != nil {
			return nil, err
		}
		out = append(out, outcomeOf(o, status, raw))
	}
	return out, rows.Err()
}

func outcomeOf(o PushOutcome, status string, raw []byte) PushOutcome {
	o.Done = status == "done" || status == "failed"
	if !o.Done || json.Unmarshal(raw, &o.Result) != nil {
		return o
	}
	o.OK = o.Result.Ok
	o.Unchanged = o.Result.Unchanged != nil && *o.Result.Unchanged
	if o.Result.HeadSHA != nil {
		o.SHA = *o.Result.HeadSHA
	}
	return o
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
	err := store.QueryRow(ctx, q, `SELECT type, meta FROM at_node_commands WHERE node_id = $1 AND type LIKE 'git.%'
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

// prepared hands a pre-task sync result to the scheduler (the brief waits for it) and reports a
// failed worktree preparation in the employee's conversation.
func (e *Engine) prepared(ctx context.Context, tx *core.Tx, meta map[string]any, r *protocol.GitResult) error {
	if str(meta["purpose"]) == "sync" {
		return e.sched.SyncDone(ctx, tx, meta, r)
	}
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
	case "not_a_repo":
		return "推送失败：工作目录里没有这个仓库的检出（" + tail(r) + "）"
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
	if IsPRKind(kind) {
		return core.Deliverable{}, ErrPRKind
	}
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

// IsPRKind reports whether a deliverable kind names a pull request: pull requests are tracked on
// the thread (at_thread_repos), never published as deliverables.
func IsPRKind(kind string) bool { return strings.EqualFold(strings.TrimSpace(kind), "pr") }

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
