// Package engine assembles the orchestration side of the control plane: node
// gateway, orchestrator, scheduler, tools, approvals, status, delivery and the
// event hub. It routes daemon messages to the engines (nodegw.Handler),
// implements the REST layer's lifecycle hooks (platform.Hooks) and runs the
// background loops.
package engine

import (
	"context"
	"log/slog"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/approvals"
	"github.com/touwaeriol/agent-teams/server/internal/auth"
	"github.com/touwaeriol/agent-teams/server/internal/core"
	"github.com/touwaeriol/agent-teams/server/internal/credvault"
	"github.com/touwaeriol/agent-teams/server/internal/delivery"
	"github.com/touwaeriol/agent-teams/server/internal/events"
	"github.com/touwaeriol/agent-teams/server/internal/fanout"
	"github.com/touwaeriol/agent-teams/server/internal/ghops"
	"github.com/touwaeriol/agent-teams/server/internal/github"
	"github.com/touwaeriol/agent-teams/server/internal/gitrelay"
	"github.com/touwaeriol/agent-teams/server/internal/kv"
	"github.com/touwaeriol/agent-teams/server/internal/nodegw"
	"github.com/touwaeriol/agent-teams/server/internal/office"
	"github.com/touwaeriol/agent-teams/server/internal/orchestrator"
	"github.com/touwaeriol/agent-teams/server/internal/scheduler"
	"github.com/touwaeriol/agent-teams/server/internal/sshgw"
	"github.com/touwaeriol/agent-teams/server/internal/status"
	"github.com/touwaeriol/agent-teams/server/internal/store"
	"github.com/touwaeriol/agent-teams/server/internal/timers"
	"github.com/touwaeriol/agent-teams/server/internal/toolsvc"
)

// Deps are the foundation objects the engines use.
type Deps struct {
	Store   *store.Store
	KV      kv.KV
	GitHub  *github.Client
	Tokens  *github.Tokens
	Box     *auth.SecretBox
	Office  *office.Registry
	Log     *slog.Logger
	Gateway nodegw.Config
	// Vault opens remote server credentials and the SSH gateway's keys (nil or not Ready: the SSH
	// gateway answers "unavailable").
	Vault *credvault.Vault
	// Relay serves standalone Git repositories (nil: projects cannot use them).
	Relay *gitrelay.Relay
	// SSH tunes the SSH gateway (zero values: the defaults of remote-servers.md §5.7).
	SSH sshgw.Config
}

// Engine holds every orchestration component.
type Engine struct {
	Store     *store.Store
	KV        kv.KV
	Core      *core.Store
	Fan       *fanout.Hub
	Events    *events.Hub
	Gateway   *nodegw.Gateway
	Status    *status.Engine
	Orch      *orchestrator.Orchestrator
	Sched     *scheduler.Scheduler
	Approvals *approvals.Engine
	Delivery  *delivery.Engine
	Tools     *toolsvc.Service
	Timers    *timers.Runner
	GH        *ghops.GitHub
	SSH       *sshgw.Gateway
	log       *slog.Logger
}

// New wires the engines together.
func New(d Deps) (*Engine, error) {
	fan := fanout.New(d.KV, d.Log)
	cs := core.New(d.Store.Pool, fan)
	gw, err := nodegw.New(d.Store, cs, fan, d.Log, d.Gateway)
	if err != nil {
		return nil, err
	}
	cs.Online = gw.Online
	gh := &ghops.GitHub{Client: d.GitHub, Tokens: d.Tokens, Box: d.Box, Store: d.Store, Relay: d.Relay}
	e := &Engine{Store: d.Store, KV: d.KV, Core: cs, Fan: fan, Events: events.New(cs, fan, d.Log), Gateway: gw, GH: gh, log: d.Log}
	vault := d.Vault
	if vault == nil {
		vault, _ = credvault.New()
	}
	e.SSH = sshgw.New(sshgw.Deps{Store: d.Store, Core: cs, Nodes: gw, Fan: fan, Vault: vault, Log: d.Log, Cfg: d.SSH})
	e.Status = status.New(cs, gw.Online, d.Log)
	e.Orch = orchestrator.New(cs, d.Store, gw, e.Status, gh, d.Office, d.Log)
	e.Sched = scheduler.New(cs, e.Orch, e.Status, fan, d.Log)
	e.Approvals = approvals.New(cs, e.Orch, gw, d.Log)
	e.Delivery = delivery.New(cs, e.Orch, e.Sched, gh, gw, fan, d.Log)
	if e.Tools, err = toolsvc.New(cs, d.Store, e.Orch, e.Sched, e.Delivery, e.Status, gw, d.Log); err != nil {
		return nil, err
	}
	if e.Timers, err = timers.New(cs, e.Orch, d.Log); err != nil {
		return nil, err
	}
	e.Tools.Schedules = e.Timers
	e.Orch.Kick = e.Sched.Kick
	e.Sched.PushWork = func(ctx context.Context, tx *core.Tx, emp core.Employee, t core.Thread) error {
		_, err := e.Delivery.Push(ctx, tx, emp, t, map[string]any{"auto": true})
		return err
	}
	e.Orch.ThreadEnded = e.Delivery.ThreadEnded
	e.Orch.Rules = e.Approvals.Rules
	e.Orch.ChatAnswers = e.Tools.NoteChatReply
	e.Tools.Reviews = e.Approvals
	gw.SetHandler(&router{e: e})
	return e, nil
}

// Start runs the background loops until ctx ends.
func (e *Engine) Start(ctx context.Context) {
	go e.Fan.Run(ctx)
	go e.Sched.Run(ctx)
	go e.Delivery.Run(ctx)
	go e.Approvals.RunSweeper(ctx, 15*time.Second)
	go e.Timers.Run(ctx)
	go e.SSH.Run(ctx)
	go func() {
		e.Orch.WarmAll(ctx)
		e.Orch.StartAllQueued(ctx)
	}()
}

// SSHHooks returns the platform.SSHHooks implementation: the remote server API's callbacks into
// the SSH gateway (cut revoked connections on every replica, push ssh.servers_update).
func (e *Engine) SSHHooks() *SSHHooks { return &SSHHooks{e: e} }

// SSHHooks implements platform.SSHHooks with the SSH gateway.
type SSHHooks struct{ e *Engine }

// CertsRevoked implements platform.SSHHooks.
func (h *SSHHooks) CertsRevoked(ctx context.Context, serials []int64) { h.e.SSH.Revoked(ctx, serials) }

// ServersChanged implements platform.SSHHooks.
func (h *SSHHooks) ServersChanged(ctx context.Context, _ string, projectIDs []string) {
	for _, id := range projectIDs {
		if err := h.e.SSH.ServersChanged(ctx, id); err != nil {
			h.e.log.Warn("ssh servers update", "project_id", id, "err", err)
		}
	}
}

// Hooks returns the platform.Hooks implementation.
func (e *Engine) Hooks() *Hooks { return &Hooks{e: e} }
