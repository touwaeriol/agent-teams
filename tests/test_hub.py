from __future__ import annotations

import asyncio
import time
from collections.abc import Awaitable, Callable

import pytest

from agent_teams import Leader, ScriptedLLM, Task
from agent_teams.llm import call, say
from agent_teams.serde import dump_turn, load_turn
from agent_teams.server.broker import MemoryBroker
from agent_teams.server.events import sse
from agent_teams.server.hub import Hub, UnknownConversation
from agent_teams.server.models import MessageView
from agent_teams.server.store import INTERRUPTED, MemoryStore
from agent_teams.types import (
    AssistantTurn,
    Reply,
    ToolCall,
    ToolResult,
    ToolTurn,
    Turn,
    UserTurn,
)

from .fakes import Worker

ALICE, BOB = "alice", "bob"


class ResumableWorker(Worker):
    resumable = True


def poem_llm() -> ScriptedLLM:
    return ScriptedLLM(
        [
            call("assign_task", {"assignee": "writer", "instruction": "poem"}),
            say("on it"),
            say("here is the poem"),
            say("second request done"),
        ]
    )


def make_hub(leader: Leader, store: MemoryStore | None = None) -> Hub:
    async def leads(_user_id: str) -> Leader:
        return leader

    return Hub(leads, store or MemoryStore(), MemoryBroker())


def poem_hub(llm: ScriptedLLM | None = None, store: MemoryStore | None = None) -> Hub:
    return make_hub(Leader("lead", llm or poem_llm(), [Worker("writer")]), store)


async def eventually(check: Callable[[], Awaitable[bool]], timeout: float = 3.0) -> None:
    deadline = time.monotonic() + timeout
    while not await check():
        if time.monotonic() > deadline:
            raise AssertionError("condition not met in time")
        await asyncio.sleep(0.01)


async def replies(hub: Hub, conversation_id: str, user: str = ALICE) -> list[MessageView]:
    return [m for m in await hub.messages(user, conversation_id) if m.role != "user"]


async def wait_for_replies(hub: Hub, conversation_id: str, count: int) -> list[str]:
    async def enough() -> bool:
        return len(await replies(hub, conversation_id)) >= count

    await eventually(enough)
    return [m.content for m in await replies(hub, conversation_id)]


async def test_hub_records_replies_and_tasks() -> None:
    hub = poem_hub()
    conversation = await hub.create(ALICE, "poems")
    events = hub.broker.subscribe(conversation.id)
    first_event = asyncio.ensure_future(anext(events))
    await asyncio.sleep(0)

    await hub.post(ALICE, conversation.id, "write a poem")

    assert await wait_for_replies(hub, conversation.id, 2) == ["on it", "here is the poem"]
    (task,) = await hub.tasks(ALICE, conversation.id)
    assert (task.id, task.status, task.output) == ("t1", "done", "writer finished")
    assert (await first_event)["type"] == "message"
    await events.aclose()
    await hub.close()


async def test_conversations_belong_to_their_owner() -> None:
    hub = poem_hub()
    mine = await hub.create(ALICE, "mine")

    assert [c.id for c in await hub.conversations(ALICE)] == [mine.id]
    assert await hub.conversations(BOB) == []
    with pytest.raises(UnknownConversation):
        await hub.post(BOB, mine.id, "hi")
    with pytest.raises(UnknownConversation):
        await hub.messages(BOB, mine.id)
    with pytest.raises(UnknownConversation):
        await hub.tasks(BOB, "missing")


async def test_each_user_gets_their_own_leader() -> None:
    teams = {
        ALICE: Leader("alice-lead", ScriptedLLM(["from alice's team"]), [Worker("w")]),
        BOB: Leader("bob-lead", ScriptedLLM(["from bob's team"]), [Worker("w")]),
    }

    async def leads(user_id: str) -> Leader:
        return teams[user_id]

    hub = Hub(leads, MemoryStore(), MemoryBroker())
    a = await hub.create(ALICE)
    b = await hub.create(BOB)
    await hub.post(ALICE, a.id, "hi")
    await hub.post(BOB, b.id, "hi")

    assert await wait_for_replies(hub, a.id, 1) == ["from alice's team"]

    async def bob_answered() -> bool:
        return len(await replies(hub, b.id, BOB)) == 1

    await eventually(bob_answered)
    assert (await replies(hub, b.id, BOB))[0].content == "from bob's team"
    await hub.close()


async def test_hub_resumes_history_after_restart() -> None:
    store = MemoryStore()
    llm = poem_llm()
    hub = poem_hub(llm, store)
    conversation = await hub.create(ALICE)
    await hub.post(ALICE, conversation.id, "write a poem")
    await wait_for_replies(hub, conversation.id, 2)
    await hub.close()

    restarted = poem_hub(llm, store)
    await restarted.post(ALICE, conversation.id, "another one")
    await wait_for_replies(restarted, conversation.id, 3)

    _system, history, _tools = llm.calls[-1]
    assert isinstance(history[0], UserTurn)
    assert history[0].text == "write a poem"
    await restarted.close()


async def test_resume_marks_interrupted_tasks_and_continues_numbering() -> None:
    store = MemoryStore()
    conversation = await store.create_conversation("c1", "", ALICE)
    await store.save_task("c1", Task("t1", "writer", "old", status="running"))
    assign = call("assign_task", {"assignee": "writer", "instruction": "new"})
    llm = ScriptedLLM([assign, say("ok")])
    hub = poem_hub(llm, store)

    await hub.post(ALICE, conversation.id, "again")

    async def two_tasks() -> bool:
        return len(await store.tasks("c1")) == 2

    await eventually(two_tasks)
    old, new = await store.tasks("c1")
    assert (old.status, old.output) == ("failed", INTERRUPTED)
    assert (new.id, new.instruction) == ("t2", "new")
    await hub.close()


async def test_restart_reattaches_running_tasks_on_resumable_employees() -> None:
    store = MemoryStore()
    await store.create_conversation("c1", "", ALICE)
    await store.save_task("c1", Task("t1", "coder", "build it", status="running"))
    await store.save_task("c1", Task("t2", "writer", "draft it", status="running"))
    await store.save_task("c1", Task("t3", "coder", "later", ("t1",), status="pending"))
    coder = ResumableWorker("coder")
    llm = ScriptedLLM([say("noted"), say("coder finished")])
    hub = make_hub(Leader("lead", llm, [coder, Worker("writer")]), store)

    await hub.post(ALICE, "c1", "status?")
    await wait_for_replies(hub, "c1", 2)

    tasks = {t.id: t for t in await store.tasks("c1")}
    assert (tasks["t1"].status, tasks["t1"].output) == ("done", "coder finished")
    assert coder.tasks == ["build it"]
    assert (tasks["t2"].status, tasks["t2"].output) == ("failed", INTERRUPTED)
    assert (tasks["t3"].status, tasks["t3"].output) == ("failed", INTERRUPTED)
    await hub.close()


async def test_recover_resumes_unfinished_conversations_without_a_message() -> None:
    store = MemoryStore()
    await store.create_conversation("c1", "", ALICE)
    await store.save_task("c1", Task("t1", "coder", "build it", status="running"))
    coder = ResumableWorker("coder")
    hub = make_hub(Leader("lead", ScriptedLLM([say("coder finished")]), [coder]), store)

    await hub.recover()

    async def done() -> bool:
        return (await store.tasks("c1"))[0].status == "done"

    await eventually(done)
    assert await store.unfinished_conversations() == []
    await hub.close()


async def test_close_user_stops_only_their_conversations() -> None:
    hub = poem_hub(ScriptedLLM(lambda _s, _h: say("ok")))
    a = await hub.create(ALICE)
    b = await hub.create(BOB)
    await hub.post(ALICE, a.id, "hi")
    await hub.post(BOB, b.id, "hi")

    await hub.close_user(ALICE)

    assert a.id not in hub._live
    assert b.id in hub._live
    await hub.close()


async def test_hub_reports_model_errors_as_messages() -> None:
    refusal = Reply(AssistantTurn(""), stop="refusal", detail="cyber: no")
    hub = poem_hub(ScriptedLLM([refusal]))
    conversation = await hub.create(ALICE)
    await hub.post(ALICE, conversation.id, "bad")

    async def errored() -> bool:
        return any(m.role == "error" for m in await hub.messages(ALICE, conversation.id))

    await eventually(errored)
    await hub.close()


async def test_sse_formats_events_and_sends_heartbeats() -> None:
    broker = MemoryBroker()
    stream = sse(broker.subscribe("c1"), heartbeat=0.05)

    assert await anext(stream) == ": ping\n\n"
    await broker.publish("c1", {"type": "activity", "text": "你好"})
    assert await anext(stream) == 'data: {"type": "activity", "text": "你好"}\n\n'
    await stream.aclose()


def test_turns_round_trip_through_json() -> None:
    turns: list[Turn] = [
        UserTurn("hi"),
        AssistantTurn("t", (ToolCall("1", "x", {"a": 1}),), raw=[{"type": "text", "text": "t"}]),
        ToolTurn((ToolResult("1", "out", is_error=True),)),
    ]
    assert [load_turn(dump_turn(t)) for t in turns] == turns
