# CLAUDE.md

Composable multi-agent teams on the Claude API (Python 3.10+, `src/` layout).

## Commands

```bash
pip install -e ".[dev]"
ruff format . && ruff check .   # lint incl. mccabe ≤ 6, nested blocks ≤ 3
mypy                            # strict, covers src/, tests/, examples/
pytest                          # offline; never hits the real API (daemon tests use a local uvicorn + fake claude)
# integration tests run when TEST_DATABASE_URL / TEST_VALKEY_URL are set
python examples/offline_demo.py
python examples/offline_leader.py   # non-blocking leader demo
```

## Architecture

- `Runnable` (runnable.py) is the single interface: `run(task, ctx) -> Result`. `Agent` and every `Team` implement `_run`; `run` adds context and `RunStarted`/`RunFinished` events.
- `Agent` only depends on the `LLM` protocol (llm/base.py). History is provider-neutral (`UserTurn`/`AssistantTurn`/`ToolTurn`); `AssistantTurn.raw` carries native blocks that must be replayed unchanged (thinking blocks).
- `Leader` (leader.py) is the non-blocking lead; `brain` is an LLM (in-process `Agent`) or a `ClaudeCodeWorker` (a `ClaudeCodeSession`, claude_session.py: `--session-id` then `--resume` per turn, board tools over MCP via a `ToolServer`). Its `TaskBoard` (tasks.py) runs members as asyncio tasks; `assign_task` returns at once; outcomes are settled in a done-callback.
- `Conversation` (conversation.py) is an inbox over one `ChatSession` (protocol: name/send/reset/snapshot/restore). The user and task reports `post`; a single consumer calls `reply`.
- Nodes: `SSHNode`/`LocalNode` (nodes.py, from YAML `nodes:`) and `RemoteNode` (remote.py): a daemon (`agent-teams node`, daemon.py) dials `/nodes/connect` with its token. `start` is idempotent per run_id (`RunContext.run_key` + employee), so runs survive reconnects and console/daemon restarts. Daemon policy: only `--allow` executables inside `--root`.
- `ClaudeCodeWorker` (claude_code.py) runs `claude -p ... --output-format stream-json --verbose` in `<node workdir>/<employee>`; progress lines become `WorkerProgress` events.
- `server/` (`[server]` extra) is the multi-user platform: `auth.py` (open registration, hashed session tokens, roles), `routes.py` (conversations, nodes with one-time tokens, team YAML), `admin.py`, `accounts.py` + `store.py` (memory and PostgreSQL), `projects.py` + `project_routes.py` (projects = GitHub repo + context; Git connections whose tokens are sealed by `secrets.py`; `github.py` talks to the GitHub API), `workspaces.py` (each user's `Leader` from their YAML + their `RemoteNode`s, per project: workdir `projects/<id>`, repo cloned by the daemon, token only in the run env, context appended to every employee's instructions; platform teams must be `claude_code`), `hub.py` (conversations scoped by owner, recovery after restart), `mcp_bridge.py` (`/mcp`, per-conversation bearer tokens), `broker.py` (Valkey pub/sub → SSE). Deploy: `docker-compose.yml` (service + PostgreSQL + Valkey) + `deploy/deploy.sh`; nodes install via `/install-node.sh`.
- `web/` is the browser app (React + Vite + Tailwind). Pages compose only `src/components/common` (platform building blocks: `Page`, `Panel`, `Field`, `FormDialog`, `ConfirmAction`, `StatusBadge`, `DataTable`… see its README; live catalogue at `/ui`) and `src/components/ui` (shadcn/ui primitives); API writes go through `useAction` (lib/mutations.ts). `npm run build` writes into `server/static/app` (git-ignored); the Dockerfile builds it. `cd web && npx tsc -b` type-checks.
- Tools: `@tool` builds a pydantic model from the signature; `RunContext`-annotated params are injected. Tool failures become `is_error` results, never exceptions.
- Config: pydantic specs in config.py, each with `build(builder)`; add a member type by adding a spec class and a `Tag` entry in `MemberSpec`.

## Conventions

- Keep functions small and flat: early returns, dict dispatch / `match`, no deep nesting.
- Extend through composition (handoff/merge callables, hooks, `options`) rather than subclassing core classes.
- Tests use `ScriptedLLM` or fakes; add a test for every behavior change.
- Default model is `claude-opus-5-5`; don't hardcode other model IDs.
