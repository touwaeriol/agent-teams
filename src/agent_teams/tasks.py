"""A non-blocking task board: assigning work returns at once, members run in the background.

Tasks may depend on earlier tasks; a dependent task starts once its
prerequisites are done and receives their outputs. Listeners are told about
every finished task, which is how results flow back to the leader.
"""

from __future__ import annotations

import asyncio
import functools
import itertools
from collections.abc import Callable, Iterable, Mapping, Sequence
from dataclasses import replace
from typing import TYPE_CHECKING, Any, Protocol, runtime_checkable

from ._text import clip
from .context import RunContext
from .errors import AgentTeamsError
from .hooks import TaskAssigned, TaskFinished, TaskStarted
from .mcp_config import ToolEndpoint
from .runnable import Runnable
from .team_tools import APPROVAL_TOOL, briefing, employee_toolkit
from .tools import Tool, Toolkit, tool, tool_name
from .types import Task

if TYPE_CHECKING:
    from .comms import Comms

Listener = Callable[[Task], None]

# A run re-attached after a restart keeps the MCP token it started with, and
# that token died with the old conversation: team tools and approvals fail.
REATTACHED_NOTE = (
    "(Note: the server restarted while this task was running. If the run kept going "
    "on its node, its team tools and permission requests stopped working at the "
    "restart and calls to them failed; check the result and re-assign the work if "
    "something is missing.)"
)


class DependencyFailed(AgentTeamsError):  # noqa: N818 - reads better as a task outcome
    """A prerequisite task did not finish successfully."""


class ToolPublisher(Protocol):
    """Publishes a toolkit over MCP (see ``claude_session.ToolServer``)."""

    def expose(self, toolkit: Toolkit, ctx: RunContext) -> ToolEndpoint: ...


@runtime_checkable
class Equippable(Protocol):
    """A member that can use the team tools (Claude Code, Codex)."""

    def with_tools(self, endpoint: ToolEndpoint, approval_tool: str | None = None) -> Runnable: ...


class TaskBoard:
    """Tracks tasks for one leader; members run concurrently as asyncio tasks.

    With ``comms`` and a ``tool_server``, members that support it get the
    employee tools (team_tools.py) over MCP for the duration of each task.
    """

    def __init__(
        self,
        members: Sequence[Runnable],
        ctx: RunContext,
        first_number: int = 1,
        *,
        comms: Comms | None = None,
        tool_server: ToolPublisher | None = None,
    ) -> None:
        self._members = {tool_name(m.name): m for m in members}
        self._ctx = ctx
        self.comms = comms
        self.tool_server = tool_server
        self._tasks: dict[str, Task] = {}
        self._handles: dict[str, asyncio.Task[str]] = {}
        self._emitting: set[asyncio.Future[None]] = set()
        self._adopted: set[str] = set()
        self._ids = itertools.count(first_number)
        self.listeners: list[Listener] = []

    @property
    def tasks(self) -> tuple[Task, ...]:
        return tuple(self._tasks.values())

    @property
    def members(self) -> Mapping[str, Runnable]:
        return dict(self._members)

    @property
    def busy(self) -> bool:
        return any(not t.finished for t in self._tasks.values())

    async def assign(
        self,
        assignee: str,
        instruction: str,
        depends_on: Iterable[str] = (),
        parent: str = "",
    ) -> Task:
        """Queue a task and return immediately; it runs in the background.

        ``parent`` marks a subtask (or review) created by the employee working
        on that task; its result goes to that employee, not to the leader.
        """
        task = Task(f"t{next(self._ids)}", assignee, instruction, tuple(depends_on), parent=parent)
        self._validate(task)
        self._tasks[task.id] = task
        self._launch(task)
        await self._ctx.emit(TaskAssigned(self._ctx.path, task))
        return task

    def _launch(self, task: Task) -> None:
        handle = asyncio.create_task(self._execute(task))
        handle.add_done_callback(functools.partial(self._settle, task.id))
        self._handles[task.id] = handle

    async def adopt(self, task: Task) -> Task:
        """Run a task from before a restart again under its original ID.

        On resumable members (daemon nodes) the same run key re-attaches to the
        run that kept going, instead of starting the work over.
        """
        task = replace(task, depends_on=(), status="pending", output="")
        self._validate(task)
        self._tasks[task.id] = task
        if self._equips(self._members[task.assignee]):
            self._adopted.add(task.id)
        self._launch(task)
        return task

    def is_resumable(self, assignee: str) -> bool:
        return bool(getattr(self._members.get(assignee), "resumable", False))

    def cancel(self, task_id: str) -> Task:
        task = self.get(task_id)
        if not task.finished:
            self._handles[task_id].cancel()
        return task

    def get(self, task_id: str) -> Task:
        if task_id not in self._tasks:
            raise KeyError(f"unknown task '{task_id}'")
        return self._tasks[task_id]

    async def outcome(self, task_id: str) -> Task:
        """Wait until one task has finished and return it."""
        self.get(task_id)
        await asyncio.wait([self._handles[task_id]])
        return self.get(task_id)

    async def wait(self) -> None:
        """Wait until every task assigned so far has finished."""
        if self._handles:
            await asyncio.wait(list(self._handles.values()))

    def close(self) -> None:
        """Cancel everything still running (e.g. when the conversation ends)."""
        for handle in self._handles.values():
            handle.cancel()

    def tools(self) -> list[Tool]:
        """The leader's interface to the board."""
        return [tool(fn) for fn in (self._assign_tool(), self._list_tool(), self._cancel_tool())]

    def summary(self) -> str:
        if not self._tasks:
            return "No tasks yet."
        return "\n".join(_describe(t) for t in self._tasks.values())

    def _validate(self, task: Task) -> None:
        if task.assignee not in self._members:
            raise ValueError(f"unknown member '{task.assignee}'; choose from {list(self._members)}")
        unknown = [d for d in task.depends_on if d not in self._tasks]
        if unknown:
            raise ValueError(f"unknown prerequisite task(s): {unknown}")

    async def _execute(self, task: Task) -> str:
        prompt = await self._prepare(task)
        running = self._update(task.id, status="running")
        await self._ctx.emit(TaskStarted(self._ctx.path, running))
        ctx = self._ctx.keyed(task.id)
        member = self._members[task.assignee]
        if not self._equips(member):
            return (await member.run(prompt, ctx)).output
        assert isinstance(member, Equippable)
        return await self._run_equipped(task, member, prompt, ctx)

    def _equips(self, member: Runnable) -> bool:
        """Whether the member runs with the employee tools on this board."""
        has_tools = self.comms is not None and self.tool_server is not None
        return has_tools and isinstance(member, Equippable)

    async def _run_equipped(
        self, task: Task, member: Equippable, prompt: str, ctx: RunContext
    ) -> str:
        """Run with the employee tools published for this task only."""
        assert self.comms is not None
        assert self.tool_server is not None
        toolkit = employee_toolkit(self, self.comms, task)
        endpoint = self.tool_server.expose(toolkit, ctx.enter(self._members[task.assignee].name))
        try:
            brief = await briefing(self, self.comms, task)
            worker = member.with_tools(endpoint, APPROVAL_TOOL)
            return (await worker.run(f"{prompt}\n\n{brief}", ctx)).output
        finally:
            revoke = getattr(self.tool_server, "revoke", None)
            if revoke is not None:
                revoke(endpoint.token)

    async def _prepare(self, task: Task) -> str:
        prerequisites = [self._handles[d] for d in task.depends_on]
        if prerequisites:
            await asyncio.wait(prerequisites)
        done = [self._tasks[d] for d in task.depends_on]
        failed = [t.id for t in done if t.status != "done"]
        if failed:
            raise DependencyFailed(f"prerequisite task(s) {failed} did not complete")
        return _with_prerequisites(task.instruction, done)

    def _settle(self, task_id: str, handle: asyncio.Task[str]) -> None:
        """Done-callback: record the outcome however the task ended.

        Runs for tasks cancelled before they started too. Listeners are called
        synchronously with the status change, so nobody sees a gap in between.
        """
        outcome = _outcome(handle)
        if task_id in self._adopted and "output" in outcome:
            outcome["output"] = f"{outcome['output']}\n\n{REATTACHED_NOTE}".lstrip()
        final = self._update(task_id, **outcome)
        self._cancel_children(task_id)
        for listener in self.listeners:
            listener(final)
        emitting = asyncio.ensure_future(self._finished(final))
        self._emitting.add(emitting)
        emitting.add_done_callback(self._emitting.discard)

    def _cancel_children(self, task_id: str) -> None:
        """Subtasks and reviews report only to their parent; once it ended, stop them."""
        for child in self._tasks.values():
            if child.parent == task_id and not child.finished:
                self._handles[child.id].cancel()

    async def _finished(self, task: Task) -> None:
        if self.comms is not None:  # nobody is left to wait for its questions
            await self.comms.abandon(task.id)
        await self._ctx.emit(TaskFinished(self._ctx.path, task))

    def _update(self, task_id: str, **changes: Any) -> Task:
        self._tasks[task_id] = replace(self._tasks[task_id], **changes)
        return self._tasks[task_id]

    def _assign_tool(self) -> Callable[..., Any]:
        async def assign_task(
            assignee: str, instruction: str, depends_on: list[str] | None = None
        ) -> str:
            """Hand a task to a team member. Returns at once; the member works in the
            background and you will receive a "[Task ...]" report when it finishes.

            Args:
                assignee: The member's name.
                instruction: Complete, self-contained instructions for the task.
                depends_on: IDs of earlier tasks whose results this task needs.
            """
            task = await self.assign(assignee, instruction, depends_on or ())
            return f"{task.id} assigned to {task.assignee}; you will get a report when it is done."

        return assign_task

    def _list_tool(self) -> Callable[..., Any]:
        async def list_tasks() -> str:
            """Show every task with its assignee and status."""
            return self.summary()

        return list_tasks

    def _cancel_tool(self) -> Callable[..., Any]:
        async def cancel_task(task_id: str) -> str:
            """Stop a task that is no longer needed.

            Args:
                task_id: The task ID, e.g. "t2".
            """
            return f"cancelling {self.cancel(task_id).id}"

        return cancel_task


def _outcome(handle: asyncio.Task[str]) -> dict[str, str]:
    if handle.cancelled():
        return {"status": "cancelled"}
    error = handle.exception()
    if error is not None:  # reported to the leader, who decides what to do
        return {"status": "failed", "output": f"{type(error).__name__}: {error}"}
    return {"status": "done", "output": handle.result()}


def task_report(task: Task) -> str:
    """The message a finished task posts back to the leader."""
    return f"[Task {task.id} · {task.assignee} · {task.status}]\n{task.output}".rstrip()


def _describe(task: Task) -> str:
    needs = f" (after {', '.join(task.depends_on)})" if task.depends_on else ""
    return f"{task.id} [{task.status}] {task.assignee}{needs}: {clip(task.instruction, 80)}"


def _with_prerequisites(instruction: str, prerequisites: Sequence[Task]) -> str:
    if not prerequisites:
        return instruction
    sections = "\n\n".join(f"## {t.id} ({t.assignee})\n{t.output}" for t in prerequisites)
    return f"{instruction}\n\n---\nResults of prerequisite tasks:\n\n{sections}"
