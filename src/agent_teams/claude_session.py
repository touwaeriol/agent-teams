"""A multi-turn conversation with Claude Code on a node: the leader's brain.

Each message is one ``claude -p`` turn in the same Claude Code session
(``--session-id`` first, ``--resume`` afterwards), so Claude Code keeps the
history itself. The task board's tools reach it over MCP: a
:class:`ToolServer` (the web service) publishes them and hands back an
endpoint that goes into ``--mcp-config``.
"""

from __future__ import annotations

import json
import uuid
from collections.abc import Mapping, Sequence
from dataclasses import dataclass, field, replace
from typing import Any, Protocol

from .claude_code import ClaudeCodeWorker
from .context import RunContext
from .tools import Toolkit
from .types import Result

MCP_SERVER = "team"


@dataclass(frozen=True)
class ToolEndpoint:
    """Where Claude Code reaches published tools (streamable HTTP MCP)."""

    url: str
    token: str
    tools: Sequence[str]


class ToolServer(Protocol):
    def expose(self, toolkit: Toolkit, ctx: RunContext) -> ToolEndpoint: ...


@dataclass
class ClaudeCodeSession:
    worker: ClaudeCodeWorker
    ctx: RunContext
    instructions: str = ""
    endpoint: ToolEndpoint | None = None
    session_id: str = field(default_factory=lambda: str(uuid.uuid4()))
    turns: int = 0
    started: bool = False

    @property
    def name(self) -> str:
        return self.worker.name

    async def send(self, message: str) -> Result:
        self.turns += 1  # every attempt gets its own run key on the node
        try:
            result = await self._turn().run(message, self.ctx.keyed(f"turn-{self.turns}"))
        except BaseException:
            if not self.started:  # nothing to resume yet: start clean next time
                self.session_id = str(uuid.uuid4())
            raise
        self.started = True
        return result

    def reset(self) -> None:
        self.session_id = str(uuid.uuid4())
        self.started = False

    def snapshot(self) -> dict[str, Any]:
        return {
            "kind": "claude_code",
            "session_id": self.session_id,
            "turns": self.turns,
            "started": self.started,
        }

    def restore(self, state: Mapping[str, Any]) -> None:
        if state.get("kind") != "claude_code":
            return
        self.session_id = str(state["session_id"])
        self.turns = int(state.get("turns", 0))
        self.started = bool(state.get("started", False))

    def _turn(self) -> ClaudeCodeWorker:
        session = (
            ["--resume", self.session_id] if self.started else ["--session-id", self.session_id]
        )
        return replace(
            self.worker,
            instructions="\n\n".join(p for p in (self.instructions, self.worker.instructions) if p),
            allowed_tools=[*self.worker.allowed_tools, *mcp_tool_names(self.endpoint)],
            extra_args=[*self.worker.extra_args, *session, *mcp_args(self.endpoint)],
        )


def mcp_tool_names(endpoint: ToolEndpoint | None) -> list[str]:
    tools = endpoint.tools if endpoint else ()
    return [f"mcp__{MCP_SERVER}__{name}" for name in tools]


def mcp_args(endpoint: ToolEndpoint | None) -> list[str]:
    if endpoint is None:
        return []
    server = {
        "type": "http",
        "url": endpoint.url,
        "headers": {"Authorization": f"Bearer {endpoint.token}"},
    }
    config = json.dumps({"mcpServers": {MCP_SERVER: server}})
    return ["--mcp-config", config, "--strict-mcp-config"]
