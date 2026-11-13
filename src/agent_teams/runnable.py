"""The common interface of agents and teams (composite pattern)."""

from __future__ import annotations

from abc import ABC, abstractmethod

from .context import RunContext
from .hooks import RunFinished, RunStarted
from .types import Result


class Runnable(ABC):
    """Anything that turns a task into a :class:`Result`.

    Agents and teams share this interface, so teams can contain other teams.
    Subclasses implement :meth:`_run`; :meth:`run` adds context and events.
    """

    name: str
    description: str

    async def run(self, task: str, ctx: RunContext | None = None) -> Result:
        ctx = (ctx or RunContext()).enter(self.name)
        await ctx.emit(RunStarted(ctx.path, task))
        result = await self._run(task, ctx)
        await ctx.emit(RunFinished(ctx.path, result))
        return result

    @abstractmethod
    async def _run(self, task: str, ctx: RunContext) -> Result: ...
