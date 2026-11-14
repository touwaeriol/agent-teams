"""The MCP bridge, driven by the official MCP client over real HTTP."""

from __future__ import annotations

import asyncio
import socket
from collections.abc import AsyncIterator
from contextlib import asynccontextmanager
from typing import Any

import httpx2
import pytest
import uvicorn
from fastapi import FastAPI
from mcp import ClientSession, types
from mcp.client.streamable_http import streamable_http_client

from agent_teams import Hooks, RunContext, TaskBoard, Toolkit, ToolStarted
from agent_teams.server.mcp_bridge import ToolBridge

from .fakes import Worker


def free_port() -> int:
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return int(sock.getsockname()[1])


@pytest.fixture
async def bridge() -> AsyncIterator[ToolBridge]:
    port = free_port()
    tools = ToolBridge(f"http://127.0.0.1:{port}")

    @asynccontextmanager
    async def lifespan(_app: FastAPI) -> AsyncIterator[None]:
        async with tools.running():
            yield

    app = FastAPI(lifespan=lifespan)
    app.router.routes.append(tools.route())
    server = uvicorn.Server(uvicorn.Config(app, host="127.0.0.1", port=port, log_level="warning"))
    serving = asyncio.create_task(server.serve())
    while not server.started:
        await asyncio.sleep(0.01)
    yield tools
    server.should_exit = True
    await serving


@asynccontextmanager
async def mcp_session(url: str, token: str) -> AsyncIterator[ClientSession]:
    headers = {"Authorization": f"Bearer {token}"}
    async with (
        httpx2.AsyncClient(headers=headers) as http,
        streamable_http_client(url, http_client=http) as (read, write),
        ClientSession(read, write) as session,
    ):
        await session.initialize()
        yield session


def text_of(result: Any) -> str:
    assert isinstance(result, types.CallToolResult)
    block = result.content[0]
    assert isinstance(block, types.TextContent)
    return block.text


async def test_leader_tools_over_mcp(bridge: ToolBridge) -> None:
    seen: list[str] = []
    hooks = Hooks()
    hooks.on(ToolStarted, lambda e: seen.append(f"{e.source} {e.call.name}"))
    ctx = RunContext(hooks, ("lead",))
    board = TaskBoard([Worker("writer")], ctx)
    endpoint = bridge.expose(Toolkit(board.tools()), ctx)

    async with mcp_session(endpoint.url, endpoint.token) as session:
        listed = await session.list_tools()
        names = [tool.name for tool in listed.tools]
        assigned = await session.call_tool(
            "assign_task", {"assignee": "writer", "instruction": "a poem"}
        )
        rejected = await session.call_tool("assign_task", {"assignee": "ghost", "instruction": "x"})

    assert names == ["assign_task", "list_tasks", "cancel_task"]
    assert endpoint.tools == ("assign_task", "list_tasks", "cancel_task")
    assert listed.tools[0].input_schema["required"] == ["assignee", "instruction"]
    assert text_of(assigned).startswith("t1 assigned to writer")
    assert isinstance(rejected, types.CallToolResult)
    assert rejected.is_error
    assert "unknown member" in text_of(rejected)
    assert seen[0] == "lead assign_task"
    await board.wait()
    assert board.get("t1").status == "done"


async def test_tokens_scope_and_revoke_access(bridge: ToolBridge) -> None:
    ctx = RunContext()
    board_a = TaskBoard([Worker("alpha")], ctx)
    board_b = TaskBoard([Worker("beta")], ctx)
    endpoint_a = bridge.expose(Toolkit(board_a.tools()), ctx)
    endpoint_b = bridge.expose(Toolkit(board_b.tools()), ctx)

    async with mcp_session(endpoint_b.url, endpoint_b.token) as session:
        result = await session.call_tool("assign_task", {"assignee": "alpha", "instruction": "x"})
    assert "unknown member" in text_of(result)  # b's token cannot reach a's board
    assert board_a.tasks == ()

    async with httpx2.AsyncClient() as http:
        missing = await http.post(endpoint_a.url, json={})
        bridge.revoke(endpoint_a.token)
        revoked = await http.post(
            endpoint_a.url, json={}, headers={"Authorization": f"Bearer {endpoint_a.token}"}
        )
    assert missing.status_code == 401
    assert revoked.status_code == 401
