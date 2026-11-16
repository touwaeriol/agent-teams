#!/usr/bin/env bash
# Local stack for the end-to-end runs: generates deploy/.env.e2e (random secrets, free port),
# bare repositories for localgh (local/hello, and local/web + local/api with a develop branch
# for run_multi.mjs), then runs postgres + valkey + migrate + control-plane + localgh on an
# empty database.
#   deploy/e2e/up.sh <scratch-dir>
set -euo pipefail
repo="$(cd "$(dirname "$0")/../.." && pwd)"
dir="$(mkdir -p "$1" && cd "$1" && pwd)"
env_file="$repo/deploy/.env.e2e"
compose() { ENV_FILE="$env_file" "$repo/deploy/compose.sh" -f "$repo/deploy/docker-compose.e2e.yml" "$@"; }
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
E2E_GH_TOKENS=ghp_e2eToken
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

# develop_branch adds a develop branch one commit ahead of main (run_multi.mjs states it as the
# repository's base branch only in the project context).
develop_branch() {
  local bare="$dir/gitroot/$1.git" work="$dir/seed-develop"
  git --git-dir "$bare" rev-parse --verify --quiet refs/heads/develop > /dev/null && return
  git clone -q "$bare" "$work"
  git -C "$work" checkout -q -b develop
  printf 'Work for the next release happens on develop.\n' > "$work/DEVELOP.md"
  git -C "$work" add DEVELOP.md
  git -C "$work" -c user.name=e2e -c user.email=e2e@localhost commit -q -m "Start develop"
  git -C "$work" push -q origin develop
  rm -rf "$work"
}

env_value() { grep -E "^$1=" "$env_file" | cut -d= -f2-; }

write_env
mkdir -p "$dir/data" "$dir/gitroot"
bare_repo local/hello hello
bare_repo local/web web
bare_repo local/api api
develop_branch local/api
compose up -d --wait postgres valkey control-plane localgh
echo "up: $(env_value AGENT_TEAMS_PUBLIC_URL)  (env $env_file)"
