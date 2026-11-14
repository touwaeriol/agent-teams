"""The little of the GitHub REST API the platform needs: who a token belongs
to, which repositories it can see, and a repository's default branch."""

from __future__ import annotations

import re
from typing import Any, Protocol

import httpx

from ..errors import AgentTeamsError

API = "https://api.github.com"
_REPO = re.compile(r"^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$")
_PREFIXES = ("https://github.com/", "http://github.com/", "git@github.com:", "github.com/")


class GitHubError(AgentTeamsError):
    """GitHub refused the request or the input is not a GitHub repository."""


def parse_repo(text: str) -> str:
    """``owner/repo`` from a GitHub URL, an SSH URL or ``owner/repo``."""
    value = text.strip()
    for prefix in _PREFIXES:
        if value.startswith(prefix):
            value = value[len(prefix) :]
    value = value.split("?")[0].split("#")[0]
    parts = [p for p in value.split("/") if p]
    slug = "/".join(parts[:2]).removesuffix(".git")
    if not _REPO.match(slug):
        raise GitHubError(
            f"不是 GitHub 仓库地址：{text}（支持 https://github.com/owner/repo 或 owner/repo）"
        )
    return slug


class GitHub(Protocol):
    async def login(self, token: str) -> str: ...
    async def repos(self, token: str) -> list[dict[str, Any]]: ...
    async def repo(self, token: str, slug: str) -> dict[str, Any]: ...


class GitHubAPI:
    def __init__(self, base: str = API, timeout: float = 15.0) -> None:
        self._base = base
        self._timeout = timeout

    async def login(self, token: str) -> str:
        data = await self._get(token, "/user")
        return str(data.get("login", ""))

    async def repos(self, token: str) -> list[dict[str, Any]]:
        params = {
            "per_page": "100",
            "sort": "pushed",
            "affiliation": "owner,collaborator,organization_member",
        }
        data = await self._get(token, "/user/repos", params)
        return [_summary(r) for r in data] if isinstance(data, list) else []

    async def repo(self, token: str, slug: str) -> dict[str, Any]:
        return _summary(await self._get(token, f"/repos/{slug}"))

    async def _get(self, token: str, path: str, params: dict[str, str] | None = None) -> Any:
        headers = {
            "Authorization": f"Bearer {token}",
            "Accept": "application/vnd.github+json",
            "X-GitHub-Api-Version": "2022-11-28",
        }
        try:
            async with httpx.AsyncClient(base_url=self._base, timeout=self._timeout) as client:
                response = await client.get(path, headers=headers, params=params)
        except httpx.HTTPError as exc:
            raise GitHubError(f"连不上 GitHub：{exc}") from exc
        if response.status_code in (401, 403):
            raise GitHubError("GitHub 拒绝了这个令牌（无效、过期或权限不够）")
        if response.status_code == 404:
            raise GitHubError("找不到这个仓库，或者令牌没有它的访问权限")
        if response.status_code >= 400:
            raise GitHubError(f"GitHub 返回 {response.status_code}：{response.text[:200]}")
        return response.json()


def _summary(repo: Any) -> dict[str, Any]:
    data = repo if isinstance(repo, dict) else {}
    return {
        "full_name": data.get("full_name", ""),
        "private": bool(data.get("private")),
        "default_branch": data.get("default_branch", "main"),
        "description": data.get("description") or "",
        "html_url": data.get("html_url", ""),
        "pushed_at": data.get("pushed_at"),
    }
