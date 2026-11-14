"""Accounts: open registration (when enabled), login sessions, roles."""

from __future__ import annotations

import asyncio

from fastapi import APIRouter, Depends, HTTPException, Request, Response, status

from .accounts import Conflict
from .models import Credentials, UserView
from .platform import platform
from .security import check_credentials, hash_password, hash_token, new_token, normalize_email
from .security import verify_password as _verify

SESSION_COOKIE = "agent_teams_session"
SESSION_SECONDS = 30 * 24 * 3600


async def current_user(request: Request) -> UserView:
    token = _bearer(request) or request.cookies.get(SESSION_COOKIE, "")
    user = await platform(request).accounts.session_user(hash_token(token)) if token else None
    if user is None:
        raise HTTPException(status.HTTP_401_UNAUTHORIZED, "请先登录")
    return user


_User = Depends(current_user)


async def current_admin(user: UserView = _User) -> UserView:
    if user.role != "admin":
        raise HTTPException(status.HTTP_403_FORBIDDEN, "需要管理员权限")
    return user


def router() -> APIRouter:
    routes = APIRouter(prefix="/auth", tags=["auth"])
    routes.add_api_route("/register", register, methods=["POST"], status_code=201)
    routes.add_api_route("/login", login, methods=["POST"])
    routes.add_api_route("/logout", logout, methods=["POST"], status_code=204)
    return routes


async def register(request: Request, body: Credentials, response: Response) -> UserView:
    accounts = platform(request).accounts
    email = normalize_email(body.email)
    reason = check_credentials(email, body.password)
    if reason:
        raise HTTPException(status.HTTP_422_UNPROCESSABLE_CONTENT, reason)
    first = await accounts.count_users() == 0
    if not (first or (await accounts.settings()).registration_open):
        raise HTTPException(status.HTTP_403_FORBIDDEN, "注册已关闭，请联系管理员")
    admin = first or email in platform(request).services.admin_emails
    password_hash = await asyncio.to_thread(hash_password, body.password)
    try:
        user = await accounts.create_user(email, password_hash, "admin" if admin else "user")
    except Conflict:
        raise HTTPException(status.HTTP_409_CONFLICT, "这个邮箱已经注册过了") from None
    await _start_session(request, user, response)
    return user


async def login(request: Request, body: Credentials, response: Response) -> UserView:
    found = await platform(request).accounts.credentials(normalize_email(body.email))
    if found is None or not await asyncio.to_thread(_verify, body.password, found[1]):
        raise HTTPException(status.HTTP_401_UNAUTHORIZED, "邮箱或密码不对")
    user = found[0]
    if not user.active:
        raise HTTPException(status.HTTP_403_FORBIDDEN, "账号已停用，请联系管理员")
    await _start_session(request, user, response)
    return user


async def logout(request: Request, response: Response) -> None:
    token = _bearer(request) or request.cookies.get(SESSION_COOKIE, "")
    if token:
        await platform(request).accounts.delete_session(hash_token(token))
    response.delete_cookie(SESSION_COOKIE)


async def _start_session(request: Request, user: UserView, response: Response) -> None:
    token, token_hash = new_token()
    await platform(request).accounts.create_session(user.id, token_hash)
    secure = platform(request).public_url.startswith("https://")  # behind TLS (e.g. Caddy)
    response.set_cookie(
        SESSION_COOKIE,
        token,
        max_age=SESSION_SECONDS,
        httponly=True,
        samesite="lax",  # sent on the redirect back from GitHub (OAuth / App install)
        secure=secure,
    )


def _bearer(request: Request) -> str:
    return request.headers.get("authorization", "").removeprefix("Bearer ").strip()
