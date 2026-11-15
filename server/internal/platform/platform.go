// Package platform declares the extension points the orchestration side of the
// control plane plugs into: the node gateway's link registry, the event
// publisher behind SSE, and lifecycle hooks for projects, users, nodes and
// GitHub webhooks. httpapi depends only on these interfaces; the defaults
// below make the foundation runnable on its own (nodes always offline, events
// only logged).
package platform

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// ErrNodeOffline is returned for commands to a node without a live link.
var ErrNodeOffline = errors.New("节点不在线")

// NodeLive is the live part of NodeView, owned by the node gateway.
type NodeLive struct {
	Online         bool           `json:"online"`
	Since          *time.Time     `json:"since"`
	Version        string         `json:"version"`
	Protocol       int            `json:"protocol"`
	Isolation      string         `json:"isolation,omitempty"` // reported by hello; "" = use the stored value
	Runtimes       map[string]any `json:"runtimes"`
	Capacity       map[string]any `json:"capacity"`
	ActiveSessions int            `json:"active_sessions"`
	ParkedSessions int            `json:"parked_sessions"`
	Queued         int            `json:"queued"`
}

// NodeRelease is at_settings.node_release.
type NodeRelease struct {
	Version   string `json:"version"`
	SHA256    string `json:"sha256"`
	Signature string `json:"signature"`
}

// NodeRegistry is the node gateway (WS /nodes/connect) as the REST API sees it.
type NodeRegistry interface {
	// Status returns the live state of a node's link (zero value when offline).
	Status(nodeID string) NodeLive
	// Disconnect closes the node's link (token rotated, node or owner deleted).
	Disconnect(ctx context.Context, nodeID, reason string)
	// Configure pushes node.config after PATCH /api/nodes/{id}.
	Configure(ctx context.Context, node store.Node)
	// Upgrade sends node.upgrade; ErrNodeOffline when not connected.
	Upgrade(ctx context.Context, nodeID string, release NodeRelease) error
	// Drain sends node.drain; ErrNodeOffline when not connected.
	Drain(ctx context.Context, nodeID string, deadline time.Duration) error
}

// Events publishes realtime events (api.md §1). The orchestration side
// persists project events in at_events and fans them out over SSE.
type Events interface {
	// Project emits event typ (memory, layout, …) on a project stream.
	Project(ctx context.Context, projectID, typ string, payload any)
	// User emits event typ (project, node, notice) on a user stream.
	User(ctx context.Context, userID, typ string, payload any)
}

// Hooks are lifecycle callbacks from REST writes into the engines.
type Hooks interface {
	// ProjectCreated runs after POST /api/projects; firstMessage goes to the coordinator.
	ProjectCreated(ctx context.Context, p store.Project, firstMessage string) error
	// ProjectDeleting runs before the project's rows are deleted (close sessions, git.purge).
	ProjectDeleting(ctx context.Context, userID, projectID string) error
	// UserCutOff runs when a user is deactivated or deleted (close sessions, cancel waits).
	UserCutOff(ctx context.Context, userID string) error
	// NodeDeleting runs before a node row is deleted (reassign its employees).
	NodeDeleting(ctx context.Context, userID, nodeID string) error
	// GitHubEvent receives verified webhook deliveries (check_run, pull_request, …).
	GitHubEvent(ctx context.Context, event string, payload json.RawMessage) error
}

// Offline is the default NodeRegistry: every node is offline.
type Offline struct{}

// Status implements NodeRegistry.
func (Offline) Status(string) NodeLive { return NodeLive{} }

// Disconnect implements NodeRegistry.
func (Offline) Disconnect(context.Context, string, string) {}

// Configure implements NodeRegistry.
func (Offline) Configure(context.Context, store.Node) {}

// Upgrade implements NodeRegistry.
func (Offline) Upgrade(context.Context, string, NodeRelease) error { return ErrNodeOffline }

// Drain implements NodeRegistry.
func (Offline) Drain(context.Context, string, time.Duration) error { return ErrNodeOffline }

// LogEvents is the default Events: it only logs.
type LogEvents struct{ Log *slog.Logger }

// Project implements Events.
func (e LogEvents) Project(_ context.Context, projectID, typ string, _ any) {
	e.Log.Debug("project event", "project_id", projectID, "type", typ)
}

// User implements Events.
func (e LogEvents) User(_ context.Context, userID, typ string, _ any) {
	e.Log.Debug("user event", "user_id", userID, "type", typ)
}

// NoHooks is the default Hooks: nothing to do.
type NoHooks struct{}

// ProjectCreated implements Hooks.
func (NoHooks) ProjectCreated(context.Context, store.Project, string) error { return nil }

// ProjectDeleting implements Hooks.
func (NoHooks) ProjectDeleting(context.Context, string, string) error { return nil }

// UserCutOff implements Hooks.
func (NoHooks) UserCutOff(context.Context, string) error { return nil }

// NodeDeleting implements Hooks.
func (NoHooks) NodeDeleting(context.Context, string, string) error { return nil }

// GitHubEvent implements Hooks.
func (NoHooks) GitHubEvent(context.Context, string, json.RawMessage) error { return nil }
