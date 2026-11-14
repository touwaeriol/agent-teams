from __future__ import annotations

import asyncio
import json
import os
import sys
from pathlib import Path

import pytest

from agent_teams import Hooks, RunContext, WorkerProgress
from agent_teams.claude_code import ClaudeCodeError, ClaudeCodeWorker, describe
from agent_teams.nodes import LocalNode, NodeCommandError

FAKE = f"{sys.executable} {Path(__file__).with_name('fake_claude.py')}"


def worker(tmp_path: Path, **options: object) -> ClaudeCodeWorker:
    return ClaudeCodeWorker(
        "coder",
        LocalNode("here", str(tmp_path)),
        claude_command=FAKE,
        **options,  # type: ignore[arg-type]
    )


def test_command_includes_only_configured_flags(tmp_path: Path) -> None:
    command = worker(
        tmp_path,
        instructions="Be careful.",
        model="opus",
        allowed_tools=["Read", "Bash(git *)"],
        max_turns=5,
        extra_args=["--bare"],
    ).command("do it")

    assert command[2:8] == [
        "-p",
        "do it",
        "--output-format",
        "stream-json",
        "--verbose",
        "--append-system-prompt",
    ]
    assert command[command.index("--allowedTools") + 1] == "Read,Bash(git *)"
    assert command[command.index("--max-turns") + 1] == "5"
    assert command[command.index("--permission-mode") + 1] == "acceptEdits"
    assert "--disallowedTools" not in command
    assert command[-1] == "--bare"


async def test_runs_claude_and_returns_the_result(tmp_path: Path) -> None:
    progress: list[str] = []
    hooks = Hooks()
    hooks.on(WorkerProgress, lambda e: progress.append(f"{e.source} {e.text}"))

    result = await worker(tmp_path).run("add a test", RunContext(hooks, ("lead",)))

    payload = json.loads(result.output)
    assert payload["cwd"] == str(tmp_path / "coder")  # each employee has its own directory
    assert payload["argv"][:2] == ["-p", "add a test"]
    assert progress[0] == f"lead/coder session s-1 in {tmp_path / 'coder'}"
    assert 'lead/coder Bash {"command": "ls"}' in progress
    assert "lead/coder not json noise" in progress
    assert progress[-1] == "lead/coder finished: success in 2 turns, $0.0123"


async def test_error_results_raise(tmp_path: Path) -> None:
    with pytest.raises(ClaudeCodeError, match="error_max_turns: too many turns"):
        await worker(tmp_path).run("fail")


async def test_crashes_raise_with_stderr(tmp_path: Path) -> None:
    with pytest.raises(NodeCommandError, match="boom on stderr"):
        await worker(tmp_path).run("crash")


async def test_cancelling_kills_the_process(tmp_path: Path) -> None:
    pidfile = tmp_path / "pid"
    running = asyncio.create_task(worker(tmp_path).run(f"slow:{pidfile}"))
    for _ in range(200):
        if pidfile.exists() and pidfile.read_text():
            break
        await asyncio.sleep(0.02)
    pid = int(pidfile.read_text())

    running.cancel()
    with pytest.raises(asyncio.CancelledError):
        await running
    await asyncio.sleep(0.2)
    with pytest.raises(ProcessLookupError):
        os.kill(pid, 0)


def test_describe_ignores_unknown_events() -> None:
    assert describe({"type": "stream_event"}) == []
    assert describe({"type": "system", "subtype": "api_retry"}) == []
