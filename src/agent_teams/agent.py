"""A single agent: an LLM, instructions and tools, driven by a tool-use loop."""

from __future__ import annotations

from collections.abc import Iterable, Mapping
from dataclasses import dataclass, field
from typing import Any

from .context import RunContext
from .conversation import Conversation
from .errors import LLMError, MaxStepsExceeded
from .hooks import LLMReplied
from .llm.base import LLM
from .runnable import Runnable
from .serde import dump_turn, load_turn
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

    def session(self, ctx: RunContext | None = None) -> Session:
        """Start a multi-turn conversation that remembers earlier messages."""
        return Session(self, ctx or RunContext())

    def open(self, ctx: RunContext | None = None) -> Conversation:
        """Start a non-blocking conversation: messages queue up and are answered in order."""
        return Conversation(self.session(ctx))

    async def respond(self, history: list[Turn], ctx: RunContext) -> Result:
        """Continue ``history`` (appending to it) until the model gives a final answer."""
        for _ in range(self.max_steps):
            reply = await self._step(history, ctx)
            if not reply.tool_calls:
                return Result(self.name, reply.text)
            history.append(ToolTurn(await self.toolkit.execute(reply.tool_calls, ctx)))
        raise MaxStepsExceeded(f"{self.name} exceeded {self.max_steps} steps")

    async def _run(self, task: str, ctx: RunContext) -> Result:
        return await self.respond([UserTurn(task)], ctx)

    async def _step(self, history: list[Turn], ctx: RunContext) -> Reply:
        reply = await self.llm.complete(self.instructions, history, self.toolkit.specs)
        await ctx.emit(LLMReplied(ctx.path, reply))
        _ensure_usable(self.name, reply)
        history.append(reply.turn)
        return reply


@dataclass
class Session:
    """A conversation with one agent (typically a team lead).

    History persists between :meth:`send` calls. A failed message is rolled
    back, so the conversation stays valid and can continue.
    """

    agent: Agent
    ctx: RunContext = field(default_factory=RunContext)
    history: list[Turn] = field(default_factory=list)

    @property
    def name(self) -> str:
        return self.agent.name

    async def send(self, message: str) -> Result:
        return await self.agent.traced(message, self.ctx, self._respond)

    def reset(self) -> None:
        self.history.clear()

    def snapshot(self) -> dict[str, Any]:
        return {"kind": "agent", "history": [dump_turn(turn) for turn in self.history]}

    def restore(self, state: Mapping[str, Any]) -> None:
        self.history[:] = [load_turn(turn) for turn in state.get("history", [])]

    async def _respond(self, message: str, ctx: RunContext) -> Result:
        checkpoint = len(self.history)
        self.history.append(UserTurn(message))
        try:
            return await self.agent.respond(self.history, ctx)
        except BaseException:
            del self.history[checkpoint:]
            raise


def _ensure_usable(name: str, reply: Reply) -> None:
    if reply.stop not in ("end", "tool_use"):
        detail = f" ({reply.detail})" if reply.detail else ""
        raise LLMError(f"{name}: model stopped with '{reply.stop}'{detail}")
