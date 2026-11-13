"""A single agent: an LLM, instructions and tools, driven by a tool-use loop."""

from __future__ import annotations

from collections.abc import Iterable
from dataclasses import dataclass, field

from .context import RunContext
from .errors import LLMError, MaxStepsExceeded
from .hooks import LLMReplied
from .llm.base import LLM
from .runnable import Runnable
from .tools import Tool, Toolkit
from .types import Reply, Result, ToolTurn, Turn, UserTurn


@dataclass(eq=False)
class Agent(Runnable):
    """Runs the model, executes requested tools, and repeats until a final answer."""

    name: str
    llm: LLM
    instructions: str = ""
    description: str = ""
    tools: Iterable[Tool] = ()
    max_steps: int = 20
    toolkit: Toolkit = field(init=False, repr=False)

    def __post_init__(self) -> None:
        self.toolkit = Toolkit(self.tools)

    async def _run(self, task: str, ctx: RunContext) -> Result:
        history: list[Turn] = [UserTurn(task)]
        for _ in range(self.max_steps):
            reply = await self._step(history, ctx)
            if not reply.tool_calls:
                return Result(self.name, reply.text)
            history.append(ToolTurn(await self.toolkit.execute(reply.tool_calls, ctx)))
        raise MaxStepsExceeded(f"{self.name} exceeded {self.max_steps} steps")

    async def _step(self, history: list[Turn], ctx: RunContext) -> Reply:
        reply = await self.llm.complete(self.instructions, history, self.toolkit.specs)
        await ctx.emit(LLMReplied(ctx.path, reply))
        _ensure_usable(self.name, reply)
        history.append(reply.turn)
        return reply


def _ensure_usable(name: str, reply: Reply) -> None:
    if reply.stop not in ("end", "tool_use"):
        detail = f" ({reply.detail})" if reply.detail else ""
        raise LLMError(f"{name}: model stopped with '{reply.stop}'{detail}")
