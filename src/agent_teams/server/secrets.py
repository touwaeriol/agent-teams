"""Encrypt stored credentials (Git tokens) with the service's secret key."""

from __future__ import annotations

import base64
import hashlib

from cryptography.fernet import Fernet, InvalidToken

from ..errors import ConfigError


class SecretBox:
    """Symmetric encryption keyed by ``AGENT_TEAMS_SECRET_KEY`` (any string)."""

    def __init__(self, secret: str) -> None:
        self._fernet = Fernet(_key(secret)) if secret else None

    @property
    def ready(self) -> bool:
        return self._fernet is not None

    def seal(self, plaintext: str) -> str:
        return self._box().encrypt(plaintext.encode()).decode()

    def open(self, ciphertext: str) -> str:
        try:
            return self._box().decrypt(ciphertext.encode()).decode()
        except InvalidToken as exc:
            raise ConfigError("无法解密已保存的凭证：AGENT_TEAMS_SECRET_KEY 变过？") from exc

    def _box(self) -> Fernet:
        if self._fernet is None:
            raise ConfigError("服务没有配置 AGENT_TEAMS_SECRET_KEY，不能保存 Git 凭证")
        return self._fernet


def _key(secret: str) -> bytes:
    return base64.urlsafe_b64encode(hashlib.sha256(secret.encode()).digest())
