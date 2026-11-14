"""Users, login sessions, nodes, team definitions and platform settings.

``AccountStore`` is the port; ``PostgresAccounts`` is the production adapter
and ``MemoryAccounts`` serves tests. Secrets are stored only as hashes.
"""

from __future__ import annotations

import json
import uuid
from datetime import datetime, timedelta, timezone
from typing import Any, Protocol

import asyncpg

from .models import NodeView, PlatformSettings, UserRole, UserView

SESSION_LIFETIME = timedelta(days=30)

SCHEMA = """
CREATE TABLE IF NOT EXISTS at_users (
    id TEXT PRIMARY KEY,
    email TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    role TEXT NOT NULL DEFAULT 'user',
    active BOOLEAN NOT NULL DEFAULT TRUE,
    max_nodes INTEGER,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS at_sessions (
    token_hash TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES at_users(id) ON DELETE CASCADE,
    expires_at TIMESTAMPTZ NOT NULL
);
CREATE TABLE IF NOT EXISTS at_nodes (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES at_users(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    token_hash TEXT NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, name)
);
CREATE TABLE IF NOT EXISTS at_teams (
    user_id TEXT PRIMARY KEY REFERENCES at_users(id) ON DELETE CASCADE,
    config TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS at_settings (
    key TEXT PRIMARY KEY,
    value JSONB NOT NULL
);
"""

_USER_COLUMNS = "id, email, role, active, max_nodes, created_at"


class Conflict(Exception):  # noqa: N818 - reads better at call sites
    """A unique value (email, node name) is already taken."""


class AccountStore(Protocol):
    async def count_users(self) -> int: ...
    async def create_user(self, email: str, password_hash: str, role: UserRole) -> UserView: ...
    async def credentials(self, email: str) -> tuple[UserView, str] | None: ...
    async def user(self, user_id: str) -> UserView | None: ...
    async def users(self) -> list[UserView]: ...
    async def update_user(self, user_id: str, changes: dict[str, Any]) -> UserView | None: ...
    async def delete_user(self, user_id: str) -> bool: ...
    async def create_session(self, user_id: str, token_hash: str) -> None: ...
    async def session_user(self, token_hash: str) -> UserView | None: ...
    async def delete_session(self, token_hash: str) -> None: ...
    async def delete_sessions(self, user_id: str) -> None: ...
    async def create_node(self, user_id: str, name: str, token_hash: str) -> NodeView: ...
    async def nodes(self, user_id: str) -> list[NodeView]: ...
    async def node_by_token(self, token_hash: str) -> tuple[NodeView, str] | None: ...
    async def set_node_token(self, user_id: str, node_id: str, token_hash: str) -> bool: ...
    async def delete_node(self, user_id: str, node_id: str) -> bool: ...
    async def team(self, user_id: str) -> str | None: ...
    async def save_team(self, user_id: str, config: str) -> None: ...
    async def settings(self) -> PlatformSettings: ...
    async def save_settings(self, settings: PlatformSettings) -> None: ...


def _now() -> datetime:
    return datetime.now(timezone.utc)


def _new_id() -> str:
    return uuid.uuid4().hex


class MemoryAccounts:
    def __init__(self) -> None:
        self._users: dict[str, UserView] = {}
        self._passwords: dict[str, str] = {}
        self._sessions: dict[str, tuple[str, datetime]] = {}
        self._nodes: dict[str, tuple[NodeView, str, str]] = {}  # id -> (node, user, token hash)
        self._teams: dict[str, str] = {}
        self._settings = PlatformSettings()

    async def count_users(self) -> int:
        return len(self._users)

    async def create_user(self, email: str, password_hash: str, role: UserRole) -> UserView:
        if any(u.email == email for u in self._users.values()):
            raise Conflict(email)
        user = UserView(
            id=_new_id(), email=email, role=role, active=True, max_nodes=None, created_at=_now()
        )
        self._users[user.id] = user
        self._passwords[user.id] = password_hash
        return user

    async def credentials(self, email: str) -> tuple[UserView, str] | None:
        user = next((u for u in self._users.values() if u.email == email), None)
        return (user, self._passwords[user.id]) if user else None

    async def user(self, user_id: str) -> UserView | None:
        return self._users.get(user_id)

    async def users(self) -> list[UserView]:
        return sorted(self._users.values(), key=lambda u: u.created_at)

    async def update_user(self, user_id: str, changes: dict[str, Any]) -> UserView | None:
        if user_id not in self._users:
            return None
        self._users[user_id] = self._users[user_id].model_copy(update=changes)
        return self._users[user_id]

    async def delete_user(self, user_id: str) -> bool:
        if self._users.pop(user_id, None) is None:
            return False
        await self.delete_sessions(user_id)
        self._nodes = {k: v for k, v in self._nodes.items() if v[1] != user_id}
        self._teams.pop(user_id, None)
        return True

    async def create_session(self, user_id: str, token_hash: str) -> None:
        self._sessions[token_hash] = (user_id, _now() + SESSION_LIFETIME)

    async def session_user(self, token_hash: str) -> UserView | None:
        user_id, expires = self._sessions.get(token_hash, ("", _now()))
        user = self._users.get(user_id)
        return user if user and user.active and expires > _now() else None

    async def delete_session(self, token_hash: str) -> None:
        self._sessions.pop(token_hash, None)

    async def delete_sessions(self, user_id: str) -> None:
        self._sessions = {k: v for k, v in self._sessions.items() if v[0] != user_id}

    async def create_node(self, user_id: str, name: str, token_hash: str) -> NodeView:
        if any(n.name == name and u == user_id for n, u, _ in self._nodes.values()):
            raise Conflict(name)
        node = NodeView(id=_new_id(), name=name, created_at=_now())
        self._nodes[node.id] = (node, user_id, token_hash)
        return node

    async def nodes(self, user_id: str) -> list[NodeView]:
        return [n for n, u, _ in self._nodes.values() if u == user_id]

    async def node_by_token(self, token_hash: str) -> tuple[NodeView, str] | None:
        return next(((n, u) for n, u, t in self._nodes.values() if t == token_hash), None)

    async def set_node_token(self, user_id: str, node_id: str, token_hash: str) -> bool:
        entry = self._nodes.get(node_id)
        if entry is None or entry[1] != user_id:
            return False
        self._nodes[node_id] = (entry[0], user_id, token_hash)
        return True

    async def delete_node(self, user_id: str, node_id: str) -> bool:
        entry = self._nodes.get(node_id)
        if entry is None or entry[1] != user_id:
            return False
        del self._nodes[node_id]
        return True

    async def team(self, user_id: str) -> str | None:
        return self._teams.get(user_id)

    async def save_team(self, user_id: str, config: str) -> None:
        self._teams[user_id] = config

    async def settings(self) -> PlatformSettings:
        return self._settings

    async def save_settings(self, settings: PlatformSettings) -> None:
        self._settings = settings


class PostgresAccounts:
    def __init__(self, pool: asyncpg.Pool) -> None:
        self._pool = pool

    async def count_users(self) -> int:
        return int(await self._pool.fetchval("SELECT count(*) FROM at_users"))

    async def create_user(self, email: str, password_hash: str, role: UserRole) -> UserView:
        try:
            row = await self._pool.fetchrow(
                f"INSERT INTO at_users (id, email, password_hash, role) VALUES ($1, $2, $3, $4) "
                f"RETURNING {_USER_COLUMNS}",
                _new_id(),
                email,
                password_hash,
                role,
            )
        except asyncpg.UniqueViolationError as exc:
            raise Conflict(email) from exc
        return UserView(**dict(row))

    async def credentials(self, email: str) -> tuple[UserView, str] | None:
        row = await self._pool.fetchrow(
            f"SELECT {_USER_COLUMNS}, password_hash FROM at_users WHERE email = $1", email
        )
        if row is None:
            return None
        data = dict(row)
        return UserView(**data), str(data.pop("password_hash"))

    async def user(self, user_id: str) -> UserView | None:
        row = await self._pool.fetchrow(
            f"SELECT {_USER_COLUMNS} FROM at_users WHERE id = $1", user_id
        )
        return UserView(**dict(row)) if row else None

    async def users(self) -> list[UserView]:
        rows = await self._pool.fetch(f"SELECT {_USER_COLUMNS} FROM at_users ORDER BY created_at")
        return [UserView(**dict(r)) for r in rows]

    async def update_user(self, user_id: str, changes: dict[str, Any]) -> UserView | None:
        allowed = {k: v for k, v in changes.items() if k in ("role", "active", "max_nodes")}
        if not allowed:
            return await self.user(user_id)
        assignments = ", ".join(f"{k} = ${i}" for i, k in enumerate(allowed, start=2))
        row = await self._pool.fetchrow(
            f"UPDATE at_users SET {assignments} WHERE id = $1 RETURNING {_USER_COLUMNS}",
            user_id,
            *allowed.values(),
        )
        return UserView(**dict(row)) if row else None

    async def delete_user(self, user_id: str) -> bool:
        result = await self._pool.execute("DELETE FROM at_users WHERE id = $1", user_id)
        return bool(result.endswith(" 1"))

    async def create_session(self, user_id: str, token_hash: str) -> None:
        await self._pool.execute(
            "INSERT INTO at_sessions (token_hash, user_id, expires_at) VALUES ($1, $2, $3)",
            token_hash,
            user_id,
            _now() + SESSION_LIFETIME,
        )

    async def session_user(self, token_hash: str) -> UserView | None:
        row = await self._pool.fetchrow(
            f"SELECT {', '.join('u.' + c.strip() for c in _USER_COLUMNS.split(','))} "
            "FROM at_sessions s JOIN at_users u ON u.id = s.user_id "
            "WHERE s.token_hash = $1 AND s.expires_at > now() AND u.active",
            token_hash,
        )
        return UserView(**dict(row)) if row else None

    async def delete_session(self, token_hash: str) -> None:
        await self._pool.execute("DELETE FROM at_sessions WHERE token_hash = $1", token_hash)

    async def delete_sessions(self, user_id: str) -> None:
        await self._pool.execute("DELETE FROM at_sessions WHERE user_id = $1", user_id)

    async def create_node(self, user_id: str, name: str, token_hash: str) -> NodeView:
        try:
            row = await self._pool.fetchrow(
                "INSERT INTO at_nodes (id, user_id, name, token_hash) VALUES ($1, $2, $3, $4) "
                "RETURNING id, name, created_at",
                _new_id(),
                user_id,
                name,
                token_hash,
            )
        except asyncpg.UniqueViolationError as exc:
            raise Conflict(name) from exc
        return NodeView(**dict(row))

    async def nodes(self, user_id: str) -> list[NodeView]:
        rows = await self._pool.fetch(
            "SELECT id, name, created_at FROM at_nodes WHERE user_id = $1 ORDER BY created_at",
            user_id,
        )
        return [NodeView(**dict(r)) for r in rows]

    async def node_by_token(self, token_hash: str) -> tuple[NodeView, str] | None:
        row = await self._pool.fetchrow(
            "SELECT id, name, created_at, user_id FROM at_nodes WHERE token_hash = $1", token_hash
        )
        if row is None:
            return None
        data = dict(row)
        user_id = str(data.pop("user_id"))
        return NodeView(**data), user_id

    async def set_node_token(self, user_id: str, node_id: str, token_hash: str) -> bool:
        result = await self._pool.execute(
            "UPDATE at_nodes SET token_hash = $3 WHERE id = $1 AND user_id = $2",
            node_id,
            user_id,
            token_hash,
        )
        return bool(result.endswith(" 1"))

    async def delete_node(self, user_id: str, node_id: str) -> bool:
        result = await self._pool.execute(
            "DELETE FROM at_nodes WHERE id = $1 AND user_id = $2", node_id, user_id
        )
        return bool(result.endswith(" 1"))

    async def team(self, user_id: str) -> str | None:
        value = await self._pool.fetchval("SELECT config FROM at_teams WHERE user_id = $1", user_id)
        return str(value) if value is not None else None

    async def save_team(self, user_id: str, config: str) -> None:
        await self._pool.execute(
            "INSERT INTO at_teams (user_id, config) VALUES ($1, $2) "
            "ON CONFLICT (user_id) DO UPDATE SET config = EXCLUDED.config, updated_at = now()",
            user_id,
            config,
        )

    async def settings(self) -> PlatformSettings:
        raw = await self._pool.fetchval("SELECT value FROM at_settings WHERE key = 'platform'")
        return PlatformSettings(**json.loads(raw)) if raw else PlatformSettings()

    async def save_settings(self, settings: PlatformSettings) -> None:
        await self._pool.execute(
            "INSERT INTO at_settings (key, value) VALUES ('platform', $1::jsonb) "
            "ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value",
            settings.model_dump_json(),
        )
