"""Publish toolkits over MCP so a Claude Code leader on a node can call them.

One endpoint (``/mcp``, stateless streamable HTTP) serves every leader; the
bearer token picks the toolkit, so each conversation sees only its own task
board. Tool schemas come straight from :class:`~agent_teams.tools.Toolkit`.
"""

from __future__ import annotations

import secrets
import uuid
from collections.abc import AsyncIterator
from contextlib import asynccontextmanager
from dataclasses import dataclass
from typing import Any

from mcp import types
from mcp.server import ServerRequestContext
from mcp.server.lowlevel import Server
from mcp.server.streamable_http_manager import StreamableHTTPSessionManager
from mcp.server.transport_security import TransportSecuritySettings
from starlette.requests import Request
from starlette.responses import JSONResponse
from starlette.routing import Route
from starlette.types import Receive, Scope, Send

from ..claude_session import ToolEndpoint
from ..context import RunContext
from ..tools import Toolkit
from ..types import ToolCall

MCP_PATH = "/mcp"


@dataclass(frozen=True)
class _Binding:
    toolkit: Toolkit
    ctx: RunContext


class ToolBridge:
    """An ASGI app plus a :class:`~agent_teams.claude_session.ToolServer`."""

    def __init__(self, public_url: str) -> None:
        self.url = public_url.rstrip("/") + MCP_PATH
        self._bindings: dict[str, _Binding] = {}
        server: Server[Any] = Server(
            "agent-teams", on_list_tools=self._list_tools, on_call_tool=self._call_tool
        )
        # Bearer tokens authenticate callers, so Host/Origin checks add nothing here.
        security = TransportSecuritySettings(enable_dns_rebinding_protection=False)
        self._manager = StreamableHTTPSessionManager(
            server, json_response=True, stateless=True, security_settings=security
        )

    def expose(self, toolkit: Toolkit, ctx: RunContext) -> ToolEndpoint:
        token = secrets.token_urlsafe(32)
        self._bindings[token] = _Binding(toolkit, ctx)
        return ToolEndpoint(self.url, token, tuple(s.name for s in toolkit.specs))

    def revoke(self, token: str) -> None:
        self._bindings.pop(token, None)

    def route(self) -> Route:
        """Mount at exactly ``/mcp`` (no redirect: MCP clients POST there)."""
        return Route(MCP_PATH, endpoint=self, methods=["GET", "POST", "DELETE"])

    @asynccontextmanager
    async def running(self) -> AsyncIterator[None]:
        async with self._manager.run():
            yield

    async def __call__(self, scope: Scope, receive: Receive, send: Send) -> None:
        if _token(scope) not in self._bindings:
            response = JSONResponse({"detail": "invalid or missing token"}, status_code=401)
            await response(scope, receive, send)
            return
        await self._manager.handle_request(scope, receive, send)

    async def _list_tools(
        self, ctx: ServerRequestContext[Any], _params: types.PaginatedRequestParams | None
    ) -> types.ListToolsResult:
        toolkit = self._binding(ctx).toolkit
        tools = [
            types.Tool(name=s.name, description=s.description, input_schema=dict(s.input_schema))
            for s in toolkit.specs
        ]
        return types.ListToolsResult(tools=tools)

    async def _call_tool(
        self, ctx: ServerRequestContext[Any], params: types.CallToolRequestParams
    ) -> types.CallToolResult:
        binding = self._binding(ctx)
        call = ToolCall(uuid.uuid4().hex, params.name, dict(params.arguments or {}))
        (result,) = await binding.toolkit.execute([call], binding.ctx)
        content: list[types.ContentBlock] = [types.TextContent(type="text", text=result.content)]
        return types.CallToolResult(content=content, is_error=result.is_error)

    def _binding(self, ctx: ServerRequestContext[Any]) -> _Binding:
        request = ctx.request
        token = _token(request.scope) if isinstance(request, Request) else ""
        binding = self._bindings.get(token)
        if binding is None:
            raise PermissionError("the MCP token is no longer valid")
        return binding


def _token(scope: Scope) -> str:
    headers = dict(scope.get("headers") or [])
    value = bytes(headers.get(b"authorization", b"")).decode("latin-1")
    return value.removeprefix("Bearer ").strip()
