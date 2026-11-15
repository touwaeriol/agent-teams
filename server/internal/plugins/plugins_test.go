package plugins

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io/fs"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	protocol "github.com/touwaeriol/agent-teams/protocol"

	"github.com/touwaeriol/agent-teams/server/internal/testdb"
)

const manifestJSON = `{"name":"tinyplug","version":"0.1.0","description":"Tiny test plugin"}`

func tiny() []File {
	return []File{
		{Path: ManifestPath, Content: manifestJSON},
		{Path: "skills/greet/SKILL.md", Content: "---\nname: greet\ndescription: Greeting code word\n---\nPINEAPPLE-42\n"},
		{Path: "commands/shout.md", Content: "---\ndescription: Shout\n---\nSay MANGO-7.\n"},
		{Path: "agents/echoer.md", Content: "---\nname: echoer\ndescription: Echo\n---\nSay KIWI-9.\n"},
		{Path: ".mcp.json", Content: `{"mcpServers":{"calc":{"command":"node","args":["${CLAUDE_PLUGIN_ROOT}/mcp/server.mjs"]}}}`},
		{Path: "mcp/server.mjs", Content: "// server", Executable: true},
		{Path: "hooks/hooks.json", Content: `{"hooks":{}}`},
	}
}

func with(files []File, path, content string) []File {
	out := []File{}
	for _, f := range files {
		if f.Path != path {
			out = append(out, f)
		}
	}
	if content != "" {
		out = append(out, File{Path: path, Content: content})
	}
	return out
}

func TestParse(t *testing.T) {
	p, err := Parse(tiny())
	if err != nil {
		t.Fatal(err)
	}
	c := p.Contents
	if p.Name != "tinyplug" || p.ManifestVersion != "0.1.0" || p.Description != "Tiny test plugin" || len(p.SHA256) != 64 {
		t.Fatalf("package %+v", p)
	}
	if fmt.Sprint(c.Skills, c.Commands, c.Agents, c.Hooks) != "[greet] [shout] [echoer] true" {
		t.Fatalf("contents %+v", c)
	}
	if s := c.MCPServers["calc"]; s.Type != "stdio" || s.Command != "node" || len(s.Args) != 1 {
		t.Fatalf("mcp %+v", c.MCPServers)
	}
	if p.Files[0].Path != ".claude-plugin/plugin.json" {
		t.Fatalf("files must be sorted: %v", p.Files[0].Path)
	}
}

func TestParseRejects(t *testing.T) {
	cases := map[string][]File{
		"no manifest":        with(tiny(), ManifestPath, ""),
		"bad json":           with(tiny(), ManifestPath, "{"),
		"bad name":           with(tiny(), ManifestPath, `{"name":"Tiny Plug"}`),
		"reserved":           with(tiny(), ManifestPath, `{"name":"agent-teams-skills"}`),
		"custom paths":       with(tiny(), ManifestPath, `{"name":"tinyplug","commands":"./cmds"}`),
		"traversal":          append(tiny(), File{Path: "../evil", Content: "x"}),
		"dot segment":        append(tiny(), File{Path: "skills/./x", Content: "x"}),
		"absolute":           append(tiny(), File{Path: "/etc/passwd", Content: "x"}),
		"nested command":     append(tiny(), File{Path: "commands/sub/x.md", Content: "x"}),
		"skill name differs": with(tiny(), "skills/greet/SKILL.md", "---\nname: other\ndescription: d\n---\n"),
		"mcp without cmd":    with(tiny(), ".mcp.json", `{"mcpServers":{"calc":{"type":"stdio"}}}`),
		"mcp bad type":       with(tiny(), ".mcp.json", `{"calc":{"type":"ws","url":"wss://x"}}`),
		"mcp bad url":        with(tiny(), ".mcp.json", `{"calc":{"type":"http","url":"ftp://x"}}`),
		"hooks path gone":    with(tiny(), ManifestPath, `{"name":"tinyplug","hooks":"./nope.json"}`),
		"binary as text":     append(tiny(), File{Path: "bin/x", Content: "\xff\xfe"}),
		"empty plugin":       {{Path: ManifestPath, Content: manifestJSON}, {Path: "README.md", Content: "x"}},
		"duplicate":          append(tiny(), File{Path: "commands/shout.md", Content: "x"}),
	}
	for name, files := range cases {
		if _, err := Parse(files); !IsProblem(err) {
			t.Errorf("%s: want a problem, got %v", name, err)
		}
	}
}

func TestParseManifestMCPAndBinary(t *testing.T) {
	files := with(tiny(), ".mcp.json", "")
	files = with(files, ManifestPath, `{"name":"tinyplug","mcpServers":"./servers.json","hooks":{"PreToolUse":[]}}`)
	files = append(files, File{Path: "servers.json", Content: `{"docs":{"url":"https://example.com/mcp"}}`},
		File{Path: "bin/tool", Content: base64.StdEncoding.EncodeToString([]byte{0, 1, 2}), Encoding: "base64"})
	p, err := Parse(files)
	if err != nil {
		t.Fatal(err)
	}
	if s := p.Contents.MCPServers["docs"]; s.Type != "http" || s.URL != "https://example.com/mcp" || !p.Contents.Hooks {
		t.Fatalf("contents %+v", p.Contents)
	}
	if p.Size <= 3 {
		t.Fatalf("size %d", p.Size)
	}
}

func TestLimits(t *testing.T) {
	big := strings.Repeat("a", MaxFileBytes+1)
	if _, err := Parse(append(tiny(), File{Path: "big.txt", Content: big})); !IsProblem(err) {
		t.Fatalf("file too big: %v", err)
	}
	many := tiny()
	for i := 0; len(many) <= MaxFiles; i++ {
		many = append(many, File{Path: fmt.Sprintf("ref/%d.md", i), Content: "x"})
	}
	if _, err := Parse(many); !IsProblem(err) {
		t.Fatalf("too many files: %v", err)
	}
	half := strings.Repeat("b", MaxFileBytes)
	if _, err := Parse(append(tiny(), File{Path: "a", Content: half}, File{Path: "b", Content: half})); !IsProblem(err) {
		t.Fatalf("total too big: %v", err)
	}
}

type entry struct {
	name    string
	content string
	mode    fs.FileMode
}

func zipOf(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		if e.mode != 0 {
			h.SetMode(e.mode)
		}
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(e.content))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func tinyEntries(prefix string) []entry {
	out := []entry{}
	for _, f := range tiny() {
		mode := fs.FileMode(0o644)
		if f.Executable {
			mode = 0o755
		}
		out = append(out, entry{prefix + f.Path, f.Content, mode})
	}
	return append(out, entry{prefix + ".DS_Store", "junk", 0o644}, entry{"__MACOSX/x", "junk", 0o644}, entry{prefix + "bin/blob", "\x00\xff", 0o644})
}

func TestFromZip(t *testing.T) {
	files, err := FromZip(zipOf(t, tinyEntries("tinyplug/")...))
	if err != nil {
		t.Fatal(err)
	}
	p, err := Parse(files)
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]File{}
	for _, f := range p.Files {
		byPath[f.Path] = f
	}
	if !byPath["mcp/server.mjs"].Executable || byPath["commands/shout.md"].Executable {
		t.Fatal("executable bits must follow the zip modes")
	}
	if b := byPath["bin/blob"]; b.Encoding != "base64" || b.Content != base64.StdEncoding.EncodeToString([]byte{0, 0xff}) {
		t.Fatalf("binary file %+v", b)
	}
	if _, ok := byPath[".DS_Store"]; ok || len(p.Files) != len(tiny())+1 {
		t.Fatalf("metadata must be skipped: %d files", len(p.Files))
	}
}

func TestFromZipRejects(t *testing.T) {
	cases := map[string][]byte{
		"symlink":   zipOf(t, append(tinyEntries(""), entry{"link", "/etc/passwd", fs.ModeSymlink | 0o777})...),
		"traversal": zipOf(t, append(tinyEntries(""), entry{"../../evil", "x", 0o644})...),
		"backslash": zipOf(t, append(tinyEntries(""), entry{`a\..\b`, "x", 0o644})...),
		"not zip":   []byte("plain text"),
	}
	for name, data := range cases {
		if _, err := FromZip(data); !IsProblem(err) {
			t.Errorf("%s: want a problem, got %v", name, err)
		}
	}
	if _, err := FromZip(make([]byte, MaxZipBytes+1)); !IsProblem(err) {
		t.Fatalf("zip too big: %v", err)
	}
	// Highly compressible files: the zip is tiny, the decompressed total is not.
	big := strings.Repeat("a", MaxFileBytes-1)
	bomb := zipOf(t, entry{ManifestPath, manifestJSON, 0}, entry{"a", big, 0}, entry{"b", big, 0}, entry{"c", big, 0})
	if len(bomb) > 64<<10 {
		t.Fatalf("test zip should compress well, is %d bytes", len(bomb))
	}
	if _, err := FromZip(bomb); !IsProblem(err) || !strings.Contains(err.Error(), "合计") {
		t.Fatalf("decompressed total: %v", err)
	}
}

func TestRefs(t *testing.T) {
	for in, want := range map[string]string{"tinyplug": "tinyplug", "tinyplug@3": "tinyplug@3"} {
		r, ok := ParseRef(in)
		if !ok || r.String() != want {
			t.Fatalf("%s -> %+v %v", in, r, ok)
		}
	}
	for _, bad := range []string{"", "Tiny", "x@0", "x@-1", "x@01", "x@a", "x@1@2"} {
		if _, ok := ParseRef(bad); ok {
			t.Fatalf("%q must not parse", bad)
		}
	}
	choices := map[string]map[int]bool{"tinyplug": {1: true, 2: true}}
	cases := map[string][]string{"": {"tinyplug"}, "没有这个插件": {"nope"}, "没有版本 3": {"tinyplug@3"},
		"重复": {"tinyplug", "tinyplug@1"}, "写法": {"Bad Name"}}
	for want, refs := range cases {
		got := CheckRefs(refs, choices)
		if (want == "" && got != "") || !strings.Contains(got, want) {
			t.Errorf("%v: got %q, want %q", refs, got, want)
		}
	}
	if CheckRefs(make([]string, MaxPerRole+1), nil) == "" {
		t.Fatal("too many plugins per role")
	}
}

func TestToBundleAndInstructions(t *testing.T) {
	p, _ := Parse(append(tiny(), File{Path: "bin/x", Content: "AAE=", Encoding: "base64"}))
	b := ToBundle(p.Name, 3, p.Files, p.Contents)
	if b.Version != 3 || fmt.Sprint(b.Skills) != "[tinyplug:greet tinyplug:shout]" || fmt.Sprint(b.Agents) != "[tinyplug:echoer]" {
		t.Fatalf("bundle %+v", b)
	}
	if s := b.McpServers["calc"]; s.Command == nil || *s.Command != "node" || s.Type != protocol.PluginMcpServerTypeStdio {
		t.Fatalf("mcp %+v", b.McpServers)
	}
	if !HasAgents([]protocol.PluginBundle{b}) || !HasSkills([]protocol.PluginBundle{b}) {
		t.Fatal("has agents/skills")
	}
	text := Instructions([]protocol.PluginBundle{b})
	for _, want := range []string{"## Skill: tinyplug:greet", "PINEAPPLE-42", "## Command: tinyplug:shout", "MANGO-7"} {
		if !strings.Contains(text, want) {
			t.Fatalf("instructions lack %q: %s", want, text)
		}
	}
	if strings.Contains(text, "KIWI-9") || Instructions(nil) != "" {
		t.Fatal("agents are Claude-only; no plugins, no text")
	}
	if s := Summarize(p.Contents); fmt.Sprint(s.MCPServers) != "[calc]" {
		t.Fatalf("summary %+v", s)
	}
}

func newPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, testdb.Migrated(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, `INSERT INTO at_users (id, email, password_hash, role) VALUES
		('u1','a@x','h','user'), ('u2','b@x','h','user'), ('ad','c@x','h','admin')`); err != nil {
		t.Fatal(err)
	}
	return pool
}

func named(name, word string) []File {
	return []File{{Path: ManifestPath, Content: `{"name":"` + name + `"}`},
		{Path: "skills/s/SKILL.md", Content: "---\nname: s\ndescription: d\n---\n" + word}}
}

func TestLibraryVersionsAndPrivacy(t *testing.T) {
	ctx := context.Background()
	pool := newPool(t)
	v1, err := Upload(ctx, pool, Owner{"u1"}, "u1", named("mine", "ONE"))
	if err != nil || v1.Latest != 1 || v1.Builtin || !v1.Enabled {
		t.Fatalf("first upload %+v %v", v1, err)
	}
	v2, err := Upload(ctx, pool, Owner{"u1"}, "u1", named("mine", "TWO"))
	if err != nil || v2.ID != v1.ID || v2.Latest != 2 || len(v2.Versions) != 2 || v2.Versions[0].Version != 2 {
		t.Fatalf("re-upload must be a new version %+v %v", v2, err)
	}
	if other, _ := List(ctx, pool, "u2"); len(other) != 0 {
		t.Fatalf("another user's plugin leaked: %+v", other)
	}
	if _, err := SetEnabled(ctx, pool, Owner{"u2"}, v1.ID, false); err != ErrNotFound {
		t.Fatalf("other user toggle: %v", err)
	}
	if _, err := Delete(ctx, pool, Owner{"u2"}, v1.ID); err != ErrNotFound {
		t.Fatalf("other user delete: %v", err)
	}
	latest, _ := Bundles(ctx, pool, "u1", []string{"mine"}, 1<<20)
	pinned, _ := Bundles(ctx, pool, "u1", []string{"mine@1"}, 1<<20)
	if len(latest) != 1 || latest[0].Version != 2 || !strings.Contains(latest[0].Files[1].Content, "TWO") ||
		len(pinned) != 1 || pinned[0].Version != 1 || !strings.Contains(pinned[0].Files[1].Content, "ONE") {
		t.Fatalf("latest %+v pinned %+v", latest, pinned)
	}
	if b, _ := Bundles(ctx, pool, "u2", []string{"mine"}, 1<<20); len(b) != 0 {
		t.Fatal("another user's plugin must not load")
	}
	if b, _ := Bundles(ctx, pool, "u1", []string{"mine"}, 10); len(b) != 0 {
		t.Fatal("over budget plugins are skipped")
	}
	if _, err := SetEnabled(ctx, pool, Owner{"u1"}, v1.ID, false); err != nil {
		t.Fatal(err)
	}
	if b, _ := Bundles(ctx, pool, "u1", []string{"mine"}, 1<<20); len(b) != 0 {
		t.Fatal("disabled plugins are not loaded")
	}
	choices, _ := Choices(ctx, pool, "u1")
	if !choices["mine"][1] || !choices["mine"][2] {
		t.Fatalf("disabled plugins stay tickable: %v", choices)
	}
}

func TestBuiltinPlugins(t *testing.T) {
	ctx := context.Background()
	pool := newPool(t)
	b, err := Upload(ctx, pool, Builtins, "ad", named("brand", "BUILTIN"))
	if err != nil || !b.Builtin {
		t.Fatalf("builtin %+v %v", b, err)
	}
	if _, err := Upload(ctx, pool, Owner{"u1"}, "u1", named("brand", "MINE")); !IsProblem(err) {
		t.Fatalf("a user upload must not take a built-in name: %v", err)
	}
	// u2 had its own "brand" before the admin published one: theirs wins for them.
	if _, err := pool.Exec(ctx, `INSERT INTO at_plugins (id, user_id, name, latest) VALUES ('pl_own','u2','brand',1);
		INSERT INTO at_plugin_versions (plugin_id, version, files, contents, size, sha256)
		VALUES ('pl_own', 1, '[{"path":"x","content":"OWN"}]', '{"skills":[]}', 3, 'h')`); err != nil {
		t.Fatal(err)
	}
	l1, _ := List(ctx, pool, "u1")
	l2, _ := List(ctx, pool, "u2")
	if len(l1) != 1 || !l1[0].Builtin || len(l2) != 1 || l2[0].ID != "pl_own" {
		t.Fatalf("lists %+v / %+v", l1, l2)
	}
	b1, _ := Bundles(ctx, pool, "u1", []string{"brand"}, 1<<20)
	b2, _ := Bundles(ctx, pool, "u2", []string{"brand"}, 1<<20)
	if len(b1) != 1 || !strings.Contains(b1[0].Files[1].Content, "BUILTIN") || len(b2) != 1 || b2[0].Files[0].Content != "OWN" {
		t.Fatalf("bundles %+v / %+v", b1, b2)
	}
	if _, err := SetEnabled(ctx, pool, Builtins, b.ID, false); err != nil {
		t.Fatal(err)
	}
	if l, _ := List(ctx, pool, "u1"); len(l) != 0 {
		t.Fatal("disabled built-ins are hidden from users")
	}
	if all, _ := ListBuiltin(ctx, pool); len(all) != 1 || all[0].Enabled {
		t.Fatalf("admin list %+v", all)
	}
	if _, err := SetEnabled(ctx, pool, Builtins, b.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO at_roles (id, user_id, tier, role, plugins) VALUES
		('ro1','u1','member','dev','{brand@1,other}'), ('ro2','u2','member','dev','{brand}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := Delete(ctx, pool, Builtins, b.ID); err != nil {
		t.Fatal(err)
	}
	var r1, r2 []string
	_ = pool.QueryRow(ctx, `SELECT plugins FROM at_roles WHERE id = 'ro1'`).Scan(&r1)
	_ = pool.QueryRow(ctx, `SELECT plugins FROM at_roles WHERE id = 'ro2'`).Scan(&r2)
	if fmt.Sprint(r1) != "[other]" || fmt.Sprint(r2) != "[brand]" {
		t.Fatalf("roles after delete: %v %v", r1, r2)
	}
}

func TestPruneKeepsPinned(t *testing.T) {
	ctx := context.Background()
	pool := newPool(t)
	if _, err := pool.Exec(ctx, `INSERT INTO at_roles (id, user_id, tier, role, plugins) VALUES ('ro1','u1','member','dev','{mine@1}')`); err != nil {
		t.Fatal(err)
	}
	var p Plugin
	var err error
	for i := 0; i < KeepVersions+2; i++ {
		if p, err = Upload(ctx, pool, Owner{"u1"}, "u1", named("mine", fmt.Sprint("V", i))); err != nil {
			t.Fatal(err)
		}
	}
	got := []int{}
	for _, v := range p.Versions {
		got = append(got, v.Version)
	}
	if len(got) != KeepVersions+1 || got[len(got)-1] != 1 || got[len(got)-2] != 3 {
		t.Fatalf("versions after prune %v", got)
	}
}
