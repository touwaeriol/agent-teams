"""Team strategies. Every team is a :class:`Runnable`, so teams nest freely."""

from __future__ import annotations

import asyncio
import itertools
from collections.abc import Callable, Iterable, Sequence
from dataclasses import dataclass

from .agent import Agent
from .context import RunContext
from .errors import ConfigError
from .llm.base import LLM
from .runnable import Runnable
from .tools import Tool, as_tool, tool_name
from .types import Result

Handoff = Callable[[str, Result], str]
Merge = Callable[[str, Sequence[Result]], str]

SUPERVISOR_INSTRUCTIONS = (
    "You lead a team and talk directly with the user. Answer simple requests yourself. "
    "For real work, break it into tasks and hand each one to the right member by calling "
    "their tool with complete, self-contained instructions; call several tools in the "
    "same turn when tasks are independent so they run in parallel. Check the results, "
    "then report back to the user yourself. Ask the user when the request is unclear."
)


def format_results(results: Iterable[Result]) -> str:
    """Render results as Markdown sections, one per contributor."""
    return "\n\n".join(f"## {r.name}\n{r.output}" for r in results)


def pass_previous(task: str, previous: Result) -> str:
    """Default :data:`Handoff`: the original task plus the previous member's output."""
    return f"{task}\n\n---\nOutput from {previous.name}:\n{previous.output}"


def combine(task: str, results: Sequence[Result]) -> str:
    """Default :data:`Merge`: the original task plus every member's output."""
    if not results:
        return task
    return f"{task}\n\n---\nContributions:\n\n{format_results(results)}"


@dataclass(eq=False)
class Team(Runnable):
    """Shared fields and helpers; subclasses implement the strategy in ``_run``."""

    name: str
    members: Sequence[Runnable]
    description: str = ""

    def __post_init__(self) -> None:
        if not self.members:
            raise ConfigError(f"team '{self.name}' has no members")

    async def _conclude(
        self,
        ctx: RunContext,
        results: Sequence[Result],
        finalizer: Runnable | None,
        prompt: str,
        default: str,
    ) -> Result:
        if finalizer is None:
            return Result(self.name, default, tuple(results))
        final = await finalizer.run(prompt, ctx)
        return Result(self.name, final.output, (*results, final))


@dataclass(eq=False)
class Sequential(Team):
    """A pipeline: each member works on the previous member's output."""

    handoff: Handoff = pass_previous

    async def _run(self, task: str, ctx: RunContext) -> Result:
        results: list[Result] = []
        prompt = task
        for member in self.members:
            results.append(await member.run(prompt, ctx))
            prompt = self.handoff(task, results[-1])
        return Result(self.name, results[-1].output, tuple(results))


@dataclass(eq=False)
class Parallel(Team):
    """Fan-out: every member gets the same task concurrently.

    With an ``aggregator`` the merged contributions are condensed into one
    answer; without one, the output is the contributions themselves.
    """

    aggregator: Runnable | None = None
    merge: Merge = combine

    async def _run(self, task: str, ctx: RunContext) -> Result:
        results = await asyncio.gather(*(m.run(task, ctx) for m in self.members))
        prompt = self.merge(task, results)
        return await self._conclude(ctx, results, self.aggregator, prompt, format_results(results))


@dataclass(eq=False)
class RoundRobin(Team):
    """A discussion: members take turns for ``rounds`` rounds, seeing the transcript."""

    rounds: int = 2
    finalizer: Runnable | None = None
    merge: Merge = combine

    async def _run(self, task: str, ctx: RunContext) -> Result:
        transcript: list[Result] = []
        speakers = itertools.chain.from_iterable(itertools.repeat(self.members, self.rounds))
        for member in speakers:
            transcript.append(await member.run(self.merge(task, transcript), ctx))
        prompt = self.merge(task, transcript)
        return await self._conclude(ctx, transcript, self.finalizer, prompt, transcript[-1].output)


def supervisor(
    name: str,
    llm: LLM,
    members: Sequence[Runnable],
    *,
    instructions: str = SUPERVISOR_INSTRUCTIONS,
    description: str = "",
    tools: Iterable[Tool] = (),
    max_steps: int = 20,
) -> Agent:
    """An agent that delegates to ``members`` through auto-generated tools."""
    if not members:
        raise ConfigError(f"supervisor '{name}' has no members")
    roster = "\n".join(f"- {tool_name(m.name)}: {m.description or m.name}" for m in members)
    return Agent(
        name=name,
        llm=llm,
        instructions=f"{instructions}\n\nTeam members (call them as tools):\n{roster}",
        description=description,
        tools=[*(as_tool(m) for m in members), *tools],
        max_steps=max_steps,
    )
