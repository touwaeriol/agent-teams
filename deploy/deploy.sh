#!/usr/bin/env bash
# One-command deploy on the server: run from anywhere inside the repository.
#   deploy/deploy.sh                          # http://<server IP>:<300x port>
#   deploy/deploy.sh --domain agent.example   # https://agent.example via the host's Caddy
set -euo pipefail
cd "$(dirname "$0")/.."

domain=""
if [ "${1:-}" = "--domain" ]; then
  domain="${2:?usage: deploy.sh [--domain DOMAIN]}"
fi

port_in_use() { (exec 3<>"/dev/tcp/127.0.0.1/$1") 2>/dev/null; }

first_free_port() {
  for port in $(seq 3001 3009); do
    port_in_use "$port" || { echo "$port"; return; }
  done
  echo "no free port in 3001-3009" >&2
  return 1
}

as_root() { if [ "$(id -u)" = 0 ]; then "$@"; else sudo "$@"; fi; }

env_value() { grep -E "^$1=" .env | tail -1 | cut -d= -f2-; }

set_env() {
  if grep -qE "^$1=" .env; then sed -i "s|^$1=.*|$1=$2|" .env; else echo "$1=$2" >> .env; fi
}

[ -f .env ] || { cp deploy/env.example .env; chmod 600 .env; }

[ -n "$(env_value PORT)" ] || set_env PORT "$(first_free_port)"
if [ -n "$domain" ]; then
  # Behind Caddy: only listen locally, and tell nodes and leaders the HTTPS address.
  set_env HOST 127.0.0.1
  set_env AGENT_TEAMS_PUBLIC_URL "https://$domain"
fi
public_host="$(hostname -I 2>/dev/null | awk '{print $1}')"
[ -n "$(env_value AGENT_TEAMS_PUBLIC_URL)" ] \
  || set_env AGENT_TEAMS_PUBLIC_URL "http://${public_host:-localhost}:$(env_value PORT)"

if grep -q "USER:PASSWORD@" .env; then
  echo "请先在 $(pwd)/.env 里填好 DATABASE_URL（本机 PostgreSQL），然后重新运行 deploy/deploy.sh" >&2
  exit 1
fi

docker compose up -d --build

port="$(env_value PORT)"
for _ in $(seq 1 30); do
  if curl -fsS "http://127.0.0.1:$port/healthz" > /dev/null 2>&1; then
    [ -z "$domain" ] || as_root deploy/caddy.sh "$domain" "$port"
    echo
    echo "部署完成：$(env_value AGENT_TEAMS_PUBLIC_URL)"
    echo "打开网页注册账号：第一个注册的账号自动成为管理员。"
    exit 0
  fi
  sleep 2
done
echo "服务没有起来，最近的日志：" >&2
docker compose logs --tail 50 agent-teams >&2
exit 1
