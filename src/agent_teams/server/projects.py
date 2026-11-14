"""Projects (a repository plus context for the team) and Git connections.

``ProjectStore`` is the port; ``PostgresProjects`` is the production adapter
and ``MemoryProjects`` serves tests. Git tokens are stored encrypted (see
``secrets.py``); this module only moves the ciphertext around.
"""

from __future__ import annotations

import uuid
from datetime import datetime, timezone
from typing import Any, Protocol

import asyncpg

from .accounts import Conflict
from .models import GitConnectionView, ProjectView

SCHEMA = """
CREATE TABLE IF NOT EXISTS at_git_connections (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES at_users(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    provider TEXT NOT NULL DEFAULT 'github',
    login TEXT NOT NULL DEFAULT '',
    token_secret TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, name)
);
CREATE TABLE IF NOT EXISTS at_projects (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES at_users(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    context TEXT NOT NULL DEFAULT '',
    repo TEXT,
    branch TEXT NOT NULL DEFAULT '',
    git_connection_id TEXT REFERENCES at_git_connections(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, name)
);
ALTER TABLE at_conversations
    ADD COLUMN IF NOT EXISTS project_id TEXT REFERENCES at_projects(id) ON DELETE CASCADE;
CREATE INDEX IF NOT EXISTS at_conversations_project ON at_conversations (project_id, created_at);
"""

_PROJECT_COLUMNS = (
    "id, name, description, context, repo, branch, git_connection_id, created_at, updated_at"
)
_CONNECTION_COLUMNS = "id, name, provider, login, created_at"
_PROJECT_FIELDS = ("name", "description", "context", "repo", "branch", "git_connection_id")


class ProjectStore(Protocol):
    async def create_connection(
        self, user_id: str, name: str, login: str, token_secret: str
    ) -> GitConnectionView: ...
    async def connections(self, user_id: str) -> list[GitConnectionView]: ...
    async def connection_secret(self, user_id: str, connection_id: str) -> str | None: ...
    async def delete_connection(self, user_id: str, connection_id: str) -> bool: ...
    async def create_project(self, user_id: str, fields: dict[str, Any]) -> ProjectView: ...
    async def projects(self, user_id: str) -> list[ProjectView]: ...
    async def project(self, user_id: str, project_id: str) -> ProjectView | None: ...
    async def update_project(
        self, user_id: str, project_id: str, fields: dict[str, Any]
    ) -> ProjectView | None: ...
    async def delete_project(self, user_id: str, project_id: str) -> bool: ...


def _now() -> datetime:
    return datetime.now(timezone.utc)


def _new_id() -> str:
    return uuid.uuid4().hex


class MemoryProjects:
    def __init__(self) -> None:
        self._connections: dict[
            str, tuple[GitConnectionView, str, str]
        ] = {}  # (view, user, secret)
        self._projects: dict[str, tuple[ProjectView, str]] = {}

    async def create_connection(
        self, user_id: str, name: str, login: str, token_secret: str
    ) -> GitConnectionView:
        if any(c.name == name and u == user_id for c, u, _ in self._connections.values()):
            raise Conflict(name)
        view = GitConnectionView(
            id=_new_id(), name=name, provider="github", login=login, created_at=_now()
        )
        self._connections[view.id] = (view, user_id, token_secret)
        return view

    async def connections(self, user_id: str) -> list[GitConnectionView]:
        return [c for c, u, _ in self._connections.values() if u == user_id]

    async def connection_secret(self, user_id: str, connection_id: str) -> str | None:
        entry = self._connections.get(connection_id)
        return entry[2] if entry and entry[1] == user_id else None

    async def delete_connection(self, user_id: str, connection_id: str) -> bool:
        entry = self._connections.get(connection_id)
        if entry is None or entry[1] != user_id:
            return False
        del self._connections[connection_id]
        for project_id, (project, owner) in list(self._projects.items()):
            if project.git_connection_id == connection_id:
                detached = project.model_copy(update={"git_connection_id": None})
                self._projects[project_id] = (detached, owner)
        return True

    async def create_project(self, user_id: str, fields: dict[str, Any]) -> ProjectView:
        if any(p.name == fields["name"] and u == user_id for p, u in self._projects.values()):
            raise Conflict(fields["name"])
        now = _now()
        view = ProjectView(id=_new_id(), created_at=now, updated_at=now, **_defaults(fields))
        self._projects[view.id] = (view, user_id)
        return view

    async def projects(self, user_id: str) -> list[ProjectView]:
        mine = [p for p, u in self._projects.values() if u == user_id]
        return sorted(mine, key=lambda p: p.updated_at, reverse=True)

    async def project(self, user_id: str, project_id: str) -> ProjectView | None:
        entry = self._projects.get(project_id)
        return entry[0] if entry and entry[1] == user_id else None

    async def update_project(
        self, user_id: str, project_id: str, fields: dict[str, Any]
    ) -> ProjectView | None:
        current = await self.project(user_id, project_id)
        if current is None:
            return None
        name = fields.get("name")
        taken = any(
            p.name == name and u == user_id and p.id != project_id
            for p, u in self._projects.values()
        )
        if name and taken:
            raise Conflict(name)
        updated = current.model_copy(update={**fields, "updated_at": _now()})
        self._projects[project_id] = (updated, user_id)
        return updated

    async def delete_project(self, user_id: str, project_id: str) -> bool:
        if await self.project(user_id, project_id) is None:
            return False
        del self._projects[project_id]
        return True


def _defaults(fields: dict[str, Any]) -> dict[str, Any]:
    base: dict[str, Any] = {
        "description": "",
        "context": "",
        "repo": None,
        "branch": "",
        "git_connection_id": None,
    }
    return {**base, **{k: v for k, v in fields.items() if k in _PROJECT_FIELDS}}


class PostgresProjects:
    def __init__(self, pool: asyncpg.Pool) -> None:
        self._pool = pool

    async def create_connection(
        self, user_id: str, name: str, login: str, token_secret: str
    ) -> GitConnectionView:
        try:
            row = await self._pool.fetchrow(
                "INSERT INTO at_git_connections (id, user_id, name, login, token_secret) "
                f"VALUES ($1, $2, $3, $4, $5) RETURNING {_CONNECTION_COLUMNS}",
                _new_id(),
                user_id,
                name,
                login,
                token_secret,
            )
        except asyncpg.UniqueViolationError as exc:
            raise Conflict(name) from exc
        return GitConnectionView(**dict(row))

    async def connections(self, user_id: str) -> list[GitConnectionView]:
        rows = await self._pool.fetch(
            f"SELECT {_CONNECTION_COLUMNS} FROM at_git_connections WHERE user_id = $1 "
            "ORDER BY created_at",
            user_id,
        )
        return [GitConnectionView(**dict(r)) for r in rows]

    async def connection_secret(self, user_id: str, connection_id: str) -> str | None:
        value = await self._pool.fetchval(
            "SELECT token_secret FROM at_git_connections WHERE id = $1 AND user_id = $2",
            connection_id,
            user_id,
        )
        return str(value) if value is not None else None

    async def delete_connection(self, user_id: str, connection_id: str) -> bool:
        result = await self._pool.execute(
            "DELETE FROM at_git_connections WHERE id = $1 AND user_id = $2",
            connection_id,
            user_id,
        )
        return bool(result.endswith(" 1"))

    async def create_project(self, user_id: str, fields: dict[str, Any]) -> ProjectView:
        data = _defaults(fields)
        try:
            row = await self._pool.fetchrow(
                "INSERT INTO at_projects (id, user_id, name, description, context, repo, branch, "
                "git_connection_id) VALUES ($1, $2, $3, $4, $5, $6, $7, $8) "
                f"RETURNING {_PROJECT_COLUMNS}",
                _new_id(),
                user_id,
                data["name"],
                data["description"],
                data["context"],
                data["repo"],
                data["branch"],
                data["git_connection_id"],
            )
        except asyncpg.UniqueViolationError as exc:
            raise Conflict(data["name"]) from exc
        return ProjectView(**dict(row))

    async def projects(self, user_id: str) -> list[ProjectView]:
        rows = await self._pool.fetch(
            f"SELECT {_PROJECT_COLUMNS} FROM at_projects WHERE user_id = $1 "
            "ORDER BY updated_at DESC",
            user_id,
        )
        return [ProjectView(**dict(r)) for r in rows]

    async def project(self, user_id: str, project_id: str) -> ProjectView | None:
        row = await self._pool.fetchrow(
            f"SELECT {_PROJECT_COLUMNS} FROM at_projects WHERE id = $1 AND user_id = $2",
            project_id,
            user_id,
        )
        return ProjectView(**dict(row)) if row else None

    async def update_project(
        self, user_id: str, project_id: str, fields: dict[str, Any]
    ) -> ProjectView | None:
        allowed = {k: v for k, v in fields.items() if k in _PROJECT_FIELDS}
        assignments = "".join(f"{k} = ${i}, " for i, k in enumerate(allowed, start=3))
        try:
            row = await self._pool.fetchrow(
                f"UPDATE at_projects SET {assignments}updated_at = now() "
                f"WHERE id = $1 AND user_id = $2 RETURNING {_PROJECT_COLUMNS}",
                project_id,
                user_id,
                *allowed.values(),
            )
        except asyncpg.UniqueViolationError as exc:
            raise Conflict(str(fields.get("name"))) from exc
        return ProjectView(**dict(row)) if row else None

    async def delete_project(self, user_id: str, project_id: str) -> bool:
        result = await self._pool.execute(
            "DELETE FROM at_projects WHERE id = $1 AND user_id = $2", project_id, user_id
        )
        return bool(result.endswith(" 1"))
