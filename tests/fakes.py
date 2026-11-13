"""Test doubles shared across test modules."""

from __future__ import annotations

import asyncio

from agent_teams.context import RunContext
from agent_teams.runnable import Runnable
from agent_teams.types import Result


class Worker(Runnable):
    """Records tasks; optionally waits on a gate or fails."""

    def __init__(self, name: str, gate: asyncio.Event | None = None, *, fail: bool = False) -> None:
        self.name = name
        self.description = f"{name} worker"
        self.gate = gate
        self.fail = fail
        self.tasks: list[str] = []

    async def _run(self, task: str, ctx: RunContext) -> Result:
        self.tasks.append(task)
        if self.gate is not None:
            await self.gate.wait()
        if self.fail:
            raise RuntimeError("broke")
        return Result(self.name, f"{self.name} finished")
