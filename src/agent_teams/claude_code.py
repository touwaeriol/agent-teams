"""An employee that is a Claude Code process on a node.

Each task runs ``claude -p <task> --output-format stream-json`` in the node's
workdir, so the employee can really read and edit files and run commands
there. Progress lines become :class:`WorkerProgress` events; the final
``result`` line becomes the task output.
"""

from __future__ import annotations

import json
import shlex
from collections.abc import Callable, Mapping, Sequence
from dataclasses import dataclass, field
from typing import Any

from ._text import clip
from .context import RunContext
from .errors import AgentTeamsError
from .hooks import WorkerProgress
from .nodes import Node, NodeCommandError
from .runnable import Runnable
from .types import Result


class ClaudeCodeError(AgentTeamsError):
    """Claude Code finished without a successful result."""


@dataclass(eq=False)
class ClaudeCodeWorker(Runnable):
    name: str
    node: Node
    description: str = ""
    instructions: str = ""
    workdir: str = ""
    model: str = ""
    permission_mode: str = "acceptEdits"
    allowed_tools: Sequence[str] = field(default_factory=tuple)
    disallowed_tools: Sequence[str] = field(default_factory=tuple)
    max_turns: int | None = None
    claude_command: str = "claude"
    extra_args: Sequence[str] = field(default_factory=tuple)

    def __post_init__(self) -> None:
        self.description = self.description or f"Claude Code on node {self.node.name}"

    @property
    def resumable(self) -> bool:
        """Whether an interrupted task can re-attach to its run (daemon nodes)."""
        return self.node.resumable

    @property
    def directory(self) -> str:
        return self.node.workdir_for(self.name, self.workdir)

    def command(self, task: str) -> list[str]:
        """The ``claude`` invocation for one task."""
        options = {
            "--append-system-prompt": self.instructions,
            "--model": self.model,
            "--permission-mode": self.permission_mode,
            "--allowedTools": ",".join(self.allowed_tools),
            "--disallowedTools": ",".join(self.disallowed_tools),
            "--max-turns": str(self.max_turns) if self.max_turns else "",
        }
        flags = [part for key, value in options.items() if value for part in (key, value)]
        return [
            *shlex.split(self.claude_command),
            "-p",
            task,
            "--output-format",
            "stream-json",
            "--verbose",
            *flags,
            *self.extra_args,
        ]

    async def _run(self, task: str, ctx: RunContext) -> Result:
        events: list[Mapping[str, Any]] = []
        try:
            await self._follow(task, ctx, events)
        except NodeCommandError:
            if not events:  # no result line: the exit status is all we know
                raise
        return Result(self.name, _output(self.name, events[-1] if events else None))

    async def _follow(self, task: str, ctx: RunContext, results: list[Mapping[str, Any]]) -> None:
        """Stream the run, reporting progress and collecting ``result`` events."""
        key = f"{ctx.run_key}/{self.name}" if ctx.run_key else ""
        async for line in self.node.stream(self.command(task), self.directory, key):
            event = _parse(line)
            if event.get("type") == "result":
                results.append(event)
            await _report(event, ctx)


def _parse(line: str) -> Mapping[str, Any]:
    try:
        event = json.loads(line)
    except json.JSONDecodeError:
        return {"type": "text", "text": line}
    return event if isinstance(event, dict) else {}


async def _report(event: Mapping[str, Any], ctx: RunContext) -> None:
    for text in describe(event):
        await ctx.emit(WorkerProgress(ctx.path, text))


def _output(name: str, final: Mapping[str, Any] | None) -> str:
    if final is None:
        raise ClaudeCodeError(f"{name}: Claude Code exited without a result")
    if final.get("is_error") or final.get("subtype") != "success":
        errors = "; ".join(final.get("errors") or []) or final.get("result") or ""
        raise ClaudeCodeError(f"{name}: {final.get('subtype', 'error')}: {errors}".rstrip(": "))
    return str(final.get("result", ""))


def describe(event: Mapping[str, Any]) -> list[str]:
    """Readable progress lines for one stream-json event."""
    describer = _DESCRIBERS.get(str(event.get("type")))
    return describer(event) if describer else []


def _describe_init(event: Mapping[str, Any]) -> list[str]:
    return [f"session {event.get('session_id', '?')} in {event.get('cwd', '?')}"]


def _describe_assistant(event: Mapping[str, Any]) -> list[str]:
    blocks = (event.get("message") or {}).get("content") or []
    return [line for block in blocks if (line := _describe_block(block))]


def _describe_block(block: Mapping[str, Any]) -> str:
    if block.get("type") == "tool_use":
        return (
            f"{block.get('name')} {clip(json.dumps(block.get('input'), ensure_ascii=False), 100)}"
        )
    if block.get("type") == "text":
        return clip(str(block.get("text", "")), 160)
    return ""


def _describe_result(event: Mapping[str, Any]) -> list[str]:
    cost = event.get("total_cost_usd")
    spent = f", ${cost:.4f}" if isinstance(cost, int | float) else ""
    return [f"finished: {event.get('subtype')} in {event.get('num_turns', '?')} turns{spent}"]


_DESCRIBERS: dict[str, Callable[[Mapping[str, Any]], list[str]]] = {
    "system": lambda e: _describe_init(e) if e.get("subtype") == "init" else [],
    "assistant": _describe_assistant,
    "result": _describe_result,
    "text": lambda e: [clip(str(e.get("text", "")), 160)] if e.get("text") else [],
}
