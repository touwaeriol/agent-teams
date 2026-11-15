package approvals

import (
	"encoding/json"
	"regexp"
	"strings"
)

// The fixed deny list (architecture.md §9 ⓪, §10.3): applied before any other
// policy, also when a thread allows everything. The daemon's PreToolUse hook
// blocks the same patterns first.
var (
	secretPaths = []string{".config/agent-teams", "node.env", ".credentials.json", "/codex/auth.json", ".codex/auth.json",
		".local/state/agent-teams", "agent-teams/cred.sock", "agent-teams/node.sock"}
	procEnviron  = regexp.MustCompile(`/proc/[^/\s]+/environ`)
	pipeToShell  = regexp.MustCompile(`\b(curl|wget)\b[^|;&]*\|\s*(sudo\s+)?(ba|z|da)?sh\b`)
	gitConfigBad = regexp.MustCompile(`\bgit\b.*\bconfig\b.*\b(core\.hookspath|credential\.)`)
)

// DenyListed returns why a tool call is always refused ("" = not listed).
func DenyListed(tool string, input json.RawMessage) string {
	text := strings.ToLower(flatten(input))
	for _, p := range secretPaths {
		if strings.Contains(text, p) {
			return "涉及节点上的秘密文件（" + p + "）"
		}
	}
	switch {
	case procEnviron.MatchString(text):
		return "读取其他进程的环境变量"
	case isShell(tool) && pipeToShell.MatchString(text):
		return "把下载内容直接交给 shell 执行"
	case isShell(tool) && gitConfigBad.MatchString(text):
		return "修改 git 钩子目录或凭证配置"
	}
	return ""
}

func isShell(tool string) bool {
	return tool == "Bash" || tool == "commandExecution" || tool == "shell" || strings.HasPrefix(tool, "codex")
}

// flatten joins every string value of the input (command, file_path, path, …).
func flatten(input json.RawMessage) string {
	var v any
	if json.Unmarshal(input, &v) != nil {
		return string(input)
	}
	var b strings.Builder
	walk(v, &b)
	return b.String()
}

func walk(v any, b *strings.Builder) {
	switch x := v.(type) {
	case string:
		b.WriteString(x)
		b.WriteByte('\n')
	case []any:
		for _, item := range x {
			walk(item, b)
		}
	case map[string]any:
		for _, item := range x {
			walk(item, b)
		}
	}
}
