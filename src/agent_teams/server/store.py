"""Persistence for conversations, messages, tasks and leader history.

``Store`` is the port; ``PostgresStore`` is the production adapter and
``MemoryStore`` serves tests and single-process demos. Tables are prefixed
with ``at_`` so they can live in an existing database.
"""

from __future__ import annotations

import json
from collections import defaultdict
from datetime import datetime, timezone
from typing import Any, Protocol

import asyncpg

from ..types import Task
from .models import ConversationView, MessageView, Role, TaskView

SCHEMA = """
CREATE TABLE IF NOT EXISTS at_conversations (
    id TEXT PRIMARY KEY,
    title TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    history JSONB NOT NULL DEFAULT '[]'::jsonb
);
CREATE TABLE IF NOT EXISTS at_messages (
    id BIGSERIAL PRIMARY KEY,
    conversation_id TEXT NOT NULL REFERENCES at_conversations(id) ON DELETE CASCADE,
    role TEXT NOT NULL,
    content TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS at_messages_conversation ON at_messages (conversation_id, id);
CREATE TABLE IF NOT EXISTS at_tasks (
    conversation_id TEXT NOT NULL REFERENCES at_conversations(id) ON DELETE CASCADE,
    id TEXT NOT NULL,
    assignee TEXT NOT NULL,
    instruction TEXT NOT NULL,
    depends_on JSONB NOT NULL,
    status TEXT NOT NULL,
    output TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (conversation_id, id)
);
"""

INTERRUPTED = "Interrupted: the server restarted before this task finished."


class Store(Protocol):
    async def create_conversation(self, conversation_id: str, title: str) -> ConversationView: ...
    async def conversations(self) -> list[ConversationView]: ...
    async def exists(self, conversation_id: str) -> bool: ...
    async def add_message(self, conversation_id: str, role: Role, content: str) -> MessageView: ...
    async def messages(self, conversation_id: str) -> list[MessageView]: ...
    async def save_task(self, conversation_id: str, task: Task) -> None: ...
    async def tasks(self, conversation_id: str) -> list[TaskView]: ...
    async def save_history(self, conversation_id: str, turns: list[dict[str, Any]]) -> None: ...
    async def history(self, conversation_id: str) -> list[dict[str, Any]]: ...
    async def interrupt_unfinished(self, conversation_id: str) -> None: ...
    async def close(self) -> None: ...


def _now() -> datetime:
    return datetime.now(timezone.utc)


class MemoryStore:
    """Keeps everything in process memory."""

    def __init__(self) -> None:
        self._conversations: dict[str, ConversationView] = {}
        self._messages: defaultdict[str, list[MessageView]] = defaultdict(list)
        self._tasks: defaultdict[str, dict[str, TaskView]] = defaultdict(dict)
        self._histories: dict[str, list[dict[str, Any]]] = {}
        self._next_id = 1

    async def create_conversation(self, conversation_id: str, title: str) -> ConversationView:
        view = ConversationView(id=conversation_id, title=title, created_at=_now())
        self._conversations[conversation_id] = view
        return view

    async def conversations(self) -> list[ConversationView]:
        return sorted(self._conversations.values(), key=lambda c: c.created_at, reverse=True)

    async def exists(self, conversation_id: str) -> bool:
        return conversation_id in self._conversations

    async def add_message(self, conversation_id: str, role: Role, content: str) -> MessageView:
        message = MessageView(id=self._next_id, role=role, content=content, created_at=_now())
        self._next_id += 1
        self._messages[conversation_id].append(message)
        return message

    async def messages(self, conversation_id: str) -> list[MessageView]:
        return list(self._messages[conversation_id])

    async def save_task(self, conversation_id: str, task: Task) -> None:
        self._tasks[conversation_id][task.id] = TaskView.of(task)

    async def tasks(self, conversation_id: str) -> list[TaskView]:
        return list(self._tasks[conversation_id].values())

    async def save_history(self, conversation_id: str, turns: list[dict[str, Any]]) -> None:
        self._histories[conversation_id] = turns

    async def history(self, conversation_id: str) -> list[dict[str, Any]]:
        return list(self._histories.get(conversation_id, []))

    async def interrupt_unfinished(self, conversation_id: str) -> None:
        tasks = self._tasks[conversation_id]
        for task_id, view in tasks.items():
            if view.status in ("pending", "running"):
                tasks[task_id] = view.model_copy(update={"status": "failed", "output": INTERRUPTED})

    async def close(self) -> None:
        return None


class PostgresStore:
    """asyncpg-backed store; call :meth:`connect` to create it."""

    def __init__(self, pool: asyncpg.Pool) -> None:
        self._pool = pool

    @classmethod
    async def connect(cls, dsn: str) -> PostgresStore:
        pool = await asyncpg.create_pool(dsn, min_size=1, max_size=5)
        async with pool.acquire() as connection:
            await connection.execute(SCHEMA)
        return cls(pool)

    async def create_conversation(self, conversation_id: str, title: str) -> ConversationView:
        row = await self._pool.fetchrow(
            "INSERT INTO at_conversations (id, title) VALUES ($1, $2) "
            "RETURNING id, title, created_at",
            conversation_id,
            title,
        )
        return ConversationView(**dict(row))

    async def conversations(self) -> list[ConversationView]:
        rows = await self._pool.fetch(
            "SELECT id, title, created_at FROM at_conversations ORDER BY created_at DESC"
        )
        return [ConversationView(**dict(r)) for r in rows]

    async def exists(self, conversation_id: str) -> bool:
        found = await self._pool.fetchval(
            "SELECT 1 FROM at_conversations WHERE id = $1", conversation_id
        )
        return found is not None

    async def add_message(self, conversation_id: str, role: Role, content: str) -> MessageView:
        row = await self._pool.fetchrow(
            "INSERT INTO at_messages (conversation_id, role, content) VALUES ($1, $2, $3) "
            "RETURNING id, role, content, created_at",
            conversation_id,
            role,
            content,
        )
        return MessageView(**dict(row))

    async def messages(self, conversation_id: str) -> list[MessageView]:
        rows = await self._pool.fetch(
            "SELECT id, role, content, created_at FROM at_messages "
            "WHERE conversation_id = $1 ORDER BY id",
            conversation_id,
        )
        return [MessageView(**dict(r)) for r in rows]

    async def save_task(self, conversation_id: str, task: Task) -> None:
        await self._pool.execute(
            "INSERT INTO at_tasks "
            "(conversation_id, id, assignee, instruction, depends_on, status, output) "
            "VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7) "
            "ON CONFLICT (conversation_id, id) DO UPDATE SET "
            "status = EXCLUDED.status, output = EXCLUDED.output, updated_at = now()",
            conversation_id,
            task.id,
            task.assignee,
            task.instruction,
            json.dumps(list(task.depends_on)),
            task.status,
            task.output,
        )

    async def tasks(self, conversation_id: str) -> list[TaskView]:
        rows = await self._pool.fetch(
            "SELECT id, assignee, instruction, depends_on, status, output FROM at_tasks "
            "WHERE conversation_id = $1 ORDER BY length(id), id",
            conversation_id,
        )
        return [TaskView(**{**dict(r), "depends_on": json.loads(r["depends_on"])}) for r in rows]

    async def save_history(self, conversation_id: str, turns: list[dict[str, Any]]) -> None:
        await self._pool.execute(
            "UPDATE at_conversations SET history = $2::jsonb WHERE id = $1",
            conversation_id,
            json.dumps(turns, ensure_ascii=False),
        )

    async def history(self, conversation_id: str) -> list[dict[str, Any]]:
        raw = await self._pool.fetchval(
            "SELECT history FROM at_conversations WHERE id = $1", conversation_id
        )
        return list(json.loads(raw)) if raw else []

    async def interrupt_unfinished(self, conversation_id: str) -> None:
        await self._pool.execute(
            "UPDATE at_tasks SET status = 'failed', output = $2, updated_at = now() "
            "WHERE conversation_id = $1 AND status IN ('pending', 'running')",
            conversation_id,
            INTERRUPTED,
        )

    async def close(self) -> None:
        await self._pool.close()
