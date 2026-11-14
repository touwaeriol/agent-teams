"""The leader: you talk to it; it hands tasks to members without blocking.

``assign_task`` returns immediately and the member works in the background.
When a task finishes, its report is posted into the leader's conversation, so
the leader can follow up or tell the user, while the user keeps chatting.
"""

from __future__ import annotations

from collections.abc import Sequence
from dataclasses import dataclass

from .agent import Agent
from .claude_code import ClaudeCodeWorker
from .claude_session import ClaudeCodeSession, ToolServer
from .context import RunContext
from .conversation import ChatSession, Conversation
from .errors import ConfigError, MaxStepsExceeded
from .llm.base import LLM
from .runnable import Runnable
from .tasks import TaskBoard
from .tools import Tool, Toolkit, tool_name
from .types import Result

LEADER_INSTRUCTIONS = (
    "You lead a team and talk directly with the user. Answer simple requests yourself. "
    "For real work, break it into tasks and hand them out with assign_task. It returns "
    "immediately and the member works in the background, so tell the user briefly what "
    "you set in motion and end your turn instead of waiting. Independent tasks run in "
    "parallel; use depends_on when a task needs another task's result. Members may "
    "work on different machines and do not share your context, so give each task "
    "everything it needs: goal, repository paths, branch names and how to verify.\n\n"
    'Messages that start with "[Task" are reports from the task board, not from the user. '
    "Check each result, assign follow-up work if needed, and once a request is complete, "
    "report the outcome to the user. While other tasks for the same request are still "
    "running, keep replies to reports to one short line. Use list_tasks to check progress "
    "and cancel_task to stop work that is no longer needed. Ask the user when a request "
    "is unclear."
)


@dataclass(eq=False)
class Leader(Runnable):
    """A team lead with a non-blocking task board over ``members``.

    ``brain`` is either an LLM (the leader is an in-process agent) or a
    :class:`ClaudeCodeWorker` (the leader is a Claude Code session on a node,
    reaching the board's tools over MCP through a ``tool_server``).
    """

    name: str
    brain: LLM | ClaudeCodeWorker
    members: Sequence[Runnable]
    instructions: str = LEADER_INSTRUCTIONS
    description: str = ""
    tools: Sequence[Tool] = ()
    max_steps: int = 20
    max_turns: int = 50

    def __post_init__(self) -> None:
        if not self.members:
            raise ConfigError(f"leader '{self.name}' has no members")

    def open(
        self,
        ctx: RunContext | None = None,
        *,
        first_task: int = 1,
        tool_server: ToolServer | None = None,
    ) -> Conversation:
        """Start a conversation with the leader; each call gets a fresh task board.

        ``first_task`` sets the first task number, so a resumed conversation
        does not reuse IDs from before.
        """
        ctx = ctx or RunContext()
        board = TaskBoard(self.members, ctx.enter(self.name), first_task)
        tools = [*board.tools(), *self.tools]
        return Conversation(self._session(ctx, tools, tool_server), board)

    def _session(
        self, ctx: RunContext, tools: list[Tool], tool_server: ToolServer | None
    ) -> ChatSession:
        prompt = f"{self.instructions}\n\nTeam members:\n{self._roster()}"
        if not isinstance(self.brain, ClaudeCodeWorker):
            agent = Agent(self.name, self.brain, prompt, self.description, tools, self.max_steps)
            return agent.session(ctx)
        if tool_server is None:
            raise ConfigError(f"leader '{self.name}' runs Claude Code and needs the web service")
        endpoint = tool_server.expose(Toolkit(tools), ctx)
        return ClaudeCodeSession(self.brain, ctx, prompt, endpoint)

    async def run(self, task: str, ctx: RunContext | None = None) -> Result:
        # Each conversation turn is traced by the session, so no outer trace here.
        return await self._run(task, ctx or RunContext())

    async def _run(self, task: str, ctx: RunContext) -> Result:
        conversation = self.open(ctx)
        conversation.post(task)
        try:
            output = await self._settle(conversation)
        finally:
            conversation.close()
        board = conversation.board
        children = (
            tuple(Result(f"{t.id}:{t.assignee}", t.output) for t in board.tasks) if board else ()
        )
        return Result(self.name, output, children)

    async def _settle(self, conversation: Conversation) -> str:
        """Answer messages (the task, then task reports) until all work is done."""
        for _ in range(self.max_turns):
            result = await conversation.reply()
            if conversation.idle:
                return result.output
        raise MaxStepsExceeded(f"{self.name} exceeded {self.max_turns} turns")

    def _roster(self) -> str:
        return "\n".join(f"- {tool_name(m.name)}: {m.description or m.name}" for m in self.members)
