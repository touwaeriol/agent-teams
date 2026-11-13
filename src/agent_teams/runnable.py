"""The common interface of agents and teams (composite pattern)."""

from __future__ import annotations

from abc import ABC, abstractmethod
from collections.abc import Awaitable, Callable

from .context import RunContext
from .hooks import RunFinished, RunStarted
from .types import Result

Work = Callable[[str, RunContext], Awaitable[Result]]


class Runnable(ABC):
    """Anything that turns a task into a :class:`Result`.

    Agents and teams share this interface, so teams can contain other teams.
    Subclasses implement :meth:`_run`; :meth:`run` adds context and events.
    """

    name: str
    description: str

    async def run(self, task: str, ctx: RunContext | None = None) -> Result:
        return await self.traced(task, ctx, self._run)

    async def traced(self, task: str, ctx: RunContext | None, work: Work) -> Result:
        """Run ``work`` one level deeper in ``ctx``, wrapped in start/finish events."""
        ctx = (ctx or RunContext()).enter(self.name)
        await ctx.emit(RunStarted(ctx.path, task))
        result = await work(task, ctx)
        await ctx.emit(RunFinished(ctx.path, result))
        return result

    @abstractmethod
    async def _run(self, task: str, ctx: RunContext) -> Result: ...
