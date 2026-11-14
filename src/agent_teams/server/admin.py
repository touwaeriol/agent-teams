"""Administrators manage users and platform settings."""

from __future__ import annotations

from typing import Any

from fastapi import APIRouter, Depends, HTTPException, Request, status

from .auth import current_admin
from .models import PlatformSettings, UserUpdate, UserView
from .platform import Platform, platform

Admin = Depends(current_admin)


def router() -> APIRouter:
    routes = APIRouter(prefix="/api/admin", tags=["admin"])
    routes.add_api_route("/users", list_users, methods=["GET"])
    routes.add_api_route("/users/{user_id}", update_user, methods=["PATCH"])
    routes.add_api_route("/users/{user_id}", delete_user, methods=["DELETE"], status_code=204)
    routes.add_api_route("/settings", get_settings, methods=["GET"])
    routes.add_api_route("/settings", save_settings, methods=["PUT"])
    return routes


async def list_users(request: Request, _admin: UserView = Admin) -> list[dict[str, Any]]:
    accounts = platform(request).accounts
    users = await accounts.users()
    return [{**u.model_dump(mode="json"), "nodes": len(await accounts.nodes(u.id))} for u in users]


async def update_user(
    request: Request, user_id: str, body: UserUpdate, admin: UserView = Admin
) -> UserView:
    state = platform(request)
    changes = body.model_dump(exclude_none=True, exclude={"clear_max_nodes"})
    if body.clear_max_nodes:
        changes["max_nodes"] = None
    if user_id == admin.id and (changes.get("role") == "user" or changes.get("active") is False):
        raise HTTPException(status.HTTP_400_BAD_REQUEST, "不能停用自己或取消自己的管理员身份")
    user = await state.accounts.update_user(user_id, changes)
    if user is None:
        raise HTTPException(status.HTTP_404_NOT_FOUND, "没有这个用户")
    if not user.active:
        await _cut_off(state, user_id)
    return user


async def delete_user(request: Request, user_id: str, admin: UserView = Admin) -> None:
    state = platform(request)
    if user_id == admin.id:
        raise HTTPException(status.HTTP_400_BAD_REQUEST, "不能删除自己")
    await _cut_off(state, user_id)
    if not await state.accounts.delete_user(user_id):
        raise HTTPException(status.HTTP_404_NOT_FOUND, "没有这个用户")


async def get_settings(request: Request, _admin: UserView = Admin) -> PlatformSettings:
    return await platform(request).accounts.settings()


async def save_settings(
    request: Request, body: PlatformSettings, _admin: UserView = Admin
) -> PlatformSettings:
    await platform(request).accounts.save_settings(body)
    return body


async def _cut_off(state: Platform, user_id: str) -> None:
    """Sign the user out everywhere, stop their chats and drop their nodes."""
    await state.accounts.delete_sessions(user_id)
    await state.hub.close_user(user_id)
    for node in await state.accounts.nodes(user_id):
        await state.registry.disconnect(node.id)
