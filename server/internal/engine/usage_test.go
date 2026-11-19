package engine_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/touwaeriol/agent-teams/server/internal/nodegw/nodetest"
)

type usageNode struct {
	ID     string          `json:"id"`
	Online bool            `json:"online"`
	Usage  json.RawMessage `json:"usage"`
}

// TestNodeUsage: node.usage reaches GET /api/nodes sanitized (unknown fields dropped, percent
// clamped, at most 8 windows, strings cut, received_at added), survives the link, and an
// invalid report is rejected without replacing the stored one.
func TestNodeUsage(t *testing.T) {
	w := newWorld(t, nil)
	b := w.signUp("ops@example.com")
	var node nodeCreated
	b.must(201, "POST", "/api/nodes", map[string]string{"name": "n1"}, &node)
	n := w.connect(node.Token, nodetest.Options{})

	list := func() usageNode {
		var nodes []usageNode
		b.must(200, "GET", "/api/nodes", nil, &nodes)
		if len(nodes) != 1 {
			t.Fatalf("nodes %+v", nodes)
		}
		return nodes[0]
	}
	if u := list().Usage; string(u) != "null" {
		t.Fatalf("usage before any report: %s", u)
	}

	windows := []any{
		map[string]any{"key": "five_hour", "used_percent": 142.5, "window_minutes": 300, "resets_at": "2026-11-19T15:00:00+08:00", "secret": "x"},
		map[string]any{"key": "weekly", "used_percent": -3, "resets_at": nil},
	}
	for i := 0; i < 10; i++ {
		windows = append(windows, map[string]any{"key": "extra", "used_percent": 1})
	}
	body := map[string]any{"token": "sk-should-not-be-kept", "runtimes": map[string]any{
		"claude": map[string]any{"auth": "subscription", "status": "ok", "plan": strings.Repeat("p", 200),
			"windows": windows, "fetched_at": "2026-11-19T10:00:00Z", "oauth_token": "secret"},
		"codex":  map[string]any{"auth": "api_key", "status": "unsupported"},
		"gemini": map[string]any{"auth": "none", "status": "ok"},
	}}
	ctx, cancel := w.waitCtx()
	defer cancel()
	if err := n.SendUnreliable(ctx, "node.usage", body); err != nil {
		t.Fatal(err)
	}
	type window struct {
		Key           string  `json:"key"`
		UsedPercent   float64 `json:"used_percent"`
		WindowMinutes *int    `json:"window_minutes"`
		ResetsAt      *string `json:"resets_at"`
	}
	type runtime struct {
		Auth       string   `json:"auth"`
		Status     string   `json:"status"`
		Plan       string   `json:"plan"`
		Windows    []window `json:"windows"`
		FetchedAt  *string  `json:"fetched_at"`
		ReceivedAt string   `json:"received_at"`
	}
	var got struct {
		Claude *runtime `json:"claude"`
		Codex  *runtime `json:"codex"`
	}
	var raw json.RawMessage
	eventually(t, "usage stored", func() bool {
		raw = list().Usage
		return string(raw) != "null"
	})
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"secret", "sk-should", "oauth_token", "gemini", "token"} {
		if strings.Contains(string(raw), leak) {
			t.Fatalf("usage kept %q: %s", leak, raw)
		}
	}
	c := got.Claude
	if c == nil || c.Auth != "subscription" || c.Status != "ok" || len(c.Plan) != 64 || len(c.Windows) != 8 ||
		c.ReceivedAt == "" || c.FetchedAt == nil || *c.FetchedAt != "2026-11-19T10:00:00Z" {
		t.Fatalf("claude %s", raw)
	}
	if w0 := c.Windows[0]; w0.Key != "five_hour" || w0.UsedPercent != 100 || w0.WindowMinutes == nil || *w0.WindowMinutes != 300 ||
		w0.ResetsAt == nil || *w0.ResetsAt != "2026-11-19T07:00:00Z" {
		t.Fatalf("window 0 %s", raw)
	}
	if w1 := c.Windows[1]; w1.UsedPercent != 0 || w1.ResetsAt != nil || w1.WindowMinutes != nil {
		t.Fatalf("window 1 %s", raw)
	}
	if got.Codex == nil || got.Codex.Auth != "api_key" || got.Codex.Status != "unsupported" || got.Codex.Windows == nil {
		t.Fatalf("codex %s", raw)
	}

	// An invalid report (unknown auth) gets bad_schema and leaves the stored one alone.
	bad := map[string]any{"runtimes": map[string]any{"claude": map[string]any{"auth": "cookie", "status": "ok"}}}
	if err := n.SendUnreliable(ctx, "node.usage", bad); err != nil {
		t.Fatal(err)
	}
	eventually(t, "bad_schema", func() bool {
		for _, e := range n.Errors() {
			if e.Code == "bad_schema" {
				return true
			}
		}
		return false
	})
	if after := list().Usage; string(after) != string(raw) {
		t.Fatalf("invalid report replaced usage: %s", after)
	}

	// The last report stays visible after the node goes offline (received_at tells its age).
	n.Close()
	eventually(t, "offline", func() bool { return !list().Online })
	if after := list().Usage; string(after) != string(raw) {
		t.Fatalf("usage after disconnect: %s", after)
	}
}
