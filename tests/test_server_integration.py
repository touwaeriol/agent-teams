"""Runs only when real services are configured:

TEST_DATABASE_URL=postgresql://... TEST_VALKEY_URL=redis://... pytest
"""

from __future__ import annotations

import asyncio
import os
import uuid

import pytest

from agent_teams import Task
from agent_teams.server.accounts import Conflict
from agent_teams.server.broker import ValkeyBroker
from agent_teams.server.store import connect_postgres

DATABASE_URL = os.environ.get("TEST_DATABASE_URL", "")
VALKEY_URL = os.environ.get("TEST_VALKEY_URL", "")


@pytest.mark.skipif(not DATABASE_URL, reason="TEST_DATABASE_URL not set")
async def test_postgres_store_round_trip() -> None:
    store, accounts = await connect_postgres(DATABASE_URL)
    conversation_id = uuid.uuid4().hex[:12]
    try:
        owner = await accounts.create_user(f"{conversation_id}@example.com", "h", "user")
        created = await store.create_conversation(conversation_id, "测试", owner.id)
        assert await store.owner(conversation_id) == owner.id
        assert await store.owner("missing") is None
        assert [c.id for c in await store.conversations(owner.id)] == [created.id]

        first = await store.add_message(conversation_id, "user", "你好")
        await store.add_message(conversation_id, "leader", "hi")
        assert [m.content for m in await store.messages(conversation_id)] == ["你好", "hi"]
        assert first.role == "user"

        for number in range(1, 12):
            await store.save_task(conversation_id, Task(f"t{number}", "w", "x", ("t1",)))
        await store.save_task(conversation_id, Task("t1", "w", "x", status="done", output="ok"))
        await store.save_task(conversation_id, Task("t2", "w", "x", status="running"))
        tasks = await store.tasks(conversation_id)
        assert [t.id for t in tasks][:3] == ["t1", "t2", "t3"]
        assert tasks[-1].id == "t11"
        assert (tasks[0].status, tasks[0].output) == ("done", "ok")
        assert tasks[2].depends_on == ["t1"]

        assert tasks[1].status == "running"
        assert conversation_id in await store.unfinished_conversations()
        assert not tasks[1].finished
        assert tasks[0].finished

        turns = {"kind": "agent", "history": [{"kind": "user", "text": "你好"}]}
        await store.save_history(conversation_id, turns)
        assert await store.history(conversation_id) == turns
    finally:
        await store.close()


@pytest.mark.skipif(not VALKEY_URL, reason="TEST_VALKEY_URL not set")
async def test_valkey_broker_fans_out() -> None:
    broker = await ValkeyBroker.connect(VALKEY_URL)
    conversation_id = uuid.uuid4().hex[:12]
    events = broker.subscribe(conversation_id)
    received = asyncio.ensure_future(anext(events))
    try:
        for _ in range(50):  # wait until the subscription is active
            await broker.publish(conversation_id, {"type": "activity", "text": "你好"})
            done, _ = await asyncio.wait({received}, timeout=0.1)
            if done:
                break
        assert received.result() == {"type": "activity", "text": "你好"}
    finally:
        await events.aclose()
        await broker.close()


@pytest.mark.skipif(not DATABASE_URL, reason="TEST_DATABASE_URL not set")
async def test_postgres_accounts_round_trip() -> None:
    store, accounts = await connect_postgres(DATABASE_URL)
    tag = uuid.uuid4().hex[:8]
    try:
        user = await accounts.create_user(f"{tag}@example.com", "hash", "user")
        with pytest.raises(Conflict):
            await accounts.create_user(f"{tag}@example.com", "hash", "user")
        found = await accounts.credentials(f"{tag}@example.com")
        assert found is not None
        assert (found[0].id, found[1]) == (user.id, "hash")
        assert (await accounts.user(user.id)) == user

        await accounts.create_session(user.id, f"s-{tag}")
        assert (await accounts.session_user(f"s-{tag}")) == user
        updated = await accounts.update_user(user.id, {"max_nodes": 2, "role": "admin"})
        assert updated is not None
        assert (updated.max_nodes, updated.role) == (2, "admin")
        await accounts.update_user(user.id, {"active": False})
        assert await accounts.session_user(f"s-{tag}") is None  # disabled users have no session

        node = await accounts.create_node(user.id, "box", f"n-{tag}")
        with pytest.raises(Conflict):
            await accounts.create_node(user.id, "box", f"n2-{tag}")
        assert await accounts.node_by_token(f"n-{tag}") == (node, user.id)
        assert await accounts.set_node_token(user.id, node.id, f"n3-{tag}")
        assert await accounts.node_by_token(f"n-{tag}") is None
        assert not await accounts.set_node_token("someone-else", node.id, "x")

        await accounts.save_team(user.id, "leader: a")
        await accounts.save_team(user.id, "leader: b")
        assert await accounts.team(user.id) == "leader: b"

        settings = await accounts.settings()
        await accounts.save_settings(settings.model_copy(update={"default_max_nodes": 7}))
        assert (await accounts.settings()).default_max_nodes == 7
        await accounts.save_settings(settings)

        assert await accounts.delete_node(user.id, node.id)
        assert await accounts.delete_user(user.id)
        assert await accounts.user(user.id) is None
    finally:
        await store.close()
