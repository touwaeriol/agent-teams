from __future__ import annotations

import asyncio

import pytest

from agent_teams import (
    Hooks,
    RunContext,
    TaskAssigned,
    TaskBoard,
    TaskFinished,
    TaskStarted,
    Toolkit,
)
from agent_teams.types import Task, ToolCall

from .fakes import Worker


def board_with(*members: Worker, hooks: Hooks | None = None) -> TaskBoard:
    return TaskBoard(members, RunContext(hooks or Hooks(), ("lead",)))


async def test_assign_returns_before_the_member_finishes() -> None:
    gate = asyncio.Event()
    board = board_with(Worker("writer", gate))
    finished: list[Task] = []
    board.listeners.append(finished.append)

    task = await board.assign("writer", "draft")
    await asyncio.sleep(0)

    assert board.get(task.id).status == "running"
    assert board.busy
    gate.set()
    await board.wait()
    assert board.get(task.id).status == "done"
    assert board.get(task.id).output == "writer finished"
    assert [t.id for t in finished] == [task.id]
    assert not board.busy


async def test_dependent_tasks_receive_prerequisite_outputs() -> None:
    research, writer = Worker("research"), Worker("writer")
    board = board_with(research, writer)

    first = await board.assign("research", "facts")
    second = await board.assign("writer", "article", depends_on=[first.id])
    await board.wait()

    assert board.get(second.id).status == "done"
    assert "article" in writer.tasks[0]
    assert "research finished" in writer.tasks[0]


async def test_failures_propagate_to_dependents() -> None:
    board = board_with(Worker("bad", fail=True), Worker("writer"))

    first = await board.assign("bad", "x")
    second = await board.assign("writer", "y", depends_on=[first.id])
    await board.wait()

    assert board.get(first.id).status == "failed"
    assert "RuntimeError: broke" in board.get(first.id).output
    assert board.get(second.id).status == "failed"
    assert "DependencyFailed" in board.get(second.id).output


async def test_cancel_stops_a_running_task() -> None:
    board = board_with(Worker("slow", asyncio.Event()))
    finished: list[Task] = []
    board.listeners.append(finished.append)

    task = await board.assign("slow", "x")
    await asyncio.sleep(0)
    board.cancel(task.id)
    await board.wait()

    assert board.get(task.id).status == "cancelled"
    assert finished[0].status == "cancelled"


async def test_close_cancels_everything() -> None:
    board = board_with(Worker("slow", asyncio.Event()))
    await board.assign("slow", "a")
    await board.assign("slow", "b")
    board.close()
    await board.wait()
    assert {t.status for t in board.tasks} == {"cancelled"}


async def test_invalid_assignments_are_rejected() -> None:
    board = board_with(Worker("writer"))
    with pytest.raises(ValueError, match="unknown member"):
        await board.assign("nobody", "x")
    with pytest.raises(ValueError, match="unknown prerequisite"):
        await board.assign("writer", "x", depends_on=["t99"])
    with pytest.raises(KeyError):
        board.get("t99")


async def test_tools_drive_the_board() -> None:
    board = board_with(Worker("writer", asyncio.Event()))
    toolkit = Toolkit(board.tools())
    ctx = RunContext()

    assert [s.name for s in toolkit.specs] == ["assign_task", "list_tasks", "cancel_task"]
    calls = [
        ToolCall("1", "assign_task", {"assignee": "writer", "instruction": "poem"}),
        ToolCall("2", "assign_task", {"assignee": "ghost", "instruction": "x"}),
    ]
    assigned, rejected = await toolkit.execute(calls, ctx)
    assert assigned.content.startswith("t1 assigned to writer")
    assert rejected.is_error
    assert "unknown member" in rejected.content

    (listed,) = await toolkit.execute([ToolCall("3", "list_tasks", {})], ctx)
    assert listed.content.startswith("t1 [")
    (cancelled,) = await toolkit.execute([ToolCall("4", "cancel_task", {"task_id": "t1"})], ctx)
    assert cancelled.content == "cancelling t1"
    await board.wait()


async def test_board_emits_task_events() -> None:
    seen: list[str] = []
    hooks = Hooks()
    hooks.on(TaskAssigned, lambda e: seen.append(f"assigned {e.task.id} at {e.source}"))
    hooks.on(TaskStarted, lambda e: seen.append(f"started {e.task.id} {e.task.status}"))
    hooks.on(TaskFinished, lambda e: seen.append(f"finished {e.task.id} {e.task.status}"))
    board = board_with(Worker("writer"), hooks=hooks)

    await board.assign("writer", "x")
    await board.wait()
    await asyncio.sleep(0)

    assert seen == ["assigned t1 at lead", "started t1 running", "finished t1 done"]


def test_summary_without_tasks() -> None:
    assert board_with(Worker("w")).summary() == "No tasks yet."
