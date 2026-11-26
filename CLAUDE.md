# CLAUDE.md

agent-teams: a multi-user platform that runs teams of Claude Code / Codex employees on the users' own machines. Production (agent.prophey.ai) runs v2; the Python v1 platform is retired and deleted (history in `docs/threads.md`, `docs/agent-comms.md`, `docs/v2/architecture.md` §14).

## Commands

```bash
server/scripts/test.sh                       # Go: gofmt check, go vet, go test against throwaway PG 17 + Valkey 9 (all in docker)
server/scripts/test.sh -run TestRoles        # extra go test args
cd node && npm test && npm run typecheck     # daemon: vitest + tsc
cd web && npm test && npm run typecheck && npm run lint && npm run build
cd protocol && npm test && npm run check:examples && npm run typecheck
cd desktop && npm ci && npm run typecheck && npm test && npm run build   # desktop shell; npm run dist:dir to package
make gen-protocol                            # schemas → message.json, bundle, TS types, Go types (Go in docker)
docker run --rm -e GOFLAGS=-mod=mod -v "$PWD:/src" -v agent-teams-gomod:/go/pkg/mod -w /src/server golang:1.26 \
  go run ./cmd/atctl openapi > web/src/api/openapi.json   # after any API change (TestOpenAPIUpToDate)
cd web && npm run gen:api                    # openapi.json → src/api/schema.d.ts
docker build -f server/Dockerfile -t agent-teams-server .   # image: web + daemon tarball + atctl
docker build -f deploy/node/Dockerfile -t agent-teams-node .  # node image: daemon + Claude Code + Codex + git/gh (deploy/node/README.md)
deploy/compose.sh up -d                      # postgres, valkey → migrate → control-plane (env: <repo>/.env)
```

Real-Claude end-to-end runs: `deploy/e2e/README.md` (`up.sh <scratch>` then `run*.mjs`; check `df -h` first, tear down afterwards). There is no Go toolchain on the host: Go always runs in docker.

## Architecture

- `/server` (Go, one binary `atctl`: serve | migrate | healthcheck | openapi | admin): Gin + huma (humagin, OpenAPI 3.1) for REST/SSE, Casbin route authorization (`internal/httpapi/authz/policy.csv` lists every route explicitly; `casbin_rule` holds only the user→role `g` rows), GORM over pgx (`internal/store`, `internal/core`), goose SQL migrations in `server/migrations` are the only schema source (no AutoMigrate), coder/websocket node gateway (`internal/nodegw`), Valkey for fan-out, locks and rate limits. Engines in `internal/`: orchestrator (who has a session where, resume), scheduler (task pool by role), toolsvc (platform MCP tools for coordinator / lead / members), approvals, status (employee state machine), delivery (branches, merge queue, CI, PRs, acceptance), github (App + PAT tokens). They are wired into the HTTP server in `cmd/atctl/wire.go`.
- Domain: a project (one or more GitHub repos + context) has a coordinator conversation; the coordinator opens threads; each thread has its own lead, while members are project-level staff shared by all threads (headcount = the user's role templates, `at_roles`, YAML view `coordinator/lead/members`; `docs/v2/shared-members.md`); main tasks → small tasks in a project-level queue claimed by idle members of the role (stickiness, dependencies, rework reservation); one branch per thread and per repository, PRs, merge queue; deliverables (published files, signed URLs); an office view driven by JSON layouts (`/office`, `docs/v2/office-layout.md`).
- `/node` (TypeScript daemon, Node ≥ 22, Claude Agent SDK + `codex app-server`): long-lived employee sessions, canUseTool approvals, platform MCP tools forwarded as `tool.call`, local git (clone / worktree / cleanup), short-lived GitHub credentials on demand. Dials `/nodes/connect` with its token; protocol 2 only. Installed by `/install-node.sh` (tarball served by the control plane).
- `/protocol`: JSON Schemas are the single source for node messages, tool args and the office layout; Go and TS types are generated (`make gen-protocol`).
- `/web`: Vue 3 + Vite + TypeScript + Element Plus + vue-query + Pinia + vue-router, Tailwind with the shared design tokens (light by default, `.dark` opt-in via `lib/theme.ts`; shells in `components/shell`: `ProjectShell` and `SettingsShell`). Pages compose `src/components/common` (`Page`, `Panel`, `Field`, `FormDialog`, `ConfirmAction`, `DataTable`…) and Element Plus (themed in `styles/element.css`); API types come from `src/api/openapi.json`, writes go through `useAction` (api/mutations.ts). `server/Dockerfile` builds it into the image.
- `/desktop`: Mac / Windows client, an Electron shell (TypeScript, electron-builder, electron-updater) that loads https://agent.prophey.ai (`AGENT_TEAMS_URL` overrides) so the site stays same-origin; no web/ or server/ changes. Pure logic (navigation, deep links `agentteams://`, permissions, menu) has vitest tests; the release workflow is `desktop/ci/desktop-release.yml`, copied into `.github/workflows` by hand (`desktop/README.md`).
- `/deploy`: production compose (`docker-compose.yml` via `compose.sh`, project dir = repo root), `env.example`, `node/` (node container image + compose example, runs as uid 1000), `e2e/` (local stack + GitHub emulator `localgh.mjs` + real-Claude runs).
- Design docs: `docs/v2/architecture.md`, `data-model.md`, `api.md`, `protocol.md`, `office-layout.md`.

## Conventions

- Keep functions small and flat: early returns, dispatch tables, no deep nesting.
- Schema changes are new goose migrations (`000N0_name.sql`, next free number, with a Down when it can be reversed) plus a test in `server/migrations/migrations_test.go`; `TestLatest` pins the highest version.
- A new route needs its line in `policy.csv` (`TestEveryRouteHasExplicitPolicy`) and a regenerated `web/src/api/openapi.json` + `schema.d.ts` (`TestOpenAPIUpToDate`).
- Every user query is scoped by user id; tool failures go back to the model as `isError` results, never as exceptions.
- Add a test for every behavior change (Go tests run against real PostgreSQL/Valkey; node and web use vitest with fakes).
- Default model is `claude-opus-5-5`; don't hardcode other model IDs.
