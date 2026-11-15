# agent-teams v2 control plane (`/server`)

Go module `github.com/touwaeriol/agent-teams/server`, one binary `atctl`.
Spec: `docs/v2/architecture.md` (§3.1, §4, §5.1, §10, §12, §14), `docs/v2/data-model.md`, `docs/v2/api.md`.
One-shot cut-over (architecture.md §14 note): there is no Python proxy, no `/internal/*`
endpoints and no protocol-1 support. v2 reads the v1 data in place.

This package is the foundation: accounts, admin, projects, Git connections and the GitHub
App, nodes, role templates, office layouts, the node installer and the web app. Orchestration
(node gateway, orchestrator, scheduler, tools, approvals, status/events/SSE, delivery) plugs
in through the extension points below.

## Layout

```
cmd/atctl/            serve | migrate | healthcheck | openapi | admin
  wire.go             THE place where orchestration plugs into the HTTP server
migrations/           goose SQL migrations (embedded), runner, v1 schema test fixture
internal/config/      env → Config, slog JSON logger
internal/auth/        scrypt (v1 format), sha256 tokens, Fernet SecretBox (v1 compatible,
                      key rotation), signed OAuth state (v1 compatible)
internal/store/       pgx v5 queries (plain SQL, every user query scoped by user_id)
internal/kv/          Valkey (valkey-go) wrapper + in-memory twin, fixed-window rate limiter
internal/github/      REST client, App JWT, App credentials (at_settings.github_app),
                      installation token cache (memory + sealed in Valkey, keyed by repos+permissions)
internal/office/      layout registry: OFFICE_DIR files (fsnotify, 300 ms debounce) +
                      at_office_layouts, extends merge, schema + structural validation
internal/roles/       role templates: validation, defaults, YAML view, v1 at_teams conversion
internal/platform/    extension interfaces (NodeRegistry, Events, Hooks) + no-op defaults
internal/httpapi/     chi + huma v2 routes, session/CSRF middleware, install-node.sh, SPA
internal/testdb/      fresh database per test from TEST_DATABASE_URL
```

## Commands

```
atctl migrate up|status|version      # DATABASE_URL; run before serve (compose "migrate" service)
atctl serve                          # refuses to start if the database is behind the binary
atctl healthcheck                    # GET http://127.0.0.1:$PORT/healthz, exit 1 unless 200
atctl openapi > web/src/api/openapi.json
atctl admin reset-password EMAIL [PASSWORD]   # no PASSWORD: random one, must be changed at login
atctl admin reseal                   # re-encrypt sealed PATs and App secrets with the first key
```

## Environment

| Variable | Meaning |
| --- | --- |
| `DATABASE_URL` | required (no in-memory mode) |
| `AGENT_TEAMS_PUBLIC_URL` (or `PUBLIC_URL`) | required; install commands, GitHub App URLs, Secure cookies on https |
| `AGENT_TEAMS_SECRET_KEY` / `AGENT_TEAMS_SECRET_KEYS=new,old` | Fernet key(s), same derivation as v1 |
| `VALKEY_URL` | optional; without it rate limits and the token cache stay in-process |
| `BIND` (0.0.0.0), `PORT` (3001) | listen address |
| `OFFICE_DIR` (or `OFFICE_LAYOUT_DIR`) | `*.json` layouts, `assets/`; a built-in default is always there |
| `WEB_DIR` | built web app (`index.html`, `assets/`), SPA fallback |
| `NODE_DIST_DIR` (/srv/node-dist) | daemon tarballs served at `/node/dist/` |
| `AGENT_TEAMS_ADMIN_EMAILS` | these emails register as admin (first user always does) |
| `GITHUB_WEBHOOK_SECRET` | optional override of the App's webhook secret |
| `LOG_LEVEL` | debug, info (default), warn, error |

## Tests

No local Go needed; everything runs in docker:

```
server/scripts/test.sh            # gofmt check, go vet, go test ./... with throwaway postgres:17 + valkey:9
server/scripts/test.sh -run TestGitHubAppFlow -v
```

Without `TEST_DATABASE_URL` / `TEST_VALKEY_URL` the integration tests skip and the unit tests
still run. Notable tests:

- `migrations`: up on an empty DB; up on the production v1 schema (`testdata/v1_schema.sql`,
  pg_dump -s) with v1 rows; the baseline changes nothing (column/index/constraint snapshot
  identical) and every v1 column survives the later migrations.
- `internal/auth`: `testdata/v1_vectors.json` was produced by the real v1 Python modules
  (`secrets.py`, `security.py`, `credentials.py`): Fernet ciphertexts, a scrypt hash, a token
  hash and a signed state all verify in Go; Go ciphertexts decrypt in Python.
- `internal/httpapi`: end-to-end over HTTP against PostgreSQL with a fake GitHub (accounts,
  CSRF, v1 sessions, projects, memory, PAT + App flow + webhook, nodes, roles, office, admin,
  static, install script). `install_script_test.go` runs `install-node.sh` in a fake HOME with
  stub `node`/`npm`/`curl`/`systemctl` (fresh install, v1 node.env upgrade, rollback, refusals).
- `cmd/atctl`: `web/src/api/openapi.json` must equal `atctl openapi` (regenerate with
  `UPDATE_OPENAPI=1`).

Image: `docker build -f server/Dockerfile -t agent-teams-server .` (repository root as
context; `server/Dockerfile.dockerignore` limits it). Stages: web (Vite) → node daemon
tarball (`node/ npm run pack`) → `atctl` → debian-slim with git.

## Security behaviour

- Session cookie `agent_teams_session` (HttpOnly, SameSite=Lax, Secure on https), `at_sessions`
  stores sha256(token) exactly like v1, so v1 sessions keep working. 30 days, slid back to 30
  when fewer than 7 remain; expired rows are deleted daily.
- CSRF: every cookie-authenticated non-GET request needs `X-CSRF-Token` equal to the session's
  `csrf_secret` (also sent as the non-HttpOnly cookie `agent_teams_csrf`, generated on the first
  request of sessions created by v1). Bearer requests, `/auth/login`, `/auth/register` and the
  signed GitHub webhook are exempt. (One-shot cut-over → the P6 rule from §10.1 applies now.)
- Rate limits: login/register 10/min/IP, node creation 10/hour/user (429 + Retry-After).
- Audit (`at_audit_log`): login, register, password change/reset, role change, user delete,
  node create/rotate/delete/isolated, Git connection add/delete, GitHub App create/delete,
  auto-approve toggle, skip-permissions on, settings, office layout save/delete.
- `must_change_password` (admin reset) blocks `/api/*` except `/api/me`, `/api/me/password`.

## Extension points (for the orchestration engines)

Everything is wired in `cmd/atctl/wire.go`:

```go
func wire(d httpapi.Deps) httpapi.Deps {
    d.Nodes = gateway            // platform.NodeRegistry
    d.Events = publisher         // platform.Events
    d.Hooks = engines            // platform.Hooks
    d.Extensions = append(d.Extensions, func(api huma.API, r chi.Router) {
        r.Get("/nodes/connect", gateway.ServeWS)       // also /nodes/v2/connect
        huma.Register(api, huma.Operation{...}, threads.Create)
    })
    return d
}
```

`openapi` calls `wire` with `d.Store == nil`: wiring may build objects and register routes
but must not do I/O. Routes added in `Extensions` run behind the session/CSRF middleware and
share the OpenAPI document (regenerate `web/src/api/openapi.json` afterwards).

| Interface / helper | Used by the REST layer for | Default |
| --- | --- | --- |
| `platform.NodeRegistry.Status(id)` | `online`, `since`, version, runtimes, capacity, session counts in `GET /api/nodes` | offline |
| `.Disconnect(ctx, id, reason)` | token rotation, node delete, user deactivate/delete | no-op |
| `.Configure(ctx, node)` | `PATCH /api/nodes/{id}` → `node.config` | no-op |
| `.Upgrade` / `.Drain` | `POST /api/nodes/{id}/upgrade|drain` (409 `node_offline` on `platform.ErrNodeOffline`) | offline |
| `platform.Events.Project(ctx, pid, type, payload)` | `memory` events, `layout` events (fan-out to projects using a layout) | log |
| `platform.Events.User(ctx, uid, type, payload)` | `project` events (create/update/delete) | log |
| `platform.Hooks.ProjectCreated(ctx, p, firstMessage)` | `first_message` of `POST /api/projects` → coordinator | no-op |
| `.ProjectDeleting(ctx, uid, pid)` | close sessions, `git.purge` before the rows go | no-op |
| `.UserCutOff(ctx, uid)` | deactivate/delete user: close sessions, cancel waits | no-op |
| `.NodeDeleting(ctx, uid, nodeID)` | reassign employees before the node row goes | no-op |
| `.GitHubEvent(ctx, event, payload)` | verified webhook deliveries (`check_run`, `pull_request`, …) | no-op |
| `httpapi.CurrentUser/RequireUser/RequireAdmin/Fail/StoreError` | shared auth and `{detail, code}` errors in extension handlers | — |
| `store.Store` (`Tx`, `NodeByToken`, `Roles`, `ProjectOf`, …) | node WS auth by sha256 token, role snapshots | — |
| `github.Tokens.Installation(ctx, id, TokenRequest{Repositories, Permissions})` | narrowed tokens for `cred.request` and push jobs (pass the same instance via `Deps.Tokens`) | — |
| `office.Registry.Get(id)` / `office.LooksOf` | office snapshot, employee looks | — |
| `store.Store.EnsurePartitions` | event partitions (already run daily by `serve`) | — |

Tables for the engines (`at_threads`, `at_employees`, `at_agent_sessions`, `at_events`, …)
are created by the migrations here; their queries belong to the orchestration packages.

## Deviations from the spec

- No Python proxy, no `/internal/*`, no `platform_version` routing, no `legacy_guard` check in
  migrations (one-shot cut-over). `platform_version` is still a column and in `UserView`.
- `go.mod` says `go 1.26.0`: the current huma, goose, x/crypto releases require it (spec: 1.25).
- GitHub: a small hand-written REST client (`internal/github`) instead of go-github +
  ghinstallation; same behaviour as v1 `github.py`, plus pagination and narrowed tokens.
- Plain pgx queries instead of sqlc.
- Office validation: JSON Schema + size, furniture count and required built-in looks.
  The geometric checks of office-layout.md §6.3 (references, nav connectivity, walls) are
  not implemented in Go yet.
- Runtime image is debian-slim (git included) rather than distroless.
- `/metrics`, OTel, `Idempotency-Key` and the orchestration endpoints of api.md are out of scope here.
- Added columns: `at_users.must_change_password`, `at_git_connections.account_type` (org vs user
  installation settings URL). Added endpoint `GET /api/office/layout?project=` (layout in effect).
- `node_release` in admin settings is omitted (not `null`) when unset; PATCH bodies clear nullable
  numbers with `clear_*` flags as in v1 (`clear_max_nodes`, `clear_max_active_sessions`, …).
- Role `sort` is the position in the list on `PUT /api/roles`.
