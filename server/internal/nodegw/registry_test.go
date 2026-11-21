package nodegw

import (
	"testing"

	protocol "github.com/touwaeriol/agent-teams/protocol"
)

// TestHelloReady: a runtime that needs a ready hello runs only where the hello reports it
// installed and logged in.
func TestHelloReady(t *testing.T) {
	s := func(v string) *string { return &v }
	b := func(v bool) *bool { return &v }
	auth := func(v protocol.AntigravityRuntimeAuth) *protocol.AntigravityRuntimeAuth { return &v }
	cases := []struct {
		name string
		rts  protocol.Runtimes
		want bool
	}{
		{"missing", protocol.Runtimes{}, false},
		{"oauth", protocol.Runtimes{Antigravity: &protocol.AntigravityRuntime{CLI: s("1.5.14"), Auth: auth("oauth")}}, true},
		{"api key installed", protocol.Runtimes{Antigravity: &protocol.AntigravityRuntime{CLI: s("1.5.14"), Auth: auth("api_key"), Installed: b(true)}}, true},
		{"not logged in", protocol.Runtimes{Antigravity: &protocol.AntigravityRuntime{CLI: s("1.5.14"), Auth: auth("none")}}, false},
		{"no auth", protocol.Runtimes{Antigravity: &protocol.AntigravityRuntime{CLI: s("1.5.14")}}, false},
		{"not installed", protocol.Runtimes{Antigravity: &protocol.AntigravityRuntime{CLI: s(""), Auth: auth("oauth"), Installed: b(false)}}, false},
	}
	for _, c := range cases {
		if got := helloReady(toMap(c.rts), "antigravity"); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// TestRuntimeReadyWithoutHelloCheck: Claude and Codex keep relying on node.usage (no hello
// check, even offline); Antigravity on an unknown node is not ready.
func TestRuntimeReadyWithoutHelloCheck(t *testing.T) {
	g := &Gateway{}
	if !g.RuntimeReady("n_x", "claude") || !g.RuntimeReady("n_x", "codex") {
		t.Fatal("claude and codex need no hello check")
	}
	if g.RuntimeReady("n_x", "antigravity") {
		t.Fatal("antigravity on an offline node")
	}
}
