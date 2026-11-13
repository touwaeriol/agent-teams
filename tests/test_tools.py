from __future__ import annotations

import asyncio
from typing import Any

import pytest

from agent_teams import RunContext, Tool, Toolkit, tool
from agent_teams.tools import tool_name
from agent_teams.types import ToolCall


@tool
def add(a: int, b: int = 1) -> int:
    """Add two integers.

    Args:
        a: The first number.
        b (int): The second number.

    Returns:
        The sum.
    """
    return a + b


@tool(name="shout", description="Upper-case text.")
async def loud(text: str, ctx: RunContext) -> str:
    return f"{text.upper()}@{'/'.join(ctx.path)}"


@tool
def explode() -> str:
    raise RuntimeError("boom")


@tool
def profile(name: str) -> dict[str, Any]:
    return {"name": name, "tags": ["a"]}


async def run_one(toolkit: Toolkit, name: str, /, **arguments: Any) -> Any:
    ctx = RunContext(path=("root",))
    (result,) = await toolkit.execute([ToolCall("1", name, arguments)], ctx)
    return result


def test_schema_comes_from_signature_and_docstring() -> None:
    assert add.name == "add"
    assert add.description == "Add two integers."
    assert add.input_schema["required"] == ["a"]
    assert add.input_schema["properties"]["a"]["description"] == "The first number."
    assert add.input_schema["properties"]["b"]["description"] == "The second number."
    assert add.input_schema["additionalProperties"] is False


def test_context_parameter_is_hidden_and_overrides_apply() -> None:
    assert loud.name == "shout"
    assert loud.description == "Upper-case text."
    assert list(loud.input_schema["properties"]) == ["text"]


async def test_executes_sync_and_async_tools() -> None:
    toolkit = Toolkit([add, loud, profile])
    assert (await run_one(toolkit, "add", a=2, b=3)).content == "5"
    assert (await run_one(toolkit, "shout", text="hi")).content == "HI@root"
    assert (await run_one(toolkit, "profile", name="x")).content == '{"name": "x", "tags": ["a"]}'


@pytest.mark.parametrize(
    ("name", "arguments", "fragment"),
    [
        ("add", {"a": "not a number"}, "ValidationError"),
        ("add", {"a": 1, "c": 2}, "ValidationError"),
        ("explode", {}, "RuntimeError: boom"),
        ("missing", {}, "Unknown tool: missing"),
    ],
)
async def test_failures_become_error_results(
    name: str, arguments: dict[str, Any], fragment: str
) -> None:
    result = await run_one(Toolkit([add, explode]), name, **arguments)
    assert result.is_error
    assert fragment in result.content


async def test_calls_run_concurrently_and_keep_order() -> None:
    @tool
    async def wait(seconds: float) -> float:
        await asyncio.sleep(seconds)
        return seconds

    calls = [ToolCall(str(i), "wait", {"seconds": s}) for i, s in enumerate([0.2, 0.1, 0.0])]
    loop = asyncio.get_running_loop()
    started = loop.time()
    results = await Toolkit([wait]).execute(calls, RunContext())
    assert loop.time() - started < 0.35
    assert [r.call_id for r in results] == ["0", "1", "2"]


def test_duplicate_names_are_rejected() -> None:
    with pytest.raises(ValueError, match="duplicate"):
        Toolkit([add, add])


def test_tool_name_is_sanitized() -> None:
    assert tool_name("research team #1") == "research_team__1"
    assert len(tool_name("x" * 100)) == 64
    assert tool_name("") == "tool"


def test_tool_is_plain_data() -> None:
    assert isinstance(add, Tool)
    assert add.spec.name == "add"
