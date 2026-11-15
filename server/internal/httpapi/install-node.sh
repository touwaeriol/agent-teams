#!/usr/bin/env bash
# Installs or upgrades the agent-teams v2 node daemon (TypeScript, Node >= 22)
# as a systemd user service running as the current user, whose Claude Code /
# Codex logins it uses.
#
#   curl -fsSL __CONSOLE__/install-node.sh | AGENT_TEAMS_NODE_TOKEN=<token> \
#     bash -s -- --console __CONSOLE__ --name build-1 [--root ~/.agents_team] [--max-sessions 4]
#
# Re-running upgrades in place. An existing ~/.config/agent-teams/node.env
# (also one written by the v1 Python daemon) supplies the token, console, name,
# root and max runs, so upgrading needs no new token. --rollback switches back
# to the previous version.
set -euo pipefail

release_version="__VERSION__"
release_sha256="__SHA256__"
console="" name="" root="" max_sessions="" rollback=0

usage() {
  echo "usage: AGENT_TEAMS_NODE_TOKEN=... install-node.sh --console URL --name NAME [--root DIR] [--max-sessions N] [--rollback]" >&2
  exit 2
}

while [ $# -gt 0 ]; do
  case "$1" in
    --console) console="$2"; shift 2 ;;
    --name) name="$2"; shift 2 ;;
    --root) root="$2"; shift 2 ;;
    --max-sessions|--max-runs) max_sessions="$2"; shift 2 ;;
    --rollback) rollback=1; shift ;;
    *) usage ;;
  esac
done

data="${XDG_DATA_HOME:-$HOME/.local/share}/agent-teams"
config="${XDG_CONFIG_HOME:-$HOME/.config}/agent-teams"
env_file="$config/node.env"
unit_dir="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user"
unit="agent-teams-node.service"

# Read KEY=value from the existing node.env without executing it.
existing() {
  [ -f "$env_file" ] || return 0
  sed -n "s/^$1=//p" "$env_file" | tail -n 1
}

token="${AGENT_TEAMS_NODE_TOKEN:-$(existing AGENT_TEAMS_NODE_TOKEN)}"
console="${console:-$(existing AGENT_TEAMS_CONSOLE)}"
console="${console:-__CONSOLE__}"
name="${name:-$(existing AGENT_TEAMS_NODE)}"
root="${root:-$(existing AGENT_TEAMS_ROOT)}"
root="${root:-$HOME/.agents_team}"
max_sessions="${max_sessions:-$(existing AGENT_TEAMS_MAX_SESSIONS)}"
max_sessions="${max_sessions:-$(existing AGENT_TEAMS_MAX_RUNS)}"
max_sessions="${max_sessions:-4}"

restart_service() {
  if systemctl --user show-environment > /dev/null 2>&1; then
    systemctl --user daemon-reload
    systemctl --user enable "$unit" > /dev/null 2>&1 || true
    systemctl --user restart "$unit"
  fi
}

do_rollback() {
  local current previous
  current="$(readlink "$data/current" 2> /dev/null || true)"
  previous="$(cd "$data" && ls -1dt node-* 2> /dev/null | grep -vx "$(basename "${current:-none}")" | head -n 1 || true)"
  [ -n "$previous" ] || { echo "没有可以回滚的上一个版本" >&2; exit 1; }
  ln -sfn "$previous" "$data/current.tmp" && mv -Tf "$data/current.tmp" "$data/current"
  restart_service
  echo "已回滚到 $previous"
  exit 0
}

[ "$rollback" = 1 ] && do_rollback

[ -n "$token" ] || { echo "缺少节点令牌：设置 AGENT_TEAMS_NODE_TOKEN（在控制台“节点”页获取）" >&2; usage; }
[ -n "$name" ] || usage
if [ -z "$release_version" ] || [ -z "$release_sha256" ]; then
  echo "控制台还没有守护进程发布包（/node/dist/），请联系管理员" >&2
  exit 1
fi

node_bin="$(command -v node || true)"
if [ -z "$node_bin" ]; then
  echo "需要 Node.js 22 或更高版本：用 nvm（nvm install 22）或发行版的 nodejs 包安装后重试" >&2
  exit 1
fi
node_major="$("$node_bin" -p 'process.versions.node.split(".")[0]')"
if [ "$node_major" -lt 22 ]; then
  echo "Node.js 版本太旧（$("$node_bin" --version)），需要 22 或更高版本" >&2
  exit 1
fi
command -v npm > /dev/null || { echo "没有找到 npm（随 Node.js 一起安装）" >&2; exit 1; }

target="$data/node-$release_version"
if [ "$(cat "$target/.installed" 2> /dev/null)" != "$release_sha256" ]; then
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' EXIT
  tgz="$tmp/agent-teams-node-$release_version.tgz"
  curl -fsSL "$console/node/dist/agent-teams-node-$release_version.tgz" -o "$tgz"
  echo "$release_sha256  $tgz" | sha256sum -c --quiet - \
    || { echo "发布包校验失败（sha256 不符）" >&2; exit 1; }
  rm -rf "$target"
  mkdir -p "$target"
  tar -xzf "$tgz" -C "$target" --strip-components=1
  if [ -f "$target/npm-shrinkwrap.json" ] || [ -f "$target/package-lock.json" ]; then
    (cd "$target" && npm ci --omit=dev --no-audit --no-fund --loglevel=error)
  else
    (cd "$target" && npm install --omit=dev --no-audit --no-fund --loglevel=error)
  fi
  echo "$release_sha256" > "$target/.installed"
fi
ln -sfn "node-$release_version" "$data/current.tmp" && mv -Tf "$data/current.tmp" "$data/current"

# Keep the current version and the one before it.
(cd "$data" && ls -1dt node-* 2> /dev/null | tail -n +3 | grep -vx "node-$release_version" | xargs -r rm -rf) || true

# systemd user services get a minimal PATH: remember where the CLIs live now.
tool_path="$(dirname "$node_bin"):/usr/local/bin:/usr/bin:/bin"
for tool in claude codex gh git; do
  if location="$(command -v "$tool")"; then
    tool_path="$(dirname "$location"):$tool_path"
  fi
done

mkdir -p "$config" "$root"
chmod 700 "$config"
umask 077
cat > "$env_file.tmp" <<ENV
AGENT_TEAMS_CONSOLE=$console
AGENT_TEAMS_NODE=$name
AGENT_TEAMS_NODE_TOKEN=$token
AGENT_TEAMS_ROOT=$root
AGENT_TEAMS_MAX_SESSIONS=$max_sessions
PATH=$tool_path
ENV
mv -f "$env_file.tmp" "$env_file"
umask 022

# The release puts the entry at dist/node.mjs (or node.mjs at the package root).
entry="$data/current/dist/node.mjs"
[ -f "$target/dist/node.mjs" ] || entry="$data/current/node.mjs"
if ! systemctl --user show-environment > /dev/null 2>&1; then
  echo "这台机器没有 systemd 用户服务。可以手动运行：" >&2
  echo "  set -a; . $env_file; set +a; nohup $node_bin $entry > $data/node.log 2>&1 &" >&2
  exit 0
fi

mkdir -p "$unit_dir"
cat > "$unit_dir/$unit" <<UNIT
[Unit]
Description=agent-teams node daemon ($name)
After=network-online.target

[Service]
EnvironmentFile=$env_file
ExecStart=$node_bin $entry run
Restart=always
RestartSec=5
KillMode=mixed

[Install]
WantedBy=default.target
UNIT

restart_service
loginctl enable-linger "$USER" 2> /dev/null \
  || echo "提示：运行 sudo loginctl enable-linger $USER，让守护进程在退出登录后继续运行" >&2

echo "已安装并启动：节点 $name -> $console（守护进程 $release_version）"
echo "查看状态：systemctl --user status agent-teams-node；日志：journalctl --user -u agent-teams-node -f"
