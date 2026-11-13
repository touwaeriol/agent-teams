from __future__ import annotations

import logging

import pytest

from agent_teams import Event, Hooks, Result, RunFinished, RunStarted, trace
from agent_teams.hooks import ToolFinished, ToolStarted
from agent_teams.types import ToolCall, ToolResult


async def test_subclass_events_reach_base_handlers() -> None:
    hooks = Hooks()
    specific: list[Event] = []
    everything: list[Event] = []
    hooks.on(RunStarted, specific.append)
    hooks.on(Event, everything.append)

    await hooks.emit(RunStarted(("a",), "task"))
    await hooks.emit(RunFinished(("a",), Result("a", "out")))

    assert len(specific) == 1
    assert len(everything) == 2


async def test_async_handlers_are_awaited() -> None:
    hooks = Hooks()
    seen: list[str] = []

    async def handler(event: RunStarted) -> None:
        seen.append(event.task)

    hooks.on(RunStarted, handler)
    await hooks.emit(RunStarted(("a",), "task"))
    assert seen == ["task"]


async def test_failing_handler_does_not_break_the_run(caplog: pytest.LogCaptureFixture) -> None:
    hooks = Hooks()
    seen: list[Event] = []

    def broken(_event: Event) -> None:
        raise ValueError("nope")

    hooks.on(Event, broken)
    hooks.on(Event, seen.append)
    with caplog.at_level(logging.ERROR):
        await hooks.emit(RunStarted(("a",), "task"))
    assert seen
    assert "hook" in caplog.text


async def test_trace_formats_events() -> None:
    lines: list[str] = []
    hooks = trace(Hooks(), lines.append)
    call = ToolCall("1", "search", {"q": "x"})

    await hooks.emit(RunStarted(("team", "agent"), "a  long\ntask"))
    await hooks.emit(ToolStarted(("team", "agent"), call))
    await hooks.emit(ToolFinished(("team", "agent"), call, ToolResult("1", "bad", is_error=True)))

    assert lines[0] == "[team/agent] ▶ start: a long task"
    assert lines[1] == "[team/agent] → search({'q': 'x'})"
    assert lines[2] == "[team/agent] ← search [error]: bad"
