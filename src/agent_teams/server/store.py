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
from .accounts import SCHEMA as ACCOUNTS_SCHEMA
from .accounts import PostgresAccounts
from .models import ConversationView, MessageView, Role, TaskView
from .projects import SCHEMA as PROJECTS_SCHEMA
from .projects import PostgresProjects

SCHEMA = """
CREATE TABLE IF NOT EXISTS at_conversations (
    id TEXT PRIMARY KEY,
    user_id TEXT REFERENCES at_users(id) ON DELETE CASCADE,
    title TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    history JSONB NOT NULL DEFAULT '{}'::jsonb
);
ALTER TABLE at_conversations
    ADD COLUMN IF NOT EXISTS user_id TEXT REFERENCES at_users(id) ON DELETE CASCADE;
CREATE INDEX IF NOT EXISTS at_conversations_user ON at_conversations (user_id, created_at);
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
    async def create_conversation(
        self, conversation_id: str, title: str, user_id: str, project_id: str | None = None
    ) -> ConversationView: ...
    async def conversations(
        self, user_id: str, project_id: str | None = None
    ) -> list[ConversationView]: ...
    async def owner(self, conversation_id: str) -> str | None: ...
    async def project_of(self, conversation_id: str) -> str | None: ...
    async def add_message(self, conversation_id: str, role: Role, content: str) -> MessageView: ...
    async def messages(self, conversation_id: str) -> list[MessageView]: ...
    async def save_task(self, conversation_id: str, task: Task) -> None: ...
    async def tasks(self, conversation_id: str) -> list[TaskView]: ...
    async def unfinished_conversations(self) -> list[str]: ...
    async def save_history(self, conversation_id: str, state: dict[str, Any]) -> None: ...
    async def history(self, conversation_id: str) -> dict[str, Any]: ...
    async def close(self) -> None: ...


def _now() -> datetime:
    return datetime.now(timezone.utc)


class MemoryStore:
    """Keeps everything in process memory."""

    def __init__(self) -> None:
        self._conversations: dict[str, ConversationView] = {}
        self._owners: dict[str, str] = {}
        self._messages: defaultdict[str, list[MessageView]] = defaultdict(list)
        self._tasks: defaultdict[str, dict[str, TaskView]] = defaultdict(dict)
        self._histories: dict[str, dict[str, Any]] = {}
        self._next_id = 1

    async def create_conversation(
        self, conversation_id: str, title: str, user_id: str, project_id: str | None = None
    ) -> ConversationView:
        view = ConversationView(
            id=conversation_id, title=title, created_at=_now(), project_id=project_id
        )
        self._conversations[conversation_id] = view
        self._owners[conversation_id] = user_id
        return view

    async def conversations(
        self, user_id: str, project_id: str | None = None
    ) -> list[ConversationView]:
        mine = [
            c
            for c in self._conversations.values()
            if self._owners[c.id] == user_id and (project_id is None or c.project_id == project_id)
        ]
        return sorted(mine, key=lambda c: c.created_at, reverse=True)

    async def owner(self, conversation_id: str) -> str | None:
        return self._owners.get(conversation_id)

    async def project_of(self, conversation_id: str) -> str | None:
        view = self._conversations.get(conversation_id)
        return view.project_id if view else None

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

    async def unfinished_conversations(self) -> list[str]:
        return [
            cid for cid, tasks in self._tasks.items() if any(not t.finished for t in tasks.values())
        ]

    async def save_history(self, conversation_id: str, state: dict[str, Any]) -> None:
        self._histories[conversation_id] = state

    async def history(self, conversation_id: str) -> dict[str, Any]:
        return dict(self._histories.get(conversation_id, {}))

    async def close(self) -> None:
        return None


async def connect_postgres(
    dsn: str,
) -> tuple[PostgresStore, PostgresAccounts, PostgresProjects]:
    """One pool for all stores; creates or upgrades the schema."""
    pool = await asyncpg.create_pool(dsn, min_size=1, max_size=10)
    async with pool.acquire() as connection:
        await connection.execute(ACCOUNTS_SCHEMA)
        await connection.execute(SCHEMA)
        await connection.execute(PROJECTS_SCHEMA)
    return PostgresStore(pool), PostgresAccounts(pool), PostgresProjects(pool)


class PostgresStore:
    """asyncpg-backed store; create it with :func:`connect_postgres`."""

    def __init__(self, pool: asyncpg.Pool) -> None:
        self._pool = pool

    async def create_conversation(
        self, conversation_id: str, title: str, user_id: str, project_id: str | None = None
    ) -> ConversationView:
        row = await self._pool.fetchrow(
            "INSERT INTO at_conversations (id, title, user_id, project_id) "
            "VALUES ($1, $2, $3, $4) RETURNING id, title, created_at, project_id",
            conversation_id,
            title,
            user_id,
            project_id,
        )
        return ConversationView(**dict(row))

    async def conversations(
        self, user_id: str, project_id: str | None = None
    ) -> list[ConversationView]:
        rows = await self._pool.fetch(
            "SELECT id, title, created_at, project_id FROM at_conversations "
            "WHERE user_id = $1 AND ($2::text IS NULL OR project_id = $2) "
            "ORDER BY created_at DESC",
            user_id,
            project_id,
        )
        return [ConversationView(**dict(r)) for r in rows]

    async def project_of(self, conversation_id: str) -> str | None:
        value = await self._pool.fetchval(
            "SELECT project_id FROM at_conversations WHERE id = $1", conversation_id
        )
        return str(value) if value is not None else None

    async def owner(self, conversation_id: str) -> str | None:
        value = await self._pool.fetchval(
            "SELECT user_id FROM at_conversations WHERE id = $1", conversation_id
        )
        return str(value) if value is not None else None

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

    async def unfinished_conversations(self) -> list[str]:
        rows = await self._pool.fetch(
            "SELECT DISTINCT conversation_id FROM at_tasks WHERE status IN ('pending', 'running')"
        )
        return [r["conversation_id"] for r in rows]

    async def save_history(self, conversation_id: str, state: dict[str, Any]) -> None:
        await self._pool.execute(
            "UPDATE at_conversations SET history = $2::jsonb WHERE id = $1",
            conversation_id,
            json.dumps(state, ensure_ascii=False),
        )

    async def history(self, conversation_id: str) -> dict[str, Any]:
        raw = await self._pool.fetchval(
            "SELECT history FROM at_conversations WHERE id = $1", conversation_id
        )
        state = json.loads(raw) if raw else {}
        return state if isinstance(state, dict) else {}

    async def close(self) -> None:
        await self._pool.close()
