package httpapi

import (
	"context"
	"strings"
	"testing"

	"github.com/touwaeriol/agent-teams/server/internal/auth"
	"github.com/touwaeriol/agent-teams/server/internal/platform"
)

type createdNode struct {
	Node    NodeView `json:"node"`
	Token   string   `json:"token"`
	Install string   `json:"install"`
	Docker  string   `json:"docker"`
	Compose string   `json:"compose"`
}

func TestNodesLifecycle(t *testing.T) {
	h := newHarness(t)
	c, u := h.signUp("a@example.com")
	var n createdNode
	c.must(201, "POST", "/api/nodes", map[string]string{"name": "rc-erio"}, &n)
	if n.Token == "" || !strings.Contains(n.Install, "AGENT_TEAMS_NODE_TOKEN="+n.Token) ||
		!strings.Contains(n.Install, "curl -fsSL http://console.test/install-node.sh") || !strings.Contains(n.Install, "--name rc-erio") {
		t.Fatalf("enrollment %+v", n)
	}
	for _, want := range []string{"docker run -d --name agent-teams-node-rc-erio --hostname rc-erio", "AGENT_TEAMS_NODE_TOKEN=" + n.Token,
		"AGENT_TEAMS_CONSOLE=http://console.test", "-v agent-teams-node-rc-erio-claude:/home/agent/.claude", "-v agent-teams-node-rc-erio-data:/home/agent/.local/share/agent-teams", "ghcr.io/example/node:test"} {
		if !strings.Contains(n.Docker, want) {
			t.Fatalf("docker run lacks %q:\n%s", want, n.Docker)
		}
	}
	for _, want := range []string{"name: agent-teams-node-rc-erio\n", "image: ghcr.io/example/node:test", `AGENT_TEAMS_NODE_TOKEN: "` + n.Token + `"`,
		`AGENT_TEAMS_CONSOLE: "http://console.test"`, "- workspace:/home/agent/.agents_team", "  state: {}"} {
		if !strings.Contains(n.Compose, want) {
			t.Fatalf("compose lacks %q:\n%s", want, n.Compose)
		}
	}
	if n.Node.Online || n.Node.Isolation != "none" || n.Node.Labels == nil {
		t.Fatalf("node view %+v", n.Node)
	}
	stored, err := h.db.NodeByToken(context.Background(), auth.HashToken(n.Token))
	if err != nil || stored.ID != n.Node.ID || stored.UserID != u.ID {
		t.Fatalf("token hash lookup: %v", err)
	}
	if r := c.do("POST", "/api/nodes", map[string]string{"name": "rc-erio"}); r.Status != 409 {
		t.Fatalf("duplicate = %d", r.Status)
	}
	if r := c.do("POST", "/api/nodes", map[string]string{"name": "bad name!"}); r.Status != 422 {
		t.Fatalf("bad name = %d", r.Status)
	}
	var patched NodeView
	c.must(200, "PATCH", "/api/nodes/"+n.Node.ID, map[string]any{"max_active_sessions": 3, "labels": []string{"gpu", " "}, "isolated": true}, &patched)
	if *patched.MaxActiveSessions != 3 || len(patched.Labels) != 1 || !patched.Isolated || len(h.nodes.configured) != 1 {
		t.Fatalf("patched %+v", patched)
	}
	var rotated Enrollment
	c.must(200, "POST", "/api/nodes/"+n.Node.ID+"/token", nil, &rotated)
	if rotated.Token == n.Token || len(h.nodes.disconnected) != 1 {
		t.Fatal("rotate did not change token or disconnect")
	}
	if _, err := h.db.NodeByToken(context.Background(), auth.HashToken(n.Token)); err == nil {
		t.Fatal("old token still valid")
	}
	if r := c.do("POST", "/api/nodes/"+n.Node.ID+"/upgrade", nil); r.Status != 409 || r.apiError(t).Code != "no_release" {
		t.Fatalf("upgrade without release = %d %s", r.Status, r.Body)
	}
	var sessions []map[string]any
	c.must(200, "GET", "/api/nodes/"+n.Node.ID+"/sessions", nil, &sessions)
	c.must(200, "GET", "/api/nodes/"+n.Node.ID+"/logs", nil, nil)
	if r := c.do("POST", "/api/nodes/"+n.Node.ID+"/drain", map[string]int{"deadline_s": 60}); r.Status != 409 {
		t.Fatalf("drain offline = %d", r.Status)
	}
	other, _ := h.signUp("b@example.com")
	if r := other.do("DELETE", "/api/nodes/"+n.Node.ID, nil); r.Status != 404 {
		t.Fatalf("other user delete = %d", r.Status)
	}
	c.must(204, "DELETE", "/api/nodes/"+n.Node.ID, nil, nil)
	var list []NodeView
	c.must(200, "GET", "/api/nodes", nil, &list)
	if len(list) != 0 {
		t.Fatal("node not deleted")
	}
}

func TestNodeQuotaAndOnlineStatus(t *testing.T) {
	h := newHarness(t)
	admin, _ := h.signUp("a@example.com")
	c, u := h.signUp("b@example.com")
	admin.must(200, "PATCH", "/api/admin/users/"+u.ID, map[string]any{"max_nodes": 1}, nil)
	var n createdNode
	c.must(201, "POST", "/api/nodes", map[string]string{"name": "one"}, &n)
	if r := c.do("POST", "/api/nodes", map[string]string{"name": "two"}); r.Status != 403 || r.apiError(t).Code != "node_quota" {
		t.Fatalf("quota = %d %s", r.Status, r.Body)
	}
	h.nodes.online[n.Node.ID] = true
	var list []NodeView
	c.must(200, "GET", "/api/nodes", nil, &list)
	if !list[0].Online || list[0].Version != "2.0.0" {
		t.Fatalf("live status %+v", list[0])
	}
	var s AdminSettings
	admin.must(200, "GET", "/api/admin/settings", nil, &s)
	s.NodeRelease = &platform.NodeRelease{Version: "2.0.1+abcdef12", SHA256: strings.Repeat("a", 64)}
	admin.must(200, "PUT", "/api/admin/settings", s, nil)
	c.must(200, "GET", "/api/nodes", nil, &list)
	if list[0].LatestVersion != "2.0.1+abcdef12" || !list[0].UpdateAvailable {
		t.Fatalf("release in view %+v", list[0])
	}
	c.must(202, "POST", "/api/nodes/"+n.Node.ID+"/upgrade", nil, nil)
	c.must(202, "POST", "/api/nodes/"+n.Node.ID+"/upgrade", map[string]any{"mode": "now"}, nil)
	if got := strings.Join(h.nodes.upgrades, ","); got != "2.0.1+abcdef12:,2.0.1+abcdef12:now" {
		t.Fatalf("upgrades sent: %s", got)
	}
	if r := c.do("POST", "/api/nodes/"+n.Node.ID+"/upgrade", map[string]any{"mode": "later"}); r.Status != 422 {
		t.Fatalf("bad mode = %d", r.Status)
	}
	h.nodes.noSelfUpdate = true
	if r := c.do("POST", "/api/nodes/"+n.Node.ID+"/upgrade", nil); r.Status != 409 || r.apiError(t).Code != "upgrade_unsupported" {
		t.Fatalf("upgrade without launcher = %d %s", r.Status, r.Body)
	}
}
