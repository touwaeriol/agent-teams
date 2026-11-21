package nodegw

import (
	"context"
	"slices"
	"time"

	protocol "github.com/touwaeriol/agent-teams/protocol"
	"github.com/touwaeriol/agent-teams/server/internal/platform"
)

// Update timeouts (architecture.md §14.3): a node that never answers the request, or a new
// release that never comes back, ends the update as failed in the view.
const (
	upgradeNoAnswer = 2 * time.Minute
	upgradeStaging  = 20 * time.Minute
	upgradeRestart  = 10 * time.Minute
)

// releaseOf is the release id a hello reports: <version>+<build>, or the bare version.
func releaseOf(d protocol.DaemonInfo) string {
	if d.Build != nil && *d.Build != "" {
		return d.Version + "+" + *d.Build
	}
	return d.Version
}

// Upgrade implements platform.NodeRegistry: node.upgrade to a daemon that runs under the launcher.
func (g *Gateway) Upgrade(ctx context.Context, nodeID string, r platform.NodeRelease, o platform.UpgradeOptions) error {
	l := g.link(nodeID)
	if l == nil {
		return platform.ErrNodeOffline
	}
	if !slices.Contains(l.hello.Features, "self_update") {
		return platform.ErrUpgradeUnsupported
	}
	mode := o.Mode
	if mode == "" {
		mode = string(protocol.NodeUpgradeModeWaitIdle)
	}
	body := protocol.NodeUpgrade{Version: r.Version, SHA256: r.SHA256, Mode: ptr(protocol.NodeUpgradeMode(mode))}
	if r.Signature != "" {
		body.Signature = ptr(r.Signature)
	}
	if o.DeadlineS > 0 {
		body.DeadlineS = ptr(o.DeadlineS)
	}
	if err := g.send(ctx, nodeID, Command{Type: protocol.TypeNodeUpgrade, Body: body}); err != nil {
		return err
	}
	now := g.now()
	g.mu.Lock()
	defer g.mu.Unlock()
	prev := g.upgrades[nodeID]
	if mode == string(protocol.NodeUpgradeModeCancel) {
		return nil
	}
	if prev != nil && prev.Target == r.Version && !upgradeOver(prev.Phase) {
		prev.Mode, prev.UpdatedAt = mode, now
		return nil
	}
	g.upgrades[nodeID] = &platform.NodeUpgradeState{Target: r.Version, Phase: "requested", Mode: mode, StartedAt: now, UpdatedAt: now}
	return nil
}

func upgradeOver(phase string) bool {
	return phase == "done" || phase == "failed" || phase == "rolled_back" || phase == "cancelled"
}

// RecordUpgrade keeps a node.upgrade_result as the node's update state.
func (g *Gateway) RecordUpgrade(nodeID string, m *protocol.NodeUpgradeResult) {
	now := g.now()
	phase := "failed"
	if m.Phase != nil {
		phase = string(*m.Phase)
	} else if m.Ok {
		phase = "done"
	}
	target := ""
	if m.Version != nil {
		target = *m.Version
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	st := g.upgrades[nodeID]
	if st == nil || (target != "" && st.Target != target) {
		st = &platform.NodeUpgradeState{Target: target, Mode: "wait_idle", StartedAt: now}
		g.upgrades[nodeID] = st
	}
	st.Phase, st.UpdatedAt, st.Error, st.ActiveTurns = phase, now, "", 0
	if m.Error != nil {
		st.Error = truncate(*m.Error, 1000)
	}
	if m.ActiveTurns != nil {
		st.ActiveTurns = *m.ActiveTurns
	}
}

// upgradeView is the node's update state with the timeouts applied; nil when none.
func (g *Gateway) upgradeView(nodeID, running string) *platform.NodeUpgradeState {
	g.mu.Lock()
	st := g.upgrades[nodeID]
	if st == nil {
		g.mu.Unlock()
		return nil
	}
	cp := *st
	g.mu.Unlock()
	if cp.Phase == "restarting" && running == cp.Target && running != "" {
		cp.Phase = "confirming" // up again on the new release, confirming itself
	}
	since := g.now().Sub(cp.UpdatedAt)
	switch {
	case cp.Phase == "requested" && since > upgradeNoAnswer:
		cp.Phase, cp.Error = "failed", "节点没有响应更新请求"
	case cp.Phase == "staging" && since > upgradeStaging:
		cp.Phase, cp.Error = "failed", "下载或安装新版本超时"
	case (cp.Phase == "restarting" || cp.Phase == "confirming") && since > upgradeRestart:
		cp.Phase, cp.Error = "failed", "新版本没有在 10 分钟内确认正常"
	}
	return &cp
}
