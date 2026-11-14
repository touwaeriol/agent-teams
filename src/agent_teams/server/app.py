"""FastAPI app: REST for history, Server-Sent Events for live updates.

All ``/api`` routes require the access token: API clients send
``Authorization: Bearer <token>``; the browser exchanges it once at
``POST /session`` for an HttpOnly cookie, so the token never appears in URLs.
"""

from __future__ import annotations

import asyncio
import json
import logging
import os
import secrets
from collections.abc import AsyncGenerator, AsyncIterator, Awaitable, Callable
from contextlib import asynccontextmanager, suppress
from dataclasses import dataclass
from importlib import resources
from typing import Any

from fastapi import (
    APIRouter,
    Depends,
    FastAPI,
    HTTPException,
    Request,
    Response,
    WebSocket,
    WebSocketDisconnect,
    status,
)
from fastapi.responses import HTMLResponse, JSONResponse, StreamingResponse

from ..agent import Agent
from ..config import load_team
from ..errors import ConfigError
from ..leader import Leader
from ..remote import NodeLink, NodeRegistry
from .broker import Broker, Event, MemoryBroker, ValkeyBroker
from .hub import Hub, UnknownConversation
from .models import (
    ConversationView,
    Login,
    MessageView,
    NewConversation,
    NewMessage,
    TaskView,
)
from .store import MemoryStore, PostgresStore, Store

logger = logging.getLogger(__name__)
HEARTBEAT_SECONDS = 15.0
SESSION_COOKIE = "agent_teams_session"
SESSION_SECONDS = 30 * 24 * 3600
HubFactory = Callable[[], Awaitable[Hub]]


@dataclass(frozen=True)
class Settings:
    """Read from the environment; only the token and the team file are required."""

    team_file: str
    token: str
    database_url: str = ""
    valkey_url: str = ""

    @classmethod
    def from_env(cls, team_file: str | None = None) -> Settings:
        token = os.environ.get("AGENT_TEAMS_TOKEN", "")
        if not token:
            raise ConfigError("AGENT_TEAMS_TOKEN must be set to protect the API")
        return cls(
            team_file=team_file or os.environ.get("TEAM_FILE", "team.yaml"),
            token=token,
            database_url=os.environ.get("DATABASE_URL", ""),
            valkey_url=os.environ.get("VALKEY_URL", ""),
        )

    def hub_factory(self) -> HubFactory:
        async def build() -> Hub:
            registry = NodeRegistry(secret=self.token)
            lead = load_team(self.team_file, registry=registry)
            if not isinstance(lead, (Leader, Agent)):
                raise ConfigError("the team file needs a leader or an agent at the top")
            return Hub(lead, await self._store(), await self._broker(), registry)

        return build

    async def _store(self) -> Store:
        if self.database_url:
            return await PostgresStore.connect(self.database_url)
        logger.warning("DATABASE_URL not set: conversations are kept in memory only")
        return MemoryStore()

    async def _broker(self) -> Broker:
        if self.valkey_url:
            return await ValkeyBroker.connect(self.valkey_url)
        logger.warning("VALKEY_URL not set: live updates work within this process only")
        return MemoryBroker()


def create_app(hub_factory: HubFactory, token: str) -> FastAPI:
    @asynccontextmanager
    async def lifespan(app: FastAPI) -> AsyncIterator[None]:
        hub = await hub_factory()
        app.state.hub = hub
        await hub.recover()
        try:
            yield
        finally:
            await hub.close()
            await hub.store.close()
            await hub.broker.close()

    app = FastAPI(title="agent-teams", lifespan=lifespan)
    app.add_exception_handler(UnknownConversation, _not_found)
    app.include_router(_api(token))
    app.include_router(_session(token))
    app.add_api_websocket_route("/nodes/connect", node_socket)

    @app.get("/", response_class=HTMLResponse, include_in_schema=False)
    async def index() -> str:
        return resources.files(__package__).joinpath("static/index.html").read_text("utf-8")

    @app.get("/healthz", include_in_schema=False)
    async def healthz() -> dict[str, bool]:
        return {"ok": True}

    return app


def _api(token: str) -> APIRouter:
    router = APIRouter(prefix="/api", dependencies=[Depends(_authorizer(token))])
    conversation = "/conversations/{conversation_id}"
    router.add_api_route("/conversations", list_conversations, methods=["GET"])
    router.add_api_route("/conversations", create_conversation, methods=["POST"], status_code=201)
    router.add_api_route(f"{conversation}/messages", list_messages, methods=["GET"])
    router.add_api_route(
        f"{conversation}/messages", post_message, methods=["POST"], status_code=202
    )
    router.add_api_route(f"{conversation}/tasks", list_tasks, methods=["GET"])
    router.add_api_route(f"{conversation}/events", events, methods=["GET"])
    router.add_api_route("/nodes", list_nodes, methods=["GET"])
    return router


async def list_conversations(request: Request) -> list[ConversationView]:
    return await _hub(request).store.conversations()


async def create_conversation(request: Request, body: NewConversation) -> ConversationView:
    return await _hub(request).create(body.title)


async def list_messages(request: Request, conversation_id: str) -> list[MessageView]:
    return await _hub(request).messages(conversation_id)


async def post_message(request: Request, conversation_id: str, body: NewMessage) -> MessageView:
    content = body.content.strip()
    if not content:
        raise HTTPException(status.HTTP_422_UNPROCESSABLE_CONTENT, "empty message")
    return await _hub(request).post(conversation_id, content)


async def list_tasks(request: Request, conversation_id: str) -> list[TaskView]:
    return await _hub(request).tasks(conversation_id)


async def events(request: Request, conversation_id: str) -> StreamingResponse:
    hub = _hub(request)
    await hub.check(conversation_id)
    headers = {"Cache-Control": "no-cache", "X-Accel-Buffering": "no"}
    stream = sse(hub.broker.subscribe(conversation_id))
    return StreamingResponse(stream, media_type="text/event-stream", headers=headers)


async def sse(
    events: AsyncGenerator[Event, None], heartbeat: float = HEARTBEAT_SECONDS
) -> AsyncGenerator[str, None]:
    """Format events as SSE, with comment heartbeats so proxies keep the stream open."""
    pending = asyncio.ensure_future(anext(events))
    try:
        while True:
            done, _ = await asyncio.wait({pending}, timeout=heartbeat)
            if not done:
                yield ": ping\n\n"
                continue
            yield f"data: {json.dumps(pending.result(), ensure_ascii=False)}\n\n"
            pending = asyncio.ensure_future(anext(events))
    finally:
        pending.cancel()
        with suppress(asyncio.CancelledError, StopAsyncIteration):
            await pending
        await events.aclose()


def _session(token: str) -> APIRouter:
    router = APIRouter(prefix="/session", include_in_schema=False)

    @router.post("", status_code=status.HTTP_204_NO_CONTENT)
    async def login(body: Login, response: Response) -> None:
        _require(body.token, token)
        response.set_cookie(
            SESSION_COOKIE, token, max_age=SESSION_SECONDS, httponly=True, samesite="strict"
        )

    @router.delete("", status_code=status.HTTP_204_NO_CONTENT)
    async def logout(response: Response) -> None:
        response.delete_cookie(SESSION_COOKIE)

    return router


def _authorizer(token: str) -> Callable[[Request], None]:
    def authorize(request: Request) -> None:
        header = request.headers.get("authorization", "").removeprefix("Bearer ").strip()
        _require(header or request.cookies.get(SESSION_COOKIE, ""), token)

    return authorize


def _require(supplied: str, expected: str) -> None:
    if not secrets.compare_digest(supplied.encode(), expected.encode()):
        raise HTTPException(status.HTTP_401_UNAUTHORIZED, "invalid or missing token")


async def list_nodes(request: Request) -> list[dict[str, Any]]:
    return _hub(request).registry.statuses()


async def node_socket(websocket: WebSocket) -> None:
    """A node daemon's connection: authenticate, then route its messages to runs."""
    registry = _hub(websocket).registry
    name = websocket.query_params.get("name", "")
    token = websocket.headers.get("authorization", "").removeprefix("Bearer ").strip()
    if not registry.verify(name, token):
        await websocket.close(code=status.WS_1008_POLICY_VIOLATION)
        return
    await websocket.accept()
    link = NodeLink(name, _serialized(websocket.send_json), await websocket.receive_json())
    registry.attach(link)
    logger.info("node %s connected", name)
    try:
        while True:
            link.deliver(await websocket.receive_json())
    except WebSocketDisconnect:
        logger.info("node %s disconnected", name)
    finally:
        registry.detach(link)


def _serialized(send: Callable[[Any], Awaitable[None]]) -> Callable[[Any], Awaitable[None]]:
    """Runs share one socket; keep their sends from interleaving."""
    lock = asyncio.Lock()

    async def locked(message: Any) -> None:
        async with lock:
            await send(message)

    return locked


def _hub(request: Request | WebSocket) -> Hub:
    hub: Hub = request.app.state.hub
    return hub


async def _not_found(_request: Request, exc: Exception) -> JSONResponse:
    return JSONResponse({"detail": f"unknown conversation {exc}"}, status_code=404)
