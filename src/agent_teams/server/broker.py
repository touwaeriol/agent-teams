"""Live event fan-out to browsers: one channel per conversation.

``ValkeyBroker`` uses Valkey pub/sub, so several server processes can share
conversations; ``MemoryBroker`` serves tests and single-process demos.
"""

from __future__ import annotations

import asyncio
import json
from collections import defaultdict
from collections.abc import AsyncGenerator
from contextlib import suppress
from typing import Any, Protocol

import valkey.asyncio as valkey

Event = dict[str, Any]
CHANNEL_PREFIX = "agent-teams:conversation:"


class Broker(Protocol):
    async def publish(self, conversation_id: str, event: Event) -> None: ...
    def subscribe(self, conversation_id: str) -> AsyncGenerator[Event, None]: ...
    async def close(self) -> None: ...


class MemoryBroker:
    def __init__(self) -> None:
        self._queues: defaultdict[str, set[asyncio.Queue[Event]]] = defaultdict(set)

    async def publish(self, conversation_id: str, event: Event) -> None:
        for queue in self._queues[conversation_id]:
            queue.put_nowait(event)

    async def subscribe(self, conversation_id: str) -> AsyncGenerator[Event, None]:
        queue: asyncio.Queue[Event] = asyncio.Queue()
        self._queues[conversation_id].add(queue)
        try:
            while True:
                yield await queue.get()
        finally:
            self._queues[conversation_id].discard(queue)

    async def close(self) -> None:
        return None


class ValkeyBroker:
    def __init__(self, client: valkey.Valkey) -> None:
        self._client = client

    @classmethod
    async def connect(cls, url: str) -> ValkeyBroker:
        client = valkey.from_url(url)
        await client.ping()
        return cls(client)

    async def publish(self, conversation_id: str, event: Event) -> None:
        payload = json.dumps(event, ensure_ascii=False, default=str)
        await self._client.publish(CHANNEL_PREFIX + conversation_id, payload)

    async def subscribe(self, conversation_id: str) -> AsyncGenerator[Event, None]:
        pubsub = self._client.pubsub(ignore_subscribe_messages=True)
        await pubsub.subscribe(CHANNEL_PREFIX + conversation_id)
        try:
            async for message in pubsub.listen():
                yield json.loads(message["data"])
        finally:
            with suppress(Exception):
                await pubsub.aclose()

    async def close(self) -> None:
        await self._client.aclose()
