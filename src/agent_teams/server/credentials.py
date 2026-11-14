"""Git credentials for a user's connections, and the platform's GitHub App.

A connection is either an access token the user pasted (decrypted on use)
or an installation of the platform's GitHub App, for which short-lived
installation tokens are minted and cached until shortly before they expire.
The App itself (created once by an administrator from a manifest) is kept
in the settings table with its secrets sealed.
"""

from __future__ import annotations

import hashlib
import hmac
import time
from dataclasses import dataclass
from typing import Any

from ..errors import ConfigError
from .github import GitHub, Token
from .projects import ProjectStore
from .secrets import SecretBox

APP_SETTING = "github_app"
REFRESH_MARGIN_SECONDS = 600.0
STATE_LIFETIME_SECONDS = 3600


@dataclass(frozen=True)
class GitHubApp:
    app_id: int
    slug: str
    name: str
    owner: str
    html_url: str
    client_id: str
    client_secret: str
    pem: str

    @property
    def install_url(self) -> str:
        return f"https://github.com/apps/{self.slug}/installations/new"

    def public(self) -> dict[str, Any]:
        return {
            "slug": self.slug,
            "name": self.name,
            "owner": self.owner,
            "html_url": self.html_url,
        }


class GitCredentials:
    def __init__(self, projects: ProjectStore, secrets: SecretBox, github: GitHub) -> None:
        self._projects = projects
        self._secrets = secrets
        self._github = github
        self._tokens: dict[int, Token] = {}

    async def token(self, user_id: str, connection_id: str) -> str:
        """A token for git and the GitHub API; raises ``ConfigError`` if unusable."""
        connection = await self._projects.connection(user_id, connection_id)
        if connection is None:
            raise ConfigError("没有这个 Git 连接")
        if connection.installation_id is None:
            secret = await self._projects.connection_secret(user_id, connection_id)
            return self._secrets.open(secret or "")
        return await self.installation_token(connection.installation_id)

    async def installation_token(self, installation_id: int) -> str:
        cached = self._tokens.get(installation_id)
        if cached is not None and cached.expires_at - time.time() > REFRESH_MARGIN_SECONDS:
            return cached.value
        app = await self.app()
        if app is None:
            raise ConfigError("平台的 GitHub App 还没有创建（管理员在“管理”页创建）")
        fresh = await self._github.installation_token(app.app_id, app.pem, installation_id)
        self._tokens[installation_id] = fresh
        return fresh.value

    async def app(self) -> GitHubApp | None:
        stored = await self._projects.setting(APP_SETTING)
        if not stored:
            return None
        return GitHubApp(
            app_id=int(stored["app_id"]),
            slug=str(stored["slug"]),
            name=str(stored.get("name", stored["slug"])),
            owner=str(stored.get("owner", "")),
            html_url=str(stored.get("html_url", "")),
            client_id=str(stored["client_id"]),
            client_secret=self._secrets.open(str(stored["client_secret"])),
            pem=self._secrets.open(str(stored["pem"])),
        )

    async def save_app(self, conversion: dict[str, Any]) -> GitHubApp:
        """Store the App GitHub created from our manifest."""
        owner = conversion.get("owner") or {}
        record = {
            "app_id": int(conversion["id"]),
            "slug": str(conversion["slug"]),
            "name": str(conversion.get("name", conversion["slug"])),
            "owner": str(owner.get("login", "")) if isinstance(owner, dict) else "",
            "html_url": str(conversion.get("html_url", "")),
            "client_id": str(conversion["client_id"]),
            "client_secret": self._secrets.seal(str(conversion["client_secret"])),
            "pem": self._secrets.seal(str(conversion["pem"])),
        }
        await self._projects.save_setting(APP_SETTING, record)
        self._tokens.clear()
        app = await self.app()
        assert app is not None
        return app

    async def forget_app(self) -> None:
        await self._projects.save_setting(APP_SETTING, None)
        self._tokens.clear()


class StateSigner:
    """Signed ``state`` values that tie a GitHub redirect to the user who started it."""

    def __init__(self, secrets: SecretBox) -> None:
        self._key = secrets.derive("oauth-state")

    def sign(self, purpose: str, user_id: str, now: float | None = None) -> str:
        issued = int(now if now is not None else time.time())
        return f"{user_id}.{issued}.{self._mac(purpose, user_id, issued)}"

    def verify(self, purpose: str, state: str, now: float | None = None) -> str | None:
        """The user ID in a valid, recent ``state``, else None."""
        parts = state.split(".")
        if len(parts) != 3 or not parts[1].isdigit():
            return None
        user_id, issued = parts[0], int(parts[1])
        expected = self._mac(purpose, user_id, issued)
        fresh = (now if now is not None else time.time()) - issued < STATE_LIFETIME_SECONDS
        return user_id if fresh and hmac.compare_digest(expected, parts[2]) else None

    def _mac(self, purpose: str, user_id: str, issued: int) -> str:
        message = f"{purpose}:{user_id}:{issued}".encode()
        return hmac.new(self._key, message, hashlib.sha256).hexdigest()[:32]
