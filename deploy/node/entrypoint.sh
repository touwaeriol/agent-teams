#!/bin/sh
# Entrypoint of the agent-teams node image. "run" (the default) starts the update launcher as
# PID 1's child (tini reaps): it seeds the image's release into the data volume, runs
# current/node.mjs, restarts it after a console update (exit 75) and rolls back a failed one.
# Anything else runs instead, e.g. `claude login` or `bash`.
set -eu

launcher="/opt/agent-teams-node/launch.mjs"

warn() { printf 'agent-teams-node: %s\n' "$*" >&2; }

preflight() {
  if [ "$(id -u)" = 0 ]; then
    warn "不能用 root 运行：Claude Code 的 bypassPermissions 模式拒绝 root。去掉 --user/user: 覆盖，使用镜像默认的 agent 用户（uid 1000）。"
    exit 1
  fi
  for d in "$HOME/.claude" "$HOME/.codex" "${AGENT_TEAMS_ROOT:-$HOME/.agents_team}" "${AGENT_TEAMS_STATE:-$HOME/.local/state/agent-teams}" "$HOME/.local/share/agent-teams"; do
    mkdir -p "$d" 2> /dev/null || true
    if [ ! -w "$d" ]; then
      warn "$d 不可写（uid $(id -u)）。绑定挂载的宿主目录需要 chown 1000:1000。"
      exit 1
    fi
  done
  [ -n "${AGENT_TEAMS_CONSOLE:-}" ] || { warn "缺少 AGENT_TEAMS_CONSOLE（控制面板地址，例如 https://agent.prophey.ai）"; exit 2; }
  [ -n "${AGENT_TEAMS_NODE_TOKEN:-}" ] || { warn "缺少 AGENT_TEAMS_NODE_TOKEN（在控制面板“节点”页添加节点时获取）"; exit 2; }
  if [ -z "${CLAUDE_CODE_OAUTH_TOKEN:-}${ANTHROPIC_API_KEY:-}${ANTHROPIC_AUTH_TOKEN:-}" ] \
    && [ ! -s "${CLAUDE_CONFIG_DIR:-$HOME/.claude}/.credentials.json" ]; then
    warn "Claude 尚未授权：设置 CLAUDE_CODE_OAUTH_TOKEN / ANTHROPIC_API_KEY，或执行 docker compose exec node claude login 后重启容器。"
  fi
}

if [ "$#" -eq 0 ] || [ "$1" = run ]; then
  [ "$#" -gt 0 ] && shift
  preflight
  exec node "$launcher" run "$@"
fi
exec "$@"
