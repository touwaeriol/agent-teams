# CLAUDE.md

Composable multi-agent teams on the Claude API (Python 3.10+, `src/` layout).

## Commands

```bash
pip install -e ".[dev]"
ruff format . && ruff check .   # lint incl. mccabe ≤ 6, nested blocks ≤ 3
mypy                            # strict, covers src/, tests/, examples/
pytest                          # offline; never hits the real API
# integration tests run when TEST_DATABASE_URL / TEST_VALKEY_URL are set
python examples/offline_demo.py
python examples/offline_leader.py   # non-blocking leader demo
```

## Architecture

- `Runnable` (runnable.py) is the single interface: `run(task, ctx) -> Result`. `Agent` and every `Team` implement `_run`; `run` adds context and `RunStarted`/`RunFinished` events.
- `Agent` only depends on the `LLM` protocol (llm/base.py). History is provider-neutral (`UserTurn`/`AssistantTurn`/`ToolTurn`); `AssistantTurn.raw` carries native blocks that must be replayed unchanged (thinking blocks).
- `AnthropicLLM` streams via `client.beta.messages.stream` with adaptive thinking, effort, and server-side refusal fallbacks; `_echo_blocks` drops pre-fallback non-text blocks.
- `Leader` (leader.py) is the non-blocking lead: its `TaskBoard` (tasks.py) runs members as asyncio tasks; `assign_task` returns at once, and every outcome (done/failed/cancelled, incl. cancelled-before-start) is settled in a done-callback that notifies listeners synchronously.
- `Conversation` (conversation.py) is an inbox over one `Session`: the user and task reports `post`, a single consumer calls `reply`. `agent-teams chat` reads input and prints replies concurrently; EOF drains, `/exit` cancels.
- Config: `nodes:` are machines (`NodeConfig` -> `SSHNode`/`LocalNode` in nodes.py, agentless SSH via the system `ssh`); `leader` + `employees` (config.py `_promote_leader`) turns the chosen agent into a `leader` over the other employees.
- `ClaudeCodeWorker` (claude_code.py) is an employee that runs `claude -p ... --output-format stream-json --verbose` on its node; progress lines become `WorkerProgress` events, the final `result` line is the output (error results win over the exit status). `agent-teams nodes` probes every node.
- `Agent.session()` returns a `Session` (multi-turn, rolls back a failed message). `supervisor()` is the blocking alternative to `Leader`.
- `server/` (optional `[server]` extra): `Hub` owns one `Conversation` + responder task per chat and mirrors messages/tasks/history to a `Store` (`PostgresStore`, `MemoryStore`) and events to a `Broker` (`ValkeyBroker`, `MemoryBroker`). FastAPI app in app.py: Bearer token or HttpOnly session cookie; SSE for live events. Deploy: `docker-compose.yml` (host network, uses the host's PG/Valkey) + `deploy/deploy.sh`.
- Tools: `@tool` builds a pydantic model from the signature; `RunContext`-annotated params are injected. `as_tool` wraps any `Runnable` (used by `supervisor`). Tool failures become `is_error` results, never exceptions.
- Config: pydantic specs in config.py, each with `build(builder)`; add a member type by adding a spec class and a `Tag` entry in `MemberSpec`.

## Conventions

- Keep functions small and flat: early returns, dict dispatch / `match`, no deep nesting.
- Extend through composition (handoff/merge callables, hooks, `options`) rather than subclassing core classes.
- Tests use `ScriptedLLM` or fakes; add a test for every behavior change.
- Default model is `claude-opus-5-5`; don't hardcode other model IDs.
