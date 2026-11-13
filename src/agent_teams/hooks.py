"""Observer hooks: watch a run without touching agent or team code.

Handlers subscribe to an event class (or ``Event`` for everything). Handler
failures are logged and never break the run.
"""

from __future__ import annotations

import inspect
import logging
from collections import defaultdict
from collections.abc import Awaitable, Callable
from dataclasses import dataclass
from typing import Any, TypeVar

from ._text import clip
from .types import Reply, Result, Task, ToolCall, ToolResult

logger = logging.getLogger(__name__)


@dataclass(frozen=True)
class Event:
    """Base event. ``path`` is the chain of runnable names, outermost first."""

    path: tuple[str, ...]

    @property
    def source(self) -> str:
        return "/".join(self.path)


@dataclass(frozen=True)
class RunStarted(Event):
    task: str


@dataclass(frozen=True)
class RunFinished(Event):
    result: Result


@dataclass(frozen=True)
class LLMReplied(Event):
    reply: Reply


@dataclass(frozen=True)
class ToolStarted(Event):
    call: ToolCall


@dataclass(frozen=True)
class ToolFinished(Event):
    call: ToolCall
    result: ToolResult


@dataclass(frozen=True)
class TaskAssigned(Event):
    task: Task


@dataclass(frozen=True)
class TaskFinished(Event):
    task: Task


E = TypeVar("E", bound=Event)
Handler = Callable[[E], Awaitable[None] | None]


class Hooks:
    """A tiny event bus keyed by event type (subclasses match parent handlers)."""

    def __init__(self) -> None:
        self._handlers: defaultdict[type[Event], list[Handler[Any]]] = defaultdict(list)

    def on(self, event_type: type[E], handler: Handler[E]) -> Handler[E]:
        self._handlers[event_type].append(handler)
        return handler

    async def emit(self, event: Event) -> None:
        for cls in type(event).__mro__:
            for handler in self._handlers.get(cls, ()):
                await _call_safely(handler, event)


_FORMATS: dict[type[Event], Callable[[Any], str]] = {
    RunStarted: lambda e: f"▶ start: {clip(e.task)}",
    RunFinished: lambda e: f"■ done: {clip(e.result.output)}",
    ToolStarted: lambda e: f"→ {e.call.name}({clip(str(dict(e.call.arguments)))})",
    ToolFinished: lambda e: (
        f"← {e.call.name}{' [error]' if e.result.is_error else ''}: {clip(e.result.content)}"
    ),
    TaskAssigned: lambda e: f"+ {e.task.id} → {e.task.assignee}: {clip(e.task.instruction)}",
    TaskFinished: lambda e: f"✓ {e.task.id} {e.task.status}: {clip(e.task.output)}",
}


def trace(hooks: Hooks, write: Callable[[str], None] = print) -> Hooks:
    """Subscribe a one-line-per-event tracer, e.g. for the CLI's ``--verbose``."""

    def log(event: Event) -> None:
        format_event = _FORMATS.get(type(event))
        if format_event is not None:
            write(f"[{event.source}] {format_event(event)}")

    hooks.on(Event, log)
    return hooks


async def _call_safely(handler: Handler[Any], event: Event) -> None:
    try:
        outcome = handler(event)
        if inspect.isawaitable(outcome):
            await outcome
    except Exception:
        logger.exception("hook %r failed on %s", handler, type(event).__name__)
