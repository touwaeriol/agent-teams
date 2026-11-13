"""Runs a nested team with scripted models, so it needs no API key.

python examples/offline_demo.py
"""

from __future__ import annotations

import asyncio

from agent_teams import Agent, Hooks, Parallel, RunContext, ScriptedLLM, Sequential, tool, trace
from agent_teams.llm import call, say


@tool
def word_count(text: str) -> int:
    """Count words in text."""
    return len(text.split())


def build_team() -> Sequential:
    researcher = Agent(
        "researcher", ScriptedLLM(["- tides are predictable\n- turbines are costly"])
    )
    critics = Parallel(
        "critics",
        [
            Agent("optimist", ScriptedLLM(["Predictability is a big win."])),
            Agent("pessimist", ScriptedLLM(["Capital cost is the blocker."])),
        ],
    )
    writer = Agent(
        "writer",
        ScriptedLLM(
            [
                call("word_count", {"text": "Tidal energy is predictable but expensive."}),
                say("Tidal energy is predictable but expensive. (6 words)"),
            ]
        ),
        tools=[word_count],
    )
    return Sequential("tidal-team", [researcher, critics, writer])


async def main() -> None:
    result = await build_team().run("Write about tidal energy", RunContext(trace(Hooks())))
    print("\nFinal:", result.output)


if __name__ == "__main__":
    asyncio.run(main())
