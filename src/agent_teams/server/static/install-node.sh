#!/usr/bin/env bash
# Installs the agent-teams node daemon on a worker machine as a systemd user
# service, running as the current user (whose Claude Code / Codex login it uses).
#
#   AGENT_TEAMS_NODE_TOKEN=<token> bash install-node.sh \
#     --console http://console:3001 --name build-1 [--root ~/.agents_team] [--max-runs 4]
#
# Get the token on the console: agent-teams node-token team.yaml build-1
# Re-running upgrades the daemon and rewrites its settings.
set -euo pipefail

console="" name="" root="$HOME/.agents_team" max_runs=4
source_spec="agent-teams[node] @ git+https://github.com/touwaeriol/agent-teams.git@claude/vigilant-noether-11aqhk"

usage() {
  sed -n '2,9p' "$0" 2>/dev/null || true
  echo "usage: AGENT_TEAMS_NODE_TOKEN=... install-node.sh --console URL --name NAME [--root DIR] [--max-runs N] [--source PIP_SPEC]" >&2
  exit 2
}

while [ $# -gt 0 ]; do
  case "$1" in
    --console) console="$2"; shift 2 ;;
    --name) name="$2"; shift 2 ;;
    --root) root="$2"; shift 2 ;;
    --max-runs) max_runs="$2"; shift 2 ;;
    --source) source_spec="$2"; shift 2 ;;
    *) usage ;;
  esac
done
[ -n "$console" ] && [ -n "$name" ] && [ -n "${AGENT_TEAMS_NODE_TOKEN:-}" ] || usage

python3 -c 'import sys; sys.exit(sys.version_info < (3, 10))' \
  || { echo "需要 Python 3.10 或更高版本" >&2; exit 1; }

home="${XDG_DATA_HOME:-$HOME/.local/share}/agent-teams"
config="${XDG_CONFIG_HOME:-$HOME/.config}/agent-teams"
venv="$home/venv"

python3 -m venv "$venv"
"$venv/bin/pip" install --quiet --upgrade pip
"$venv/bin/pip" install --quiet --upgrade "$source_spec"

# systemd user services get a minimal PATH: remember where the CLIs live now.
tool_path="/usr/local/bin:/usr/bin:/bin"
for tool in claude codex; do
  if location="$(command -v "$tool")"; then
    tool_path="$(dirname "$location"):$tool_path"
  fi
done
command -v claude > /dev/null || echo "警告：没有找到 claude，请先安装 Claude Code 并登录" >&2

mkdir -p "$config" "$root"
umask 077
cat > "$config/node.env" <<EOF
AGENT_TEAMS_CONSOLE=$console
AGENT_TEAMS_NODE=$name
AGENT_TEAMS_NODE_TOKEN=$AGENT_TEAMS_NODE_TOKEN
AGENT_TEAMS_ROOT=$root
AGENT_TEAMS_MAX_RUNS=$max_runs
PATH=$tool_path
EOF
umask 022

if ! systemctl --user show-environment > /dev/null 2>&1; then
  echo "这台机器没有 systemd 用户服务。可以手动运行：" >&2
  echo "  set -a; . $config/node.env; set +a; nohup $venv/bin/agent-teams node > $home/node.log 2>&1 &" >&2
  exit 0
fi

unit_dir="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user"
mkdir -p "$unit_dir"
cat > "$unit_dir/agent-teams-node.service" <<EOF
[Unit]
Description=agent-teams node daemon ($name)
After=network-online.target

[Service]
EnvironmentFile=$config/node.env
ExecStart=$venv/bin/agent-teams node
Restart=always
RestartSec=5
KillMode=mixed

[Install]
WantedBy=default.target
EOF

systemctl --user daemon-reload
systemctl --user enable --now agent-teams-node.service
systemctl --user restart agent-teams-node.service
# Keep running after logout and start at boot.
loginctl enable-linger "$USER" 2> /dev/null \
  || echo "提示：运行 sudo loginctl enable-linger $USER，让守护进程在退出登录后继续运行" >&2

echo "已安装并启动：节点 $name -> $console"
echo "查看状态：systemctl --user status agent-teams-node；日志：journalctl --user -u agent-teams-node -f"
