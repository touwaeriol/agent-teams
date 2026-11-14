#!/usr/bin/env bash
# One-command deploy on the server: run from anywhere inside the repository.
#   deploy/deploy.sh
set -euo pipefail
cd "$(dirname "$0")/.."

port_in_use() { (exec 3<>"/dev/tcp/127.0.0.1/$1") 2>/dev/null; }

first_free_port() {
  for port in $(seq 3001 3009); do
    port_in_use "$port" || { echo "$port"; return; }
  done
  echo "no free port in 3001-3009" >&2
  return 1
}

env_value() { grep -E "^$1=" .env | tail -1 | cut -d= -f2-; }

set_env() {
  if grep -qE "^$1=" .env; then sed -i "s|^$1=.*|$1=$2|" .env; else echo "$1=$2" >> .env; fi
}

[ -f team.yaml ] || cp examples/company.yaml team.yaml
[ -f .env ] || { cp deploy/env.example .env; chmod 600 .env; }

[ -n "$(env_value PORT)" ] || set_env PORT "$(first_free_port)"
public_host="$(hostname -I 2>/dev/null | awk '{print $1}')"
[ -n "$(env_value AGENT_TEAMS_PUBLIC_URL)" ] || set_env AGENT_TEAMS_PUBLIC_URL "http://${public_host:-localhost}:$(env_value PORT)"
[ -n "$(env_value AGENT_TEAMS_TOKEN)" ] || set_env AGENT_TEAMS_TOKEN "$(head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n')"

missing=()
[ -n "$(env_value ANTHROPIC_API_KEY)" ] || missing+=(ANTHROPIC_API_KEY)
grep -q "USER:PASSWORD@" .env && missing+=(DATABASE_URL)
if [ ${#missing[@]} -gt 0 ]; then
  echo "请先在 $(pwd)/.env 里填好：${missing[*]}，然后重新运行 deploy/deploy.sh" >&2
  exit 1
fi

# SSH identity used to reach the nodes (claude_code employees).
if [ ! -f ssh/id_ed25519 ]; then
  mkdir -p ssh && chmod 700 ssh
  ssh-keygen -q -t ed25519 -N "" -C "agent-teams@$(hostname)" -f ssh/id_ed25519
fi

docker compose build
# The container runs as uid 1000; make the key readable by it without sudo.
docker compose run --rm --no-deps --user root --entrypoint chown agent-teams -R 1000:1000 /home/app/.ssh
docker compose up -d

port="$(env_value PORT)"
for _ in $(seq 1 30); do
  if curl -fsS "http://127.0.0.1:$port/healthz" > /dev/null 2>&1; then
    echo
    echo "部署完成：$(env_value AGENT_TEAMS_PUBLIC_URL)"
    echo "访问令牌：$(env_value AGENT_TEAMS_TOKEN)"
    echo
    echo "添加守护进程节点（team.yaml 的 nodes 里先写上 - name: <节点名>）："
    echo "  docker compose exec agent-teams agent-teams node-token /app/team.yaml <节点名>"
    echo "  它会打印令牌和在节点上执行的安装命令；节点在线状态见网页右侧"
    echo
    echo "SSH 节点和通过 SSH 安装守护进程用的公钥（加到节点的 ~/.ssh/authorized_keys）："
    cat ssh/id_ed25519.pub
    exit 0
  fi
  sleep 2
done
echo "服务没有起来，最近的日志：" >&2
docker compose logs --tail 50 agent-teams >&2
exit 1
