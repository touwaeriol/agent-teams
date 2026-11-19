package engine_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/touwaeriol/agent-teams/server/internal/engine"

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

type refreshResult struct {
	Usage *struct {
		Claude *struct {
			Windows []struct {
				Key   string `json:"key"`
				Label string `json:"label"`
			} `json:"windows"`
			ReceivedAt string `json:"received_at"`
		} `json:"claude"`
	} `json:"usage"`
	Stale bool `json:"stale"`
}

// TestNodeUsageRefresh: POST /api/nodes/{id}/usage/refresh asks the node once for concurrent
// callers, serves a report younger than 60 s from the cache, returns stale data when the node
// does not answer, and answers 409 for an old daemon or an offline node.
func TestNodeUsageRefresh(t *testing.T) {
	clock := struct {
		sync.Mutex
		now time.Time
	}{now: time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)}
	now := func() time.Time { clock.Lock(); defer clock.Unlock(); return clock.now }
	advance := func(d time.Duration) { clock.Lock(); clock.now = clock.now.Add(d); clock.Unlock() }
	w := newWorldWith(t, nil, func(e *engine.Engine) {
		e.Gateway.Now = now
		e.Gateway.RefreshWait = time.Second
	})
	b := w.signUp("ops@example.com")
	var old, node nodeCreated
	b.must(201, "POST", "/api/nodes", map[string]string{"name": "old"}, &old)
	b.must(201, "POST", "/api/nodes", map[string]string{"name": "n1"}, &node)
	w.connect(old.Token, nodetest.Options{})
	if code, raw := b.do("POST", "/api/nodes/"+old.Node.ID+"/usage/refresh", nil); code != 409 || !strings.Contains(string(raw), "usage_unsupported") {
		t.Fatalf("old daemon: %d %s", code, raw)
	}
	n := w.connect(node.Token, nodetest.Options{Features: []string{"sessions", "usage"}})
	path := "/api/nodes/" + node.Node.ID + "/usage/refresh"
	ctx, cancel := w.waitCtx()
	defer cancel()
	noMoreRefresh := func(what string) {
		t.Helper()
		short, stop := contextShort()
		defer stop()
		if _, err := n.Wait(short, "node.usage_refresh", nil); err == nil {
			t.Fatalf("%s: unexpected node.usage_refresh", what)
		}
	}

	// Three concurrent refreshes share one node.usage_refresh and all get the answer.
	results := make(chan refreshResult, 3)
	for i := 0; i < 3; i++ {
		go func() {
			var r refreshResult
			code, raw := b.do("POST", path, nil)
			if code != 200 || json.Unmarshal(raw, &r) != nil {
				r.Stale = true
			}
			results <- r
		}()
	}
	if _, err := n.Wait(ctx, "node.usage_refresh", nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	report := map[string]any{"runtimes": map[string]any{"claude": map[string]any{"auth": "subscription", "status": "ok",
		"windows": []any{map[string]any{"key": "weekly_model", "label": strings.Repeat("L", 50), "used_percent": 3}}}}}
	if err := n.SendUnreliable(ctx, "node.usage", report); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		r := <-results
		if r.Stale || r.Usage == nil || r.Usage.Claude == nil || len(r.Usage.Claude.Windows) != 1 ||
			r.Usage.Claude.Windows[0].Label != strings.Repeat("L", 40) {
			t.Fatalf("refresh %d: %+v", i, r)
		}
	}
	noMoreRefresh("concurrent refreshes")

	// 59 s later the cache answers; at 61 s the node is asked again.
	advance(59 * time.Second)
	var r refreshResult
	b.must(200, "POST", path, nil, &r)
	if r.Stale || r.Usage == nil {
		t.Fatalf("cached: %+v", r)
	}
	noMoreRefresh("59 s")
	advance(2 * time.Second)
	start := time.Now()
	b.must(200, "POST", path, nil, &r) // the node does not answer: stale after RefreshWait
	if !r.Stale || r.Usage == nil || r.Usage.Claude == nil || time.Since(start) < 900*time.Millisecond {
		t.Fatalf("timeout: %+v after %s", r, time.Since(start))
	}
	if _, err := n.Wait(ctx, "node.usage_refresh", nil); err != nil {
		t.Fatal(err)
	}

	// The caller that started a refresh goes away: it gets stale data at once, and once the wait
	// has run out the next refresh asks the node again and gets its answer.
	gone, leave := context.WithCancel(ctx)
	left := make(chan bool, 1)
	go func() {
		_, stale, err := w.e.Gateway.RefreshUsage(gone, node.Node.ID)
		left <- stale && err == nil
	}()
	if _, err := n.Wait(ctx, "node.usage_refresh", nil); err != nil {
		t.Fatal(err)
	}
	leave()
	if !<-left {
		t.Fatal("cancelled refresh: want stale, no error")
	}
	time.Sleep(1200 * time.Millisecond) // > RefreshWait: the abandoned wait has ended by itself
	go func() {
		var r refreshResult
		code, raw := b.do("POST", path, nil)
		if code != 200 || json.Unmarshal(raw, &r) != nil {
			r.Stale = true
		}
		results <- r
	}()
	if _, err := n.Wait(ctx, "node.usage_refresh", nil); err != nil {
		t.Fatalf("refresh after a cancelled one: %v", err)
	}
	if err := n.SendUnreliable(ctx, "node.usage", report); err != nil {
		t.Fatal(err)
	}
	if r := <-results; r.Stale || r.Usage == nil || r.Usage.Claude == nil {
		t.Fatalf("refresh after a cancelled one: %+v", r)
	}

	n.Close()
	var nodes []usageNode
	eventually(t, "offline", func() bool {
		b.must(200, "GET", "/api/nodes", nil, &nodes)
		for _, x := range nodes {
			if x.ID == node.Node.ID {
				return !x.Online
			}
		}
		return false
	})
	if code, raw := b.do("POST", path, nil); code != 409 || !strings.Contains(string(raw), "node_offline") {
		t.Fatalf("offline: %d %s", code, raw)
	}
}

// TestNodeUsageSupported: NodeView.usage_supported follows hello.features while online and
// the stored last hello while offline.
func TestNodeUsageSupported(t *testing.T) {
	w := newWorld(t, nil)
	b := w.signUp("ops@example.com")
	var old, node nodeCreated
	b.must(201, "POST", "/api/nodes", map[string]string{"name": "old"}, &old)
	b.must(201, "POST", "/api/nodes", map[string]string{"name": "n1"}, &node)
	type view struct {
		ID             string `json:"id"`
		Online         bool   `json:"online"`
		UsageSupported bool   `json:"usage_supported"`
	}
	get := func() map[string]view {
		var nodes []view
		b.must(200, "GET", "/api/nodes", nil, &nodes)
		m := map[string]view{}
		for _, v := range nodes {
			m[v.ID] = v
		}
		return m
	}
	if v := get()[node.Node.ID]; v.UsageSupported {
		t.Fatalf("never connected: %+v", v)
	}
	o := w.connect(old.Token, nodetest.Options{Features: []string{"sessions"}})
	n := w.connect(node.Token, nodetest.Options{Features: []string{"sessions", "usage"}})
	m := get()
	if v := m[old.Node.ID]; !v.Online || v.UsageSupported {
		t.Fatalf("old daemon: %+v", v)
	}
	if v := m[node.Node.ID]; !v.Online || !v.UsageSupported {
		t.Fatalf("usage daemon: %+v", v)
	}
	o.Close()
	n.Close()
	eventually(t, "offline", func() bool {
		m := get()
		return !m[old.Node.ID].Online && !m[node.Node.ID].Online
	})
	m = get()
	if m[old.Node.ID].UsageSupported || !m[node.Node.ID].UsageSupported {
		t.Fatalf("offline uses last hello: %+v", m)
	}
}
