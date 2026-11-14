from __future__ import annotations

import json
import sys
from pathlib import Path
from typing import Any

import pytest

from agent_teams import ClaudeCodeWorker, LocalNode, RunContext
from agent_teams.claude_code import ClaudeCodeError
from agent_teams.claude_session import ClaudeCodeSession, ToolEndpoint

FAKE = f"{sys.executable} {Path(__file__).with_name('fake_claude.py')}"
ENDPOINT = ToolEndpoint("http://console:3001/mcp", "tok", ("assign_task", "list_tasks"))


def session(tmp_path: Path, endpoint: ToolEndpoint | None = ENDPOINT) -> ClaudeCodeSession:
    worker = ClaudeCodeWorker(
        "lead", LocalNode("a", str(tmp_path)), instructions="Be brief.", claude_command=FAKE
    )
    return ClaudeCodeSession(worker, RunContext(run_key="c1"), "You lead a team.", endpoint)


def argv_of(output: str) -> list[str]:
    return list(json.loads(output)["argv"])


def flag(argv: list[str], name: str) -> str:
    return argv[argv.index(name) + 1]


async def test_first_turn_creates_the_session_then_resumes(tmp_path: Path) -> None:
    chat = session(tmp_path)

    first = argv_of((await chat.send("hello")).output)
    second = argv_of((await chat.send("again")).output)

    assert flag(first, "--session-id") == chat.session_id
    assert "--resume" not in first
    assert flag(second, "--resume") == chat.session_id
    assert chat.turns == 2


async def test_turns_carry_mcp_config_and_leader_prompt(tmp_path: Path) -> None:
    argv = argv_of((await session(tmp_path).send("hello")).output)

    config: dict[str, Any] = json.loads(flag(argv, "--mcp-config"))
    server = config["mcpServers"]["team"]
    assert server == {
        "type": "http",
        "url": "http://console:3001/mcp",
        "headers": {"Authorization": "Bearer tok"},
    }
    assert "--strict-mcp-config" in argv
    assert flag(argv, "--allowedTools") == "mcp__team__assign_task,mcp__team__list_tasks"
    assert flag(argv, "--append-system-prompt") == "You lead a team.\n\nBe brief."


async def test_without_an_endpoint_no_mcp_flags(tmp_path: Path) -> None:
    argv = argv_of((await session(tmp_path, endpoint=None).send("hello")).output)
    assert "--mcp-config" not in argv


async def test_a_failed_first_turn_starts_a_fresh_session(tmp_path: Path) -> None:
    chat = session(tmp_path)
    before = chat.session_id
    with pytest.raises(ClaudeCodeError):
        await chat.send("fail")
    assert chat.session_id != before
    assert not chat.started
    argv = argv_of((await chat.send("ok")).output)
    assert flag(argv, "--session-id") == chat.session_id


async def test_snapshot_round_trip(tmp_path: Path) -> None:
    chat = session(tmp_path)
    await chat.send("hello")

    restored = session(tmp_path)
    restored.restore(chat.snapshot())
    argv = argv_of((await restored.send("again")).output)

    assert flag(argv, "--resume") == chat.session_id
    assert restored.turns == 2
    restored.restore({"kind": "agent", "history": []})  # other kinds are ignored
    assert restored.session_id == chat.session_id


def test_reset_starts_a_new_session(tmp_path: Path) -> None:
    chat = session(tmp_path)
    chat.started = True
    before = chat.session_id
    chat.reset()
    assert chat.session_id != before
    assert not chat.started
