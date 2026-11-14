"""Projects and Git connections: what a signed-in user's team works on."""

from __future__ import annotations

from typing import Any

from fastapi import APIRouter, Depends, HTTPException, Request, status

from ..errors import ConfigError
from .accounts import Conflict
from .auth import current_user
from .github import GitHubError, parse_repo
from .models import (
    GitConnectionView,
    NewGitConnection,
    ProjectInput,
    ProjectUpdate,
    ProjectView,
    UserView,
)
from .platform import Platform, platform

User = Depends(current_user)


def router() -> APIRouter:
    routes = APIRouter(prefix="/api")
    routes.add_api_route("/git/connections", list_connections, methods=["GET"])
    routes.add_api_route("/git/connections", add_connection, methods=["POST"], status_code=201)
    routes.add_api_route(
        "/git/connections/{connection_id}",
        delete_connection,
        methods=["DELETE"],
        status_code=204,
    )
    routes.add_api_route("/git/connections/{connection_id}/repos", list_repos, methods=["GET"])
    routes.add_api_route("/projects", list_projects, methods=["GET"])
    routes.add_api_route("/projects", create_project, methods=["POST"], status_code=201)
    routes.add_api_route("/projects/{project_id}", get_project, methods=["GET"])
    routes.add_api_route("/projects/{project_id}", update_project, methods=["PATCH"])
    routes.add_api_route(
        "/projects/{project_id}", delete_project, methods=["DELETE"], status_code=204
    )
    return routes


async def list_connections(request: Request, user: UserView = User) -> list[GitConnectionView]:
    return await platform(request).projects.connections(user.id)


async def add_connection(
    request: Request, body: NewGitConnection, user: UserView = User
) -> GitConnectionView:
    state = platform(request)
    token = body.token.strip()
    if not token:
        raise HTTPException(status.HTTP_422_UNPROCESSABLE_CONTENT, "请填写访问令牌")
    if not state.services.secrets.ready:
        raise HTTPException(
            status.HTTP_503_SERVICE_UNAVAILABLE,
            "服务没有配置 AGENT_TEAMS_SECRET_KEY，不能保存 Git 凭证",
        )
    login = await _github(state.services.github.login(token))
    name = body.name.strip() or f"GitHub · {login}"
    try:
        return await state.projects.create_connection(
            user.id, name, login, state.services.secrets.seal(token)
        )
    except Conflict:
        raise HTTPException(status.HTTP_409_CONFLICT, f"已经有叫 {name} 的连接了") from None


async def delete_connection(request: Request, connection_id: str, user: UserView = User) -> None:
    if not await platform(request).projects.delete_connection(user.id, connection_id):
        raise HTTPException(status.HTTP_404_NOT_FOUND, "没有这个连接")


async def list_repos(
    request: Request, connection_id: str, user: UserView = User
) -> list[dict[str, Any]]:
    state = platform(request)
    connection = await state.projects.connection(user.id, connection_id)
    token = await _token(state, user.id, connection_id)
    github = state.services.github
    listing = github.installation_repos if connection and connection.kind == "app" else github.repos
    repos: list[dict[str, Any]] = await _github(listing(token))
    return repos


async def list_projects(request: Request, user: UserView = User) -> list[ProjectView]:
    return await platform(request).projects.projects(user.id)


async def get_project(request: Request, project_id: str, user: UserView = User) -> ProjectView:
    project = await platform(request).projects.project(user.id, project_id)
    if project is None:
        raise HTTPException(status.HTTP_404_NOT_FOUND, "没有这个项目")
    return project


async def create_project(
    request: Request, body: ProjectInput, user: UserView = User
) -> ProjectView:
    state = platform(request)
    fields = await _checked(state, user.id, body.model_dump())
    try:
        return await state.projects.create_project(user.id, fields)
    except Conflict:
        raise HTTPException(
            status.HTTP_409_CONFLICT, f"已经有叫 {fields['name']} 的项目了"
        ) from None


async def update_project(
    request: Request, project_id: str, body: ProjectUpdate, user: UserView = User
) -> ProjectView:
    state = platform(request)
    current = await get_project(request, project_id, user)
    changes = body.model_dump(exclude_unset=True, exclude={"clear_repo"})
    if body.clear_repo:
        changes.update(repo=None, branch="", git_connection_id=None)
    merged = {**current.model_dump(), **changes}
    fields = await _checked(state, user.id, merged, revalidate="repo" in changes)
    updates = {k: fields[k] for k in (*changes, "branch") if k in fields}
    try:
        project = await state.projects.update_project(user.id, project_id, updates)
    except Conflict:
        raise HTTPException(
            status.HTTP_409_CONFLICT, f"已经有叫 {fields['name']} 的项目了"
        ) from None
    if project is None:
        raise HTTPException(status.HTTP_404_NOT_FOUND, "没有这个项目")
    return project


async def delete_project(request: Request, project_id: str, user: UserView = User) -> None:
    if not await platform(request).projects.delete_project(user.id, project_id):
        raise HTTPException(status.HTTP_404_NOT_FOUND, "没有这个项目")


async def _checked(
    state: Platform, user_id: str, data: dict[str, Any], revalidate: bool = True
) -> dict[str, Any]:
    """Normalize a project: a name, and a repository the chosen connection can reach."""
    name = str(data.get("name") or "").strip()
    if not name:
        raise HTTPException(status.HTTP_422_UNPROCESSABLE_CONTENT, "请填写项目名称")
    fields = {**data, "name": name}
    repo = str(data.get("repo") or "").strip()
    if not repo:
        return {**fields, "repo": None, "branch": "", "git_connection_id": None}
    connection_id = data.get("git_connection_id")
    if not connection_id:
        raise HTTPException(
            status.HTTP_422_UNPROCESSABLE_CONTENT, "关联仓库需要先选择一个 Git 连接"
        )
    try:
        slug = parse_repo(repo)
    except GitHubError as exc:
        raise HTTPException(status.HTTP_422_UNPROCESSABLE_CONTENT, str(exc)) from exc
    token = await _token(state, user_id, str(connection_id))
    if not revalidate and data.get("branch"):
        return {**fields, "repo": slug}
    found = await _github(state.services.github.repo(token, slug))
    branch = str(data.get("branch") or "").strip() or str(found["default_branch"])
    return {**fields, "repo": found["full_name"] or slug, "branch": branch}


async def _token(state: Platform, user_id: str, connection_id: str) -> str:
    if await state.projects.connection(user_id, connection_id) is None:
        raise HTTPException(status.HTTP_404_NOT_FOUND, "没有这个 Git 连接")
    try:
        token: str = await _github(state.credentials.token(user_id, connection_id))
    except ConfigError as exc:
        raise HTTPException(status.HTTP_409_CONFLICT, str(exc)) from exc
    return token


async def _github(call: Any) -> Any:
    try:
        return await call
    except GitHubError as exc:
        raise HTTPException(status.HTTP_422_UNPROCESSABLE_CONTENT, str(exc)) from exc
