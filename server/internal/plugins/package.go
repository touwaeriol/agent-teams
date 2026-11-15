// Package plugins is the plugin library (architecture.md §6.5, data-model.md §2.9): Claude Code
// plugin packages (.claude-plugin/plugin.json plus skills/, commands/, agents/, hooks/,
// .mcp.json …) that a user uploads for their own roles or an admin publishes as platform
// built-ins. Each upload of a name is a new version; session.open carries the exact version
// a role uses to the daemon as a PluginBundle.
package plugins

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/touwaeriol/agent-teams/server/internal/skills"
)

// Limits of one plugin package (decompressed sizes).
const (
	MaxFiles      = 200
	MaxFileBytes  = 512 << 10
	MaxBytes      = 1 << 20
	MaxZipBytes   = 2 << 20
	MaxMCPServers = 8
	ManifestPath  = ".claude-plugin/plugin.json"
	// Reserved is the daemon's own plugin that carries a role's skills.
	Reserved = "agent-teams-skills"
)

var (
	namePattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	segmentPattern = regexp.MustCompile(`^[A-Za-z0-9_.@+-]+$`)
	itemPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
	serverPattern  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

// File is one file of a package. Binary files are stored base64-encoded.
type File struct {
	Path       string `json:"path"`
	Content    string `json:"content"`
	Encoding   string `json:"encoding,omitempty" enum:"utf8,base64"`
	Executable bool   `json:"executable,omitempty"`
}

// MCPServer is one MCP server the plugin declares (stdio, http or sse).
type MCPServer struct {
	Type    string            `json:"type"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

// Contents is what a package provides, read from its layout at upload.
type Contents struct {
	Skills     []string             `json:"skills"`
	Commands   []string             `json:"commands"`
	Agents     []string             `json:"agents"`
	Hooks      bool                 `json:"hooks"`
	MCPServers map[string]MCPServer `json:"mcp_servers"`
}

// Package is a validated upload.
type Package struct {
	Name            string
	ManifestVersion string
	Description     string
	Files           []File
	Contents        Contents
	Size            int
	SHA256          string
}

// Problem is a validation error shown to the user.
type Problem struct{ msg string }

func (p Problem) Error() string { return p.msg }

func problem(format string, a ...any) error { return Problem{fmt.Sprintf(format, a...)} }

// IsProblem reports whether err is a validation error.
func IsProblem(err error) bool {
	var p Problem
	return errors.As(err, &p)
}

// Parse validates a package: file limits, the manifest, and what the layout provides.
func Parse(files []File) (Package, error) {
	size, err := checkFiles(files)
	if err != nil {
		return Package{}, err
	}
	byPath := map[string]File{}
	for _, f := range files {
		byPath[f.Path] = f
	}
	pkg, man, err := readManifest(byPath)
	if err != nil {
		return Package{}, err
	}
	if pkg.Contents, err = readContents(files, byPath, man); err != nil {
		return Package{}, err
	}
	pkg.Files = slices.Clone(files)
	sort.Slice(pkg.Files, func(a, b int) bool { return pkg.Files[a].Path < pkg.Files[b].Path })
	pkg.Size, pkg.SHA256 = size, digest(pkg.Files)
	return pkg, nil
}

func checkFiles(files []File) (int, error) {
	if len(files) == 0 || len(files) > MaxFiles {
		return 0, problem("插件需要 1 到 %d 个文件", MaxFiles)
	}
	total := 0
	seen := map[string]bool{}
	for _, f := range files {
		n, err := checkFile(f)
		if err != nil {
			return 0, err
		}
		if seen[f.Path] {
			return 0, problem("文件重复：%s", f.Path)
		}
		seen[f.Path] = true
		if total += n; total > MaxBytes {
			return 0, problem("插件文件合计不能超过 %d KB", MaxBytes>>10)
		}
	}
	if p := dirConflict(seen); p != "" {
		return 0, problem("%s 既是文件又是目录", p)
	}
	return total, nil
}

// dirConflict returns a path that is both a file and a parent folder of another file ("a" and
// "a/b"); such a package cannot be written to disk, so the daemon would fail every session open.
func dirConflict(paths map[string]bool) string {
	for p := range paths {
		for i := 0; i < len(p); i++ {
			if p[i] == '/' && paths[p[:i]] {
				return p[:i]
			}
		}
	}
	return ""
}

// checkFile validates one file and returns its decoded size.
func checkFile(f File) (int, error) {
	if !ValidPath(f.Path) {
		return 0, problem("文件路径不合法：%q（只能是相对路径，不能含 . 或 .. 段）", f.Path)
	}
	n, err := decodedSize(f)
	if err != nil {
		return 0, err
	}
	if n > MaxFileBytes {
		return 0, problem("%s 超过 %d KB", f.Path, MaxFileBytes>>10)
	}
	return n, nil
}

// ValidPath accepts relative paths of plain segments; "." and ".." never navigate.
func ValidPath(p string) bool {
	if p == "" || len(p) > 300 {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "." || seg == ".." || !segmentPattern.MatchString(seg) {
			return false
		}
	}
	return true
}

func decodedSize(f File) (int, error) {
	switch f.Encoding {
	case "", "utf8":
		if !utf8.ValidString(f.Content) {
			return 0, problem("%s 不是 UTF-8 文本，二进制文件需要 base64", f.Path)
		}
		return len(f.Content), nil
	case "base64":
		raw, err := base64.StdEncoding.DecodeString(f.Content)
		if err != nil {
			return 0, problem("%s 的 base64 内容无效", f.Path)
		}
		return len(raw), nil
	}
	return 0, problem("%s 的编码只能是 utf8 或 base64", f.Path)
}

// textOf returns a file's text; binary files have none.
func textOf(f File) (string, bool) {
	if f.Encoding == "base64" {
		return "", false
	}
	return f.Content, true
}

func digest(files []File) string {
	h := sha256.New()
	for _, f := range files {
		fmt.Fprintf(h, "%s\x00%s\x00%s\x00%t\x00", f.Path, f.Encoding, f.Content, f.Executable)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// manifest is the part of plugin.json the platform reads.
type manifest struct {
	raw map[string]json.RawMessage
}

func (m manifest) str(key string) (string, error) {
	v, ok := m.raw[key]
	if !ok {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		return "", problem("plugin.json 的 %s 必须是字符串", key)
	}
	return strings.TrimSpace(s), nil
}

// unsupported are manifest fields that move components out of the default folders.
var unsupported = []string{"commands", "agents", "skills", "outputStyles", "lspServers"}

func readManifest(byPath map[string]File) (Package, manifest, error) {
	f, ok := byPath[ManifestPath]
	text, isText := textOf(f)
	if !ok || !isText {
		return Package{}, manifest{}, problem("插件根目录需要 %s", ManifestPath)
	}
	m := manifest{}
	if err := json.Unmarshal([]byte(text), &m.raw); err != nil || m.raw == nil {
		return Package{}, m, problem("plugin.json 不是有效的 JSON 对象")
	}
	for _, key := range unsupported {
		if _, ok := m.raw[key]; ok {
			return Package{}, m, problem("暂不支持 plugin.json 里的 %s 字段：请把文件放在默认目录（skills/、commands/、agents/）", key)
		}
	}
	return manifestFields(m)
}

func manifestFields(m manifest) (Package, manifest, error) {
	name, err := m.str("name")
	if err != nil {
		return Package{}, m, err
	}
	if !namePattern.MatchString(name) {
		return Package{}, m, problem("plugin.json 需要 name（小写字母、数字和 -，最多 64 个字符），现在是 %q", name)
	}
	if name == Reserved {
		return Package{}, m, problem("%s 是平台保留的名字", Reserved)
	}
	version, err := m.str("version")
	if err != nil || len(version) > 64 {
		return Package{}, m, problem("plugin.json 的 version 必须是不超过 64 个字符的字符串")
	}
	desc, err := m.str("description")
	if err != nil {
		return Package{}, m, err
	}
	return Package{Name: name, ManifestVersion: version, Description: truncate(desc, 500)}, m, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func readContents(files []File, byPath map[string]File, m manifest) (Contents, error) {
	c := Contents{Skills: []string{}, Commands: []string{}, Agents: []string{}, MCPServers: map[string]MCPServer{}}
	for _, f := range files {
		if err := classify(&c, f); err != nil {
			return c, err
		}
	}
	var err error
	if c.Hooks, err = hasHooks(byPath, m); err != nil {
		return c, err
	}
	if err := collectMCP(&c, byPath, m); err != nil {
		return c, err
	}
	sort.Strings(c.Skills)
	sort.Strings(c.Commands)
	sort.Strings(c.Agents)
	if len(c.Skills)+len(c.Commands)+len(c.Agents)+len(c.MCPServers) == 0 && !c.Hooks {
		return c, problem("插件里没有可用内容：需要 skills/、commands/、agents/、hooks 或 MCP 服务器中的至少一项")
	}
	return c, nil
}

// classify records a skill, command or agent file.
func classify(c *Contents, f File) error {
	parts := strings.Split(f.Path, "/")
	switch {
	case parts[0] == "skills" && len(parts) == 3 && parts[2] == skills.EntryFile:
		return addSkill(c, parts[1], f)
	case parts[0] == "commands" && strings.HasSuffix(f.Path, ".md"):
		return addCommand(c, parts)
	case parts[0] == "agents" && strings.HasSuffix(f.Path, ".md"):
		return addAgent(c, parts, f)
	}
	return nil
}

func addSkill(c *Contents, dir string, f File) error {
	if !itemPattern.MatchString(dir) {
		return problem("技能目录名不合法：skills/%s", dir)
	}
	text, _ := textOf(f)
	if name := skills.FrontMatter(text)["name"]; name != "" && name != dir {
		return problem("skills/%s/SKILL.md 的 name 是 %q，需要和目录名一致", dir, name)
	}
	c.Skills = append(c.Skills, dir)
	return nil
}

func addCommand(c *Contents, parts []string) error {
	if len(parts) != 2 {
		return problem("命令文件只能直接放在 commands/ 下：%s", strings.Join(parts, "/"))
	}
	name := strings.TrimSuffix(parts[1], ".md")
	if !itemPattern.MatchString(name) {
		return problem("命令文件名不合法：commands/%s", parts[1])
	}
	c.Commands = append(c.Commands, name)
	return nil
}

func addAgent(c *Contents, parts []string, f File) error {
	if len(parts) != 2 {
		return problem("子代理文件只能直接放在 agents/ 下：%s", strings.Join(parts, "/"))
	}
	text, _ := textOf(f)
	name := skills.FrontMatter(text)["name"]
	if name == "" {
		name = strings.TrimSuffix(parts[1], ".md")
	}
	if !itemPattern.MatchString(name) {
		return problem("子代理名字不合法：%s", name)
	}
	c.Agents = append(c.Agents, name)
	return nil
}

// hasHooks: hooks/hooks.json, or a manifest "hooks" object or path to a file in the package.
func hasHooks(byPath map[string]File, m manifest) (bool, error) {
	_, file := byPath["hooks/hooks.json"]
	raw, ok := m.raw["hooks"]
	if !ok {
		return file, nil
	}
	var p string
	if json.Unmarshal(raw, &p) == nil {
		if _, ok := byPath[cleanRef(p)]; !ok {
			return false, problem("plugin.json 的 hooks 指向的文件不存在：%s", p)
		}
		return true, nil
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return false, problem("plugin.json 的 hooks 需要是对象或包内文件路径")
	}
	return true, nil
}

func cleanRef(p string) string {
	p = strings.TrimPrefix(strings.TrimSpace(p), "${CLAUDE_PLUGIN_ROOT}/")
	return path.Clean(strings.TrimPrefix(p, "./"))
}

// collectMCP reads .mcp.json and the manifest's mcpServers (object or package file).
func collectMCP(c *Contents, byPath map[string]File, m manifest) error {
	sources := [][]byte{}
	if f, ok := byPath[".mcp.json"]; ok {
		text, _ := textOf(f)
		sources = append(sources, []byte(text))
	}
	if raw, ok := m.raw["mcpServers"]; ok {
		src, err := manifestMCP(raw, byPath)
		if err != nil {
			return err
		}
		sources = append(sources, src)
	}
	for _, src := range sources {
		if err := addServers(c, src); err != nil {
			return err
		}
	}
	return nil
}

func manifestMCP(raw json.RawMessage, byPath map[string]File) ([]byte, error) {
	var p string
	if json.Unmarshal(raw, &p) != nil {
		return raw, nil
	}
	f, ok := byPath[cleanRef(p)]
	if !ok {
		return nil, problem("plugin.json 的 mcpServers 指向的文件不存在：%s", p)
	}
	text, _ := textOf(f)
	return []byte(text), nil
}

// addServers parses {"mcpServers": {...}} or a bare map of servers.
func addServers(c *Contents, src []byte) error {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(src, &doc); err != nil {
		return problem("MCP 配置不是有效的 JSON 对象")
	}
	if inner, ok := doc["mcpServers"]; ok {
		doc = nil
		if err := json.Unmarshal(inner, &doc); err != nil {
			return problem("mcpServers 需要是对象")
		}
	}
	for name, raw := range doc {
		if err := addServer(c, name, raw); err != nil {
			return err
		}
	}
	return nil
}

func addServer(c *Contents, name string, raw json.RawMessage) error {
	if !serverPattern.MatchString(name) {
		return problem("MCP 服务器名不合法：%q", name)
	}
	if _, dup := c.MCPServers[name]; dup {
		return problem("MCP 服务器重复：%s", name)
	}
	if len(c.MCPServers) >= MaxMCPServers {
		return problem("一个插件最多 %d 个 MCP 服务器", MaxMCPServers)
	}
	var s MCPServer
	if err := json.Unmarshal(raw, &s); err != nil {
		return problem("MCP 服务器 %s 的配置无效", name)
	}
	s, err := normalizeServer(name, s)
	if err != nil {
		return err
	}
	c.MCPServers[name] = s
	return nil
}

func normalizeServer(name string, s MCPServer) (MCPServer, error) {
	if s.Type == "" && s.Command != "" {
		s.Type = "stdio"
	}
	if s.Type == "" && s.URL != "" {
		s.Type = "http"
	}
	switch s.Type {
	case "stdio":
		if strings.TrimSpace(s.Command) == "" {
			return s, problem("MCP 服务器 %s 需要 command", name)
		}
		s.URL, s.Headers = "", nil
	case "http", "sse":
		if !strings.HasPrefix(s.URL, "https://") && !strings.HasPrefix(s.URL, "http://") {
			return s, problem("MCP 服务器 %s 需要 http(s) 地址", name)
		}
		s.Command, s.Args, s.Env = "", nil, nil
	default:
		return s, problem("MCP 服务器 %s 的类型只能是 stdio、http 或 sse", name)
	}
	return s, nil
}

// Ref is a role's plugin entry: "name" follows the latest version, "name@3" pins version 3.
type Ref struct {
	Name    string
	Version int // 0 = latest
}

// ParseRef reads "name" or "name@N".
func ParseRef(s string) (Ref, bool) {
	name, ver, pinned := strings.Cut(strings.TrimSpace(s), "@")
	if !namePattern.MatchString(name) {
		return Ref{}, false
	}
	if !pinned {
		return Ref{Name: name}, true
	}
	n := 0
	if _, err := fmt.Sscanf(ver, "%d", &n); err != nil || n < 1 || fmt.Sprint(n) != ver {
		return Ref{}, false
	}
	return Ref{Name: name, Version: n}, true
}

func (r Ref) String() string {
	if r.Version == 0 {
		return r.Name
	}
	return fmt.Sprintf("%s@%d", r.Name, r.Version)
}
