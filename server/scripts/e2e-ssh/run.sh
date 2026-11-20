#!/usr/bin/env bash
# Remote servers end to end (讨论串 #38, docs/v2/remote-servers-verification.md): a throwaway
# PostgreSQL, the real console (atctl serve), two real SSH targets and the real node daemon driven
# by node/test/e2e-ssh.test.ts (scripted Claude sessions, real ssh / scp / rsync).
#
#   server/scripts/e2e-ssh/run.sh            # from the repository root or anywhere
#
# Needs: go, node (with node/node_modules installed), PostgreSQL binaries (initdb, pg_ctl, postgres;
# PGBIN, default: on PATH or ~/.local/pg/bin), OpenSSH (ssh, scp, sshd, ssh-keygen, sftp-server)
# and rsync. Ports: PG 55499, console 3738, targets 2231 / 2232 (override with the variables below).
# The report is written to $WORK/e2e-report.txt. Test tooling only.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
WORK="${WORK:-$(mktemp -d /tmp/at-e2e-ssh.XXXXXX)}"
PGPORT="${PGPORT:-55499}"
PORT="${PORT:-3738}"
PGBIN="${PGBIN:-$(dirname "$(command -v pg_ctl 2>/dev/null || echo "$HOME/.local/pg/bin/pg_ctl")")}"
SSHD="${SSHD:-/usr/sbin/sshd}"
mkdir -p "$WORK"
echo "work directory: $WORK"

pids=()
cleanup() {
  for p in "${pids[@]}"; do kill "$p" 2>/dev/null || true; done
  [ -f "$WORK/sshd.pid" ] && kill "$(cat "$WORK/sshd.pid")" 2>/dev/null || true
  "$PGBIN/pg_ctl" -D "$WORK/pg" stop -m fast >/dev/null 2>&1 || true
}
trap cleanup EXIT

rand() { head -c 12 /dev/urandom | od -An -tx1 | tr -d ' \n'; }

# 1. PostgreSQL and the console.
"$PGBIN/initdb" -D "$WORK/pg" -U postgres -A trust >/dev/null
"$PGBIN/pg_ctl" -D "$WORK/pg" -o "-p $PGPORT -k $WORK" -l "$WORK/pg.log" start >/dev/null
export DATABASE_URL="postgres://postgres@127.0.0.1:$PGPORT/postgres?sslmode=disable"
(cd "$ROOT/server" && go build -o "$WORK/atctl" ./cmd/atctl && go build -o "$WORK/e2e-ssh" ./scripts/e2e-ssh)
for _ in $(seq 50); do "$WORK/atctl" migrate up >/dev/null 2>&1 && break; sleep 0.2; done
"$WORK/atctl" migrate version
# The targets are on 127.0.0.1, which the console refuses to dial unless allowed (sshdial).
"$WORK/e2e-ssh" allow -cidr 127.0.0.1/32
AGENT_TEAMS_SECRET_KEYS="$(rand)$(rand)" AGENT_TEAMS_PUBLIC_URL="http://127.0.0.1:$PORT" PORT="$PORT" BIND=127.0.0.1 \
  NODE_DIST_DIR="$WORK/node-dist" "$WORK/atctl" serve >"$WORK/console.log" 2>&1 &
pids+=($!)

# 2. Targets: a password server (Go, runs commands for real) and OpenSSH sshd with a key.
PW="e2e-pw-$(rand)"
KP="e2e-pp-$(rand)"
"$WORK/e2e-ssh" target -listen 127.0.0.1:2231 -password "$PW" -hostkey "$WORK/target_hostkey" >"$WORK/target.log" 2>&1 &
pids+=($!)
ssh-keygen -q -t ed25519 -N "" -f "$WORK/sshd_hostkey"
ssh-keygen -q -t ed25519 -N "$KP" -C e2e -f "$WORK/client_key"
cp "$WORK/client_key.pub" "$WORK/authorized_keys"
cat >"$WORK/sshd_config" <<EOF
Port 2232
ListenAddress 127.0.0.1
HostKey $WORK/sshd_hostkey
AuthorizedKeysFile $WORK/authorized_keys
PasswordAuthentication no
KbdInteractiveAuthentication no
UsePAM no
StrictModes no
PidFile $WORK/sshd.pid
Subsystem sftp internal-sftp
LogLevel VERBOSE
EOF
"$SSHD" -f "$WORK/sshd_config" -E "$WORK/sshd.log"

for _ in $(seq 100); do curl -fs "http://127.0.0.1:$PORT/healthz" >/dev/null && break; sleep 0.2; done
curl -fs "http://127.0.0.1:$PORT/healthz"
echo

# 3. The daemon side.
cd "$ROOT/node"
node scripts/protocol-deps.mjs >/dev/null
AT_E2E_URL="http://127.0.0.1:$PORT" AT_E2E_WORK="$WORK" AT_E2E_PW="$PW" AT_E2E_KEY_PASS="$KP" AT_E2E_USER="$(id -un)" \
  npx vitest run test/e2e-ssh.test.ts
echo "report: $WORK/e2e-report.txt"
