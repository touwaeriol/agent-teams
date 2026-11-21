package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/touwaeriol/agent-teams/server/internal/auth"
	"github.com/touwaeriol/agent-teams/server/internal/platform"
	"github.com/touwaeriol/agent-teams/server/internal/store"
)

var nodeNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,39}$`)

// NodeView is api.md §2 NodeView (a superset of v1's).
type NodeView struct {
	ID             string         `json:"id"`
	Name           string         `json:"name"`
	Online         bool           `json:"online"`
	Since          *time.Time     `json:"since"`
	LastSeenAt     *time.Time     `json:"last_seen_at"`
	Version        string         `json:"version"`
	Protocol       int            `json:"protocol"`
	Isolation      string         `json:"isolation" enum:"agent_user,sandbox,none"`
	Runtimes       map[string]any `json:"runtimes"`
	Capacity       map[string]any `json:"capacity"`
	ActiveSessions int            `json:"active_sessions"`
	ParkedSessions int            `json:"parked_sessions"`
	Queued         int            `json:"queued"`
	// Usage is the latest node.usage report (nil = never reported since the console started).
	Usage             *platform.NodeUsage `json:"usage" doc:"Latest Claude Code / Codex subscription quota reported by the node; null when none"`
	UsageSupported    bool                `json:"usage_supported" doc:"The daemon reports usage (hello.features has \"usage\"; the last hello while offline)"`
	Isolated          bool                `json:"isolated"`
	Labels            []string            `json:"labels"`
	MaxActiveSessions *int                `json:"max_active_sessions"`
	ParkAfterS        *int                `json:"park_after_s"`
	CreatedAt         time.Time           `json:"created_at"`
	// LatestVersion is the daemon release the console serves; "" when it has none.
	LatestVersion string `json:"latest_version" doc:"Release id the console serves (<version>+<build>); empty when none"`
	// UpdateAvailable: online, on another release than LatestVersion.
	UpdateAvailable bool `json:"update_available"`
	// SelfUpdate: the daemon runs under the update launcher, so POST .../upgrade works.
	SelfUpdate bool `json:"self_update" doc:"hello.features has self_update (the last hello while offline)"`
	// RuntimeStatus: the daemon reports its runtime CLIs (node.runtimes), so POST .../recheck works
	// and only runtimes that are ready there are scheduled on it.
	RuntimeStatus bool `json:"runtime_status" doc:"hello.features has runtime_status (the last hello while offline): the daemon reports its CLIs and POST /api/nodes/{id}/recheck works"`
	// CLIs are the runtime CLIs as the daemon last reported them (empty for an older daemon).
	CLIs    []store.NodeRuntime        `json:"clis" doc:"Install and login state of each runtime CLI as last reported (node.runtimes), ordered by runtime; empty when the daemon never reported"`
	Upgrade *platform.NodeUpgradeState `json:"upgrade,omitempty" doc:"Last update asked of the node since the console started"`
}

// lastHello is the part of the stored hello used while a node is offline.
type lastHello struct {
	Version  string         `json:"version"`
	Protocol int            `json:"protocol"`
	Runtimes map[string]any `json:"runtimes"`
	Capacity map[string]any `json:"capacity"`
	Features []string       `json:"features"`
}

func (s *Server) nodeView(n store.Node, latest string, clis []store.NodeRuntime) NodeView {
	live := s.nodes.Status(n.ID)
	var hello lastHello
	_ = json.Unmarshal(n.LastHello, &hello)
	v := NodeView{ID: n.ID, Name: n.Name, Online: live.Online, Since: live.Since, LastSeenAt: n.LastSeenAt,
		Version: first(live.Version, hello.Version), Protocol: firstInt(live.Protocol, hello.Protocol),
		Isolation: first(live.Isolation, n.Isolation, "none"), Runtimes: firstMap(live.Runtimes, hello.Runtimes),
		Capacity: firstMap(live.Capacity, hello.Capacity), ActiveSessions: live.ActiveSessions,
		ParkedSessions: live.ParkedSessions, Queued: live.Queued, Usage: live.Usage, Isolated: n.Isolated, Labels: n.Labels,
		MaxActiveSessions: n.MaxActiveSessions, ParkAfterS: n.ParkAfterS, CreatedAt: n.CreatedAt}
	features := hello.Features
	if live.Online {
		features = live.Features
	}
	v.UsageSupported = slices.Contains(features, "usage")
	v.SelfUpdate = slices.Contains(features, "self_update")
	v.RuntimeStatus = slices.Contains(features, "runtime_status")
	v.CLIs = withReady(clis, time.Now())
	v.LatestVersion, v.Upgrade = latest, live.Upgrade
	v.UpdateAvailable = live.Online && latest != "" && v.Version != latest
	if v.Labels == nil {
		v.Labels = []string{}
	}
	return v
}

func first(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func firstInt(a, b int) int {
	if a != 0 {
		return a
	}
	return b
}

func firstMap(a, b map[string]any) map[string]any {
	if len(a) > 0 {
		return a
	}
	if b == nil {
		return map[string]any{}
	}
	return b
}

// Enrollment is the one-time token and the command that installs the daemon with it.
type Enrollment struct {
	Token   string `json:"token" doc:"Shown once"`
	Install string `json:"install" doc:"Shell command for the node"`
}

type nodesOutput struct{ Body []NodeView }

type nodeOutput struct{ Body NodeView }

type newNodeInput struct {
	Body struct {
		Name string `json:"name" maxLength:"40"`
	}
}

type newNodeOutput struct {
	Body struct {
		Node NodeView `json:"node"`
		Enrollment
	}
}

type enrollmentOutput struct{ Body Enrollment }

type nodeIDInput struct {
	ID string `path:"id" maxLength:"64"`
}

type nodePatchInput struct {
	ID   string `path:"id" maxLength:"64"`
	Body struct {
		MaxActiveSessions      *int     `json:"max_active_sessions,omitempty" minimum:"1" maximum:"64"`
		ClearMaxActiveSessions bool     `json:"clear_max_active_sessions,omitempty"`
		ParkAfterS             *int     `json:"park_after_s,omitempty" minimum:"0"`
		ClearParkAfterS        bool     `json:"clear_park_after_s,omitempty"`
		Isolated               *bool    `json:"isolated,omitempty"`
		Labels                 []string `json:"labels,omitempty" maxItems:"20"`
	}
}

type upgradeInput struct {
	ID   string `path:"id" maxLength:"64"`
	Body *struct {
		Mode      string `json:"mode,omitempty" enum:"wait_idle,now,cancel" doc:"wait_idle (default): switch once no turn runs; now: interrupt running turns; cancel: stop an update that has not switched"`
		DeadlineS int    `json:"deadline_s,omitempty" minimum:"0" maximum:"86400" doc:"wait_idle: report drain_timeout after this many seconds (default 1800)"`
	}
}

type drainInput struct {
	ID   string `path:"id" maxLength:"64"`
	Body struct {
		DeadlineS int `json:"deadline_s" minimum:"0" maximum:"86400"`
	}
}

type nodeSessionsOutput struct{ Body []store.NodeSession }

type nodeLogsInput struct {
	ID     string `path:"id" maxLength:"64"`
	Level  string `query:"level" enum:"warn,error,info,"`
	Before string `query:"before"`
	Limit  int    `query:"limit" minimum:"0" maximum:"200"`
}

type nodeLogsOutput struct {
	Body struct {
		Items      []store.NodeLog `json:"items"`
		NextCursor *string         `json:"next_cursor"`
	}
}

func (s *Server) registerNodes(api huma.API) {
	tags := []string{"nodes"}
	huma.Register(api, huma.Operation{OperationID: "list-nodes", Method: "GET", Path: "/api/nodes", Tags: tags}, s.listNodes)
	huma.Register(api, huma.Operation{OperationID: "add-node", Method: "POST", Path: "/api/nodes", Tags: tags, DefaultStatus: 201,
		Summary: "Register a node; returns its one-time token and install command"}, s.addNode)
	huma.Register(api, huma.Operation{OperationID: "update-node", Method: "PATCH", Path: "/api/nodes/{id}", Tags: tags}, s.updateNode)
	huma.Register(api, huma.Operation{OperationID: "rotate-node-token", Method: "POST", Path: "/api/nodes/{id}/token", Tags: tags,
		Summary: "Rotate the token; the old one stops working at once"}, s.rotateNodeToken)
	huma.Register(api, huma.Operation{OperationID: "upgrade-node", Method: "POST", Path: "/api/nodes/{id}/upgrade", Tags: tags, DefaultStatus: 202}, s.upgradeNode)
	huma.Register(api, huma.Operation{OperationID: "drain-node", Method: "POST", Path: "/api/nodes/{id}/drain", Tags: tags, DefaultStatus: 202}, s.drainNode)
	huma.Register(api, huma.Operation{OperationID: "delete-node", Method: "DELETE", Path: "/api/nodes/{id}", Tags: tags, DefaultStatus: 204}, s.deleteNode)
	huma.Register(api, huma.Operation{OperationID: "node-sessions", Method: "GET", Path: "/api/nodes/{id}/sessions", Tags: tags}, s.nodeSessions)
	huma.Register(api, huma.Operation{OperationID: "refresh-node-usage", Method: "POST", Path: "/api/nodes/{id}/usage/refresh", Tags: tags,
		Summary: "Fetch the node's subscription quota now (cached for 60 s; waits up to 15 s for the node)"}, s.refreshNodeUsage)
	huma.Register(api, huma.Operation{OperationID: "recheck-node", Method: "POST", Path: "/api/nodes/{id}/recheck", Tags: tags,
		Summary: "Check the node's runtime CLIs (install, login) and quota now; waits up to 15 s and returns the node"}, s.recheckNode)
	huma.Register(api, huma.Operation{OperationID: "node-logs", Method: "GET", Path: "/api/nodes/{id}/logs", Tags: tags}, s.nodeLogs)
}

func (s *Server) listNodes(ctx context.Context, _ *struct{}) (*nodesOutput, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.store.ListNodes(ctx, p.User.ID)
	if err != nil {
		return nil, err
	}
	out := make([]NodeView, len(rows))
	latest := s.currentRelease(ctx).Version
	ids := make([]string, len(rows))
	for i, n := range rows {
		ids[i] = n.ID
	}
	clis, err := s.store.NodeRuntimes(ctx, ids)
	if err != nil {
		return nil, err
	}
	for i, n := range rows {
		out[i] = s.nodeView(n, latest, clis[n.ID])
	}
	return &nodesOutput{Body: out}, nil
}

func (s *Server) addNode(ctx context.Context, in *newNodeInput) (*newNodeOutput, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(in.Body.Name)
	if !nodeNamePattern.MatchString(name) {
		return nil, fail(http.StatusUnprocessableEntity, "validation", "节点名只能用字母、数字、- 和 _")
	}
	if err := s.rateLimit(ctx, "node-create:"+p.User.ID, 10, time.Hour); err != nil {
		return nil, err
	}
	if err := s.checkNodeQuota(ctx, p.User); err != nil {
		return nil, err
	}
	token, hash := auth.NewToken()
	node, err := s.store.CreateNode(ctx, p.User.ID, name, hash)
	if errors.Is(err, store.ErrConflict) {
		return nil, fail(http.StatusConflict, "node_exists", fmt.Sprintf("已经有叫 %s 的节点了", name))
	}
	if err != nil {
		return nil, err
	}
	s.audit(ctx, p.User.ID, "node_create", "node", node.ID, map[string]string{"name": name})
	out := &newNodeOutput{}
	out.Body.Node = s.nodeView(node, s.currentRelease(ctx).Version, nil)
	out.Body.Enrollment = s.enrollment(name, token)
	return out, nil
}

func (s *Server) checkNodeQuota(ctx context.Context, u store.User) error {
	settings, err := s.platformSettings(ctx)
	if err != nil {
		return err
	}
	limit := settings.DefaultMaxNodes
	if u.MaxNodes != nil {
		limit = *u.MaxNodes
	}
	count, err := s.store.CountNodes(ctx, u.ID)
	if err != nil {
		return err
	}
	if count >= limit {
		return fail(http.StatusForbidden, "node_quota", fmt.Sprintf("最多可以添加 %d 个节点", limit))
	}
	return nil
}

func (s *Server) enrollment(name, token string) Enrollment {
	console := s.cfg.PublicURL
	cmd := fmt.Sprintf("curl -fsSL %s/install-node.sh | AGENT_TEAMS_NODE_TOKEN=%s bash -s -- --console %s --name %s",
		console, token, shellQuote(console), shellQuote(name))
	return Enrollment{Token: token, Install: cmd}
}

// shellQuote is Python's shlex.quote.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	safe := regexp.MustCompile(`^[A-Za-z0-9@%+=:,./_-]+$`)
	if safe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

func (s *Server) updateNode(ctx context.Context, in *nodePatchInput) (*nodeOutput, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	node, err := s.store.UpdateNode(ctx, p.User.ID, in.ID, nodeChanges(in))
	if err != nil {
		return nil, storeErr(err, "没有这个节点")
	}
	if in.Body.Isolated != nil {
		s.audit(ctx, p.User.ID, "node_isolated", "node", node.ID, map[string]bool{"isolated": *in.Body.Isolated})
	}
	s.nodes.Configure(ctx, node)
	return s.nodeOutput(ctx, node)
}

func nodeChanges(in *nodePatchInput) []store.Change {
	b := in.Body
	var ch []store.Change
	add := func(cond bool, col string, val any) {
		if cond {
			ch = append(ch, store.Change{Col: col, Val: val})
		}
	}
	add(b.MaxActiveSessions != nil && !b.ClearMaxActiveSessions, "max_active_sessions", b.MaxActiveSessions)
	add(b.ClearMaxActiveSessions, "max_active_sessions", nil)
	add(b.ParkAfterS != nil && !b.ClearParkAfterS, "park_after_s", b.ParkAfterS)
	add(b.ClearParkAfterS, "park_after_s", nil)
	add(b.Isolated != nil, "isolated", deref(b.Isolated))
	add(b.Labels != nil, "labels", cleanLabels(b.Labels))
	return ch
}

func cleanLabels(labels []string) []string {
	out := []string{}
	for _, l := range labels {
		if l = strings.TrimSpace(l); l != "" && len(l) <= 40 {
			out = append(out, l)
		}
	}
	return out
}

func (s *Server) rotateNodeToken(ctx context.Context, in *nodeIDInput) (*enrollmentOutput, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	node, err := s.store.NodeOf(ctx, p.User.ID, in.ID)
	if err != nil {
		return nil, storeErr(err, "没有这个节点")
	}
	token, hash := auth.NewToken()
	if err := s.store.SetNodeToken(ctx, p.User.ID, in.ID, hash); err != nil {
		return nil, storeErr(err, "没有这个节点")
	}
	s.nodes.Disconnect(ctx, in.ID, "token_rotated")
	s.audit(ctx, p.User.ID, "node_token_rotate", "node", in.ID, nil)
	return &enrollmentOutput{Body: s.enrollment(node.Name, token)}, nil
}

// upgradeNode sends node.upgrade with the release the console serves (architecture.md §14.3).
func (s *Server) upgradeNode(ctx context.Context, in *upgradeInput) (*struct{}, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	node, err := s.store.NodeOf(ctx, p.User.ID, in.ID)
	if err != nil {
		return nil, storeErr(err, "没有这个节点")
	}
	release := s.currentRelease(ctx)
	if release.Version == "" {
		return nil, fail(http.StatusConflict, "no_release", "控制面板还没有守护进程发布包")
	}
	var opts platform.UpgradeOptions
	if in.Body != nil {
		opts = platform.UpgradeOptions{Mode: in.Body.Mode, DeadlineS: in.Body.DeadlineS}
	}
	if err := s.nodes.Upgrade(ctx, in.ID, release, opts); err != nil {
		return nil, nodeCommandErr(err)
	}
	s.audit(ctx, p.User.ID, "node_upgrade", "node", node.ID, map[string]any{"version": release.Version, "mode": opts.Mode})
	return nil, nil
}

func (s *Server) drainNode(ctx context.Context, in *drainInput) (*struct{}, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.store.NodeOf(ctx, p.User.ID, in.ID); err != nil {
		return nil, storeErr(err, "没有这个节点")
	}
	return nil, nodeCommandErr(s.nodes.Drain(ctx, in.ID, time.Duration(in.Body.DeadlineS)*time.Second))
}

type usageRefreshOutput struct {
	Body struct {
		Usage *platform.NodeUsage `json:"usage" doc:"Latest report (same as NodeView.usage); null when the node never sent one"`
		Stale bool                `json:"stale,omitempty" doc:"The node did not answer in time; usage is the previous report"`
	}
}

// refreshNodeUsage is POST /api/nodes/{id}/usage/refresh. It waits for the node without
// holding a transaction (the ownership check is a plain read).
func (s *Server) refreshNodeUsage(ctx context.Context, in *nodeIDInput) (*usageRefreshOutput, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.store.NodeOf(ctx, p.User.ID, in.ID); err != nil {
		return nil, storeErr(err, "没有这个节点")
	}
	usage, stale, err := s.nodes.RefreshUsage(ctx, in.ID)
	if errors.Is(err, platform.ErrUsageUnsupported) {
		return nil, fail(http.StatusConflict, "usage_unsupported", "节点的守护进程版本不支持查询余量")
	}
	if err != nil {
		return nil, nodeCommandErr(err)
	}
	out := &usageRefreshOutput{}
	out.Body.Usage, out.Body.Stale = usage, stale
	return out, nil
}

// nodeOutput is the node as GET /api/nodes lists it, with its stored CLI states.
func (s *Server) nodeOutput(ctx context.Context, node store.Node) (*nodeOutput, error) {
	clis, err := s.store.NodeRuntimes(ctx, []string{node.ID})
	if err != nil {
		return nil, err
	}
	return &nodeOutput{Body: s.nodeView(node, s.currentRelease(ctx).Version, clis[node.ID])}, nil
}

// withReady fills NodeRuntime.Ready (never nil, for the JSON array).
func withReady(rows []store.NodeRuntime, now time.Time) []store.NodeRuntime {
	out := make([]store.NodeRuntime, len(rows))
	for i, r := range rows {
		r.ReadyNow = r.Ready(now)
		out[i] = r
	}
	return out
}

type recheckOutput struct {
	Body struct {
		Node  NodeView `json:"node" doc:"The node with the CLI states the answer stored (same as GET /api/nodes)"`
		Stale bool     `json:"stale,omitempty" doc:"The node did not answer in time; clis are the previous report"`
	}
}

// recheckNode is POST /api/nodes/{id}/recheck: node.runtimes_refresh (and node.usage_refresh) to a
// daemon with runtime_status, waiting for its answer without holding a transaction. A runtime that
// became ready releases the sessions held for it (engine router RuntimesChanged).
func (s *Server) recheckNode(ctx context.Context, in *nodeIDInput) (*recheckOutput, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.store.NodeOf(ctx, p.User.ID, in.ID); err != nil {
		return nil, storeErr(err, "没有这个节点")
	}
	if err := s.rateLimit(ctx, "node-recheck:"+in.ID, 20, time.Minute); err != nil {
		return nil, err
	}
	stale, err := s.nodes.RecheckRuntimes(ctx, in.ID)
	if errors.Is(err, platform.ErrRuntimeStatusUnsupported) {
		return nil, fail(http.StatusConflict, "unsupported_daemon", err.Error())
	}
	if err != nil {
		return nil, nodeCommandErr(err)
	}
	node, err := s.store.NodeOf(ctx, p.User.ID, in.ID)
	if err != nil {
		return nil, storeErr(err, "没有这个节点")
	}
	view, err := s.nodeOutput(ctx, node)
	if err != nil {
		return nil, err
	}
	out := &recheckOutput{}
	out.Body.Node, out.Body.Stale = view.Body, stale
	return out, nil
}

func nodeCommandErr(err error) error {
	if errors.Is(err, platform.ErrNodeOffline) {
		return fail(http.StatusConflict, "node_offline", "节点不在线")
	}
	if errors.Is(err, platform.ErrUpgradeUnsupported) {
		return fail(http.StatusConflict, "upgrade_unsupported", err.Error())
	}
	return err
}

func (s *Server) deleteNode(ctx context.Context, in *nodeIDInput) (*struct{}, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.store.NodeOf(ctx, p.User.ID, in.ID); err != nil {
		return nil, storeErr(err, "没有这个节点")
	}
	if err := s.hooks.NodeDeleting(ctx, p.User.ID, in.ID); err != nil {
		return nil, err
	}
	if err := s.store.DeleteNode(ctx, p.User.ID, in.ID); err != nil {
		return nil, storeErr(err, "没有这个节点")
	}
	s.nodes.Disconnect(ctx, in.ID, "node_deleted")
	s.audit(ctx, p.User.ID, "node_delete", "node", in.ID, nil)
	return nil, nil
}

func (s *Server) nodeSessions(ctx context.Context, in *nodeIDInput) (*nodeSessionsOutput, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.store.NodeOf(ctx, p.User.ID, in.ID); err != nil {
		return nil, storeErr(err, "没有这个节点")
	}
	rows, err := s.store.NodeSessions(ctx, in.ID)
	return &nodeSessionsOutput{Body: rows}, err
}

func (s *Server) nodeLogs(ctx context.Context, in *nodeLogsInput) (*nodeLogsOutput, error) {
	p, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.store.NodeOf(ctx, p.User.ID, in.ID); err != nil {
		return nil, storeErr(err, "没有这个节点")
	}
	before, _ := strconv.ParseInt(in.Before, 10, 64)
	limit := pageLimit(in.Limit)
	rows, err := s.store.NodeLogs(ctx, in.ID, in.Level, before, limit)
	if err != nil {
		return nil, err
	}
	out := &nodeLogsOutput{}
	out.Body.Items = rows
	if len(rows) == limit {
		out.Body.NextCursor = cursorString(rows[len(rows)-1].ID)
	}
	return out, nil
}
