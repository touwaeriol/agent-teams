"""The multi-user web service: ``agent-teams serve``.

Users register and sign in, register their own machines (node daemons dial in
over WebSocket with a per-node token) and define their team; the leader and
the employees run as Claude Code on those machines. The leader reaches its
task board over MCP at ``/mcp``. Administrators manage users and settings.
"""

from __future__ import annotations

import asyncio
import logging
import os
from collections.abc import AsyncIterator, Awaitable, Callable
from contextlib import asynccontextmanager
from dataclasses import dataclass
from importlib import resources
from typing import Any

from fastapi import FastAPI, WebSocket, WebSocketDisconnect, status
from fastapi.responses import HTMLResponse, JSONResponse, PlainTextResponse
from starlette.requests import Request
from starlette.routing import Route
from starlette.types import Receive, Scope, Send

from ..errors import ConfigError
from ..remote import NodeLink
from . import admin, auth, routes
from .accounts import AccountStore, MemoryAccounts
from .broker import Broker, MemoryBroker, ValkeyBroker
from .hub import UnknownConversation
from .mcp_bridge import MCP_PATH
from .platform import Platform, Services, platform
from .security import hash_token
from .store import MemoryStore, Store, connect_postgres

logger = logging.getLogger(__name__)
ServicesFactory = Callable[[], Awaitable[Services]]


@dataclass(frozen=True)
class Settings:
    """From the environment. ``AGENT_TEAMS_PUBLIC_URL`` is how nodes and Claude
    Code leaders reach this service (install commands and the MCP endpoint)."""

    public_url: str
    database_url: str = ""
    valkey_url: str = ""
    admin_emails: frozenset[str] = frozenset()

    @classmethod
    def from_env(cls) -> Settings:
        public_url = os.environ.get("AGENT_TEAMS_PUBLIC_URL", "")
        if not public_url:
            raise ConfigError("set AGENT_TEAMS_PUBLIC_URL, e.g. http://203.0.113.5:3001")
        emails = os.environ.get("AGENT_TEAMS_ADMIN_EMAILS", "")
        return cls(
            public_url=public_url,
            database_url=os.environ.get("DATABASE_URL", ""),
            valkey_url=os.environ.get("VALKEY_URL", ""),
            admin_emails=frozenset(e.strip().lower() for e in emails.split(",") if e.strip()),
        )

    async def services(self) -> Services:
        store: Store
        accounts: AccountStore
        if self.database_url:
            store, accounts = await connect_postgres(self.database_url)
        else:
            logger.warning("DATABASE_URL not set: everything is kept in memory only")
            store, accounts = MemoryStore(), MemoryAccounts()
        return Services(store, accounts, await self._broker(), self.public_url, self.admin_emails)

    async def _broker(self) -> Broker:
        if self.valkey_url:
            return await ValkeyBroker.connect(self.valkey_url)
        logger.warning("VALKEY_URL not set: live updates work within this process only")
        return MemoryBroker()


def create_app(services: ServicesFactory) -> FastAPI:
    @asynccontextmanager
    async def lifespan(app: FastAPI) -> AsyncIterator[None]:
        state = Platform.assemble(await services())
        app.state.platform = state
        async with state.bridge.running():
            await state.hub.recover()
            try:
                yield
            finally:
                await state.hub.close()
                await state.services.store.close()
                await state.services.broker.close()

    app = FastAPI(title="agent-teams", lifespan=lifespan)
    app.add_exception_handler(UnknownConversation, _not_found)
    for module in (auth, routes, admin):
        app.include_router(module.router())
    app.add_api_websocket_route("/nodes/connect", node_socket)
    app.add_api_route("/", index, methods=["GET"], include_in_schema=False)
    app.add_api_route("/install-node.sh", install_script, methods=["GET"], include_in_schema=False)
    app.add_api_route("/healthz", healthz, methods=["GET"], include_in_schema=False)
    app.router.routes.append(
        Route(MCP_PATH, endpoint=_McpEndpoint(), methods=["GET", "POST", "DELETE"])
    )
    return app


class _McpEndpoint:
    """Forwards ``/mcp`` to the running bridge (an ASGI app built at startup)."""

    async def __call__(self, scope: Scope, receive: Receive, send: Send) -> None:
        await scope["app"].state.platform.bridge(scope, receive, send)


async def node_socket(websocket: WebSocket) -> None:
    """A node daemon's connection: identify the node by its token, then route messages."""
    state = platform(websocket)
    token = websocket.headers.get("authorization", "").removeprefix("Bearer ").strip()
    found = await state.accounts.node_by_token(hash_token(token)) if token else None
    owner = await state.accounts.user(found[1]) if found else None
    if found is None or owner is None or not owner.active:
        await websocket.close(code=status.WS_1008_POLICY_VIOLATION)
        return
    node = found[0]
    await websocket.accept()
    hello = await websocket.receive_json()
    link = NodeLink(node.id, _serialized(websocket.send_json), hello, websocket.close)
    state.registry.attach(link)
    logger.info("node %s (%s) connected", node.name, node.id)
    try:
        while True:
            link.deliver(await websocket.receive_json())
    except WebSocketDisconnect:
        logger.info("node %s disconnected", node.name)
    finally:
        state.registry.detach(link)


def _serialized(send: Callable[[Any], Awaitable[None]]) -> Callable[[Any], Awaitable[None]]:
    """Runs share one socket; keep their sends from interleaving."""
    lock = asyncio.Lock()

    async def locked(message: Any) -> None:
        async with lock:
            await send(message)

    return locked


async def index() -> HTMLResponse:
    return HTMLResponse(_static("index.html"))


async def install_script() -> PlainTextResponse:
    return PlainTextResponse(_static("install-node.sh"), media_type="text/x-shellscript")


async def healthz() -> dict[str, bool]:
    return {"ok": True}


def _static(name: str) -> str:
    return resources.files(__package__).joinpath("static").joinpath(name).read_text("utf-8")


async def _not_found(_request: Request, exc: Exception) -> JSONResponse:
    return JSONResponse({"detail": f"没有这个对话：{exc}"}, status_code=404)
