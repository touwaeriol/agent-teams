"""An employee that is an OpenAI Codex CLI process on a node.

Each task runs ``codex exec --json ... -- <task>`` (non-interactive) in the
node's workdir. Codex prints one JSON event per line (``thread.started``,
``turn.started``, ``item.started`` / ``item.completed``, ``turn.completed``
or ``turn.failed``, and top-level ``error`` notices while it retries).
Items become :class:`WorkerProgress` events; the last ``agent_message`` of a
completed turn is the task output, a failed turn raises :class:`CodexError`.

Team tools reach Codex as a streamable HTTP MCP server named ``team`` set
with ``-c mcp_servers.team.*`` overrides. ``codex exec`` cannot route
permission prompts to a tool, so the approval tool is hidden and the
``sandbox`` policy decides what Codex may do. See docs/agent-comms.md
("Codex：实际核对") for what was checked against the real CLI.
"""

from __future__ import annotations

import json
import shlex
from collections.abc import Callable, Mapping, Sequence
from dataclasses import dataclass, field, replace
from typing import Any, Literal

from ._text import clip
from .context import RunContext
from .errors import AgentTeamsError
from .hooks import WorkerProgress
from .mcp_config import MCP_SERVER, ToolEndpoint
from .nodes import Node, NodeCommandError
from .runnable import Runnable
from .types import Result

Sandbox = Literal["read-only", "workspace-write", "danger-full-access"]
#: Team tools such as ``ask_user`` block until someone answers (seconds).
TOOL_TIMEOUT_SECONDS = 86400


class CodexError(AgentTeamsError):
    """Codex finished without a successful turn."""


@dataclass(eq=False)
class CodexWorker(Runnable):
    name: str
    node: Node
    description: str = ""
    instructions: str = ""
    workdir: str = ""
    model: str = ""
    sandbox: Sandbox = "workspace-write"
    codex_command: str = "codex"
    extra_args: Sequence[str] = field(default_factory=tuple)
    can_delegate: bool = False  # may hand subtasks to other employees
    endpoint: ToolEndpoint | None = None
    hidden_tools: Sequence[str] = field(default_factory=tuple)

    def __post_init__(self) -> None:
        self.description = self.description or f"Codex on node {self.node.name}"

    @property
    def resumable(self) -> bool:
        """Whether an interrupted task can re-attach to its run (daemon nodes)."""
        return self.node.resumable

    @property
    def directory(self) -> str:
        return self.node.workdir_for(self.name, self.workdir)

    def with_tools(self, endpoint: ToolEndpoint, approval_tool: str | None = None) -> CodexWorker:
        """A copy that reaches the team tools at ``endpoint``.

        ``codex exec`` has no permission-prompt hook, so ``approval_tool`` is
        hidden from the model instead of being wired up; the sandbox decides.
        """
        hidden = (approval_tool,) if approval_tool else ()
        return replace(self, endpoint=endpoint, hidden_tools=hidden)

    def command(self, task: str) -> list[str]:
        """The ``codex exec`` invocation for one task."""
        model = ["--model", self.model] if self.model else []
        return [
            *shlex.split(self.codex_command),
            "exec",
            "--json",
            "--skip-git-repo-check",
            "--sandbox",
            self.sandbox,
            *model,
            *self.config_args(),
            *self.extra_args,
            "--",
            task,
        ]

    def config_args(self) -> list[str]:
        """``-c key=value`` overrides (values are TOML)."""
        overrides: dict[str, object | None] = {
            "developer_instructions": self.instructions or None,
            **_mcp_overrides(self.endpoint, self.hidden_tools),
        }
        return [
            part
            for key, value in overrides.items()
            if value is not None
            for part in ("-c", f"{key}={_toml(value)}")
        ]

    async def _run(self, task: str, ctx: RunContext) -> Result:
        outcome = _Outcome()
        try:
            await self._follow(task, ctx, outcome)
        except NodeCommandError:
            if outcome.failure is None:  # nothing better than the exit status
                raise
        return Result(self.name, outcome.output(self.name))

    async def _follow(self, task: str, ctx: RunContext, outcome: _Outcome) -> None:
        key = f"{ctx.run_key}/{self.name}" if ctx.run_key else ""
        async for line in self.node.stream(self.command(task), self.directory, key):
            event = _parse(line)
            outcome.record(event)
            for text in describe(event):
                await ctx.emit(WorkerProgress(ctx.path, text))


def _mcp_overrides(
    endpoint: ToolEndpoint | None, hidden: Sequence[str]
) -> dict[str, object | None]:
    if endpoint is None:
        return {}
    prefix = f"mcp_servers.{MCP_SERVER}"
    return {
        f"{prefix}.url": endpoint.url,
        f"{prefix}.http_headers": {"Authorization": f"Bearer {endpoint.token}"},
        f"{prefix}.tool_timeout_sec": TOOL_TIMEOUT_SECONDS,
        f"{prefix}.disabled_tools": list(hidden) if hidden else None,
    }


def _toml(value: object) -> str:
    """A TOML value: strings, ints, string lists and inline tables of strings."""
    if isinstance(value, str):
        return json.dumps(value, ensure_ascii=False)  # a valid TOML basic string
    if isinstance(value, list):
        return "[" + ", ".join(_toml(item) for item in value) + "]"
    if isinstance(value, dict):
        pairs = ", ".join(f"{json.dumps(k)} = {_toml(v)}" for k, v in value.items())
        return "{" + pairs + "}"
    return str(value)


@dataclass
class _Outcome:
    """What a run's events say about how it ended."""

    message: str | None = None
    completed: bool = False
    failure: str | None = None

    def record(self, event: Mapping[str, Any]) -> None:
        kind = event.get("type")
        item = event.get("item") or {}
        if kind == "item.completed" and item.get("type") == "agent_message":
            self.message = str(item.get("text", ""))
        elif kind == "turn.completed":
            self.completed = True
        elif kind == "turn.failed":
            self.failure = str((event.get("error") or {}).get("message", "turn failed"))

    def output(self, name: str) -> str:
        if self.failure is not None:
            raise CodexError(f"{name}: {self.failure}")
        if not self.completed:
            raise CodexError(f"{name}: Codex exited without finishing the turn")
        return self.message or ""


def _parse(line: str) -> Mapping[str, Any]:
    try:
        event = json.loads(line)
    except json.JSONDecodeError:
        return {"type": "text", "text": line}
    return event if isinstance(event, dict) else {}


def describe(event: Mapping[str, Any]) -> list[str]:
    """Readable progress lines for one ``codex exec --json`` event."""
    describer = _DESCRIBERS.get(str(event.get("type")))
    return describer(event) if describer else []


def _describe_item(event: Mapping[str, Any]) -> list[str]:
    item = event.get("item") or {}
    describer = _ITEMS.get(str(item.get("type")))
    line = describer(item) if describer else ""
    return [line] if line else []


def _describe_started(event: Mapping[str, Any]) -> list[str]:
    item = event.get("item") or {}
    if item.get("type") == "command_execution":
        return [f"$ {clip(str(item.get('command', '')), 160)}"]
    if item.get("type") == "mcp_tool_call":
        arguments = clip(json.dumps(item.get("arguments"), ensure_ascii=False), 100)
        return [f"{item.get('server')}.{item.get('tool')} {arguments}"]
    return []


def _describe_command(item: Mapping[str, Any]) -> str:
    if item.get("status") == "completed":
        return ""
    output = clip(str(item.get("aggregated_output", "")).strip(), 160)
    return f"command failed (exit {item.get('exit_code')}): {output}"


def _describe_tool_call(item: Mapping[str, Any]) -> str:
    error = item.get("error")
    return f"{item.get('tool')} failed: {clip(str(error), 160)}" if error else ""


def _describe_turn(event: Mapping[str, Any]) -> list[str]:
    usage = event.get("usage") or {}
    spent = f"{usage.get('input_tokens', '?')} in / {usage.get('output_tokens', '?')} out tokens"
    return [f"finished: {spent}"]


def _text_of(key: str, prefix: str = "") -> Callable[[Mapping[str, Any]], str]:
    def text(item: Mapping[str, Any]) -> str:
        value = str(item.get(key) or "")
        return f"{prefix}{clip(value, 160)}" if value else ""

    return text


_ITEMS: dict[str, Callable[[Mapping[str, Any]], str]] = {
    "agent_message": _text_of("text"),
    "reasoning": _text_of("text"),
    "error": _text_of("message", "warning: "),
    "command_execution": _describe_command,
    "mcp_tool_call": _describe_tool_call,
    "file_change": lambda item: f"changed {len(item.get('changes') or [])} file(s)",
}

_DESCRIBERS: dict[str, Callable[[Mapping[str, Any]], list[str]]] = {
    "thread.started": lambda e: [f"thread {e.get('thread_id', '?')}"],
    "item.started": _describe_started,
    "item.completed": _describe_item,
    "turn.completed": _describe_turn,
    "turn.failed": lambda e: [f"failed: {(e.get('error') or {}).get('message', '?')}"],
    "error": lambda e: [f"error: {clip(str(e.get('message', '')), 160)}"],
    "text": lambda e: [clip(str(e.get("text", "")), 160)] if e.get("text") else [],
}
