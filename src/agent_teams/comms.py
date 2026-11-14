"""Communication inside one conversation: mail, questions, approvals and the plan.

Employees and the leader leave each other messages, employees ask the leader
or the user questions (and wait for the answer), Claude Code permission
requests become approval questions, and the leader keeps a plan. ``Comms`` is
independent of the web service: a :class:`CommsSink` persists and publishes
every change.
"""

from __future__ import annotations

import asyncio
import itertools
import time
from collections.abc import Callable, Iterable, Mapping, Sequence
from dataclasses import dataclass, field, replace
from typing import Any, Literal, Protocol, TypeAlias

from .errors import AgentTeamsError

LEAD = "lead"
EVERYONE = "*"
ALLOW, DENY, ALLOW_ALWAYS = "allow", "deny", "allow_always"
APPROVAL_OPTIONS = (ALLOW, DENY)

Audience: TypeAlias = Literal["lead", "user"]
QuestionKind: TypeAlias = Literal["question", "approval"]
QuestionStatus: TypeAlias = Literal["open", "answered", "cancelled"]
StepStatus: TypeAlias = Literal["todo", "doing", "done"]
Plan: TypeAlias = list[dict[str, str]]
STEP_STATUSES = frozenset({"todo", "doing", "done"})


class QuestionClosedError(AgentTeamsError):
    """The question was already answered or cancelled."""


class QuestionCancelledError(AgentTeamsError):
    """Nobody will answer this question any more (task ended, server restarted)."""


@dataclass(frozen=True)
class Mail:
    """A message; ``recipient`` is a member, ``"lead"`` or ``"*"`` (everyone)."""

    id: int
    sender: str
    recipient: str
    body: str
    created_at: float
    read_by: tuple[str, ...] = ()

    def is_for(self, name: str) -> bool:
        if self.recipient == EVERYONE:
            return self.sender != name
        return self.recipient == name

    def read_for(self, name: str) -> bool:
        return name in self.read_by


@dataclass(frozen=True)
class Question:
    id: str
    asker: str
    task_id: str | None
    audience: Audience
    kind: QuestionKind
    text: str
    options: tuple[str, ...] = ()
    answer: str | None = None
    status: QuestionStatus = "open"
    created_at: float = field(default_factory=time.time)
    answered_at: float | None = None
    detail: Mapping[str, Any] = field(default_factory=dict)


class CommsSink(Protocol):
    """Persists and publishes changes (the web service; tests use a recorder)."""

    async def on_mail(self, mail: Mail) -> None: ...
    async def on_read(self, mail: Sequence[Mail]) -> None: ...
    async def on_question(self, question: Question) -> None: ...
    async def on_plan(self, plan: Plan) -> None: ...


class NullSink:
    async def on_mail(self, mail: Mail) -> None:
        return None

    async def on_read(self, mail: Sequence[Mail]) -> None:
        return None

    async def on_question(self, question: Question) -> None:
        return None

    async def on_plan(self, plan: Plan) -> None:
        return None


Notify = Callable[[str], None]


class Comms:
    """Mail, questions and the plan of one conversation.

    ``notify`` posts a message into the leader's conversation (set by
    :meth:`agent_teams.leader.Leader.open`).
    """

    def __init__(self, sink: CommsSink | None = None, notify: Notify | None = None) -> None:
        self.sink: CommsSink = sink or NullSink()
        self.notify = notify
        self._mail: list[Mail] = []
        self._questions: dict[str, Question] = {}
        self._waiting: dict[str, asyncio.Future[str]] = {}
        self._always: set[tuple[str, str]] = set()
        self._plan: Plan = []
        self._mail_ids = itertools.count(1)
        self._question_ids = itertools.count(1)

    # -- mail -------------------------------------------------------------

    @property
    def mail(self) -> tuple[Mail, ...]:
        return tuple(self._mail)

    async def send(self, sender: str, recipient: str, body: str) -> Mail:
        mail = Mail(next(self._mail_ids), sender, recipient, body, time.time())
        self._mail.append(mail)
        await self.sink.on_mail(mail)
        if recipient == LEAD:
            self._post(f"[Message from {sender}]\n{body}")
        return mail

    async def unread(self, name: str) -> list[Mail]:
        return [m for m in self._mail if m.is_for(name) and not m.read_for(name)]

    async def mark_read(self, name: str) -> list[Mail]:
        """Mark everything for ``name`` read; returns what was unread."""
        unread = await self.unread(name)
        if not unread:
            return []
        changed = {m.id: replace(m, read_by=(*m.read_by, name)) for m in unread}
        self._mail = [changed.get(m.id, m) for m in self._mail]
        await self.sink.on_read(list(changed.values()))
        return unread

    # -- questions --------------------------------------------------------

    @property
    def questions(self) -> tuple[Question, ...]:
        return tuple(self._questions.values())

    def question(self, question_id: str) -> Question:
        if question_id not in self._questions:
            raise KeyError(f"unknown question '{question_id}'")
        return self._questions[question_id]

    async def ask(
        self,
        asker: str,
        task_id: str | None,
        audience: Audience,
        text: str,
        *,
        options: Iterable[str] = (),
        kind: QuestionKind = "question",
        detail: Mapping[str, Any] | None = None,
    ) -> Question:
        question = Question(
            f"q{next(self._question_ids)}",
            asker,
            task_id,
            audience,
            kind,
            text,
            tuple(options),
            detail=dict(detail or {}),
        )
        self._questions[question.id] = question
        self._waiting[question.id] = asyncio.get_running_loop().create_future()
        await self.sink.on_question(question)
        if audience == LEAD:
            where = f" · {task_id}" if task_id else ""
            self._post(
                f"[Question {question.id} from {asker}{where}]\n{text}\n(answer with reply_to)"
            )
        return question

    async def wait(self, question_id: str) -> str:
        """Block until the question is answered; raises if it gets cancelled."""
        question = self.question(question_id)
        if question.status == "answered":
            return question.answer or ""
        if question.status == "cancelled":
            raise QuestionCancelledError(f"question {question_id} was cancelled")
        return await asyncio.shield(self._waiting[question_id])

    async def answer(self, question_id: str, answer: str) -> Question:
        question = self._open(question_id)
        _check_answer(question, answer)
        done = self._close(question, status="answered", answer=answer)
        self._settle(question_id).set_result(answer)
        await self.sink.on_question(done)
        if done.asker == LEAD and done.audience == "user":
            self._post(f"[Answer to {done.id}] {answer}")
        if question.kind == "approval" and answer == ALLOW_ALWAYS:
            await self._allow_always(question.asker, _tool_of(question))
        return done

    async def _allow_always(self, asker: str, tool_name: str) -> None:
        """Remember the choice and release that employee's other open requests for the tool."""
        self._always.add((asker, tool_name))
        for other in list(self._questions.values()):
            same = other.asker == asker and _tool_of(other) == tool_name
            if same and other.kind == "approval" and other.status == "open":
                await self.answer(other.id, ALLOW)

    async def cancel(self, question_id: str) -> Question:
        done = self._close(self._open(question_id), status="cancelled")
        waiting = self._settle(question_id)
        waiting.set_exception(QuestionCancelledError(f"question {question_id} was cancelled"))
        waiting.exception()  # nobody may be waiting; do not warn about it
        await self.sink.on_question(done)
        return done

    async def abandon(self, task_id: str) -> None:
        """Cancel the open questions of a task that ended."""
        await self._cancel_where(lambda q: q.task_id == task_id)

    async def abandon_waiting(self) -> None:
        """Cancel every open question someone is blocked on (the conversation ends).

        The leader's own ``ask_user`` questions block nobody and stay answerable.
        """
        await self._cancel_where(_blocks_someone)

    async def approve(
        self, asker: str, task_id: str | None, tool_name: str, tool_input: Mapping[str, Any]
    ) -> bool:
        """Ask the user whether ``asker`` may use a tool; blocks until they decide."""
        if (asker, tool_name) in self._always:
            return True
        question = await self.ask(
            asker,
            task_id,
            "user",
            f"{asker} wants to use {tool_name}",
            options=APPROVAL_OPTIONS,
            kind="approval",
            detail={"tool_name": tool_name, "input": dict(tool_input)},
        )
        return await self.wait(question.id) in (ALLOW, ALLOW_ALWAYS)

    # -- plan ---------------------------------------------------------------

    @property
    def plan(self) -> Plan:
        return [dict(step) for step in self._plan]

    async def set_plan(self, steps: Iterable[Mapping[str, str]]) -> Plan:
        self._plan = [_step(s) for s in steps]
        await self.sink.on_plan(self.plan)
        return self.plan

    # -- restart ------------------------------------------------------------

    async def restore(
        self, mail: Iterable[Mail], questions: Iterable[Question], plan: Plan
    ) -> None:
        """Load state saved before a restart. Nobody waits on old open questions
        any more, so they are cancelled; the leader's own questions to the user
        stay open (answering them posts to the leader, which needs no waiter)."""
        self._mail = sorted(mail, key=lambda m: m.id)
        self._questions = {q.id: q for q in sorted(questions, key=_question_number)}
        self._plan = [_step(s) for s in plan]
        self._mail_ids = itertools.count(max((m.id for m in self._mail), default=0) + 1)
        last = max((_question_number(q) for q in self._questions.values()), default=0)
        self._question_ids = itertools.count(last + 1)
        for question in list(self._questions.values()):
            if question.status == "open" and _blocks_someone(question):
                done = self._close(question, status="cancelled")
                await self.sink.on_question(done)

    # -- internals ----------------------------------------------------------

    async def _cancel_where(self, match: Callable[[Question], bool]) -> None:
        for question_id in list(self._questions):
            question = self._questions[question_id]  # fresh: a cancel may have run meanwhile
            if question.status == "open" and match(question):
                await self.cancel(question_id)

    def _post(self, message: str) -> None:
        if self.notify is not None:
            self.notify(message)

    def _open(self, question_id: str) -> Question:
        question = self.question(question_id)
        if question.status != "open":
            raise QuestionClosedError(f"question {question_id} is already {question.status}")
        return question

    def _close(self, question: Question, **changes: Any) -> Question:
        done = replace(question, answered_at=time.time(), **changes)
        self._questions[question.id] = done
        return done

    def _settle(self, question_id: str) -> asyncio.Future[str]:
        waiting = self._waiting.pop(question_id, None)
        if waiting is None:
            waiting = asyncio.get_running_loop().create_future()
        return waiting


def _check_answer(question: Question, answer: str) -> None:
    if question.kind == "approval" and answer not in (ALLOW, DENY, ALLOW_ALWAYS):
        raise ValueError("an approval is answered with allow, deny or allow_always")
    if not answer.strip():
        raise ValueError("the answer is empty")


def _blocks_someone(question: Question) -> bool:
    """Employees wait on their questions; the leader's ask_user returns at once."""
    return question.asker != LEAD


def _tool_of(question: Question) -> str:
    return str(question.detail.get("tool_name", ""))


def _step(step: Mapping[str, str]) -> dict[str, str]:
    status = str(step.get("status") or "todo")
    if status not in STEP_STATUSES:
        raise ValueError(f"plan step status must be one of {sorted(STEP_STATUSES)}")
    return {"title": str(step.get("title", "")), "status": status}


def _question_number(question: Question) -> int:
    digits = question.id.lstrip("q")
    return int(digits) if digits.isdigit() else 0
