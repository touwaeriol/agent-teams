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
	"github.com/touwaeriol/agent-teams/server/internal/status"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// DefaultModel is the platform default (CLAUDE.md: claude-opus-5-5).
const DefaultModel = "claude-opus-5-5"

// Errors the REST layer maps to status codes.
var (
	ErrNoNode       = errors.New("没有可用的节点：请先在“节点”页添加并启动一个节点")
	ErrLegacy       = errors.New("旧对话只读")
	ErrNotActive    = errors.New("讨论串不在进行中")
	ErrNoEmployee   = errors.New("没有这个员工")
	ErrBadState     = errors.New("当前状态不允许这个操作")
	ErrNotFound     = core.ErrNotFound
	ErrRoleNotFound = errors.New("团队里没有这个岗位")
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

	// Kick asks the scheduler to look at a thread's task pool (set by the engine).
	Kick func(ctx context.Context, threadID string)
	// ThreadEnded runs (after commit) when a thread reaches done/failed/archived: cleanup.
	ThreadEnded func(ctx context.Context, threadID string)
	// Approvals is consulted for allowed_tools expansion of remembered rules.
	Rules func(ctx context.Context, q core.DB, projectID string, threadID *string, employee string) ([]string, error)
}

// New builds the orchestrator.
func New(cs *core.Store, st *store.Store, gw *nodegw.Gateway, se *status.Engine, gh *ghops.GitHub, reg *office.Registry,
	log *slog.Logger) *Orchestrator {
	return &Orchestrator{core: cs, store: st, gw: gw, status: se, gh: gh, office: reg, log: log,
		Kick:        func(context.Context, string) {},
		ThreadEnded: func(context.Context, string) {},
		Rules:       func(context.Context, core.DB, string, *string, string) ([]string, error) { return nil, nil }}
}

// Core exposes the data layer.
func (o *Orchestrator) Core() *core.Store { return o.core }

// Gateway exposes the node gateway.
func (o *Orchestrator) Gateway() *nodegw.Gateway { return o.gw }

// Status exposes the status engine.
func (o *Orchestrator) Status() *status.Engine { return o.status }

// project loads a project by id (no owner scope: engines act for the owner).
func (o *Orchestrator) project(ctx context.Context, q core.DB, id string) (store.Project, error) {
	var userID string
	if err := q.QueryRow(ctx, `SELECT user_id FROM at_projects WHERE id = $1`, id).Scan(&userID); err != nil {
		return store.Project{}, core.MapErr(err)
	}
	return o.store.ProjectOf(ctx, userID, id)
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
	if o.gh == nil {
		return false
	}
	p, err := o.project(ctx, o.core.Pool, projectID)
	if err != nil {
		return false
	}
	_, err = o.gh.RepoOf(ctx, p)
	return err == nil
}

// ConversationOf is the conversation an employee's messages and cards belong to.
func (o *Orchestrator) ConversationOf(ctx context.Context, tx *core.Tx, e core.Employee) (string, error) {
	return o.conversationOf(ctx, tx, e)
}
