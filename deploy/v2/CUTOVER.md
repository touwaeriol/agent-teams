# v2 production cut-over (one-shot) and rollback

Decision (architecture.md §14 note): stop v1, migrate the database in place, start the Go
control plane on the same port, then reinstall the node daemons. No proxy, no gradual
rollout. Nothing in this file has been run against production; it was rehearsed on rc with
`deploy/v2/e2e/` (v1 schema + rows written by the v1 Python code → `atctl migrate up` →
login, session, node token, sealed token, v1 team and conversations all accepted; then a
real coordinator → lead → member run).

## What production looks like today (ovh)

| Item | Value |
| --- | --- |
| Directory | `~/agent-teams` (owned by the deploy user) |
| Source | `~/agent-teams/src/` (a `git archive`, commit in `src/.deployed-sha`) — not a git checkout |
| Compose | `~/agent-teams/docker-compose.yml`, project `agent-teams`: `agent-teams` (127.0.0.1:3001), `agent-teams-pg` (postgres:17-alpine, `./data/postgres`), `agent-teams-valkey` (valkey 9.1.2, `./data/valkey`, requirepass + AOF) |
| Env | `~/agent-teams/.env` (0600): `PORT`, `POSTGRES_PASSWORD`, `VALKEY_PASSWORD`, `AGENT_TEAMS_SECRET_KEY`, `AGENT_TEAMS_PUBLIC_URL=https://agent.prophey.ai`, … |
| Proxy | `~/caddy/Caddyfile`: `agent.prophey.ai` → `127.0.0.1:3001`, `flush_interval -1`. **Unchanged.** |
| Nodes | `rc-erio` (user erio on rc) and `cvp` (user cvp on rc): systemd user unit `agent-teams-node`, v1 venv `~/.local/share/agent-teams/venv`, root `~/.agents_team`, `~/.config/agent-teams/node.env` |

`POSTGRES_PASSWORD`, `VALKEY_PASSWORD` and `AGENT_TEAMS_SECRET_KEY` are reused **unchanged**:
the data directories were initialised with the first two, and the third opens every sealed Git
token and the GitHub App secrets (a different key = all of them unreadable).

Below, `$V1` is the deployed v1 commit (`cat ~/agent-teams/src/.deployed-sha`) and `$V2` the
v2 commit being released. Times: tell users the window in Beijing time (UTC+8).

## 0. Rehearse on a copy of the production database (day before)

On ovh, with nothing stopped:

```bash
cd ~/agent-teams
mkdir -p backups
docker compose exec -T postgres pg_dump -U agent_teams -Fc agent_teams > backups/rehearsal-$(date -u +%Y%m%dT%H%M).dump
```

Copy the dump to rc (scp into the session scratch directory, never into the repository) and:

```bash
docker run -d --rm --name at-rehearsal-pg -e POSTGRES_PASSWORD=x -e POSTGRES_USER=agent_teams -e POSTGRES_DB=agent_teams postgres:17-alpine
docker exec -i at-rehearsal-pg pg_restore -U agent_teams -d agent_teams --no-owner < rehearsal.dump
docker run --rm --network container:at-rehearsal-pg -e DATABASE_URL='postgres://agent_teams:x@127.0.0.1:5432/agent_teams?sslmode=disable' \
  agent-teams-server:$V2 migrate up
docker run --rm --network container:at-rehearsal-pg -e DATABASE_URL='postgres://agent_teams:x@127.0.0.1:5432/agent_teams?sslmode=disable' \
  agent-teams-server:$V2 migrate status
docker rm -f at-rehearsal-pg
```

`migrate up` must finish without errors, `migrate status` must show every migration applied.
(The repository test `server/migrations` already proves the baseline is a no-op on the
production `pg_dump -s`.)

Also check the service and role names the v1 compose used (the commands here assume the
service is `postgres` and the role/database `agent_teams`): `grep -nE 'POSTGRES_(USER|DB)' ~/agent-teams/docker-compose.yml`.
If they are not `agent_teams`, add `POSTGRES_USER=` / `POSTGRES_DB=` with the real values to `.env`.

## 1. Prepare (no downtime)

On rc, from the repository at `$V2`:

```bash
git archive --format=tar.gz -o /tmp/claude-1000/agent-teams-$V2.tgz $V2
scp /tmp/claude-1000/agent-teams-$V2.tgz ovh:~/agent-teams/
```

On ovh:

```bash
cd ~/agent-teams
mkdir src.new && tar -xzf agent-teams-$V2.tgz -C src.new && echo $V2 > src.new/.deployed-sha
docker build -f src.new/server/Dockerfile -t agent-teams-server:$V2 src.new     # web + daemon tgz + atctl
docker tag agent-teams:latest agent-teams:rollback-$V1                          # v1 image kept for rollback
docker run --rm agent-teams-server:$V2 migrate version                          # prints the latest migration number
```

Prepare the new env lines (do not edit `.env` yet):

```bash
cat > env.v2-additions <<EOF
COMPOSE_PROJECT_NAME=agent-teams
BUILD_CONTEXT=./src
OFFICE_HOST_DIR=./src/office
AGENT_TEAMS_IMAGE=agent-teams-server:$V2
EOF
grep -E '^(PORT|BIND|AGENT_TEAMS_PUBLIC_URL)=' .env    # PORT must be the port Caddy proxies (3001); BIND empty or 127.0.0.1
```

On each node machine, check Node.js ≥ 22 and npm as the node's user (`node -v; npm -v`) for
both `erio` and `cvp`; install Node 22 first if missing (nvm or the distro package).

## 2. Cut over (downtime: a few minutes)

```bash
cd ~/agent-teams
# 2.1 nothing important running in v1?
docker compose exec -T postgres psql -U agent_teams -d agent_teams -Atc \
  "SELECT count(*) FROM at_tasks WHERE status IN ('pending','running')"
# 2.2 stop the v1 service (database and valkey keep running)
docker compose stop agent-teams
# 2.3 snapshot the database before migrating: this is the rollback point
docker compose exec -T postgres pg_dump -U agent_teams -Fc agent_teams > backups/pre-v2-$(date -u +%Y%m%dT%H%M).dump
ls -l backups/                                      # non-empty dump
# 2.4 swap compose, source and env
cp docker-compose.yml docker-compose.v1.yml
cp .env .env.v1
mv src src.v1-$V1 && mv src.new src
cp src/deploy/v2/docker-compose.yml docker-compose.yml
cat env.v2-additions >> .env
# 2.5 remove the v1 containers (bind-mounted data stays; never use -v)
docker compose -f docker-compose.v1.yml down
# 2.6 migrate and start
docker compose up -d --wait postgres valkey
docker compose run --rm migrate
docker compose up -d --wait control-plane
docker compose ps
```

`docker compose -f docker-compose.v1.yml down` must happen before the new `postgres` service
starts: both use `./data/postgres`, and two servers on one data directory corrupt it.

## 3. Verify the server

```bash
curl -fsS http://127.0.0.1:3001/healthz && curl -fsS http://127.0.0.1:3001/readyz
curl -fsS https://agent.prophey.ai/healthz
curl -fsS https://agent.prophey.ai/install-node.sh | sed -n 's/^release_version=//p'     # "2.0.0"
docker compose logs --tail 50 control-plane
```

In the browser (existing session cookie keeps working; otherwise log in as admin@prophey.ai
with the password in `~/agent-teams/admin-credentials`):

- the project list shows the existing project; Settings → GitHub lists the App connection and
  its repositories (proves `AGENT_TEAMS_SECRET_KEY` opens the sealed installation data);
- Settings → Team shows the role templates converted from the v1 team;
- the v1 conversations are listed and open read-only (`GET /api/conversations` lists them);
- Nodes shows `rc-erio` and `cvp` offline (expected until step 4; the v1 daemons speak
  protocol 1 and are refused with 4426).

## 4. Reinstall the node daemons (per node, as its user)

On rc, as `erio` (node `rc-erio`), then the same as `cvp` (node `cvp`):

```bash
cp ~/.config/agent-teams/node.env ~/.config/agent-teams/node.env.v1
cp ~/.config/systemd/user/agent-teams-node.service ~/.config/systemd/user/agent-teams-node.service.v1
systemctl --user stop agent-teams-node
curl -fsSL https://agent.prophey.ai/install-node.sh | bash -s -- --console https://agent.prophey.ai --name rc-erio   # --name cvp for cvp
systemctl --user status agent-teams-node --no-pager
journalctl --user -u agent-teams-node -n 30 --no-pager
```

The installer reuses the token, root and session limit from the existing `node.env` (same
sha256 token hash, no new token), downloads `/node/dist/agent-teams-node-2.0.0.tgz`, checks its
sha256, installs it under `~/.local/share/agent-teams/node-2.0.0/` with `current` pointing to
it, and rewrites the unit to `ExecStart=<node> …/current/node.mjs`. The v1 venv stays in place.
The Nodes page must show both nodes online with version 2.0.0 and Claude "已登录".

If linger is off (`loginctl show-user $USER -p Linger`), the daemon stops at logout:
`sudo loginctl enable-linger erio cvp` (needs the user's sudo).

## 5. Smoke test

Create a scratch project on a throwaway repository, ask the coordinator for a one-line change,
answer the approval card, and watch the thread reach the merge queue. Then delete the project
(it purges the worktrees on the nodes).

## Rollback

### Server

```bash
cd ~/agent-teams
docker compose stop control-plane
# Restore the pre-migration snapshot: v1 must not see v2 rows (its startup recovery would pick
# up v2 tasks, architecture.md §14.1.4). Data written while v2 ran is lost.
# Recreate the database instead of pg_restore --clean: tables only v2 created (and goose's
# version table) would otherwise survive and a later re-run would skip the migrations.
docker compose exec -T postgres psql -U agent_teams -d postgres -v ON_ERROR_STOP=1 \
  -c 'DROP DATABASE agent_teams WITH (FORCE)' -c 'CREATE DATABASE agent_teams OWNER agent_teams'
docker compose exec -T postgres pg_restore -U agent_teams -d agent_teams --no-owner < backups/pre-v2-<ts>.dump
docker compose down                                  # v2 containers; data stays
cp docker-compose.v1.yml docker-compose.yml
cp .env.v1 .env
mv src src.v2-$V2 && mv src.v1-$V1 src
docker compose up -d                                 # v1 image agent-teams:latest (= agent-teams:rollback-$V1)
curl -fsS http://127.0.0.1:3001/healthz
```

If `agent-teams:latest` was rebuilt meanwhile, set `image: agent-teams:rollback-$V1` in the v1
compose before `up`. If no v2 thread was ever created, `pg_restore` can be skipped: the v2
migrations only add tables and columns, which v1 ignores.

### Nodes

As each node user:

```bash
systemctl --user stop agent-teams-node
cp ~/.config/agent-teams/node.env.v1 ~/.config/agent-teams/node.env
cp ~/.config/systemd/user/agent-teams-node.service.v1 ~/.config/systemd/user/agent-teams-node.service
systemctl --user daemon-reload && systemctl --user start agent-teams-node
```

Between v2 releases, `install-node.sh --rollback` switches `current` back to the previous
`node-<version>` instead.

## Afterwards

- Keep `backups/pre-v2-*.dump`, `src.v1-$V1`, `docker-compose.v1.yml`, `.env.v1` and the
  `agent-teams:rollback-$V1` image for two weeks, then delete them.
- Later upgrades: new `src`, `docker build … -t agent-teams-server:<sha>`, update
  `AGENT_TEAMS_IMAGE` in `.env`, `docker compose run --rm migrate && docker compose up -d control-plane`;
  nodes upgrade from the Nodes page (`node.upgrade`) once a release signing key is configured,
  or by re-running `install-node.sh`.
