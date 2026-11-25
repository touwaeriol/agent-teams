// Package orchestrator decides which employees have sessions and where
// (architecture.md §6, §7.1): the project coordinator (never touches code),
// thread leads and members instantiated from role templates, message routing
// user ↔ coordinator ↔ threads, session open/release/resume/handoff on the
// nodes, thread lifecycle with the running-thread limit, and status reported
// upward to the coordinator. Everything it decides is in PostgreSQL; node
// commands go through the gateway's outbox.
package orchestrator

import (
	"context"
	"errors"
	"log/slog"

	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/ghops"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw"
	"github.com/touwaeriol/agent-teams/server/internal/office"
	"github.com/touwaeriol/agent-teams/server/internal/runtimes"
	"github.com/touwaeriol/agent-teams/server/internal/status"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// DefaultModel is the last resort of a Claude session when neither the employee, the project,
// nor the role names a model (CLAUDE.md: claude-opus-5-5).
const DefaultModel = runtimes.ClaudeDefaultModel

// DefaultCodexModel is the last resort of a Codex session: empty, i.e. the Codex CLI picks its
// own default (the node leaves an empty model out of thread/start; app-server rejects model "",
// so this needs a node that does). Never a Claude id: Codex cannot run it.
const DefaultCodexModel = ""

// builtinModel is the built-in last resort of a runtime (DefaultModel for Claude,
// DefaultCodexModel for Codex: the registry holds both).
func builtinModel(runtime string) string {
	return runtimes.Of(runtime).DefaultModel
}

// Errors the REST layer maps to status codes.
var (
	ErrNoNode        = errors.New("没有可用的节点：请先在“节点”页添加并启动一个节点")
	ErrNotActive     = errors.New("讨论串不在进行中")
	ErrNotArchived   = errors.New("讨论串没有归档")
	ErrArchived      = errors.New("讨论串已归档，取消归档后再继续")
	ErrTooManyImages = errors.New("一条消息最多 20 个附件")
	ErrNoEmployee    = errors.New("没有这个员工")
	ErrBadState      = errors.New("当前状态不允许这个操作")
	ErrNotFound      = core.ErrNotFound
	ErrRoleNotFound  = errors.New("团队里没有这个岗位")
	ErrPRAwaiting    = errors.New("讨论串还有等待合并的 PR：请先接受（合并）或拒绝，不能直接标记为完成")
	ErrMerging       = errors.New("合并队列里还有正在合并的条目，等它们处理完再标记为完成")
)

// Orchestrator is the session and thread engine.
type Orchestrator struct {
	core   *core.Store
	store  *store.Store
	gw     *nodegw.Gateway
	status *status.Engine
	gh     *ghops.GitHub
	office *office.Registry
	log    *slog.Logger

	// Kick asks the scheduler to look at a project's task queue (set by the engine).
	Kick func(ctx context.Context, projectID string)
	// ThreadEnded runs (after commit) when a thread reaches done/failed/archived: cleanup.
	ThreadEnded func(ctx context.Context, threadID string)
	// Approvals is consulted for allowed_tools expansion of remembered rules.
	Rules func(ctx context.Context, q core.DB, projectID string, threadID *string, employee string) ([]string, error)
	// RefContext resolves @ reference chips (meta.refs) again into the context block a
	// forwarded message carries to the lead (set by the REST layer, which owns the lookups).
	RefContext func(ctx context.Context, userID, projectID string, chips []map[string]any) string
	// ChatAnswers runs when the user types a message while the employee still has question cards
	// open for the user in this conversation (set by the engine: toolsvc owns questions): waiting
	// calls are released and the returned note ("" = none) tells the employee to close the cards
	// the message answers (close_question).
	ChatAnswers func(ctx context.Context, tx *core.Tx, c core.Conversation, emp core.Employee, userID, content string) (string, error)
}

// New builds the orchestrator.
func New(cs *core.Store, st *store.Store, gw *nodegw.Gateway, se *status.Engine, gh *ghops.GitHub, reg *office.Registry,
	log *slog.Logger) *Orchestrator {
	return &Orchestrator{core: cs, store: st, gw: gw, status: se, gh: gh, office: reg, log: log,
		Kick:        func(context.Context, string) {},
		ThreadEnded: func(context.Context, string) {},
		Rules:       func(context.Context, core.DB, string, *string, string) ([]string, error) { return nil, nil },
		ChatAnswers: func(context.Context, *core.Tx, core.Conversation, core.Employee, string, string) (string, error) {
			return "", nil
		}}
}

// Core exposes the data layer.
func (o *Orchestrator) Core() *core.Store { return o.core }

// Gateway exposes the node gateway.
func (o *Orchestrator) Gateway() *nodegw.Gateway { return o.gw }

// Status exposes the status engine.
func (o *Orchestrator) Status() *status.Engine { return o.status }

// project loads a project by id (no owner scope: engines act for the owner).
func (o *Orchestrator) project(ctx context.Context, q core.DB, id string) (store.Project, error) {
	userID, err := core.ProjectOwner(ctx, q, id)
	if err != nil {
		return store.Project{}, err
	}
	return o.store.On(q).ProjectOf(ctx, userID, id)
}

// Project exposes project loading to the other engines.
func (o *Orchestrator) Project(ctx context.Context, id string) (store.Project, error) {
	return o.project(ctx, o.core.Pool, id)
}

// touch recomputes an employee's status after the transaction commits.
func (o *Orchestrator) touch(tx *core.Tx, employeeID, cause string) {
	tx.AfterCommit(func(ctx context.Context) {
		o.status.Observe(ctx, employeeID, status.Observation{Kind: status.Touch, Cause: cause})
	})
}

// Touch exposes touch to the other engines.
func (o *Orchestrator) Touch(tx *core.Tx, employeeID, cause string) { o.touch(tx, employeeID, cause) }

// HasRepo reports whether a project works in a GitHub repository.
func (o *Orchestrator) HasRepo(ctx context.Context, projectID string) bool {
	p, err := o.project(ctx, o.core.Pool, projectID)
	if err != nil {
		return false
	}
	return len(o.Repos(ctx, p)) > 0
}

// ConversationOf is the conversation an employee's messages and cards belong to.
func (o *Orchestrator) ConversationOf(ctx context.Context, tx *core.Tx, e core.Employee) (string, error) {
	return o.conversationOf(ctx, tx, e)
}
