package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	protocol "github.com/touwaeriol/agent-teams/protocol"

	"github.com/touwaeriol/agent-teams/server/internal/store"
)

// MaxPerRole caps how many plugins one role may tick.
const MaxPerRole = 8

// visible is the plugins a user's roles resolve: their own (enabled or not), and the enabled
// built-ins whose name they do not use themselves.
const visible = `(p.user_id = $1 OR (p.user_id IS NULL AND p.enabled
	AND NOT EXISTS (SELECT 1 FROM at_plugins o WHERE o.user_id = $1 AND o.name = p.name)))`

// tickable is visible plus disabled built-ins: a role that already ticks a built-in an admin
// later disabled must still save (the plugin just is not loaded), like a disabled own plugin.
const tickable = `(p.user_id = $1 OR (p.user_id IS NULL
	AND NOT EXISTS (SELECT 1 FROM at_plugins o WHERE o.user_id = $1 AND o.name = p.name)))`

// Choices maps each plugin name a user's roles may tick to its stored versions.
func Choices(ctx context.Context, db store.DB, userID string) (map[string]map[int]bool, error) {
	rows, err := db.Query(ctx, `SELECT p.name, v.version FROM at_plugins p
		JOIN at_plugin_versions v ON v.plugin_id = p.id WHERE `+tickable, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[int]bool{}
	for rows.Next() {
		var name string
		var v int
		if err := rows.Scan(&name, &v); err != nil {
			return nil, err
		}
		if out[name] == nil {
			out[name] = map[int]bool{}
		}
		out[name][v] = true
	}
	return out, rows.Err()
}

// CheckRefs validates a role's plugin entries against Choices; "" when they are fine.
func CheckRefs(refs []string, choices map[string]map[int]bool) string {
	if len(refs) > MaxPerRole {
		return fmt.Sprintf("一个岗位最多 %d 个插件", MaxPerRole)
	}
	seen := map[string]bool{}
	for _, s := range refs {
		r, ok := ParseRef(s)
		switch {
		case !ok:
			return "插件写法是 name 或 name@版本号：" + s
		case seen[r.Name]:
			return "插件重复：" + r.Name
		case choices != nil && choices[r.Name] == nil:
			return "没有这个插件：" + r.Name
		case choices != nil && r.Version > 0 && !choices[r.Name][r.Version]:
			return fmt.Sprintf("插件 %s 没有版本 %d", r.Name, r.Version)
		}
		seen[r.Name] = true
	}
	return ""
}

// Bundles loads a role's plugins at their exact versions for session.open. Disabled or
// deleted plugins are skipped, and so is a plugin that would take the total past budget bytes.
func Bundles(ctx context.Context, db store.DB, userID string, refs []string, budget int) ([]protocol.PluginBundle, error) {
	out := []protocol.PluginBundle{}
	for _, s := range refs {
		r, ok := ParseRef(s)
		if !ok || len(out) == MaxPerRole {
			continue
		}
		b, size, err := load(ctx, db, userID, r)
		if errors.Is(err, ErrNotFound) || (err == nil && size > budget) {
			continue
		}
		if err != nil {
			return nil, err
		}
		budget -= size
		out = append(out, b)
	}
	return out, nil
}

func load(ctx context.Context, db store.DB, userID string, r Ref) (protocol.PluginBundle, int, error) {
	var version int
	var filesRaw, contentsRaw []byte
	err := db.QueryRow(ctx, `SELECT v.version, v.files, v.contents FROM at_plugins p
		JOIN at_plugin_versions v ON v.plugin_id = p.id
		WHERE `+visible+` AND p.enabled AND p.name = $2 AND v.version = COALESCE(NULLIF($3, 0), p.latest)
		ORDER BY p.user_id IS NULL LIMIT 1`, userID, r.Name, r.Version).Scan(&version, &filesRaw, &contentsRaw)
	if errors.Is(err, pgx.ErrNoRows) {
		return protocol.PluginBundle{}, 0, ErrNotFound
	}
	if err != nil {
		return protocol.PluginBundle{}, 0, err
	}
	var files []File
	var c Contents
	if err := json.Unmarshal(filesRaw, &files); err != nil {
		return protocol.PluginBundle{}, 0, err
	}
	if err := json.Unmarshal(contentsRaw, &c); err != nil {
		return protocol.PluginBundle{}, 0, err
	}
	return ToBundle(r.Name, version, files, c), encodedSize(files), nil
}

func encodedSize(files []File) int {
	n := 0
	for _, f := range files {
		n += len(f.Content) + len(f.Path)
	}
	return n
}

// ToBundle renders one plugin version as session.open's PluginBundle.
func ToBundle(name string, version int, files []File, c Contents) protocol.PluginBundle {
	b := protocol.PluginBundle{Name: name, Version: version, Files: make([]protocol.PluginFile, len(files)),
		Skills: []string{}, Agents: []string{}}
	for i, f := range files {
		b.Files[i] = bundleFile(f)
	}
	for _, s := range append(append([]string{}, c.Skills...), c.Commands...) {
		b.Skills = append(b.Skills, name+":"+s)
	}
	for _, a := range c.Agents {
		b.Agents = append(b.Agents, name+":"+a)
	}
	if len(c.MCPServers) > 0 {
		b.McpServers = protocol.PluginBundleMcpServers{}
		for k, s := range c.MCPServers {
			b.McpServers[k] = bundleServer(s)
		}
	}
	return b
}

func bundleFile(f File) protocol.PluginFile {
	out := protocol.PluginFile{Path: f.Path, Content: f.Content}
	if f.Encoding == "base64" {
		enc := protocol.PluginFileEncodingBase64
		out.Encoding = &enc
	}
	if f.Executable {
		x := true
		out.Executable = &x
	}
	return out
}

func bundleServer(s MCPServer) protocol.PluginMcpServer {
	out := protocol.PluginMcpServer{Type: protocol.PluginMcpServerType(s.Type), Args: s.Args,
		Env: protocol.PluginMcpServerEnv(s.Env), Headers: protocol.PluginMcpServerHeaders(s.Headers)}
	if s.Command != "" {
		out.Command = &s.Command
	}
	if s.URL != "" {
		out.URL = &s.URL
	}
	return out
}

// HasAgents reports whether any bundle ships subagents (they need the Agent tool).
func HasAgents(bundles []protocol.PluginBundle) bool {
	for _, b := range bundles {
		if len(b.Agents) > 0 {
			return true
		}
	}
	return false
}

// HasSkills reports whether any bundle ships skills or commands (they need the Skill tool).
func HasSkills(bundles []protocol.PluginBundle) bool {
	for _, b := range bundles {
		if len(b.Skills) > 0 {
			return true
		}
	}
	return false
}

// Instructions renders the plugins' skills and commands as text for Codex, which loads
// neither; subagents and hooks are Claude-only and left out.
func Instructions(bundles []protocol.PluginBundle) string {
	var b strings.Builder
	for _, p := range bundles {
		for _, f := range p.Files {
			if title := codexTitle(p.Name, f); title != "" {
				fmt.Fprintf(&b, "\n## %s\n\n%s\n", title, strings.TrimSpace(f.Content))
			}
		}
	}
	if b.Len() == 0 {
		return ""
	}
	return "\n\n# Plugin skills and commands\n\nFollow the matching skill or command when a task calls for it.\n" + b.String()
}

func codexTitle(plugin string, f protocol.PluginFile) string {
	if f.Encoding != nil && *f.Encoding == protocol.PluginFileEncodingBase64 {
		return ""
	}
	parts := strings.Split(f.Path, "/")
	switch {
	case len(parts) == 3 && parts[0] == "skills" && parts[2] == "SKILL.md":
		return "Skill: " + plugin + ":" + parts[1]
	case len(parts) == 2 && parts[0] == "commands" && strings.HasSuffix(parts[1], ".md"):
		return "Command: " + plugin + ":" + strings.TrimSuffix(parts[1], ".md")
	}
	return ""
}
