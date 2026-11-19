#!/usr/bin/env bash
# Runs the Go tests in docker (no local Go needed) against throwaway
# PostgreSQL 17 and Valkey 9 containers, then removes them.
#   server/scripts/test.sh                 # go test ./...
#   server/scripts/test.sh -run TestRoles  # extra go test args
# GO_MODCACHE names the module cache volume (it must be writable by the caller:
# `make go-modcache`); CI uses its own.
set -euo pipefail
repo="$(cd "$(dirname "$0")/../.." && pwd)"
id="$$-$RANDOM"
net="at-server-test-$id"
cleanup() {
  docker rm -f "at-pg-$id" "at-vk-$id" > /dev/null 2>&1 || true
  docker network rm "$net" > /dev/null 2>&1 || true
}
trap cleanup EXIT
docker network create "$net" > /dev/null
docker run -d --rm --name "at-pg-$id" --network "$net" -e POSTGRES_PASSWORD=pw postgres:17-alpine > /dev/null
docker run -d --rm --name "at-vk-$id" --network "$net" valkey/valkey:9 > /dev/null
for _ in $(seq 60); do docker exec "at-pg-$id" pg_isready -U postgres > /dev/null 2>&1 && break; sleep 1; done
sleep 1
docker run --rm --network "$net" --user "$(id -u):$(id -g)" \
  -e HOME=/tmp -e GOPATH=/tmp/gopath -e GOCACHE=/go/pkg/mod/.gocache -e GOMODCACHE=/go/pkg/mod -e GOFLAGS=-mod=mod \
  -e "TEST_DATABASE_URL=postgres://postgres:pw@at-pg-$id:5432/postgres?sslmode=disable" \
  -e "TEST_VALKEY_URL=redis://at-vk-$id:6379/0" \
  -v "$repo:/src" -v "${GO_MODCACHE:-agent-teams-gomod}:/go/pkg/mod" -w /src/server golang:1.26 \
  sh -c 'test -z "$(gofmt -l .)" || { gofmt -l .; echo "gofmt needed" >&2; exit 1; }; go vet ./... && go test -count=1 "$@" ./...' sh "$@"
