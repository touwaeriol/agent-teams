"""Questions, mail and the plan through the hub and the HTTP API (in-memory services)."""

from __future__ import annotations

import asyncio
import time
from collections.abc import AsyncGenerator
from dataclasses import replace
from typing import Any

import pytest
from fastapi.testclient import TestClient

from agent_teams import Leader, ScriptedLLM, Task
from agent_teams.comms import Mail, Question
from agent_teams.llm import call, say
from agent_teams.server.broker import Event, MemoryBroker
from agent_teams.server.hub import Hub, NotAnswerable, UnknownQuestion
from agent_teams.server.platform import Platform
from agent_teams.server.store import MemoryStore, Store

from .fakes import Worker
from .test_api import client, signup  # noqa: F401 - the client fixture
from .test_hub import ALICE, BOB, eventually

PLAN = [{"title": "design", "status": "done"}, {"title": "build", "status": "doing"}]


def planning_llm() -> ScriptedLLM:
    return ScriptedLLM(
        [
            call("ask_user", {"question": "which region?", "options": ["eu", "us"]}),
            say("asked"),
            call("update_plan", {"steps": PLAN}),
            call("broadcast", {"text": "region is eu"}),
            say("planned"),
        ]
    )


def hub_with(store: MemoryStore | None = None, llm: ScriptedLLM | None = None) -> Hub:
    async def leads(_user_id: str, _project_id: str | None) -> Leader:
        return Leader("lead", llm or planning_llm(), [Worker("writer")])

    return Hub(leads, store or MemoryStore(), MemoryBroker())


async def collect(events: AsyncGenerator[Event, None], into: list[Event]) -> None:
    async for event in events:
        into.append(event)


async def test_questions_mail_and_plan_through_the_hub() -> None:
    hub = hub_with()
    conversation = await hub.create(ALICE, "x")
    seen: list[Event] = []
    listening = asyncio.create_task(collect(hub.broker.subscribe(conversation.id), seen))
    await asyncio.sleep(0)
    await hub.post(ALICE, conversation.id, "deploy")

    async def asked() -> bool:
        return bool(await hub.questions(ALICE, conversation.id))

    await eventually(asked)
    (question,) = await hub.questions(ALICE, conversation.id)
    assert (question.asker, question.audience, question.options) == ("lead", "user", ["eu", "us"])
    with pytest.raises(UnknownQuestion):
        await hub.answer(ALICE, conversation.id, "q9", "x")
    answered = await hub.answer(ALICE, conversation.id, question.id, "eu")
    assert (answered.status, answered.answer) == ("answered", "eu")
    with pytest.raises(NotAnswerable):
        await hub.answer(ALICE, conversation.id, question.id, "us")

    async def planned() -> bool:
        return bool(await hub.mail(ALICE, conversation.id))

    await eventually(planned)
    plan = await hub.plan(ALICE, conversation.id)
    (mail,) = await hub.mail(ALICE, conversation.id)
    assert [s.model_dump() for s in plan.steps] == PLAN
    assert (mail.sender, mail.recipient, mail.body) == ("lead", "*", "region is eu")
    assert await hub.questions(ALICE, conversation.id) == []
    assert [q.status for q in await hub.questions(ALICE, conversation.id, "all")] == ["answered"]

    await eventually(lambda: _has(seen, "mail"))
    kinds = [e["type"] for e in seen if e["type"] in ("question", "plan", "mail")]
    assert kinds == ["question", "question", "plan", "mail"]
    assert next(e for e in seen if e["type"] == "plan")["plan"] == {"steps": PLAN}
    assert next(e for e in seen if e["type"] == "question")["question"]["status"] == "open"
    listening.cancel()
    await hub.close()


async def _has(events: list[Event], kind: str) -> bool:
    return any(e["type"] == kind for e in events)


async def test_lead_questions_are_not_answered_by_the_user_and_restarts_cancel() -> None:
    store = MemoryStore()
    hub = hub_with(store)
    conversation = await hub.create(ALICE, "x")
    await hub.post(ALICE, conversation.id, "deploy")

    async def asked() -> bool:
        return bool(await hub.questions(ALICE, conversation.id))

    await eventually(asked)
    comms = hub._live[conversation.id].comms
    lead_question = await comms.ask("writer", "t1", "lead", "which db?")
    with pytest.raises(NotAnswerable, match="负责人"):
        await hub.answer(ALICE, conversation.id, lead_question.id, "pg")
    with pytest.raises(KeyError):
        await hub.questions(BOB, conversation.id)
    await hub.close()

    statuses = {q.id: q.status for q in await store.questions(conversation.id)}
    assert statuses == {"q1": "open", "q2": "cancelled"}  # closing cancels what blocks someone

    restarted = hub_with(store, ScriptedLLM([say("hi")]))
    await restarted.post(ALICE, conversation.id, "again")
    statuses = {q.id: q.status for q in await restarted.questions(ALICE, conversation.id, "all")}
    assert statuses == {"q1": "open", "q2": "cancelled"}
    live = restarted._live[conversation.id].comms
    assert (await live.ask("lead", None, "user", "more?")).id == "q3"
    await restarted.close()


async def test_the_leaders_question_is_answerable_after_a_restart() -> None:
    store = MemoryStore()
    hub = hub_with(store, ScriptedLLM([call("ask_user", {"question": "region?"}), say("asked")]))
    conversation = await hub.create(ALICE, "x")
    await hub.post(ALICE, conversation.id, "deploy")

    async def asked() -> bool:
        return any(m.content == "asked" for m in await hub.messages(ALICE, conversation.id))

    await eventually(asked)
    employee = await hub._live[conversation.id].comms.ask("writer", "t1", "user", "why?")
    crashed = hub._live.pop(conversation.id)  # a crash: nothing gets closed properly
    crashed.responder.cancel()

    restarted = hub_with(store, ScriptedLLM([say("noted")]))
    await restarted.recover()
    assert conversation.id not in restarted._live  # no unfinished tasks: not resumed
    assert [q.id for q in await restarted.questions(ALICE, conversation.id)] == ["q1", "q2"]

    answered = await restarted.answer(ALICE, conversation.id, "q1", "eu")
    assert (answered.status, answered.answer) == ("answered", "eu")
    with pytest.raises(NotAnswerable, match="取消"):
        await restarted.answer(ALICE, conversation.id, employee.id, "because")
    assert await restarted.questions(ALICE, conversation.id) == []

    async def told() -> bool:
        found = await restarted.messages(ALICE, conversation.id)
        return any(m.content == "noted" for m in found)

    await eventually(told)
    history = restarted._live[conversation.id].conversation.session.snapshot()
    assert "[Answer to q1] eu" in str(history)
    await restarted.close()


def test_question_routes(client: TestClient) -> None:  # noqa: F811 - the fixture
    alice = signup(client, "alice@example.com")
    bob = signup(client, "bob@example.com")
    state: Platform = client.app.state.platform  # type: ignore[attr-defined]

    async def leads(_user_id: str, _project_id: str | None) -> Leader:
        return Leader("lead", planning_llm(), [Worker("writer")])

    state.hub.leads = leads
    conversation = client.post("/api/conversations", json={}, headers=alice).json()
    base = f"/api/conversations/{conversation['id']}"
    assert client.post(f"{base}/messages", json={"content": "go"}, headers=alice).status_code == 202

    question = wait_for(lambda: client.get(f"{base}/questions", headers=alice).json())[0]
    assert question["id"] == "q1"
    assert question["kind"] == "question"
    assert question["options"] == ["eu", "us"]
    assert question["answered_at"] is None
    assert isinstance(question["created_at"], str)

    answer = f"{base}/questions/q1/answer"
    assert client.get(f"{base}/questions", headers=bob).status_code == 404
    assert client.post(answer, json={"answer": "eu"}, headers=bob).status_code == 404
    assert client.post(answer, json={"answer": " "}, headers=alice).status_code == 422
    missing = client.post(f"{base}/questions/q7/answer", json={"answer": "x"}, headers=alice)
    assert missing.status_code == 404
    done = client.post(answer, json={"answer": "eu"}, headers=alice)
    assert done.status_code == 200, done.text
    assert (done.json()["status"], done.json()["answer"]) == ("answered", "eu")
    assert client.post(answer, json={"answer": "us"}, headers=alice).status_code == 409

    mail = wait_for(lambda: client.get(f"{base}/mail", headers=alice).json())
    assert [(m["sender"], m["recipient"], m["body"]) for m in mail] == [
        ("lead", "*", "region is eu")
    ]
    assert client.get(f"{base}/plan", headers=alice).json() == {"steps": PLAN}
    assert client.get(f"{base}/questions", headers=alice).json() == []
    everything = client.get(f"{base}/questions?status=all", headers=alice).json()
    assert [q["status"] for q in everything] == ["answered"]
    assert client.get(f"{base}/plan", headers=bob).status_code == 404
    assert client.get(f"{base}/mail", headers=bob).status_code == 404


def wait_for(fetch: Any, timeout: float = 5.0) -> Any:
    deadline = time.monotonic() + timeout
    while not (value := fetch()):
        assert time.monotonic() < deadline, "timed out"
        time.sleep(0.02)
    return value


async def exercise_comms_store(store: Store, conversation_id: str) -> None:
    """The comms part of the store contract (memory and Postgres)."""
    await store.save_mail(conversation_id, Mail(1, "lead", "*", "全体注意", 1_700_000_000.5))
    await store.save_mail(conversation_id, Mail(2, "a", "b", "hi", 1_700_000_001.0))
    await store.save_mail(
        conversation_id, Mail(1, "lead", "*", "全体注意", 1_700_000_000.5, ("a",))
    )
    mail = await store.mail(conversation_id)
    assert [(m.id, m.body, m.read_by) for m in mail] == [(1, "全体注意", ("a",)), (2, "hi", ())]
    assert mail[0].created_at == pytest.approx(1_700_000_000.5)

    approval = Question(
        "q1",
        "a",
        "t1",
        "user",
        "approval",
        "a wants to use Bash",
        ("allow", "deny"),
        created_at=1_700_000_002.0,
        detail={"tool_name": "Bash", "input": {"command": "ls"}},
    )
    await store.save_question(conversation_id, approval)
    await store.save_question(
        conversation_id, Question("q2", "lead", None, "user", "question", "?", created_at=1.7e9 + 3)
    )
    await store.save_question(
        conversation_id,
        replace(approval, status="answered", answer="allow", answered_at=1_700_000_004.0),
    )
    first, second = await store.questions(conversation_id)
    assert first == replace(approval, status="answered", answer="allow", answered_at=1.7e9 + 4)
    assert (second.id, second.task_id, second.answered_at) == ("q2", None, None)

    assert await store.plan(conversation_id) == []
    await store.save_plan(conversation_id, PLAN)
    assert await store.plan(conversation_id) == PLAN

    await store.save_task(conversation_id, Task("t2", "qa", "review", parent="t1"))
    (task,) = await store.tasks(conversation_id)
    assert task.parent == "t1"
    assert task.to_task().parent == "t1"


async def test_memory_store_keeps_comms() -> None:
    store = MemoryStore()
    await store.create_conversation("c1", "", ALICE)
    await exercise_comms_store(store, "c1")
