"""What a signed-in user can do: chat, manage their nodes, edit their team."""

from __future__ import annotations

import re
import shlex
from typing import Any

import yaml
from fastapi import APIRouter, Depends, HTTPException, Request, status
from fastapi.responses import StreamingResponse

from ..config import parse_yaml
from ..errors import ConfigError
from .accounts import Conflict
from .auth import current_user
from .events import sse
from .models import (
    ConversationView,
    MessageView,
    NewConversation,
    NewMessage,
    NewNode,
    TaskView,
    TeamConfig,
    UserView,
)
from .platform import Platform, platform
from .security import new_token
from .workspaces import DEFAULT_TEAM

User = Depends(current_user)
_NODE_NAME = re.compile(r"^[A-Za-z0-9][A-Za-z0-9_-]{0,39}$")


def router() -> APIRouter:
    routes = APIRouter(prefix="/api")
    conversation = "/conversations/{conversation_id}"
    routes.add_api_route("/me", me, methods=["GET"])
    routes.add_api_route("/conversations", list_conversations, methods=["GET"])
    routes.add_api_route("/conversations", create_conversation, methods=["POST"], status_code=201)
    routes.add_api_route(f"{conversation}/messages", list_messages, methods=["GET"])
    routes.add_api_route(
        f"{conversation}/messages", post_message, methods=["POST"], status_code=202
    )
    routes.add_api_route(f"{conversation}/tasks", list_tasks, methods=["GET"])
    routes.add_api_route(f"{conversation}/events", events, methods=["GET"])
    routes.add_api_route("/nodes", list_nodes, methods=["GET"])
    routes.add_api_route("/nodes", add_node, methods=["POST"], status_code=201)
    routes.add_api_route("/nodes/{node_id}/token", rotate_token, methods=["POST"])
    routes.add_api_route("/nodes/{node_id}", delete_node, methods=["DELETE"], status_code=204)
    routes.add_api_route("/team", get_team, methods=["GET"])
    routes.add_api_route("/team", save_team, methods=["PUT"])
    return routes


async def me(user: UserView = User) -> UserView:
    return user


async def list_conversations(
    request: Request, user: UserView = User, project_id: str | None = None
) -> list[ConversationView]:
    return await platform(request).hub.conversations(user.id, project_id)


async def create_conversation(
    request: Request, body: NewConversation, user: UserView = User
) -> ConversationView:
    state = platform(request)
    if body.project_id and await state.projects.project(user.id, body.project_id) is None:
        raise HTTPException(status.HTTP_404_NOT_FOUND, "没有这个项目")
    return await state.hub.create(user.id, body.title, body.project_id or None)


async def list_messages(
    request: Request, conversation_id: str, user: UserView = User
) -> list[MessageView]:
    return await platform(request).hub.messages(user.id, conversation_id)


async def post_message(
    request: Request, conversation_id: str, body: NewMessage, user: UserView = User
) -> MessageView:
    content = body.content.strip()
    if not content:
        raise HTTPException(status.HTTP_422_UNPROCESSABLE_CONTENT, "消息不能为空")
    try:
        return await platform(request).hub.post(user.id, conversation_id, content)
    except ConfigError as exc:
        raise HTTPException(status.HTTP_409_CONFLICT, str(exc)) from exc


async def list_tasks(
    request: Request, conversation_id: str, user: UserView = User
) -> list[TaskView]:
    return await platform(request).hub.tasks(user.id, conversation_id)


async def events(
    request: Request, conversation_id: str, user: UserView = User
) -> StreamingResponse:
    hub = platform(request).hub
    await hub.check(user.id, conversation_id)
    headers = {"Cache-Control": "no-cache", "X-Accel-Buffering": "no"}
    stream = sse(hub.broker.subscribe(conversation_id))
    return StreamingResponse(stream, media_type="text/event-stream", headers=headers)


async def list_nodes(request: Request, user: UserView = User) -> list[dict[str, Any]]:
    state = platform(request)
    nodes = await state.accounts.nodes(user.id)
    return [{**n.model_dump(mode="json"), **state.registry.status(n.id)} for n in nodes]


async def add_node(request: Request, body: NewNode, user: UserView = User) -> dict[str, Any]:
    state = platform(request)
    name = body.name.strip()
    if not _NODE_NAME.match(name):
        raise HTTPException(status.HTTP_422_UNPROCESSABLE_CONTENT, "节点名只能用字母、数字、- 和 _")
    await _check_quota(state, user)
    token, token_hash = new_token()
    try:
        node = await state.accounts.create_node(user.id, name, token_hash)
    except Conflict:
        raise HTTPException(status.HTTP_409_CONFLICT, f"已经有叫 {name} 的节点了") from None
    return {"node": node.model_dump(mode="json"), **_enrollment(state, name, token)}


async def rotate_token(request: Request, node_id: str, user: UserView = User) -> dict[str, Any]:
    state = platform(request)
    node = next((n for n in await state.accounts.nodes(user.id) if n.id == node_id), None)
    token, token_hash = new_token()
    if node is None or not await state.accounts.set_node_token(user.id, node_id, token_hash):
        raise HTTPException(status.HTTP_404_NOT_FOUND, "没有这个节点")
    await state.registry.disconnect(node_id)  # the old token stops working right away
    return _enrollment(state, node.name, token)


async def delete_node(request: Request, node_id: str, user: UserView = User) -> None:
    state = platform(request)
    if not await state.accounts.delete_node(user.id, node_id):
        raise HTTPException(status.HTTP_404_NOT_FOUND, "没有这个节点")
    await state.registry.disconnect(node_id)


async def get_team(request: Request, user: UserView = User) -> dict[str, Any]:
    state = platform(request)
    config = await state.accounts.team(user.id)
    error = await _problem(state, user, config) if config else "还没有保存团队配置"
    text = config or DEFAULT_TEAM
    return {"config": text, "team": _structured(text), "saved": config is not None, "error": error}


async def save_team(request: Request, body: TeamConfig, user: UserView = User) -> dict[str, Any]:
    state = platform(request)
    config = _yaml(body.team) if body.team is not None else body.config
    problem = await _problem(state, user, config)
    if problem:
        raise HTTPException(status.HTTP_422_UNPROCESSABLE_CONTENT, problem)
    await state.accounts.save_team(user.id, config)
    return {"config": config, "team": _structured(config), "saved": True, "error": ""}


def _structured(config: str) -> dict[str, Any] | None:
    """The team definition as data for the form editor, or None if it cannot be parsed."""
    try:
        return dict(parse_yaml(config, "团队配置"))
    except ConfigError:
        return None


def _yaml(team: dict[str, Any]) -> str:
    return yaml.safe_dump(team, allow_unicode=True, sort_keys=False, width=1000)


async def _problem(state: Platform, user: UserView, config: str) -> str:
    """Why the team definition cannot be used, or an empty string."""
    try:
        state.workspaces.build(config, await state.accounts.nodes(user.id))
    except ConfigError as exc:
        return str(exc)
    return ""


async def _check_quota(state: Platform, user: UserView) -> None:
    settings = await state.accounts.settings()
    limit = user.max_nodes if user.max_nodes is not None else settings.default_max_nodes
    if len(await state.accounts.nodes(user.id)) >= limit:
        raise HTTPException(status.HTTP_403_FORBIDDEN, f"最多可以添加 {limit} 个节点")


def _enrollment(state: Platform, name: str, token: str) -> dict[str, Any]:
    """The one-time token and the command that installs the daemon with it."""
    console = state.public_url
    command = (
        f"curl -fsSL {console}/install-node.sh | AGENT_TEAMS_NODE_TOKEN={token} "
        f"bash -s -- --console {shlex.quote(console)} --name {shlex.quote(name)}"
    )
    return {"token": token, "install": command}
