from __future__ import annotations

import asyncio
from collections.abc import Sequence

import pytest

from agent_teams import (
    Agent,
    ConfigError,
    Hooks,
    Parallel,
    Result,
    RoundRobin,
    RunContext,
    RunStarted,
    ScriptedLLM,
    Sequential,
    supervisor,
)
from agent_teams.context import RunContext as Ctx
from agent_teams.llm import call, say
from agent_teams.runnable import Runnable
from agent_teams.types import Turn, UserTurn


class Echo(Runnable):
    """Records every task it receives and answers with a fixed prefix."""

    def __init__(self, name: str, delay: float = 0.0) -> None:
        self.name = name
        self.description = f"echo {name}"
        self.delay = delay
        self.tasks: list[str] = []

    async def _run(self, task: str, ctx: Ctx) -> Result:
        self.tasks.append(task)
        await asyncio.sleep(self.delay)
        return Result(self.name, f"{self.name} says ok")


def first_user_text(history: Sequence[Turn]) -> str:
    turn = history[0]
    assert isinstance(turn, UserTurn)
    return turn.text


async def test_sequential_hands_off_previous_output() -> None:
    a, b = Echo("a"), Echo("b")
    result = await Sequential("pipe", [a, b]).run("write")

    assert a.tasks == ["write"]
    assert "write" in b.tasks[0]
    assert "a says ok" in b.tasks[0]
    assert result.output == "b says ok"
    assert [c.name for c in result.children] == ["a", "b"]


async def test_parallel_runs_concurrently_without_aggregator() -> None:
    members = [Echo(n, delay=0.1) for n in "abc"]
    loop = asyncio.get_running_loop()
    started = loop.time()
    result = await Parallel("fan", members).run("task")

    assert loop.time() - started < 0.25
    assert result.output == "## a\na says ok\n\n## b\nb says ok\n\n## c\nc says ok"


async def test_parallel_aggregator_sees_all_contributions() -> None:
    judge = Echo("judge")
    result = await Parallel("fan", [Echo("a"), Echo("b")], aggregator=judge).run("task")

    assert result.output == "judge says ok"
    assert "a says ok" in judge.tasks[0]
    assert "b says ok" in judge.tasks[0]
    assert [c.name for c in result.children] == ["a", "b", "judge"]


async def test_round_robin_takes_turns_and_builds_transcript() -> None:
    a, b, final = Echo("a"), Echo("b"), Echo("final")
    result = await RoundRobin("chat", [a, b], rounds=2, finalizer=final).run("topic")

    assert a.tasks[0] == "topic"
    assert "a says ok" in b.tasks[0]
    assert len(a.tasks) == len(b.tasks) == 2
    assert [c.name for c in result.children] == ["a", "b", "a", "b", "final"]
    assert result.output == "final says ok"


async def test_round_robin_without_finalizer_returns_last_turn() -> None:
    result = await RoundRobin("chat", [Echo("a"), Echo("b")], rounds=1).run("topic")
    assert result.output == "b says ok"


async def test_supervisor_delegates_through_tools() -> None:
    writer = Agent(
        "writer",
        ScriptedLLM(lambda _s, history: say(f"draft of {first_user_text(history)}")),
        description="Writes drafts.",
    )
    lead_llm = ScriptedLLM([call("writer", {"task": "a haiku"}), say("approved")])
    lead = supervisor("lead", lead_llm, [writer])

    paths: list[tuple[str, ...]] = []
    hooks = Hooks()
    hooks.on(RunStarted, lambda e: paths.append(e.path))
    result = await lead.run("make a poem", RunContext(hooks))

    assert result.output == "approved"
    assert "writer: Writes drafts." in lead.instructions
    assert paths == [("lead",), ("lead", "writer")]
    tool_turn = lead_llm.calls[1][1][2]
    assert "draft of a haiku" in str(tool_turn)


async def test_teams_nest() -> None:
    inner = Parallel("inner", [Echo("x"), Echo("y")])
    outer = Sequential("outer", [inner, Echo("z")])
    result = await outer.run("t")
    assert result.children[0].name == "inner"
    assert len(result.children[0].children) == 2


@pytest.mark.parametrize("team", [Sequential, Parallel, RoundRobin])
def test_empty_teams_are_rejected(team: type[Sequential]) -> None:
    with pytest.raises(ConfigError):
        team("empty", [])


def test_supervisor_without_members_is_rejected() -> None:
    with pytest.raises(ConfigError):
        supervisor("lead", ScriptedLLM([]), [])
