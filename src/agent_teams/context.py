"""Per-run context passed down through nested agents and teams."""

from __future__ import annotations

from dataclasses import dataclass, field, replace

from .hooks import Event, Hooks


@dataclass(frozen=True)
class RunContext:
    """Immutable; ``enter`` returns a child context one level deeper."""

    hooks: Hooks = field(default_factory=Hooks)
    path: tuple[str, ...] = ()

    def enter(self, name: str) -> RunContext:
        return replace(self, path=(*self.path, name))

    async def emit(self, event: Event) -> None:
        await self.hooks.emit(event)
