from __future__ import annotations

import asyncio
import json
import sys
from collections.abc import Sequence
from pathlib import Path

import pytest

from agent_teams import (
    ClaudeCodeWorker,
    ConfigError,
    Leader,
    LocalNode,
    MaxStepsExceeded,
    ScriptedLLM,
)
from agent_teams.llm import call, say
from agent_teams.types import AssistantTurn, Reply, ToolCall, ToolTurn, Turn, UserTurn

from .fakes import Worker


def last_user_text(history: Sequence[Turn]) -> str:
    turn = history[-1]
    return turn.text if isinstance(turn, UserTurn) else ""


async def test_leader_answers_while_members_work() -> None:
    gate = asyncio.Event()
    llm = ScriptedLLM(
        [
            call("assign_task", {"assignee": "writer", "instruction": "a poem"}),
            say("on it"),
            say("still working"),
            say("here is the poem"),
        ]
    )
    conversation = Leader("lead", llm, [Worker("writer", gate)]).open()

    conversation.post("write a poem")
    assert (await conversation.reply()).output == "on it"
    assert not conversation.idle

    conversation.post("status?")
    assert (await conversation.reply()).output == "still working"

    gate.set()
    assert (await conversation.reply()).output == "here is the poem"
    assert conversation.idle
    report = last_user_text(llm.calls[-1][1])
    assert report == "[Task t1 · writer · done]\nwriter finished"


async def test_leader_prompt_lists_members_and_board_tools() -> None:
    llm = ScriptedLLM(["hi"])
    conversation = Leader("lead", llm, [Worker("writer")]).open()
    conversation.post("hello")
    await conversation.reply()

    system, _history, specs = llm.calls[0]
    assert "- writer: writer worker" in system
    assert {"assign_task", "list_tasks", "cancel_task"} <= {s.name for s in specs}


def scripted_lead(_system: str, history: Sequence[Turn]) -> Reply:
    """Fan out two tasks, chain a third on both, and finish after its report."""
    last = history[-1]
    if isinstance(last, ToolTurn):
        return say("started")
    if last_user_text(history).startswith("[Task t3"):
        return say("final report")
    if last_user_text(history).startswith("[Task"):
        return say("noted")
    calls = (
        ToolCall("a", "assign_task", {"assignee": "research", "instruction": "facts"}),
        ToolCall("b", "assign_task", {"assignee": "critic", "instruction": "risks"}),
        ToolCall(
            "c",
            "assign_task",
            {"assignee": "writer", "instruction": "article", "depends_on": ["t1", "t2"]},
        ),
    )
    return Reply(AssistantTurn("", calls), stop="tool_use")


async def test_run_waits_for_all_tasks_then_returns_final_answer() -> None:
    research, critic, writer = Worker("research"), Worker("critic"), Worker("writer")
    leader = Leader("lead", ScriptedLLM(scripted_lead), [research, critic, writer])

    result = await leader.run("write an article")

    assert result.output == "final report"
    assert [c.name for c in result.children] == ["t1:research", "t2:critic", "t3:writer"]
    assert "research finished" in writer.tasks[0]
    assert "critic finished" in writer.tasks[0]


async def test_run_gives_up_after_max_turns() -> None:
    llm = ScriptedLLM(
        lambda _s, history: (
            call("assign_task", {"assignee": "w", "instruction": "again"})
            if not isinstance(history[-1], ToolTurn)
            else say("waiting")
        )
    )
    with pytest.raises(MaxStepsExceeded):
        await Leader("lead", llm, [Worker("w")], max_turns=3).run("loop forever")


def test_leader_needs_members() -> None:
    with pytest.raises(ConfigError):
        Leader("lead", ScriptedLLM([]), [])


async def test_leader_dispatches_to_claude_code_on_two_nodes(tmp_path: Path) -> None:
    fake = f"{sys.executable} {Path(__file__).with_name('fake_claude.py')}"
    api_dir, web_dir = tmp_path / "api", tmp_path / "web"
    api_dir.mkdir()
    web_dir.mkdir()
    backend = ClaudeCodeWorker("backend", LocalNode("node-a", str(api_dir)), claude_command=fake)
    frontend = ClaudeCodeWorker("frontend", LocalNode("node-b", str(web_dir)), claude_command=fake)

    def lead(_system: str, history: Sequence[Turn]) -> Reply:
        last = history[-1]
        if isinstance(last, ToolTurn):
            return say("both started")
        if last_user_text(history).startswith("[Task"):
            return say("report received")
        calls = (
            ToolCall("a", "assign_task", {"assignee": "backend", "instruction": "add endpoint"}),
            ToolCall("b", "assign_task", {"assignee": "frontend", "instruction": "add page"}),
        )
        return Reply(AssistantTurn("", calls), stop="tool_use")

    result = await Leader("lead", ScriptedLLM(lead), [backend, frontend]).run("ship the feature")

    assert result.output == "report received"
    outputs = {child.name: json.loads(child.output) for child in result.children}
    assert outputs["t1:backend"]["cwd"] == str(api_dir)
    assert outputs["t2:frontend"]["cwd"] == str(web_dir)
    assert outputs["t2:frontend"]["argv"][:2] == ["-p", "add page"]
