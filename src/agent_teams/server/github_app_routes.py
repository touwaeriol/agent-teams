"""One-click GitHub: the platform's GitHub App.

An administrator creates the App once from a manifest (GitHub hosts the
form; we only receive a code to exchange). Users then click "连接 GitHub",
install the App on their account or organization and pick repositories;
GitHub sends them back with an installation ID and an OAuth code, which
proves the installation is theirs before it becomes a Git connection.
"""

from __future__ import annotations

import json
import secrets
from typing import Any
from urllib.parse import urlencode, urlsplit

from fastapi import APIRouter, Depends, HTTPException, Request, status
from fastapi.responses import RedirectResponse
from pydantic import BaseModel

from ..errors import ConfigError
from .accounts import Conflict
from .auth import current_admin, current_user
from .credentials import GitHubApp
from .github import GitHubError
from .models import UserView
from .platform import Platform, platform

User = Depends(current_user)
Admin = Depends(current_admin)
CREATE, INSTALL = "github-app-create", "github-app-install"
BASE = "/api/git/github-app"


class ManifestRequest(BaseModel):
    organization: str = ""


def router() -> APIRouter:
    routes = APIRouter()
    routes.add_api_route(BASE, app_status, methods=["GET"])
    routes.add_api_route(f"{BASE}/install", install, methods=["GET"], include_in_schema=False)
    routes.add_api_route(f"{BASE}/installed", installed, methods=["GET"], include_in_schema=False)
    routes.add_api_route(f"{BASE}/created", created, methods=["GET"], include_in_schema=False)
    routes.add_api_route("/api/admin/github-app/manifest", manifest, methods=["POST"])
    routes.add_api_route("/api/admin/github-app", forget, methods=["DELETE"], status_code=204)
    return routes


async def app_status(request: Request, _user: UserView = User) -> dict[str, Any]:
    app = await _app(platform(request))
    if app is None:
        return {"configured": False}
    return {"configured": True, "app": app.public(), "install_url": f"{BASE}/install"}


async def manifest(
    request: Request, body: ManifestRequest, admin: UserView = Admin
) -> dict[str, Any]:
    """What the browser posts to GitHub to create the App (GitHub's own form)."""
    state = platform(request)
    if not state.services.secrets.ready:
        raise HTTPException(
            status.HTTP_503_SERVICE_UNAVAILABLE, "服务没有配置 AGENT_TEAMS_SECRET_KEY"
        )
    url = state.public_url
    org = body.organization.strip()
    target = (
        f"https://github.com/organizations/{org}/settings/apps/new"
        if org
        else "https://github.com/settings/apps/new"
    )
    signed = state.states.sign(CREATE, admin.id)
    return {
        "action": f"{target}?{urlencode({'state': signed})}",
        "manifest": json.dumps(_manifest(url)),
    }


def _manifest(url: str) -> dict[str, Any]:
    host = urlsplit(url).hostname or "agent-teams"
    return {
        "name": f"Agent Teams {host.split('.')[0]} {secrets.token_hex(2)}",
        "url": url,
        "redirect_url": f"{url}{BASE}/created",
        "callback_urls": [f"{url}{BASE}/installed"],
        "setup_url": f"{url}{BASE}/installed",
        "setup_on_update": True,
        "request_oauth_on_install": True,
        "public": True,  # users install it on their own accounts
        "hook_attributes": {"url": f"{url}{BASE}/webhook", "active": False},
        "default_permissions": {
            "contents": "write",
            "pull_requests": "write",
            "workflows": "write",
            "metadata": "read",
        },
        "default_events": [],
    }


async def created(
    request: Request, code: str = "", state: str = "", admin: UserView = Admin
) -> RedirectResponse:
    """GitHub created the App and sent us back a one-time code for its secrets."""
    service = platform(request)
    if service.states.verify(CREATE, state) != admin.id:
        return _back("/admin", error="创建请求已过期或不是你发起的，请重新创建")
    try:
        app = await service.credentials.save_app(
            await service.services.github.convert_manifest(code)
        )
    except (GitHubError, KeyError) as exc:
        return _back("/admin", error=f"保存 GitHub App 失败：{exc}")
    return _back("/admin", github_app=app.slug)


async def forget(request: Request, _admin: UserView = Admin) -> None:
    await platform(request).credentials.forget_app()


async def install(request: Request, user: UserView = User) -> RedirectResponse:
    service = platform(request)
    app = await _app(service)
    if app is None:
        return _back("/settings/git", error="管理员还没有创建平台的 GitHub App")
    query = urlencode({"state": service.states.sign(INSTALL, user.id)})
    return RedirectResponse(f"{app.install_url}?{query}", status_code=status.HTTP_303_SEE_OTHER)


async def installed(
    request: Request,
    *,
    installation_id: int = 0,
    code: str = "",
    state: str = "",
    setup_action: str = "",
    user: UserView = User,
) -> RedirectResponse:
    """Back from GitHub after installing (or changing) the App."""
    service = platform(request)
    if state and service.states.verify(INSTALL, state) != user.id:
        return _back("/settings/git", error="授权请求已过期或不是你发起的，请重新连接")
    existing = [
        c
        for c in await service.projects.connections(user.id)
        if c.installation_id == installation_id
    ]
    if existing:
        return _back("/settings/git", connected=existing[0].name)
    if not installation_id or not code:
        return _back("/settings/git", error="GitHub 没有返回安装信息，请重新连接")
    try:
        account = await _verified_account(service, code, installation_id)
        name = await _save(service, user.id, account, installation_id)
    except (GitHubError, ConfigError) as exc:
        return _back("/settings/git", error=str(exc))
    return _back("/settings/git", connected=name)


async def _verified_account(service: Platform, code: str, installation_id: int) -> str:
    """The account the installation belongs to, after checking this GitHub user can see it."""
    app = await _app(service)
    if app is None:
        raise ConfigError("管理员还没有创建平台的 GitHub App")
    github = service.services.github
    user_token = await github.user_token(app.client_id, app.client_secret, code)
    installations = await github.user_installations(user_token)
    match = next((i for i in installations if int(i.get("id", 0)) == installation_id), None)
    if match is None:
        raise ConfigError("这个安装不属于你的 GitHub 账号")
    account = match.get("account") or {}
    return str(account.get("login", "")) if isinstance(account, dict) else ""


async def _save(service: Platform, user_id: str, account: str, installation_id: int) -> str:
    name = f"GitHub App · {account or installation_id}"
    try:
        await service.projects.create_connection(user_id, name, account, "", installation_id)
    except Conflict:
        name = f"{name} · {installation_id}"
        await service.projects.create_connection(user_id, name, account, "", installation_id)
    return name


async def _app(service: Platform) -> GitHubApp | None:
    try:
        return await service.credentials.app()
    except ConfigError:
        return None


def _back(path: str, **params: str) -> RedirectResponse:
    return RedirectResponse(f"{path}?{urlencode(params)}", status_code=status.HTTP_303_SEE_OTHER)
