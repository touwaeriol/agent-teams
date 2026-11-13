"""A deterministic LLM for tests and offline demos."""

from __future__ import annotations

import itertools
from collections.abc import Callable, Iterable, Mapping, Sequence
from typing import Any

from ..types import AssistantTurn, Reply, ToolCall, ToolSpec, Turn

Responder = Callable[[str, Sequence[Turn]], Reply]

_ids = itertools.count(1)


def say(text: str) -> Reply:
    """A final text reply."""
    return Reply(AssistantTurn(text), stop="end")


def call(name: str, arguments: Mapping[str, Any] | None = None, text: str = "") -> Reply:
    """A reply that requests one tool call."""
    tool_call = ToolCall(f"call_{next(_ids)}", name, dict(arguments or {}))
    return Reply(AssistantTurn(text, (tool_call,)), stop="tool_use")


class ScriptedLLM:
    """Plays back canned replies in order, or delegates to a responder function."""

    def __init__(self, script: Iterable[Reply | str] | Responder) -> None:
        self._respond = script if callable(script) else _playback(script)
        self.calls: list[tuple[str, tuple[Turn, ...], tuple[ToolSpec, ...]]] = []

    async def complete(
        self, system: str, history: Sequence[Turn], tools: Sequence[ToolSpec]
    ) -> Reply:
        self.calls.append((system, tuple(history), tuple(tools)))
        return self._respond(system, history)


def _playback(script: Iterable[Reply | str]) -> Responder:
    replies = iter([say(r) if isinstance(r, str) else r for r in script])

    def respond(_system: str, _history: Sequence[Turn]) -> Reply:
        reply = next(replies, None)
        if reply is None:
            raise RuntimeError("ScriptedLLM ran out of replies")
        return reply

    return respond
