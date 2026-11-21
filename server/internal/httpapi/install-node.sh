#!/usr/bin/env bash
# Installs or upgrades the agent-teams v2 node daemon (TypeScript, Node >= 22)
# as a systemd user service running as the current user, whose Claude Code /
# Codex logins it uses.
#
#   curl -fsSL __CONSOLE__/install-node.sh | AGENT_TEAMS_NODE_TOKEN=<token> \
#     bash -s -- --console __CONSOLE__ --name build-1 [--root ~/.agents_team] [--max-sessions N]
#
# Re-running upgrades in place (safe on the running release; afterwards the console's
# "更新" button updates the node). An existing ~/.config/agent-teams/node.env
# supplies the token, console, name, root and max sessions, so upgrading needs
# no new token; other lines in it are kept. Sessions are unlimited unless --max-sessions caps them. --rollback switches back
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
    --max-sessions) max_sessions="$2"; shift 2 ;;
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
if [ -z "$max_sessions" ]; then
  max_sessions="$(existing AGENT_TEAMS_MAX_SESSIONS)"
  # 4 was the old default cap, written by every earlier install: drop it (no limit now).
  [ "$max_sessions" = 4 ] && max_sessions=""
fi

restart_service() {
  if systemctl --user show-environment > /dev/null 2>&1; then
    systemctl --user daemon-reload
    systemctl --user enable "$unit" > /dev/null 2>&1 || true
    systemctl --user restart "$unit"
  fi
}

do_rollback() {
  local previous
  previous="$(readlink "$data/previous" 2> /dev/null || true)"
  [ -n "$previous" ] && [ -d "$data/$previous" ] || { echo "没有可以回滚的上一个版本" >&2; exit 1; }
  local current
  current="$(readlink "$data/current" 2> /dev/null || true)"
  ln -sfn "$previous" "$data/current.tmp" && mv -Tf "$data/current.tmp" "$data/current"
  [ -n "$current" ] && ln -sfn "$current" "$data/previous.tmp" && mv -Tf "$data/previous.tmp" "$data/previous"
  rm -f "$data/upgrade.json"
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

# Releases live in releases/<version>+<build>/; current and previous are symlinks the launcher
# (bin/launch.mjs) follows. The running release is never deleted (architecture.md §14.3).
releases="$data/releases"
target="$releases/$release_version"
mkdir -p "$releases" "$data/bin"
current_dir=""
if cur_link="$(readlink "$data/current" 2> /dev/null)"; then
  case "$cur_link" in /*) current_dir="$cur_link" ;; *) current_dir="$data/$cur_link" ;; esac
fi
if [ "$(cat "$target/.installed" 2> /dev/null)" != "$release_sha256" ]; then
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' EXIT
  tgz="$tmp/agent-teams-node.tgz"
  curl -fsSL "$console/node/dist/agent-teams-node-$release_version.tgz" -o "$tgz"
  echo "$release_sha256  $tgz" | sha256sum -c --quiet - \
    || { echo "发布包校验失败（sha256 不符）" >&2; exit 1; }
  staging="$target.staging"
  rm -rf "$staging"
  mkdir -p "$staging"
  tar -xzf "$tgz" -C "$staging" --strip-components=1
  # Same lock file as the running release: hard-link its node_modules instead of downloading again.
  if [ -n "$current_dir" ] && [ -d "$current_dir/node_modules" ] && [ -f "$staging/npm-shrinkwrap.json" ] \
    && cmp -s "$current_dir/npm-shrinkwrap.json" "$staging/npm-shrinkwrap.json" \
    && cp -al "$current_dir/node_modules" "$staging/node_modules" 2> /dev/null; then
    :
  elif [ -f "$staging/npm-shrinkwrap.json" ] || [ -f "$staging/package-lock.json" ]; then
    rm -rf "$staging/node_modules"
    (cd "$staging" && npm ci --omit=dev --no-audit --no-fund --loglevel=error)
  else
    (cd "$staging" && npm install --omit=dev --no-audit --no-fund --loglevel=error)
  fi
  echo "$release_sha256" > "$staging/.installed"
  if [ -d "$target" ]; then
    mv "$target" "$target.old.$$"
    mv "$staging" "$target"
    rm -rf "$target.old.$$"
  else
    mv "$staging" "$target"
  fi
fi
if [ "${cur_link:-}" != "releases/$release_version" ]; then
  [ -n "${cur_link:-}" ] && ln -sfn "$cur_link" "$data/previous.tmp" && mv -Tf "$data/previous.tmp" "$data/previous"
  ln -sfn "releases/$release_version" "$data/current.tmp" && mv -Tf "$data/current.tmp" "$data/current"
fi
# An install is a confirmed release: the launcher must not count it as an unconfirmed update.
printf '{"state":"confirmed","from":"","to":"%s","boots":0,"switched_at":0,"reported":true}\n' "$release_version" > "$data/upgrade.json"

# Keep current and previous; older releases (and old node-<version> dirs) go.
keep_cur="$(basename "$(readlink "$data/current")")"
keep_prev="$(basename "$(readlink "$data/previous" 2> /dev/null || echo none)")"
for d in "$releases"/* "$data"/node-*; do
  [ -d "$d" ] || continue
  b="$(basename "$d")"
  case "$b" in *.staging) continue ;; esac
  [ "$b" = "$keep_cur" ] || [ "$b" = "$keep_prev" ] || rm -rf "$d"
done

# The launcher starts current/node.mjs, restarts it on exit 75 and rolls back a failed update.
launcher="$data/bin/launch.mjs"
if [ -f "$target/launch.mjs" ]; then
  cp "$target/launch.mjs" "$launcher.tmp" && chmod 755 "$launcher.tmp" && mv -f "$launcher.tmp" "$launcher"
fi

# systemd user services get a minimal PATH: remember where the CLIs live now.
tool_path="$(dirname "$node_bin"):/usr/local/bin:/usr/bin:/bin"
for tool in claude codex gh git; do
  if location="$(command -v "$tool")"; then
    tool_path="$(dirname "$location"):$tool_path"
  fi
done

mkdir -p "$config" "$root"
chmod 700 "$config"
# Lines of the old node.env this script does not manage (AGENT_TEAMS_PASS_ENV, proxies, …) stay.
extra=""
if [ -f "$env_file" ]; then
  extra="$(grep -vE '^(AGENT_TEAMS_CONSOLE|AGENT_TEAMS_NODE|AGENT_TEAMS_NODE_TOKEN|AGENT_TEAMS_ROOT|AGENT_TEAMS_MAX_SESSIONS|PATH)=' "$env_file" || true)"
fi
umask 077
cat > "$env_file.tmp" <<ENV
AGENT_TEAMS_CONSOLE=$console
AGENT_TEAMS_NODE=$name
AGENT_TEAMS_NODE_TOKEN=$token
AGENT_TEAMS_ROOT=$root
AGENT_TEAMS_MAX_SESSIONS=$max_sessions
PATH=$tool_path
ENV
[ -n "$extra" ] && printf '%s\n' "$extra" >> "$env_file.tmp"
mv -f "$env_file.tmp" "$env_file"
umask 022

# Releases with the launcher start through it; older ones run node.mjs directly.
if [ -f "$launcher" ] && [ -f "$target/launch.mjs" ]; then
  entry="$launcher"
else
  entry="$data/current/dist/node.mjs"
  [ -f "$target/dist/node.mjs" ] || entry="$data/current/node.mjs"
fi
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
TimeoutStopSec=120

[Install]
WantedBy=default.target
UNIT

restart_service
loginctl enable-linger "$USER" 2> /dev/null \
  || echo "提示：运行 sudo loginctl enable-linger $USER，让守护进程在退出登录后继续运行" >&2

echo "已安装并启动：节点 $name -> $console（守护进程 $release_version）"
echo "查看状态：systemctl --user status agent-teams-node；日志：journalctl --user -u agent-teams-node -f"
