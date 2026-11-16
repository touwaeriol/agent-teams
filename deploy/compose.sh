#!/usr/bin/env bash
# docker compose for v2 with the repository root as project directory.
#   deploy/compose.sh up -d                       # uses <repo>/.env
#   ENV_FILE=deploy/.env.e2e deploy/compose.sh -f deploy/docker-compose.e2e.yml up -d
set -euo pipefail
repo="$(cd "$(dirname "$0")/.." && pwd)"
env_file="${ENV_FILE:-$repo/.env}"
case "$env_file" in /*) ;; *) env_file="$repo/$env_file" ;; esac
[ -f "$env_file" ] || { echo "missing env file $env_file (see deploy/env.example)" >&2; exit 1; }
exec docker compose --project-directory "$repo" --env-file "$env_file" -f "$repo/deploy/docker-compose.yml" "$@"
