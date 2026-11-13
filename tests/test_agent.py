from __future__ import annotations

import pytest

from agent_teams import (
    Agent,
    Event,
    Hooks,
    LLMError,
    LLMReplied,
    MaxStepsExceeded,
    RunContext,
    RunFinished,
    RunStarted,
    ScriptedLLM,
    ToolFinished,
    ToolStarted,
    tool,
)
from agent_teams.llm import call, say
from agent_teams.types import AssistantTurn, Reply, ToolTurn, UserTurn


@tool
def double(n: int) -> int:
    """Double a number."""
    return n * 2


async def test_returns_final_text() -> None:
    agent = Agent("solo", ScriptedLLM(["hello"]), instructions="be nice")
    result = await agent.run("hi")
    assert result.output == "hello"
    assert result.name == "solo"


async def test_tool_loop_feeds_results_back() -> None:
    llm = ScriptedLLM([call("double", {"n": 21}), say("42")])
    agent = Agent("calc", llm, instructions="sys", tools=[double])

    assert (await agent.run("double 21")).output == "42"

    system, history, specs = llm.calls[1]
    assert system == "sys"
    assert [s.name for s in specs] == ["double"]
    assert isinstance(history[0], UserTurn)
    assert isinstance(history[1], AssistantTurn)
    assert isinstance(history[2], ToolTurn)
    assert history[2].results[0].content == "42"


async def test_step_budget_is_enforced() -> None:
    llm = ScriptedLLM(lambda _system, _history: call("double", {"n": 1}))
    agent = Agent("loop", llm, tools=[double], max_steps=3)
    with pytest.raises(MaxStepsExceeded):
        await agent.run("go")
    assert len(llm.calls) == 3


@pytest.mark.parametrize("stop", ["refusal", "max_tokens", "other"])
async def test_unusable_replies_raise(stop: str) -> None:
    reply = Reply(AssistantTurn("partial"), stop=stop, detail="why")  # type: ignore[arg-type]
    with pytest.raises(LLMError, match=stop):
        await Agent("a", ScriptedLLM([reply])).run("x")


async def test_emits_events_in_order() -> None:
    seen: list[str] = []
    hooks = Hooks()
    hooks.on(Event, lambda e: seen.append(type(e).__name__))
    agent = Agent("calc", ScriptedLLM([call("double", {"n": 2}), say("4")]), tools=[double])

    await agent.run("x", RunContext(hooks))

    assert seen == [
        RunStarted.__name__,
        LLMReplied.__name__,
        ToolStarted.__name__,
        ToolFinished.__name__,
        LLMReplied.__name__,
        RunFinished.__name__,
    ]


async def test_session_remembers_earlier_messages() -> None:
    llm = ScriptedLLM(["nice to meet you", "your name is Ann"])
    chat = Agent("lead", llm).session()

    await chat.send("I am Ann")
    result = await chat.send("what is my name?")

    assert result.output == "your name is Ann"
    _system, history, _tools = llm.calls[1]
    assert [type(t).__name__ for t in history] == ["UserTurn", "AssistantTurn", "UserTurn"]


async def test_session_rolls_back_failed_messages() -> None:
    refusal = Reply(AssistantTurn(""), stop="refusal")
    chat = Agent("lead", ScriptedLLM(["hi", refusal, "still here"])).session()

    await chat.send("hello")
    with pytest.raises(LLMError):
        await chat.send("bad request")
    assert len(chat.history) == 2

    assert (await chat.send("ok?")).output == "still here"
    chat.reset()
    assert chat.history == []


async def test_session_emits_run_events_under_agent_path() -> None:
    paths: list[tuple[str, ...]] = []
    hooks = Hooks()
    hooks.on(RunStarted, lambda e: paths.append(e.path))
    await Agent("lead", ScriptedLLM(["a"])).session(RunContext(hooks)).send("x")
    assert paths == [("lead",)]
