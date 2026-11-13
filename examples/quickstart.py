"""A supervisor team on Claude, defined in Python.

export ANTHROPIC_API_KEY=...   # or `ant auth login`
python examples/quickstart.py "Plan a three-day trip to Kyoto"
"""

from __future__ import annotations

import asyncio
import sys
from datetime import date

from agent_teams import Agent, AnthropicLLM, Hooks, RunContext, supervisor, tool, trace


@tool
def today() -> str:
    """Return today's date in ISO format."""
    return date.today().isoformat()


async def main(task: str) -> None:
    llm = AnthropicLLM()  # claude-opus-5-5, adaptive thinking, high effort
    planner = Agent(
        "planner",
        llm,
        description="Drafts day-by-day itineraries.",
        instructions="Draft a realistic day-by-day plan for the task you receive.",
        tools=[today],
    )
    critic = Agent(
        "critic",
        AnthropicLLM(effort="medium"),
        description="Finds gaps and risks in a plan.",
        instructions="Point out gaps, risks and unrealistic timings in the plan you receive.",
    )
    lead = supervisor("lead", llm, [planner, critic])

    hooks = trace(Hooks(), lambda line: print(line, file=sys.stderr))
    result = await lead.run(task, RunContext(hooks))
    print(result.output)


if __name__ == "__main__":
    asyncio.run(main(" ".join(sys.argv[1:]) or "Plan a three-day trip to Kyoto"))
