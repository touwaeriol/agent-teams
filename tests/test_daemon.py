"""Console <-> node daemon over a real WebSocket (uvicorn in-process)."""

from __future__ import annotations

import asyncio
import json
import os
import socket
import sys
from collections.abc import AsyncIterator
from dataclasses import dataclass
from pathlib import Path

import pytest
import uvicorn
from fastapi import FastAPI

from agent_teams import ClaudeCodeWorker, RunContext
from agent_teams.daemon import Daemon, DaemonConfig
from agent_teams.nodes import NodeCommandError
from agent_teams.remote import NodeOffline, NodeRegistry, RemoteNode
from agent_teams.server.accounts import MemoryAccounts
from agent_teams.server.app import create_app
from agent_teams.server.broker import MemoryBroker
from agent_teams.server.platform import Services
from agent_teams.server.security import new_token
from agent_teams.server.store import MemoryStore

FAKE = Path(__file__).with_name("fake_claude.py")
CLAUDE = [sys.executable, str(FAKE)]


def free_port() -> int:
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return int(sock.getsockname()[1])


@dataclass
class Enrolled:
    """A node registered for a user, as the web console would store it."""

    accounts: MemoryAccounts
    node_id: str
    token: str


async def enroll() -> Enrolled:
    accounts = MemoryAccounts()
    user = await accounts.create_user("owner@example.com", "unused", "user")
    token, token_hash = new_token()
    node = await accounts.create_node(user.id, "n1", token_hash)
    return Enrolled(accounts, node.id, token)


@dataclass
class Console:
    port: int
    enrolled: Enrolled
    app: FastAPI
    server: uvicorn.Server
    task: asyncio.Task[None]

    @property
    def url(self) -> str:
        return f"http://127.0.0.1:{self.port}"

    @property
    def registry(self) -> NodeRegistry:
        registry: NodeRegistry = self.app.state.platform.registry
        return registry

    async def online(self, timeout: float = 10) -> None:
        await self.registry.link(self.enrolled.node_id, timeout)

    async def stop(self) -> None:
        self.server.should_exit = True
        await self.task


async def start_console(port: int, enrolled: Enrolled) -> Console:
    async def services() -> Services:
        url = f"http://127.0.0.1:{port}"
        return Services(MemoryStore(), enrolled.accounts, MemoryBroker(), url)

    app = create_app(services)
    config = uvicorn.Config(
        app, host="127.0.0.1", port=port, log_level="warning", timeout_graceful_shutdown=1
    )
    server = uvicorn.Server(config)
    task = asyncio.create_task(server.serve())
    while not server.started:
        await asyncio.sleep(0.01)
    return Console(port, enrolled, app, server, task)


def daemon_config(console: Console, tmp_path: Path, token: str | None = None) -> DaemonConfig:
    return DaemonConfig(
        console=console.url,
        name="n1",
        token=token or console.enrolled.token,
        root=tmp_path / "root",
        state=tmp_path / "state",
        allow=frozenset({Path(sys.executable).name}),
    )


@dataclass
class Running:
    daemon: Daemon
    task: asyncio.Task[None]

    async def stop(self) -> None:
        self.task.cancel()
        with pytest.raises(asyncio.CancelledError):
            await self.task


def start_daemon(config: DaemonConfig) -> Running:
    daemon = Daemon(config)
    return Running(daemon, asyncio.create_task(daemon.serve_forever()))


@pytest.fixture
async def console() -> AsyncIterator[Console]:
    running = await start_console(free_port(), await enroll())
    yield running
    await running.stop()


@pytest.fixture
async def node(console: Console, tmp_path: Path) -> AsyncIterator[Running]:
    running = start_daemon(daemon_config(console, tmp_path))
    await console.online()
    yield running
    await running.stop()


def remote(console: Console, connect_timeout: float = 60.0) -> RemoteNode:
    key = console.enrolled.node_id
    return RemoteNode("n1", console.registry, key=key, connect_timeout=connect_timeout)


async def collect(lines: AsyncIterator[str]) -> list[str]:
    return [line async for line in lines]


def ticks(lines: list[str]) -> list[str]:
    """The fake's "tick N" progress texts, in order."""
    events = [json.loads(line) for line in lines if line.startswith("{")]
    blocks = [e["message"]["content"][0] for e in events if e["type"] == "assistant"]
    return [b["text"] for b in blocks if b.get("text", "").startswith("tick")]


def result_of(lines: list[str]) -> dict[str, object]:
    final = json.loads(lines[-1])
    assert final["type"] == "result"
    return dict(json.loads(final["result"]))


async def test_runs_on_the_daemon_inside_its_root(
    console: Console, node: Running, tmp_path: Path
) -> None:
    lines = await collect(remote(console).stream([*CLAUDE, "-p", "hi"], "proj/a", "k1"))

    assert result_of(lines)["cwd"] == str((tmp_path / "root" / "proj" / "a").resolve())
    status = console.registry.status(console.enrolled.node_id)
    assert status["online"] is True
    assert status["max_runs"] == 4


async def test_worker_gets_its_own_directory_and_progress(
    console: Console, node: Running, tmp_path: Path
) -> None:
    worker = ClaudeCodeWorker("backend", remote(console), claude_command=" ".join(CLAUDE))
    result = await worker.run("add endpoint", RunContext(run_key="conv1/t1"))

    assert json.loads(result.output)["cwd"] == str((tmp_path / "root" / "backend").resolve())
    assert worker.resumable


async def test_resumes_after_the_connection_drops(console: Console, node: Running) -> None:
    lines: list[str] = []

    async def follow() -> None:
        async for line in remote(console).stream([*CLAUDE, "-p", "tick:12"], "w", "k-drop"):
            lines.append(line)

    following = asyncio.create_task(follow())
    while sum("tick" in line for line in lines) < 3:
        await asyncio.sleep(0.02)
    assert node.daemon.connection is not None
    await node.daemon.connection.close()  # network blip
    await following

    assert ticks(lines) == [f"tick {n}" for n in range(12)]
    assert json.loads(lines[-1])["type"] == "result"


async def test_reattaches_after_the_console_restarts(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    marker = tmp_path / "starts"
    monkeypatch.setenv("FAKE_CLAUDE_MARKER", str(marker))
    port = free_port()
    enrolled = await enroll()  # the accounts database outlives the console process
    first = await start_console(port, enrolled)
    daemon = start_daemon(daemon_config(first, tmp_path))
    await first.online()
    command = [*CLAUDE, "-p", "tick:10"]
    seen: list[str] = []

    async def follow() -> None:
        async for line in remote(first).stream(command, "w", "conv/t1/coder"):
            seen.append(line)

    abandoned = asyncio.create_task(follow())
    while not any("tick 2" in line for line in seen):
        await asyncio.sleep(0.02)
    await first.stop()  # the console goes away mid-run

    second = await start_console(port, enrolled)
    await second.online(15)
    lines = await collect(remote(second).stream(command, "w", "conv/t1/coder"))

    assert ticks(lines) == [f"tick {n}" for n in range(10)]  # full replay from the start
    assert json.loads(lines[-1])["type"] == "result"
    assert len(marker.read_text().splitlines()) == 1  # the work ran once
    abandoned.cancel()
    await second.stop()
    await daemon.stop()


async def test_cancelling_kills_the_remote_process(
    console: Console, node: Running, tmp_path: Path
) -> None:
    pidfile = tmp_path / "pid"
    stream = remote(console).stream([*CLAUDE, "-p", f"slow:{pidfile}"], "w", "k-slow")
    following = asyncio.create_task(collect(stream))
    for _ in range(300):
        if pidfile.exists() and pidfile.read_text():
            break
        await asyncio.sleep(0.02)
    pid = int(pidfile.read_text())

    following.cancel()
    with pytest.raises(asyncio.CancelledError):
        await following
    for _ in range(100):
        try:
            os.kill(pid, 0)
        except ProcessLookupError:
            break
        await asyncio.sleep(0.05)
    with pytest.raises(ProcessLookupError):
        os.kill(pid, 0)


@pytest.mark.parametrize(
    ("command", "cwd", "fragment"),
    [
        (["/bin/echo", "hi"], "w", "executable not allowed"),
        ([*CLAUDE, "-p", "hi"], "../outside", "outside"),
        ([*CLAUDE, "-p", "hi"], "/etc", "outside"),
    ],
)
async def test_node_policy_rejects_other_commands_and_paths(
    console: Console, node: Running, command: list[str], cwd: str, fragment: str
) -> None:
    with pytest.raises(NodeCommandError, match=rf"exit 126: .*{fragment}"):
        await collect(remote(console).stream(command, cwd, f"k-{fragment}-{cwd}"))


async def test_bad_tokens_are_rejected(console: Console, tmp_path: Path) -> None:
    impostor = start_daemon(daemon_config(console, tmp_path, token="wrong"))
    with pytest.raises(NodeOffline):
        await collect(remote(console, connect_timeout=1.0).stream([*CLAUDE, "-p", "x"], "w"))
    await impostor.stop()


async def test_finished_runs_survive_a_daemon_restart(
    console: Console, tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    marker = tmp_path / "starts"
    monkeypatch.setenv("FAKE_CLAUDE_MARKER", str(marker))
    config = daemon_config(console, tmp_path)
    first = start_daemon(config)
    await console.online()
    command = [*CLAUDE, "-p", "hi"]
    before = await collect(remote(console).stream(command, "w", "k-persist"))
    await first.stop()

    second = start_daemon(config)
    await console.online()
    after = await collect(remote(console).stream(command, "w", "k-persist"))

    assert after == before
    assert len(marker.read_text().splitlines()) == 1
    await second.stop()


async def test_runs_interrupted_by_a_daemon_restart_report_lost(
    console: Console, tmp_path: Path
) -> None:
    config = daemon_config(console, tmp_path)
    runs = config.state / "runs"
    runs.mkdir(parents=True)
    daemon = start_daemon(config)
    await console.online()
    log = daemon.daemon.runner._log_path("k-lost")
    log.write_text(json.dumps({"seq": 1, "line": "partial"}) + "\n", encoding="utf-8")

    with pytest.raises(NodeCommandError, match=r"exit 137: .*restarted"):
        await collect(remote(console).stream([*CLAUDE, "-p", "hi"], "w", "k-lost"))
    await daemon.stop()
