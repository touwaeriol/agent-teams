"""Server-Sent Events: live updates for the browser."""

from __future__ import annotations

import asyncio
import json
from collections.abc import AsyncGenerator
from contextlib import suppress

from .broker import Event

HEARTBEAT_SECONDS = 15.0


async def sse(
    events: AsyncGenerator[Event, None], heartbeat: float = HEARTBEAT_SECONDS
) -> AsyncGenerator[str, None]:
    """Format events as SSE, with comment heartbeats so proxies keep the stream open."""
    pending = asyncio.ensure_future(anext(events))
    try:
        while True:
            done, _ = await asyncio.wait({pending}, timeout=heartbeat)
            if not done:
                yield ": ping\n\n"
                continue
            yield f"data: {json.dumps(pending.result(), ensure_ascii=False)}\n\n"
            pending = asyncio.ensure_future(anext(events))
    finally:
        pending.cancel()
        with suppress(asyncio.CancelledError, StopAsyncIteration):
            await pending
        await events.aclose()
