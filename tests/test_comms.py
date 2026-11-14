"""Comms: mail, questions, approvals and the plan of one conversation."""

from __future__ import annotations

import asyncio
from collections.abc import Sequence

import pytest

from agent_teams.comms import (
    Comms,
    Mail,
    Plan,
    Question,
    QuestionCancelledError,
    QuestionClosedError,
)


class Recorder:
    def __init__(self) -> None:
        self.mail: list[Mail] = []
        self.read: list[Mail] = []
        self.questions: list[Question] = []
        self.plans: list[Plan] = []

    async def on_mail(self, mail: Mail) -> None:
        self.mail.append(mail)

    async def on_read(self, mail: Sequence[Mail]) -> None:
        self.read.extend(mail)

    async def on_question(self, question: Question) -> None:
        self.questions.append(question)

    async def on_plan(self, plan: Plan) -> None:
        self.plans.append(plan)


def comms_with() -> tuple[Comms, Recorder, list[str]]:
    sink = Recorder()
    posted: list[str] = []
    return Comms(sink, posted.append), sink, posted


async def test_mail_to_a_member_and_to_the_lead() -> None:
    comms, sink, posted = comms_with()

    await comms.send("dev-a", "dev-b", "api is ready")
    await comms.send("dev-a", "lead", "blocked on CI")

    assert [m.body for m in await comms.unread("dev-b")] == ["api is ready"]
    assert await comms.unread("dev-a") == []
    assert posted == ["[Message from dev-a]\nblocked on CI"]
    assert [m.id for m in sink.mail] == [1, 2]

    assert [m.body for m in await comms.mark_read("dev-b")] == ["api is ready"]
    assert await comms.unread("dev-b") == []
    assert [m.read_by for m in sink.read] == [("dev-b",)]
    assert await comms.mark_read("dev-b") == []


async def test_broadcast_reaches_everyone_but_the_sender_once_each() -> None:
    comms, _sink, posted = comms_with()

    await comms.send("lead", "*", "freeze main")

    assert [m.body for m in await comms.unread("dev-a")] == ["freeze main"]
    assert await comms.unread("lead") == []
    await comms.mark_read("dev-a")
    assert await comms.unread("dev-a") == []
    assert [m.body for m in await comms.unread("dev-b")] == ["freeze main"]
    assert posted == []


async def test_ask_wait_answer() -> None:
    comms, sink, posted = comms_with()

    question = await comms.ask("dev-a", "t2", "lead", "which db?")
    waiting = asyncio.create_task(comms.wait(question.id))
    await asyncio.sleep(0)
    assert not waiting.done()
    assert posted == ["[Question q1 from dev-a · t2]\nwhich db?\n(answer with reply_to)"]

    answered = await comms.answer(question.id, "postgres")

    assert await waiting == "postgres"
    assert (answered.status, answered.answer) == ("answered", "postgres")
    assert answered.answered_at is not None
    assert [q.status for q in sink.questions] == ["open", "answered"]
    assert await comms.wait(question.id) == "postgres"  # already answered
    with pytest.raises(QuestionClosedError):
        await comms.answer(question.id, "mysql")


async def test_user_questions_do_not_notify_the_lead_unless_the_lead_asked() -> None:
    comms, _sink, posted = comms_with()

    mine = await comms.ask("dev-a", "t1", "user", "deploy now?", options=["yes", "no"])
    leads = await comms.ask("lead", None, "user", "which region?")
    await comms.answer(mine.id, "yes")
    await comms.answer(leads.id, "eu")

    assert mine.options == ("yes", "no")
    assert posted == ["[Answer to q2] eu"]


async def test_cancel_and_abandon_fail_the_waiters() -> None:
    comms, _sink, _posted = comms_with()
    first = await comms.ask("dev-a", "t1", "user", "a?")
    second = await comms.ask("dev-a", "t1", "lead", "b?")
    other = await comms.ask("dev-b", "t2", "user", "c?")
    waiting = asyncio.create_task(comms.wait(first.id))
    await asyncio.sleep(0)

    await comms.abandon("t1")

    with pytest.raises(QuestionCancelledError):
        await waiting
    assert comms.question(second.id).status == "cancelled"
    assert comms.question(other.id).status == "open"
    with pytest.raises(QuestionCancelledError):
        await comms.wait(second.id)
    with pytest.raises(QuestionClosedError):
        await comms.answer(first.id, "late")


async def test_answers_are_checked() -> None:
    comms, _sink, _posted = comms_with()
    question = await comms.ask("dev-a", "t1", "user", "why?")
    with pytest.raises(ValueError, match="empty"):
        await comms.answer(question.id, "  ")
    with pytest.raises(KeyError):
        await comms.answer("q9", "x")


async def test_approval_allow_deny_and_allow_always() -> None:
    comms, sink, _posted = comms_with()

    async def decide(answer: str) -> None:
        while not [q for q in comms.questions if q.status == "open"]:
            await asyncio.sleep(0)
        (question,) = [q for q in comms.questions if q.status == "open"]
        await comms.answer(question.id, answer)

    async def approve(asker: str, tool: str, answer: str) -> bool:
        deciding = asyncio.create_task(decide(answer))
        allowed = await comms.approve(asker, "t1", tool, {"command": "ls"})
        await deciding
        return allowed

    assert await approve("dev-a", "Bash", "deny") is False
    assert await approve("dev-a", "Bash", "allow") is True
    assert await approve("dev-a", "Bash", "allow_always") is True
    asked = len(comms.questions)
    assert await comms.approve("dev-a", "t3", "Bash", {"command": "rm"}) is True  # no new card
    assert len(comms.questions) == asked
    assert await approve("dev-b", "Bash", "allow") is True  # only for that employee

    approval = sink.questions[0]
    assert approval.kind == "approval"
    assert approval.options == ("allow", "deny")
    assert approval.detail == {"tool_name": "Bash", "input": {"command": "ls"}}
    question = await comms.ask("dev-a", "t1", "user", "x", kind="approval")
    with pytest.raises(ValueError, match="allow"):
        await comms.answer(question.id, "maybe")


async def test_allow_always_releases_other_open_approvals_for_that_tool() -> None:
    comms, _sink, _posted = comms_with()
    first = asyncio.create_task(comms.approve("dev-a", "t1", "Bash", {"command": "ls"}))
    second = asyncio.create_task(comms.approve("dev-a", "t2", "Bash", {"command": "pwd"}))
    other_tool = asyncio.create_task(comms.approve("dev-a", "t1", "Edit", {"file_path": "a"}))
    other_member = asyncio.create_task(comms.approve("dev-b", "t4", "Bash", {"command": "ls"}))
    while len(comms.questions) < 4:
        await asyncio.sleep(0)

    await comms.answer("q1", "allow_always")

    assert await first is True
    assert await second is True
    status = {q.id: (q.status, q.answer) for q in comms.questions}
    assert status["q2"] == ("answered", "allow")
    assert status["q3"] == ("open", None)
    assert status["q4"] == ("open", None)
    await comms.answer("q3", "deny")
    await comms.answer("q4", "deny")
    assert await other_tool is False
    assert await other_member is False


async def test_plan() -> None:
    comms, sink, _posted = comms_with()

    plan = await comms.set_plan([{"title": "design", "status": "done"}, {"title": "build"}])

    assert plan == [{"title": "design", "status": "done"}, {"title": "build", "status": "todo"}]
    assert comms.plan == plan
    assert sink.plans == [plan]
    with pytest.raises(ValueError, match="status"):
        await comms.set_plan([{"title": "x", "status": "blocked"}])


async def test_the_leaders_own_questions_survive_restore_and_stay_answerable() -> None:
    comms, sink, posted = comms_with()
    questions = [
        Question("q1", "lead", None, "user", "question", "which db?"),
        Question("q2", "dev-a", "t1", "user", "question", "why?"),
    ]

    await comms.restore([], questions, [])

    assert [(q.id, q.status) for q in comms.questions] == [("q1", "open"), ("q2", "cancelled")]
    assert [q.id for q in sink.questions] == ["q2"]
    assert (await comms.answer("q1", "postgres")).status == "answered"
    assert posted == ["[Answer to q1] postgres"]


async def test_abandon_waiting_cancels_employee_questions_only() -> None:
    comms, _sink, _posted = comms_with()
    await comms.ask("lead", None, "user", "which db?")
    employee = await comms.ask("dev-a", "t1", "user", "why?")
    lead_question = await comms.ask("dev-a", "t1", "lead", "how?")
    waiting = asyncio.create_task(comms.wait(employee.id))
    await asyncio.sleep(0)

    await comms.abandon_waiting()

    statuses = [q.status for q in comms.questions]
    assert statuses == ["open", "cancelled", "cancelled"]
    with pytest.raises(QuestionCancelledError):
        await waiting
    assert comms.question(lead_question.id).status == "cancelled"


async def test_restore_cancels_open_questions_and_continues_numbering() -> None:
    comms, sink, _posted = comms_with()
    mail = [Mail(3, "a", "b", "hi", 1.0), Mail(1, "b", "a", "yo", 0.5, ("a",))]
    questions = [
        Question("q2", "a", "t1", "user", "question", "open?"),
        Question("q1", "a", "t1", "lead", "question", "done?", answer="yes", status="answered"),
    ]

    await comms.restore(mail, questions, [{"title": "x", "status": "doing"}])

    assert [q.status for q in comms.questions] == ["answered", "cancelled"]
    assert [q.id for q in sink.questions] == ["q2"]
    assert [m.body for m in await comms.unread("b")] == ["hi"]
    assert comms.plan == [{"title": "x", "status": "doing"}]
    assert (await comms.send("a", "b", "next")).id == 4
    assert (await comms.ask("a", None, "user", "next?")).id == "q3"
