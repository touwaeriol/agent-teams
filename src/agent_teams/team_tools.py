"""Team tools: what employees and the leader use to talk to each other.

Employees reach their toolkit over MCP while they work on a task (one
endpoint per task, see :meth:`agent_teams.tasks.TaskBoard`); the leader's
extra tools go next to its task board tools.
"""

from __future__ import annotations

import json
from collections.abc import Callable, Mapping
from typing import TYPE_CHECKING, Any

from pydantic import BaseModel

from .comms import LEAD, Comms, StepStatus
from .context import RunContext
from .hooks import WorkerProgress
from .runnable import Runnable
from .tools import Tool, Toolkit, tool
from .types import Task

if TYPE_CHECKING:
    from .tasks import TaskBoard

APPROVAL_TOOL = "approve"
DENIED = "用户拒绝了这个操作"
_KINDS = {"ClaudeCodeWorker": "claude_code", "CodexWorker": "codex"}


def employee_toolkit(board: TaskBoard, comms: Comms, task: Task) -> Toolkit:
    """The tools of the employee working on ``task``."""
    tools = _EmployeeTools(board, comms, task)
    functions: list[Callable[..., Any]] = [
        tools.report_progress,
        tools.ask_lead,
        tools.ask_user,
        tools.send_message,
        tools.read_messages,
        tools.list_team,
        tools.get_task,
        tools.request_review,
    ]
    if can_delegate(board.members[task.assignee]):
        functions += [tools.assign_subtask, tools.wait_task]
    return Toolkit([*(tool(f) for f in functions), _approval_tool(comms, task)])


def leader_tools(board: TaskBoard, comms: Comms) -> list[Tool]:
    tools = _LeaderTools(board, comms)
    functions: list[Callable[..., Any]] = [
        tools.reply_to,
        tools.broadcast,
        tools.send_message,
        tools.update_plan,
        tools.ask_user,
    ]
    return [tool(f) for f in functions]


def can_delegate(member: Runnable) -> bool:
    return bool(getattr(member, "can_delegate", False))


async def briefing(board: TaskBoard, comms: Comms, task: Task) -> str:
    """Appended to an employee's task: the team, unread mail, and the tools."""
    unread = await comms.mark_read(task.assignee)
    sections = [f"---\nYour team:\n{roster(board, exclude=task.assignee)}"]
    if unread:
        sections.append("Unread messages:\n" + "\n".join(_mail_line(m) for m in unread))
    sections.append(
        _TOOLS_HINT + (_DELEGATE_HINT if can_delegate(board.members[task.assignee]) else "")
    )
    return "\n\n".join(sections)


_TOOLS_HINT = (
    "You can use the team tools (mcp__team__*): report_progress to tell the team about "
    "milestones of long work; ask_lead when you are blocked or need a decision (it waits "
    "for the answer); ask_user only for what only the user can decide; send_message and "
    "read_messages to coordinate with the others; list_team and get_task for context; "
    "request_review to have another member check your work before you finish. Do the "
    "work yourself where you can and ask only when you really need to."
)
_DELEGATE_HINT = (
    " You may also hand parts of the work to other members with assign_subtask and "
    "collect their results with wait_task."
)


def roster(board: TaskBoard, exclude: str = "") -> str:
    lines = [f"- {LEAD}: the team lead"]
    for name, member in board.members.items():
        if name == exclude:
            continue
        lines.append(f"- {name}: {member.description or name}")
    return "\n".join(lines)


def _mail_line(mail: Any) -> str:
    to = " (to everyone)" if mail.recipient == "*" else ""
    return f"[from {mail.sender}{to}] {mail.body}"


def _describe_task(task: Task) -> str:
    parent = f", subtask of {task.parent}" if task.parent else ""
    lines = [
        f"{task.id} · {task.assignee} · {task.status}{parent}",
        f"Instruction:\n{task.instruction}",
    ]
    if task.output:
        lines.append(f"Result:\n{task.output}")
    return "\n\n".join(lines)


def _report(kind: str, task: Task) -> str:
    return f"[{kind} {task.id} · {task.assignee} · {task.status}]\n{task.output}".rstrip()


class _EmployeeTools:
    def __init__(self, board: TaskBoard, comms: Comms, task: Task) -> None:
        self.board = board
        self.comms = comms
        self.task = task
        self.name = task.assignee

    async def report_progress(self, text: str, ctx: RunContext) -> str:
        """Tell the team how the task is going (shown live to the user).

        Args:
            text: One or two sentences on what is done and what comes next.
        """
        await ctx.emit(WorkerProgress(ctx.path, text))
        return "ok"

    async def ask_lead(self, question: str) -> str:
        """Ask the team lead and wait for the answer.

        Args:
            question: The question, with the context the lead needs to answer it.
        """
        asked = await self.comms.ask(self.name, self.task.id, "lead", question)
        return await self.comms.wait(asked.id)

    async def ask_user(self, question: str, options: list[str] | None = None) -> str:
        """Ask the user and wait for the answer. Use only for decisions only the user can make.

        Args:
            question: The question for the user.
            options: Choices to offer, if the answer is one of a few.
        """
        asked = await self.comms.ask(
            self.name, self.task.id, "user", question, options=options or ()
        )
        return await self.comms.wait(asked.id)

    async def send_message(self, to: str, text: str) -> str:
        """Leave a message for another team member or the lead.

        Args:
            to: A member's name, or "lead".
            text: The message.
        """
        if to == self.name:
            raise ValueError("you cannot send a message to yourself")
        if to != LEAD and to not in self.board.members:
            raise ValueError(f"unknown member '{to}'; choose from {[LEAD, *self.board.members]}")
        await self.comms.send(self.name, to, text)
        return "sent"

    async def read_messages(self) -> str:
        """Read your unread messages (including broadcasts from the lead)."""
        unread = await self.comms.mark_read(self.name)
        return "\n".join(_mail_line(m) for m in unread) or "No new messages."

    async def list_team(self) -> str:
        """List the team: each member's role, type, node and current tasks."""
        return "\n".join(
            [f"- {LEAD}: the team lead"]
            + [self._member_line(name, m) for name, m in self.board.members.items()]
        )

    async def get_task(self, task_id: str) -> str:
        """Show a task's instruction, status and result.

        Args:
            task_id: The task ID, e.g. "t2".
        """
        return _describe_task(self.board.get(task_id))

    async def request_review(self, reviewer: str, summary: str) -> str:
        """Ask another member to review your work; waits for the review and returns it.

        Args:
            reviewer: The member who should review.
            summary: What you did, where it is (branch, files) and what to check.
        """
        if reviewer == self.name:
            raise ValueError("you cannot review your own work; choose another member")
        instruction = f"Review requested by {self.name} (task {self.task.id}):\n\n{summary}"
        review = await self.board.assign(reviewer, instruction, parent=self.task.id)
        return _report("Review", await self.board.outcome(review.id))

    async def assign_subtask(self, assignee: str, instruction: str) -> str:
        """Hand part of your task to another member. Returns at once with the task ID;
        use wait_task to get the result. Subtasks still running when your own task
        ends are cancelled.

        Args:
            assignee: The member's name.
            instruction: Complete, self-contained instructions.
        """
        if assignee == self.name:
            raise ValueError("you cannot assign a subtask to yourself")
        subtask = await self.board.assign(assignee, instruction, parent=self.task.id)
        return f"{subtask.id} assigned to {subtask.assignee}; use wait_task to get its result."

    async def wait_task(self, task_id: str) -> str:
        """Wait until one of your subtasks is finished and return its result.

        Args:
            task_id: The subtask's ID.
        """
        if self.board.get(task_id).parent != self.task.id:
            raise ValueError(f"{task_id} is not one of your subtasks")
        return _report("Task", await self.board.outcome(task_id))

    def _member_line(self, name: str, member: Runnable) -> str:
        kind = _KINDS.get(type(member).__name__, type(member).__name__)
        node = getattr(getattr(member, "node", None), "name", "")
        where = f", node {node}" if node else ""
        working = [t.id for t in self.board.tasks if t.assignee == name and not t.finished]
        doing = f"; working on {', '.join(working)}" if working else ""
        me = " (you)" if name == self.name else ""
        return f"- {name}{me} ({kind}{where}): {member.description or name}{doing}"


def _approval_tool(comms: Comms, task: Task) -> Tool:
    """Claude Code's ``--permission-prompt-tool``: the user decides on the page."""

    async def handle(arguments: Mapping[str, Any], _ctx: RunContext) -> str:
        name = str(arguments.get("tool_name", ""))
        tool_input = arguments.get("input")
        tool_input = dict(tool_input) if isinstance(tool_input, Mapping) else {}
        allowed = await comms.approve(task.assignee, task.id, name, tool_input)
        if allowed:
            return json.dumps({"behavior": "allow", "updatedInput": tool_input})
        return json.dumps({"behavior": "deny", "message": DENIED}, ensure_ascii=False)

    schema = {
        "type": "object",
        "properties": {
            "tool_name": {"type": "string"},
            "input": {"type": "object"},
            "tool_use_id": {"type": "string"},
        },
        "required": ["tool_name", "input"],
    }
    return Tool(APPROVAL_TOOL, "Ask the user for permission to use a tool.", schema, handle)


class PlanStep(BaseModel):
    title: str
    status: StepStatus = "todo"


class _LeaderTools:
    def __init__(self, board: TaskBoard, comms: Comms) -> None:
        self.board = board
        self.comms = comms

    async def reply_to(self, question_id: str, answer: str) -> str:
        """Answer a "[Question ...]" from an employee; the employee is waiting for it.

        Args:
            question_id: The question's ID, e.g. "q3".
            answer: Your answer.
        """
        if self.comms.question(question_id).audience != LEAD:
            raise ValueError(f"{question_id} is for the user, not for you")
        await self.comms.answer(question_id, answer)
        return f"answered {question_id}"

    async def broadcast(self, text: str) -> str:
        """Send a message to every employee (they read it at their next task or check).

        Args:
            text: The message.
        """
        await self.comms.send(LEAD, "*", text)
        return "sent to everyone"

    async def send_message(self, to: str, text: str) -> str:
        """Leave a message for one employee.

        Args:
            to: The employee's name.
            text: The message.
        """
        if to not in self.board.members:
            raise ValueError(f"unknown member '{to}'; choose from {list(self.board.members)}")
        await self.comms.send(LEAD, to, text)
        return "sent"

    async def update_plan(self, steps: list[PlanStep]) -> str:
        """Replace the plan shown to the user next to the conversation.

        Args:
            steps: Every step in order, each with a title and a status (todo, doing, done).
        """
        plan = await self.comms.set_plan(s.model_dump() for s in steps)
        return f"plan updated ({len(plan)} steps)"

    async def ask_user(self, question: str, options: list[str] | None = None) -> str:
        """Ask the user a question on the page. Returns at once; the answer arrives
        later as a message "[Answer to <id>] ...".

        Args:
            question: The question.
            options: Choices to offer, if the answer is one of a few.
        """
        asked = await self.comms.ask(LEAD, None, "user", question, options=options or ())
        return f'{asked.id} asked; the answer will arrive as "[Answer to {asked.id}] ...".'
