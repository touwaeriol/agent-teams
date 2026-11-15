package engine_test

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"testing"

	"github.com/touwaeriol/agent-teams/server/internal/store"
)

func pluginZipB64(t *testing.T, files map[string]string) string {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, _ := zw.Create(name)
		_, _ = w.Write([]byte(content))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// TestPluginReachesSessionOpen: a plugin ticked on the designer role arrives in session.open at
// its exact (pinned) version with its skills, agents and MCP servers, plus the Skill and Agent tools.
func TestPluginReachesSessionOpen(t *testing.T) {
	_, open := startDesignTeamWith(t, func(tm *team, designer *store.Role) {
		for _, word := range []string{"ONE", "TWO"} {
			tm.b.must(201, "POST", "/api/plugins", map[string]any{"zip": pluginZipB64(t, map[string]string{
				".claude-plugin/plugin.json": `{"name":"tinyplug","version":"0.1.0"}`,
				"skills/greet/SKILL.md":      "---\nname: greet\ndescription: d\n---\n" + word,
				"agents/echoer.md":           "---\nname: echoer\ndescription: e\n---\nKIWI-9",
				".mcp.json":                  `{"calc":{"command":"node","args":["${CLAUDE_PLUGIN_ROOT}/s.mjs"]}}`,
				"s.mjs":                      "// server",
			})}, nil)
		}
		designer.Plugins = []string{"tinyplug@1"}
	})
	if len(open.Plugins) != 1 {
		t.Fatalf("plugins %+v", open.Plugins)
	}
	p := open.Plugins[0]
	if p.Name != "tinyplug" || p.Version != 1 || len(p.Skills) != 1 || p.Skills[0] != "tinyplug:greet" ||
		len(p.Agents) != 1 || p.McpServers["calc"].Command == nil {
		t.Fatalf("plugin bundle %+v", p)
	}
	found := false
	for _, f := range p.Files {
		found = found || (f.Path == "skills/greet/SKILL.md" && f.Content == "---\nname: greet\ndescription: d\n---\nONE")
	}
	if !found || !contains(open.Claude.Tools, "Agent") || !contains(open.Claude.Tools, "Skill") {
		t.Fatalf("pinned version 1 files / tools: %+v %v", p.Files, open.Claude.Tools)
	}
}
