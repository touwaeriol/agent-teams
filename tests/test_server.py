from __future__ import annotations

import asyncio
import time
from collections.abc import Awaitable, Callable

import pytest
from fastapi.testclient import TestClient
from starlette.websockets import WebSocketDisconnect

from agent_teams import Leader, ScriptedLLM, Task
from agent_teams.llm import call, say
from agent_teams.remote import NodeRegistry
from agent_teams.server.app import create_app, sse
from agent_teams.server.broker import MemoryBroker
from agent_teams.server.hub import Hub, UnknownConversation
from agent_teams.server.models import MessageView
from agent_teams.server.serde import dump_turn, load_turn
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


def poem_llm() -> ScriptedLLM:
    return ScriptedLLM(
        [
            call("assign_task", {"assignee": "writer", "instruction": "poem"}),
            say("on it"),
            say("here is the poem"),
            say("second request done"),
        ]
    )


def make_hub(llm: ScriptedLLM, store: MemoryStore | None = None) -> Hub:
    leader = Leader("lead", llm, [Worker("writer")])
    return Hub(leader, store or MemoryStore(), MemoryBroker())


async def eventually(check: Callable[[], Awaitable[bool]], timeout: float = 3.0) -> None:
    deadline = time.monotonic() + timeout
    while not await check():
        if time.monotonic() > deadline:
            raise AssertionError("condition not met in time")
        await asyncio.sleep(0.01)


async def leader_messages(hub: Hub, conversation_id: str) -> list[MessageView]:
    return [m for m in await hub.messages(conversation_id) if m.role != "user"]


async def test_hub_records_replies_and_tasks() -> None:
    hub = make_hub(poem_llm())
    conversation = await hub.create("poems")
    events = hub.broker.subscribe(conversation.id)
    first_event = asyncio.ensure_future(anext(events))
    await asyncio.sleep(0)

    await hub.post(conversation.id, "write a poem")

    async def two_replies() -> bool:
        return len(await leader_messages(hub, conversation.id)) == 2

    await eventually(two_replies)
    replies = [m.content for m in await leader_messages(hub, conversation.id)]
    assert replies == ["on it", "here is the poem"]
    (task,) = await hub.tasks(conversation.id)
    assert (task.id, task.status, task.output) == ("t1", "done", "writer finished")
    assert (await first_event)["type"] == "message"
    await events.aclose()
    await hub.close()


async def test_hub_resumes_history_after_restart() -> None:
    store = MemoryStore()
    llm = poem_llm()
    hub = make_hub(llm, store)
    conversation = await hub.create()
    await hub.post(conversation.id, "write a poem")

    async def finished() -> bool:
        return len(await leader_messages(hub, conversation.id)) == 2

    await eventually(finished)
    await hub.close()

    restarted = make_hub(llm, store)
    await restarted.post(conversation.id, "another one")

    async def answered() -> bool:
        return len(await leader_messages(restarted, conversation.id)) == 3

    await eventually(answered)
    _system, history, _tools = llm.calls[-1]
    assert isinstance(history[0], UserTurn)
    assert history[0].text == "write a poem"
    assert len(history) > 3
    await restarted.close()


async def test_resume_marks_interrupted_tasks_and_continues_numbering() -> None:
    store = MemoryStore()
    conversation = await store.create_conversation("c1", "")
    await store.save_task("c1", Task("t1", "writer", "old", status="running"))
    llm = ScriptedLLM(
        [call("assign_task", {"assignee": "writer", "instruction": "new"}), say("ok")]
    )
    hub = make_hub(llm, store)

    await hub.post(conversation.id, "again")

    async def two_tasks() -> bool:
        return len(await store.tasks("c1")) == 2

    await eventually(two_tasks)
    old, new = await store.tasks("c1")
    assert (old.status, old.output) == ("failed", INTERRUPTED)
    assert (new.id, new.instruction) == ("t2", "new")
    await hub.close()


async def test_hub_reports_model_errors_as_messages() -> None:
    hub = make_hub(ScriptedLLM([Reply(AssistantTurn(""), stop="refusal", detail="cyber: no")]))
    conversation = await hub.create()
    await hub.post(conversation.id, "bad")

    async def errored() -> bool:
        return any(m.role == "error" for m in await hub.messages(conversation.id))

    await eventually(errored)
    await hub.close()


async def test_unknown_conversations_are_rejected() -> None:
    hub = make_hub(ScriptedLLM([]))
    with pytest.raises(UnknownConversation):
        await hub.post("missing", "hi")
    with pytest.raises(UnknownConversation):
        await hub.messages("missing")


async def test_sse_formats_events_and_sends_heartbeats() -> None:
    broker = MemoryBroker()
    stream = sse(broker.subscribe("c1"), heartbeat=0.05)

    assert await anext(stream) == ": ping\n\n"
    await broker.publish("c1", {"type": "activity", "text": "你好"})
    assert await anext(stream) == 'data: {"type": "activity", "text": "你好"}\n\n'

    await stream.aclose()
    await broker.publish("c1", {"type": "after close"})  # no subscribers left


def test_turns_round_trip_through_json() -> None:
    turns: list[Turn] = [
        UserTurn("hi"),
        AssistantTurn("t", (ToolCall("1", "x", {"a": 1}),), raw=[{"type": "text", "text": "t"}]),
        ToolTurn((ToolResult("1", "out", is_error=True),)),
    ]
    assert [load_turn(dump_turn(t)) for t in turns] == turns


@pytest.fixture
def client() -> TestClient:
    async def factory() -> Hub:
        return make_hub(poem_llm())

    return TestClient(create_app(factory, "secret"))


AUTH = {"Authorization": "Bearer secret"}


def test_api_requires_the_token(client: TestClient) -> None:
    with client:
        assert client.get("/api/conversations").status_code == 401
        assert (
            client.get("/api/conversations", headers={"Authorization": "Bearer x"}).status_code
            == 401
        )
        assert client.get("/api/conversations?token=secret").status_code == 401
        assert client.get("/healthz").json() == {"ok": True}
        assert "Agent Teams" in client.get("/").text


def test_browser_session_cookie(client: TestClient) -> None:
    with client:
        assert client.post("/session", json={"token": "wrong"}).status_code == 401
        login = client.post("/session", json={"token": "secret"})
        assert login.status_code == 204
        assert "httponly" in login.headers["set-cookie"].lower()
        assert client.get("/api/conversations").status_code == 200
        client.delete("/session")
        client.cookies.clear()
        assert client.get("/api/conversations").status_code == 401


def test_api_conversation_flow(client: TestClient) -> None:
    with client:
        created = client.post("/api/conversations", json={"title": "诗"}, headers=AUTH)
        assert created.status_code == 201
        conversation_id = created.json()["id"]
        base = f"/api/conversations/{conversation_id}"

        posted = client.post(f"{base}/messages", json={"content": "write a poem"}, headers=AUTH)
        assert posted.status_code == 202
        assert posted.json()["role"] == "user"
        assert (
            client.post(f"{base}/messages", json={"content": "  "}, headers=AUTH).status_code == 422
        )

        deadline = time.monotonic() + 3
        while len(client.get(f"{base}/messages", headers=AUTH).json()) < 3:
            assert time.monotonic() < deadline
            time.sleep(0.02)

        roles = [m["role"] for m in client.get(f"{base}/messages", headers=AUTH).json()]
        assert roles == ["user", "leader", "leader"]
        assert client.get(f"{base}/tasks", headers=AUTH).json()[0]["status"] == "done"
        assert [c["title"] for c in client.get("/api/conversations", headers=AUTH).json()] == ["诗"]


def test_api_unknown_conversation_is_404(client: TestClient) -> None:
    with client:
        base = "/api/conversations/nope"
        assert client.get(f"{base}/messages", headers=AUTH).status_code == 404
        assert (
            client.post(f"{base}/messages", json={"content": "x"}, headers=AUTH).status_code == 404
        )
        assert client.get(f"{base}/events", headers=AUTH).status_code == 404


class ResumableWorker(Worker):
    resumable = True


async def test_restart_reattaches_running_tasks_on_resumable_employees() -> None:
    store = MemoryStore()
    await store.create_conversation("c1", "")
    await store.save_task("c1", Task("t1", "coder", "build it", status="running"))
    await store.save_task("c1", Task("t2", "writer", "draft it", status="running"))
    await store.save_task("c1", Task("t3", "coder", "later", ("t1",), status="pending"))
    coder = ResumableWorker("coder")
    llm = ScriptedLLM([say("noted"), say("coder finished")])
    hub = Hub(Leader("lead", llm, [coder, Worker("writer")]), store, MemoryBroker())

    await hub.post("c1", "status?")

    async def settled() -> bool:
        return len(await leader_messages(hub, "c1")) == 2

    await eventually(settled)
    tasks = {t.id: t for t in await store.tasks("c1")}
    assert (tasks["t1"].status, tasks["t1"].output) == ("done", "coder finished")
    assert coder.tasks == ["build it"]  # re-run under the same ID -> same run key
    assert (tasks["t2"].status, tasks["t2"].output) == ("failed", INTERRUPTED)
    assert (tasks["t3"].status, tasks["t3"].output) == ("failed", INTERRUPTED)
    _system, history, _tools = llm.calls[-1]
    assert any("[Task t1 · coder · done]" in str(turn) for turn in history)
    await hub.close()


def test_api_lists_nodes() -> None:
    registry = NodeRegistry("s")
    registry.declare("build-1")

    async def factory() -> Hub:
        return Hub(
            Leader("lead", ScriptedLLM([]), [Worker("w")]), MemoryStore(), MemoryBroker(), registry
        )

    with TestClient(create_app(factory, "secret")) as client:
        nodes = client.get("/api/nodes", headers=AUTH).json()
        assert nodes == [{"name": "build-1", "online": False}]
        assert client.get("/api/nodes").status_code == 401


def test_node_socket_rejects_bad_tokens() -> None:
    registry = NodeRegistry("s")
    registry.declare("build-1")

    async def factory() -> Hub:
        return Hub(
            Leader("lead", ScriptedLLM([]), [Worker("w")]), MemoryStore(), MemoryBroker(), registry
        )

    with TestClient(create_app(factory, "secret")) as client:
        headers = {"Authorization": "Bearer wrong"}
        with (
            pytest.raises(WebSocketDisconnect),
            client.websocket_connect("/nodes/connect?name=build-1", headers=headers),
        ):
            pass


async def test_recover_resumes_unfinished_conversations_without_a_message() -> None:
    store = MemoryStore()
    await store.create_conversation("c1", "")
    await store.save_task("c1", Task("t1", "coder", "build it", status="running"))
    await store.create_conversation("c2", "")
    await store.save_task("c2", Task("t1", "coder", "old", status="done", output="ok"))
    coder = ResumableWorker("coder")
    hub = Hub(Leader("lead", ScriptedLLM([say("coder finished")]), [coder]), store, MemoryBroker())

    await hub.recover()

    async def done() -> bool:
        return (await store.tasks("c1"))[0].status == "done"

    await eventually(done)
    assert await store.unfinished_conversations() == []
    assert coder.tasks == ["build it"]
    await hub.close()
