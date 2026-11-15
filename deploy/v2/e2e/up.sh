#!/usr/bin/env bash
# Local v2 stack on a v1 database: generates deploy/v2/.env.e2e (random secrets, free port),
# bare repositories for localgh, loads the production v1 schema, seeds it with the real v1
# Python code, then runs migrate + control-plane + localgh.
#   deploy/v2/e2e/up.sh <scratch-dir> <v1-schema.sql>
set -euo pipefail
repo="$(cd "$(dirname "$0")/../../.." && pwd)"
dir="$(mkdir -p "$1" && cd "$1" && pwd)"
schema="$2"
env_file="$repo/deploy/v2/.env.e2e"
compose() { ENV_FILE="$env_file" "$repo/deploy/v2/compose.sh" -f "$repo/deploy/v2/docker-compose.e2e.yml" "$@"; }
port_free() { ! (exec 3<>"/dev/tcp/127.0.0.1/$1") 2>/dev/null; }
pick_port() { for p in $(seq 3470 3499); do port_free "$p" && { echo "$p"; return; }; done; exit 1; }

write_env() {
  [ -f "$env_file" ] && return
  local port; port="$(pick_port)"
  umask 077
  cat > "$env_file" <<ENV
COMPOSE_PROJECT_NAME=at-v2e2e
PORT=$port
BIND=127.0.0.1
AGENT_TEAMS_PUBLIC_URL=http://127.0.0.1:$port
POSTGRES_PASSWORD=$(openssl rand -hex 24)
VALKEY_PASSWORD=$(openssl rand -hex 24)
AGENT_TEAMS_SECRET_KEY=$(openssl rand -hex 24)
AGENT_TEAMS_IMAGE=agent-teams-server:e2e
DATA_DIR=$dir/data
E2E_GIT_ROOT=$dir/gitroot
E2E_UID=$(id -u)
E2E_GID=$(id -g)
E2E_GH_TOKENS=ghp_v1SealedToken0123456789abcdef,ghp_e2eToken
LOG_LEVEL=debug
ENV
}

bare_repo() {
  local bare="$dir/gitroot/$1.git" work="$dir/seed-$2"
  [ -d "$bare" ] && return
  git init -q --bare -b main "$bare"
  git init -q -b main "$work"
  printf '# %s\n\nA tiny repository for the agent-teams v2 end-to-end test.\n' "$2" > "$work/README.md"
  git -C "$work" add README.md
  git -C "$work" -c user.name=e2e -c user.email=e2e@localhost commit -q -m "Initial commit"
  git -C "$work" push -q "$bare" main
  rm -rf "$work"
}

env_value() { grep -E "^$1=" "$env_file" | cut -d= -f2-; }

load_v1() {
  [ -f "$dir/v1.json" ] && return
  local net; net="$(env_value COMPOSE_PROJECT_NAME)_default"
  local url="postgresql://agent_teams:$(env_value POSTGRES_PASSWORD)@postgres:5432/agent_teams"
  compose exec -T postgres psql -q -v ON_ERROR_STOP=1 -U agent_teams -d agent_teams < "$schema"
  docker run --rm --network "$net" -v "$repo:/src:ro" -e "DATABASE_URL=$url" \
    -e "AGENT_TEAMS_SECRET_KEY=$(env_value AGENT_TEAMS_SECRET_KEY)" -e PYTHONPATH=/src/src python:3.12-slim \
    sh -c 'pip install -q --root-user-action=ignore anthropic pydantic pyyaml asyncpg cryptography httpx >&2 &&
      python /src/deploy/v2/e2e/v1_seed.py' > "$dir/v1.json"
}

write_env
mkdir -p "$dir/data" "$dir/gitroot"
bare_repo local/hello hello
bare_repo local/v1repo v1repo
compose up -d --wait postgres valkey
load_v1
compose up -d --wait control-plane localgh
echo "up: $(env_value AGENT_TEAMS_PUBLIC_URL)  (env $env_file, v1 rows $dir/v1.json)"
