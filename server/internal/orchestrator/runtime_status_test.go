package orchestrator

import (
	"strings"
	"testing"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// TestHeld: only a pick the console knows cannot run is held: an online node that reports its
// runtime CLIs (gated) without the runtime ready. An offline node keeps the old wait and an old
// daemon the old benefit of the doubt.
func TestHeld(t *testing.T) {
	cases := []struct {
		name string
		got  nodeState
		want bool
	}{
		{"usable", nodeState{id: "a", usable: true, online: true, gated: true}, false},
		{"gated, not ready", nodeState{id: "a", online: true, gated: true}, true},
		{"gated, first report not in yet", nodeState{id: "a", online: true, gated: true, unseen: true}, true},
		{"offline: waits for the node as before", nodeState{id: "a"}, false},
		{"old daemon said not logged in: as before", nodeState{id: "a", online: true}, false},
	}
	for _, c := range cases {
		if got := held(c.got, []nodeState{c.got}); got != c.want {
			t.Errorf("%s: got %v", c.name, got)
		}
	}
}

// TestNodeRuntimeReady: installed and logged in by any means, not expired.
func TestNodeRuntimeReady(t *testing.T) {
	now := time.Now()
	past, future := now.Add(-time.Hour), now.Add(time.Hour)
	cases := []struct {
		r    store.NodeRuntime
		want bool
	}{
		{store.NodeRuntime{Installed: true, Auth: "subscription"}, true},
		{store.NodeRuntime{Installed: true, Auth: "chatgpt", AuthExpiresAt: &future}, true},
		{store.NodeRuntime{Installed: true, Auth: "api_key"}, true},
		{store.NodeRuntime{Installed: true, Auth: "none"}, false},
		{store.NodeRuntime{Installed: true, Auth: "unknown"}, false},
		{store.NodeRuntime{Installed: false, Auth: "subscription"}, false},
		{store.NodeRuntime{Installed: true, Auth: "subscription", Expired: true}, false},
		{store.NodeRuntime{Installed: true, Auth: "subscription", AuthExpiresAt: &past}, false},
	}
	for i, c := range cases {
		if got := c.r.Ready(now); got != c.want {
			t.Errorf("case %d %+v: got %v", i, c.r, got)
		}
	}
}

// TestHoldReason: one clause per runtime of the role, naming what is missing.
func TestHoldReason(t *testing.T) {
	notInstalled := &store.NodeRuntime{Runtime: "codex"}
	loggedOut := &store.NodeRuntime{Runtime: "claude", Installed: true, Auth: "none"}
	expired := &store.NodeRuntime{Runtime: "claude", Installed: true, Auth: "subscription", Expired: true}
	cases := []struct {
		name   string
		rts    []string
		states []nodeState
		want   []string
	}{
		{"not logged in", []string{"claude"}, []nodeState{{id: "a", rt: "claude", online: true, gated: true, rs: loggedOut}},
			[]string{"没有已登录 Claude Code 的在线节点"}},
		{"not installed anywhere", []string{"codex"}, []nodeState{{id: "a", rt: "codex", online: true, gated: true, rs: notInstalled},
			{id: "b", rt: "codex", online: true, gated: true}}, []string{"Codex 没有安装在任何在线的候选节点上"}},
		{"expired", []string{"claude"}, []nodeState{{id: "a", rt: "claude", online: true, gated: true, rs: expired}},
			[]string{"Claude Code 在在线节点上的登录已过期"}},
		{"first report not in yet", []string{"claude"}, []nodeState{{id: "a", rt: "claude", online: true, gated: true, unseen: true}},
			[]string{"正在检测节点上 Claude Code 的安装和登录状态"}},
		{"two runtimes", []string{"claude", "codex"}, []nodeState{{id: "a", rt: "claude", online: true, gated: true, rs: loggedOut},
			{id: "a", rt: "codex"}}, []string{"没有已登录 Claude Code 的在线节点；没有能运行 Codex 的在线节点"}},
	}
	for _, c := range cases {
		got := holdReason(c.rts, c.states)
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: %q lacks %q", c.name, got, w)
			}
		}
	}
}
