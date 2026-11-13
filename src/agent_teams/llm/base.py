"""The LLM port: the only thing agents know about a model backend."""

from __future__ import annotations

from collections.abc import Sequence
from typing import Protocol, runtime_checkable

from ..types import Reply, ToolSpec, Turn


@runtime_checkable
class LLM(Protocol):
    """A stateless chat-completion backend.

    Implementations translate the neutral history into their native format
    and normalize the response into a :class:`Reply`.
    """

    async def complete(
        self, system: str, history: Sequence[Turn], tools: Sequence[ToolSpec]
    ) -> Reply: ...
