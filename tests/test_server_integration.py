"""Runs only when real services are configured:

TEST_DATABASE_URL=postgresql://... TEST_VALKEY_URL=redis://... pytest
"""

from __future__ import annotations

import asyncio
import os
import uuid

import pytest

from agent_teams import Task
from agent_teams.server.broker import ValkeyBroker
from agent_teams.server.store import INTERRUPTED, PostgresStore

DATABASE_URL = os.environ.get("TEST_DATABASE_URL", "")
VALKEY_URL = os.environ.get("TEST_VALKEY_URL", "")


@pytest.mark.skipif(not DATABASE_URL, reason="TEST_DATABASE_URL not set")
async def test_postgres_store_round_trip() -> None:
    store = await PostgresStore.connect(DATABASE_URL)
    conversation_id = uuid.uuid4().hex[:12]
    try:
        created = await store.create_conversation(conversation_id, "测试")
        assert await store.exists(conversation_id)
        assert not await store.exists("missing")
        assert created.id in [c.id for c in await store.conversations()]

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

        await store.interrupt_unfinished(conversation_id)
        tasks = await store.tasks(conversation_id)
        assert (tasks[1].status, tasks[1].output) == ("failed", INTERRUPTED)

        turns = [{"kind": "user", "text": "你好"}]
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
