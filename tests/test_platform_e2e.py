"""The whole platform: user -> console -> Claude Code leader (MCP) -> employee on a node.

Real HTTP console (uvicorn), real node daemon over WebSocket, real MCP calls;
only Claude Code itself is faked (tests/fake_claude.py).
"""

from __future__ import annotations

import asyncio
import json
import socket
import sys
import time
from collections.abc import AsyncIterator
from pathlib import Path
from typing import Any

import httpx
import pytest
import uvicorn

from agent_teams.daemon import Daemon, DaemonConfig
from agent_teams.server.accounts import MemoryAccounts
from agent_teams.server.app import create_app
from agent_teams.server.broker import MemoryBroker
from agent_teams.server.platform import Services
from agent_teams.server.store import MemoryStore

FAKE_CLAUDE = f"{sys.executable} {Path(__file__).with_name('fake_claude.py')}"
TEAM = f"""\
leader: lead
employees:
  - name: lead
    type: claude_code
    node: box
    claude_command: "{FAKE_CLAUDE}"
  - name: worker
    type: claude_code
    node: box
    description: does the work
    claude_command: "{FAKE_CLAUDE}"
"""


def free_port() -> int:
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return int(sock.getsockname()[1])


@pytest.fixture
async def console() -> AsyncIterator[str]:
    port = free_port()
    url = f"http://127.0.0.1:{port}"

    async def services() -> Services:
        return Services(MemoryStore(), MemoryAccounts(), MemoryBroker(), url)

    server = uvicorn.Server(
        uvicorn.Config(create_app(services), host="127.0.0.1", port=port, log_level="warning")
    )
    serving = asyncio.create_task(server.serve())
    while not server.started:
        await asyncio.sleep(0.01)
    yield url
    server.should_exit = True
    await serving


async def until(check: Any, timeout: float = 30.0) -> Any:
    deadline = time.monotonic() + timeout
    while not (value := await check()):
        assert time.monotonic() < deadline, "timed out"
        await asyncio.sleep(0.1)
    return value


async def test_leader_delegates_over_mcp_and_gets_the_report(console: str, tmp_path: Path) -> None:
    async with httpx.AsyncClient(base_url=console) as api:
        account = {"email": "dev@example.com", "password": "long enough"}
        assert (await api.post("/auth/register", json=account)).status_code == 201
        enrollment = (await api.post("/api/nodes", json={"name": "box"})).json()

        daemon = Daemon(
            DaemonConfig(
                console=console,
                name="box",
                token=enrollment["token"],
                root=tmp_path / "root",
                state=tmp_path / "state",
                allow=frozenset({Path(sys.executable).name}),
            )
        )
        running = asyncio.create_task(daemon.serve_forever())

        async def online() -> bool:
            nodes = (await api.get("/api/nodes")).json()
            return bool(nodes[0]["online"])

        await until(online)
        saved = await api.put("/api/team", json={"config": TEAM})
        assert saved.status_code == 200, saved.text

        conversation = (await api.post("/api/conversations", json={"title": "e2e"})).json()
        base = f"/api/conversations/{conversation['id']}"
        posted = await api.post(f"{base}/messages", json={"content": "delegate:worker:tick:3"})
        assert posted.status_code == 202

        async def two_replies() -> list[dict[str, Any]] | None:
            messages = (await api.get(f"{base}/messages")).json()
            replies = [m for m in messages if m["role"] != "user"]
            return replies if len(replies) >= 2 else None

        first, second = await until(two_replies)
        tasks = (await api.get(f"{base}/tasks")).json()

        running.cancel()
        with pytest.raises(asyncio.CancelledError):
            await running

    assert first["role"] == "leader", first
    assert "t1 assigned to worker" in first["content"]
    session = first["content"].split("(", 1)[1].split(")", 1)[0]
    assert second["content"] == f"report ({session}): [Task t1 · worker · done]"
    (task,) = tasks
    assert (task["assignee"], task["status"]) == ("worker", "done")
    assert task["instruction"] == "tick:3"
    assert json.loads(task["output"])["cwd"] == str((tmp_path / "root" / "worker").resolve())
