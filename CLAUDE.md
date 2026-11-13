# CLAUDE.md

Composable multi-agent teams on the Claude API (Python 3.10+, `src/` layout).

## Commands

```bash
pip install -e ".[dev]"
ruff format . && ruff check .   # lint incl. mccabe ≤ 6, nested blocks ≤ 3
mypy                            # strict, covers src/, tests/, examples/
pytest                          # offline; never hits the real API
python examples/offline_demo.py
```

## Architecture

- `Runnable` (runnable.py) is the single interface: `run(task, ctx) -> Result`. `Agent` and every `Team` implement `_run`; `run` adds context and `RunStarted`/`RunFinished` events.
- `Agent` only depends on the `LLM` protocol (llm/base.py). History is provider-neutral (`UserTurn`/`AssistantTurn`/`ToolTurn`); `AssistantTurn.raw` carries native blocks that must be replayed unchanged (thinking blocks).
- `AnthropicLLM` streams via `client.beta.messages.stream` with adaptive thinking, effort, and server-side refusal fallbacks; `_echo_blocks` drops pre-fallback non-text blocks.
- Tools: `@tool` builds a pydantic model from the signature; `RunContext`-annotated params are injected. `as_tool` wraps any `Runnable` (used by `supervisor`). Tool failures become `is_error` results, never exceptions.
- Config: pydantic specs in config.py, each with `build(builder)`; add a node type by adding a spec class and a `Tag` entry in `NodeSpec`.

## Conventions

- Keep functions small and flat: early returns, dict dispatch / `match`, no deep nesting.
- Extend through composition (handoff/merge callables, hooks, `options`) rather than subclassing core classes.
- Tests use `ScriptedLLM` or fakes; add a test for every behavior change.
- Default model is `claude-opus-5-5`; don't hardcode other model IDs.
