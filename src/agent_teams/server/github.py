"""The little of the GitHub API the platform needs: who a token belongs to,
which repositories it can see, a repository's default branch, and the GitHub
App flow (create from a manifest, user authorization on install,
installation tokens)."""

from __future__ import annotations

import base64
import json
import re
import time
from dataclasses import dataclass
from datetime import datetime
from typing import Any, Protocol

import httpx
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import padding, rsa

from ..errors import AgentTeamsError

API = "https://api.github.com"
WEB = "https://github.com"
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
    async def installation_repos(self, token: str) -> list[dict[str, Any]]: ...
    async def convert_manifest(self, code: str) -> dict[str, Any]: ...
    async def installation_token(self, app_id: int, pem: str, installation_id: int) -> Token: ...
    async def user_token(self, client_id: str, client_secret: str, code: str) -> str: ...
    async def user_installations(self, user_token: str) -> list[dict[str, Any]]: ...


@dataclass(frozen=True)
class Token:
    value: str
    expires_at: float  # unix seconds


class GitHubAPI:
    def __init__(self, base: str = API, web: str = WEB, timeout: float = 15.0) -> None:
        self._base = base
        self._web = web
        self._timeout = timeout

    async def login(self, token: str) -> str:
        data = await self._call("GET", "/user", token)
        return str(data.get("login", ""))

    async def repos(self, token: str) -> list[dict[str, Any]]:
        params = {
            "per_page": "100",
            "sort": "pushed",
            "affiliation": "owner,collaborator,organization_member",
        }
        data = await self._call("GET", "/user/repos", token, params=params)
        return [_summary(r) for r in data] if isinstance(data, list) else []

    async def repo(self, token: str, slug: str) -> dict[str, Any]:
        return _summary(await self._call("GET", f"/repos/{slug}", token))

    async def installation_repos(self, token: str) -> list[dict[str, Any]]:
        data = await self._call(
            "GET", "/installation/repositories", token, params={"per_page": "100"}
        )
        return [_summary(r) for r in data.get("repositories", [])]

    async def convert_manifest(self, code: str) -> dict[str, Any]:
        data = await self._call("POST", f"/app-manifests/{code}/conversions", None)
        return dict(data)

    async def installation_token(self, app_id: int, pem: str, installation_id: int) -> Token:
        jwt = app_jwt(app_id, pem)
        path = f"/app/installations/{installation_id}/access_tokens"
        data = await self._call("POST", path, jwt)
        expires = datetime.fromisoformat(str(data["expires_at"]).replace("Z", "+00:00"))
        return Token(str(data["token"]), expires.timestamp())

    async def user_token(self, client_id: str, client_secret: str, code: str) -> str:
        body = {"client_id": client_id, "client_secret": client_secret, "code": code}
        data = await self._call(
            "POST", "/login/oauth/access_token", None, json=body, base=self._web
        )
        if "access_token" not in data:
            raise GitHubError(f"GitHub 授权失败：{data.get('error_description') or data}")
        return str(data["access_token"])

    async def user_installations(self, user_token: str) -> list[dict[str, Any]]:
        data = await self._call(
            "GET", "/user/installations", user_token, params={"per_page": "100"}
        )
        return list(data.get("installations", []))

    async def _call(
        self,
        method: str,
        path: str,
        token: str | None,
        *,
        params: dict[str, str] | None = None,
        json: dict[str, Any] | None = None,
        base: str | None = None,
    ) -> Any:
        headers = {"Accept": "application/vnd.github+json", "X-GitHub-Api-Version": "2022-11-28"}
        if token:
            headers["Authorization"] = f"Bearer {token}"
        try:
            async with httpx.AsyncClient(
                base_url=base or self._base, timeout=self._timeout
            ) as client:
                response = await client.request(
                    method, path, headers=headers, params=params, json=json
                )
        except httpx.HTTPError as exc:
            raise GitHubError(f"连不上 GitHub：{exc}") from exc
        _check(response)
        return response.json()


def _check(response: httpx.Response) -> None:
    if response.status_code in (401, 403):
        raise GitHubError("GitHub 拒绝了这个令牌（无效、过期或权限不够）")
    if response.status_code == 404:
        raise GitHubError("找不到这个仓库，或者令牌没有它的访问权限")
    if response.status_code >= 400:
        raise GitHubError(f"GitHub 返回 {response.status_code}：{response.text[:200]}")


def app_jwt(app_id: int, pem: str, now: float | None = None) -> str:
    """A GitHub App JWT (RS256, valid ~9 minutes) for app-level API calls."""
    issued = int(now if now is not None else time.time()) - 60
    header = _b64(json_dumps({"alg": "RS256", "typ": "JWT"}))
    claims = _b64(json_dumps({"iat": issued, "exp": issued + 540, "iss": str(app_id)}))
    signing_input = f"{header}.{claims}".encode()
    key = serialization.load_pem_private_key(pem.encode(), password=None)
    if not isinstance(key, rsa.RSAPrivateKey):
        raise GitHubError("GitHub App 私钥不是 RSA 密钥")
    signature = key.sign(signing_input, padding.PKCS1v15(), hashes.SHA256())
    return f"{header}.{claims}.{_b64(signature)}"


def _b64(data: bytes) -> str:
    return base64.urlsafe_b64encode(data).rstrip(b"=").decode()


def json_dumps(value: Any) -> bytes:
    return json.dumps(value, separators=(",", ":")).encode()


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
