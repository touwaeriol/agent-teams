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

// ErrUsageUnsupported: the node's daemon does not report usage (hello.features lacks "usage").
var ErrUsageUnsupported = errors.New("节点不支持查询余量")

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
	// Features is hello.features of the live link (nil while offline).
	Features []string `json:"features,omitempty"`
	// Usage is the latest node.usage report; kept after the link drops (nil = never reported).
	Usage *NodeUsage `json:"usage"`
}

// NodeUsage is the latest node.usage report of a node (NodeView.usage).
type NodeUsage struct {
	Claude *RuntimeUsage `json:"claude,omitempty"`
	Codex  *RuntimeUsage `json:"codex,omitempty"`
}

// RuntimeUsage is the subscription quota of one runtime as the daemon reported it.
type RuntimeUsage struct {
	Auth       string        `json:"auth" enum:"subscription,chatgpt,api_key,none" doc:"How the runtime is logged in"`
	Status     string        `json:"status" enum:"ok,error,unsupported"`
	Plan       string        `json:"plan,omitempty"`
	Windows    []UsageWindow `json:"windows"`
	Error      string        `json:"error,omitempty"`
	FetchedAt  *time.Time    `json:"fetched_at" doc:"When the daemon fetched the data"`
	ReceivedAt time.Time     `json:"received_at" doc:"When the console received the report"`
}

// UsageWindow is one rate-limit window (five_hour, weekly, primary, ...).
type UsageWindow struct {
	Key           string     `json:"key" doc:"five_hour, weekly, weekly_opus, weekly_sonnet, weekly_model (Claude); primary, secondary (Codex)"`
	Label         string     `json:"label,omitempty" maxLength:"40" doc:"Display name from the provider, e.g. the model of a weekly_model window"`
	UsedPercent   float64    `json:"used_percent" minimum:"0" maximum:"100"`
	WindowMinutes *int       `json:"window_minutes,omitempty"`
	ResetsAt      *time.Time `json:"resets_at"`
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
	// RefreshUsage returns the node's usage, asking the node (node.usage_refresh) unless the
	// cached report is fresh; stale = the node did not answer in time. ErrNodeOffline when not
	// connected, ErrUsageUnsupported when the daemon does not report usage.
	RefreshUsage(ctx context.Context, nodeID string) (usage *NodeUsage, stale bool, err error)
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
	// ProjectArchived runs after a project is archived (pause its threads, close its sessions).
	ProjectArchived(ctx context.Context, userID, projectID string) error
	// UserCutOff runs when a user is deactivated or deleted (close sessions, cancel waits).
	UserCutOff(ctx context.Context, userID string) error
	// NodeDeleting runs before a node row is deleted (reassign its employees).
	NodeDeleting(ctx context.Context, userID, nodeID string) error
	// RolesSaved runs after the user's role templates were saved (re-staff the projects).
	RolesSaved(ctx context.Context, userID string) error
	// GitHubEvent receives verified webhook deliveries (check_run, pull_request, …).
	GitHubEvent(ctx context.Context, event string, payload json.RawMessage) error
}

// Offline is the default NodeRegistry: every node is offline.
type Offline struct{}

// Status implements NodeRegistry.
func (Offline) Status(string) NodeLive { return NodeLive{} }

// RefreshUsage implements NodeRegistry.
func (Offline) RefreshUsage(context.Context, string) (*NodeUsage, bool, error) {
	return nil, false, ErrNodeOffline
}

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

// ProjectArchived implements Hooks.
func (NoHooks) ProjectArchived(context.Context, string, string) error { return nil }

// UserCutOff implements Hooks.
func (NoHooks) UserCutOff(context.Context, string) error { return nil }

// NodeDeleting implements Hooks.
func (NoHooks) NodeDeleting(context.Context, string, string) error { return nil }

// RolesSaved implements Hooks.
func (NoHooks) RolesSaved(context.Context, string) error { return nil }

// GitHubEvent implements Hooks.
func (NoHooks) GitHubEvent(context.Context, string, json.RawMessage) error { return nil }
