"""Codex employees through a real node daemon (uvicorn console + daemon in-process)."""

from __future__ import annotations

import sys
from collections.abc import AsyncIterator
from pathlib import Path

import pytest

from agent_teams import CodexWorker, RunContext, tool
from agent_teams.server.mcp_bridge import ToolBridge
from agent_teams.tools import Toolkit

from .test_daemon import (
    Console,
    daemon_config,
    enroll,
    free_port,
    remote,
    start_console,
    start_daemon,
)

FAKE_CODEX = [sys.executable, str(Path(__file__).with_name("fake_codex.py"))]


@pytest.fixture
async def daemon_console(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> AsyncIterator[Console]:
    """A console with one online daemon that finds a ``codex`` on its PATH."""
    bin_dir = tmp_path / "bin"
    bin_dir.mkdir()
    codex = bin_dir / "codex"
    codex.write_text("#!/bin/sh\necho 'codex-cli 9.9.9-test'\n")
    codex.chmod(0o755)
    monkeypatch.setenv("PATH", f"{bin_dir}:{Path(sys.executable).parent}:/usr/bin:/bin")
    console = await start_console(free_port(), await enroll())
    daemon = start_daemon(daemon_config(console, tmp_path))
    await console.online()
    yield console
    await daemon.stop()
    await console.stop()


@tool
def shout(text: str) -> str:
    """Upper-case the text."""
    return text.upper()


async def test_hello_reports_the_codex_version(daemon_console: Console) -> None:
    status = daemon_console.registry.status(daemon_console.enrolled.node_id)

    assert status["codex"] == "codex-cli 9.9.9-test"


async def test_codex_employee_calls_team_tools_from_the_daemon(
    daemon_console: Console, tmp_path: Path
) -> None:
    bridge: ToolBridge = daemon_console.app.state.platform.bridge
    endpoint = bridge.expose(Toolkit([shout]), RunContext())
    worker = CodexWorker("cx", remote(daemon_console), codex_command=" ".join(FAKE_CODEX))

    result = await worker.with_tools(endpoint, "approve").run(
        'tool:shout:{"text": "hi"}', RunContext(run_key="c1/t1")
    )

    assert result.output == "tool said: HI"
    assert worker.resumable
    assert (tmp_path / "root" / "cx").is_dir()
