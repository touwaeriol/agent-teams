package httpapi

import (
	"fmt"
	"strings"
)

// Enrollment is the one-time token and the ways to start the daemon with it: on the host
// (install-node.sh + systemd user service) or in the node container image (deploy/node).
type Enrollment struct {
	Token   string `json:"token" doc:"Shown once"`
	Install string `json:"install" doc:"Shell command for the node (install-node.sh on the host)"`
	Docker  string `json:"docker" doc:"docker run command that starts the node container with this token"`
	Compose string `json:"compose" doc:"compose.yml that starts the node container with this token (docker compose up -d)"`
}

func (s *Server) enrollment(name, token string) Enrollment {
	console := s.cfg.NodeConsole()
	cmd := fmt.Sprintf("curl -fsSL %s/install-node.sh | AGENT_TEAMS_NODE_TOKEN=%s bash -s -- --console %s --name %s",
		console, token, shellQuote(console), shellQuote(name))
	n := nodeContainer{name: name, host: strings.ReplaceAll(name, "_", "-"), slug: "agent-teams-node-" + strings.ToLower(name), console: console, token: token, image: s.cfg.NodeImage}
	return Enrollment{Token: token, Install: cmd, Docker: n.dockerRun(), Compose: n.compose()}
}

// nodeContainer renders the container snippets. Names derive from the node name (letters, digits,
// - and _; lowercased because compose project names must be), so several nodes fit on one host; the
// hostname swaps _ for - (not valid in host names).
type nodeContainer struct{ name, host, slug, console, token, image string }

// nodeVolumes are the image's persistent directories (deploy/node/compose.yml).
var nodeVolumes = []struct{ key, path, note string }{
	{"claude", "/home/agent/.claude", "Claude Code 登录和会话记录"},
	{"codex", "/home/agent/.codex", "Codex 登录和线程"},
	{"workspace", "/home/agent/.agents_team", "工作区：仓库、worktree"},
	{"state", "/home/agent/.local/state/agent-teams", "守护进程状态（会话恢复）"},
	{"data", "/home/agent/.local/share/agent-teams", "守护进程版本（网页一键更新）"},
}

// volume is the named volume of one of nodeVolumes, shared by docker run and compose so a node
// can switch between them and keep its logins and workspace.
func (n nodeContainer) volume(key string) string { return n.slug + "-" + key }

func (n nodeContainer) dockerRun() string {
	lines := []string{
		fmt.Sprintf("docker run -d --name %s --hostname %s --restart unless-stopped --stop-timeout 60", n.slug, n.host),
		"-e AGENT_TEAMS_CONSOLE=" + shellQuote(n.console),
		"-e AGENT_TEAMS_NODE_TOKEN=" + shellQuote(n.token),
	}
	for _, v := range nodeVolumes {
		lines = append(lines, fmt.Sprintf("-v %s:%s", n.volume(v.key), v.path))
	}
	lines = append(lines, "--log-opt max-size=20m --log-opt max-file=5", shellQuote(n.image))
	return strings.Join(lines, " \\\n  ")
}

func (n nodeContainer) compose() string {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }
	w("# agent-teams 节点 %s。保存为 compose.yml（含令牌，chmod 600），同目录执行 docker compose up -d", n.name)
	w("name: %s", n.slug)
	w("services:")
	w("  node:")
	w("    image: %s", n.image)
	w("    hostname: %q", n.host) // quoted: a name like 123 or true must stay a string
	w("    restart: unless-stopped")
	w("    stop_grace_period: 60s   # 留时间让守护进程暂停会话")
	w("    environment:")
	w("      AGENT_TEAMS_CONSOLE: %q", n.console)
	w("      AGENT_TEAMS_NODE_TOKEN: %q", n.token)
	w("      # AGENT_TEAMS_MAX_SESSIONS: \"4\"          # 同时运行的员工会话上限，默认不限")
	w("      # Claude 授权：填其中一个，或都不填、启动后执行 docker compose exec node claude login")
	w("      # CLAUDE_CODE_OAUTH_TOKEN: \"\"            # claude setup-token 得到（订阅账号）")
	w("      # ANTHROPIC_API_KEY: \"\"")
	w("      # OPENAI_API_KEY: \"\"                     # Codex，也可以 codex login")
	w("      # 需要代理时（控制面板连接、git、Claude/Codex 都会走它）：")
	w("      # HTTPS_PROXY: \"http://user:pass@proxy.example:8080\"")
	w("      # HTTP_PROXY: \"http://user:pass@proxy.example:8080\"")
	w("      # NO_PROXY: \"localhost,127.0.0.1\"")
	w("    volumes:")
	for _, v := range nodeVolumes {
		w("      - %s:%s   # %s", v.key, v.path, v.note)
	}
	w("      # - ~/.ssh:/home/agent/.ssh:ro   # git 走 ssh 时打开")
	w("    # 资源上限（可选）：")
	w("    # cpus: \"4\"")
	w("    # mem_limit: 8g")
	w("    logging:")
	w("      driver: json-file")
	w("      options: { max-size: \"20m\", max-file: \"5\" }")
	w("volumes:")
	for _, v := range nodeVolumes {
		w("  %s: { name: %s }", v.key, n.volume(v.key)) // the docker run volumes, not <project>_<key>
	}
	return b.String()
}
