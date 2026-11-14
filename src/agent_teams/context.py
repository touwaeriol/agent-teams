"""Per-run context passed down through nested agents and teams."""

from __future__ import annotations

from dataclasses import dataclass, field, replace

from .hooks import Event, Hooks


@dataclass(frozen=True)
class RunContext:
    """Immutable; ``enter`` returns a child context one level deeper.

    ``run_key`` identifies a unit of work stably across restarts (e.g.
    ``<conversation>/<task>``); resumable nodes use it to re-attach to a run.
    """

    hooks: Hooks = field(default_factory=Hooks)
    path: tuple[str, ...] = ()
    run_key: str = ""

    def enter(self, name: str) -> RunContext:
        return replace(self, path=(*self.path, name))

    def keyed(self, part: str) -> RunContext:
        key = f"{self.run_key}/{part}" if self.run_key else part
        return replace(self, run_key=key)

    async def emit(self, event: Event) -> None:
        await self.hooks.emit(event)
