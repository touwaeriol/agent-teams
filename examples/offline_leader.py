"""Shows the non-blocking leader with scripted models (no API key needed).

    python examples/offline_leader.py

The leader assigns two tasks and answers at once; while the members are still
working, the user asks for progress and gets an answer; then the reports
arrive and the leader wraps up.
"""

from __future__ import annotations

import asyncio
from collections.abc import Sequence

from agent_teams import Agent, Hooks, Leader, RunContext, ScriptedLLM, trace
from agent_teams.llm import LLM, say
from agent_teams.types import AssistantTurn, Reply, ToolCall, ToolSpec, Turn


class Slow:
    """Wraps any LLM and adds latency, to make background work visible."""

    def __init__(self, llm: LLM, seconds: float) -> None:
        self.llm = llm
        self.seconds = seconds

    async def complete(
        self, system: str, history: Sequence[Turn], tools: Sequence[ToolSpec]
    ) -> Reply:
        await asyncio.sleep(self.seconds)
        return await self.llm.complete(system, history, tools)


def assign_two_tasks() -> Reply:
    calls = (
        ToolCall("1", "assign_task", {"assignee": "researcher", "instruction": "调研三家竞品"}),
        ToolCall(
            "2",
            "assign_task",
            {"assignee": "writer", "instruction": "写对比方案", "depends_on": ["t1"]},
        ),
    )
    return Reply(AssistantTurn("", calls), stop="tool_use")


def build_leader() -> Leader:
    lead_llm = ScriptedLLM(
        [
            assign_two_tasks(),
            say("已安排：researcher 调研（t1），writer 在调研完成后写方案（t2）。"),
            say("researcher 还在调研，writer 等着接手。"),
            say("调研完成，writer 开始写方案。"),
            say("方案完成：A 最便宜，B 功能最全，推荐 B。"),
        ]
    )
    researcher = Agent(
        "researcher", Slow(ScriptedLLM(["A 便宜、B 功能全、C 一般"]), 1.0), description="调研员"
    )
    writer = Agent("writer", Slow(ScriptedLLM(["对比方案：推荐 B"]), 1.0), description="写手")
    return Leader("lead", lead_llm, [researcher, writer])


async def main() -> None:
    conversation = build_leader().open(RunContext(trace(Hooks())))

    conversation.post("调研三家竞品，然后写一份对比方案")
    print("lead:", (await conversation.reply()).output)

    conversation.post("进度怎么样？")  # asked while the members are still working
    print("lead:", (await conversation.reply()).output)
    print(conversation.status())

    while not conversation.idle:  # task reports arrive as messages
        print("lead:", (await conversation.reply()).output)


if __name__ == "__main__":
    asyncio.run(main())
