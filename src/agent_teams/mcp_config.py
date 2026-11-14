"""How Claude Code (or another agent CLI) reaches tools published over MCP."""

from __future__ import annotations

import json
from collections.abc import Sequence
from dataclasses import dataclass

MCP_SERVER = "team"


@dataclass(frozen=True)
class ToolEndpoint:
    """Where Claude Code reaches published tools (streamable HTTP MCP)."""

    url: str
    token: str
    tools: Sequence[str]


def mcp_tool_name(name: str) -> str:
    """The name Claude Code gives a tool of the team server."""
    return f"mcp__{MCP_SERVER}__{name}"


def mcp_tool_names(endpoint: ToolEndpoint | None) -> list[str]:
    tools = endpoint.tools if endpoint else ()
    return [mcp_tool_name(name) for name in tools]


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
