"""Console side of the node daemon protocol.

Each node daemon keeps one WebSocket open to the console (it dials out, so
nodes behind NAT work). JSON messages:

* node -> console: ``hello`` {node info}, ``output`` {run_id, seq, line},
  ``exit`` {run_id, code, stderr}
* console -> node: ``start`` {run_id, command, cwd, after}, ``cancel`` {run_id}

``start`` is idempotent per ``run_id``: if the daemon already has that run
(still running, finished, or only in its on-disk log) it replays the output
after ``after`` instead of starting again. That is what lets a run survive
network drops and console restarts.
"""

from __future__ import annotations

import asyncio
import hashlib
import hmac
import time
import uuid
from collections.abc import AsyncIterator, Awaitable, Callable, Mapping, Sequence
from contextlib import suppress
from dataclasses import dataclass, field
from typing import Any

from .errors import AgentTeamsError
from .nodes import Node, check_exit

PROTOCOL_VERSION = 1
Message = dict[str, Any]
Send = Callable[[Message], Awaitable[None]]
_LOST: Message = {"type": "lost"}


class NodeOffline(AgentTeamsError):  # noqa: N818 - reads better as a state
    """The node's daemon is not connected."""


class LinkLost(Exception):  # noqa: N818 - internal control flow
    """The connection dropped while a run was being followed."""


def node_token(secret: str, name: str, revision: int = 1) -> str:
    """The token a node's daemon presents; bump ``revision`` to revoke it."""
    message = f"agent-teams-node:{name}:{revision}".encode()
    return hmac.new(secret.encode(), message, hashlib.sha256).hexdigest()


class NodeLink:
    """One connected daemon; multiplexes many runs over its socket."""

    def __init__(self, name: str, send: Send, hello: Mapping[str, Any]) -> None:
        self.name = name
        self.hello = dict(hello)
        self.connected_at = time.time()
        self._send = send
        self._runs: dict[str, asyncio.Queue[Message]] = {}

    async def run(
        self, run_id: str, command: Sequence[str], cwd: str, after: int
    ) -> AsyncIterator[Message]:
        """Start or re-attach to ``run_id``; yield its messages until ``exit``."""
        queue: asyncio.Queue[Message] = asyncio.Queue()
        self._runs[run_id] = queue
        try:
            start = {"command": list(command), "cwd": cwd, "after": after}
            await self._send({"type": "start", "run_id": run_id, **start})
            while (message := await queue.get()) is not _LOST:
                yield message
                if message["type"] == "exit":
                    return
            raise LinkLost(self.name)
        finally:
            if self._runs.get(run_id) is queue:
                del self._runs[run_id]

    async def cancel(self, run_id: str) -> None:
        with suppress(Exception):
            await self._send({"type": "cancel", "run_id": run_id})

    def deliver(self, message: Mapping[str, Any]) -> None:
        queue = self._runs.get(str(message.get("run_id")))
        if queue is not None:
            queue.put_nowait(dict(message))

    def close(self) -> None:
        for queue in self._runs.values():
            queue.put_nowait(_LOST)


class NodeRegistry:
    """Which daemons are connected; verifies their tokens."""

    def __init__(self, secret: str = "") -> None:
        self._secret = secret
        self._revisions: dict[str, int] = {}
        self._links: dict[str, NodeLink] = {}
        self._online: dict[str, asyncio.Event] = {}

    def declare(self, name: str, revision: int = 1) -> None:
        self._revisions[name] = revision

    def token_for(self, name: str) -> str:
        if not self._secret or name not in self._revisions:
            raise NodeOffline(f"node '{name}' is not a configured daemon node")
        return node_token(self._secret, name, self._revisions[name])

    def verify(self, name: str, token: str) -> bool:
        if not self._secret or name not in self._revisions:
            return False
        return hmac.compare_digest(token.encode(), self.token_for(name).encode())

    def attach(self, link: NodeLink) -> None:
        previous = self._links.get(link.name)
        if previous is not None:
            previous.close()
        self._links[link.name] = link
        self._event(link.name).set()

    def detach(self, link: NodeLink) -> None:
        link.close()
        if self._links.get(link.name) is link:
            del self._links[link.name]
            self._event(link.name).clear()

    async def link(self, name: str, timeout: float) -> NodeLink:
        """The node's current connection, waiting up to ``timeout`` seconds for it."""
        try:
            await asyncio.wait_for(self._event(name).wait(), timeout)
        except asyncio.TimeoutError:
            raise NodeOffline(f"node '{name}' has not connected within {timeout:.0f}s") from None
        return self._links[name]

    def statuses(self) -> list[dict[str, Any]]:
        return [self._status(name) for name in self._revisions]

    def _status(self, name: str) -> dict[str, Any]:
        link = self._links.get(name)
        if link is None:
            return {"name": name, "online": False}
        info = {k: v for k, v in link.hello.items() if k != "type"}
        return {**info, "name": name, "online": True, "since": link.connected_at}

    def _event(self, name: str) -> asyncio.Event:
        return self._online.setdefault(name, asyncio.Event())


@dataclass
class RemoteNode(Node):
    """A machine whose daemon is connected to this console."""

    name: str
    registry: NodeRegistry = field(repr=False)
    workdir: str = ""  # relative to the daemon's --root
    connect_timeout: float = 60.0
    reconnect_timeout: float = 3600.0
    resumable = True

    def stream(self, command: Sequence[str], cwd: str, key: str = "") -> AsyncIterator[str]:
        return _RemoteRun(self, key or uuid.uuid4().hex, list(command), cwd).lines()


@dataclass
class _RemoteRun:
    node: RemoteNode
    run_id: str
    command: list[str]
    cwd: str
    after: int = 0
    finished: bool = False
    link: NodeLink | None = None

    async def lines(self) -> AsyncIterator[str]:
        timeout = self.node.connect_timeout
        try:
            while not self.finished:
                self.link = await self.node.registry.link(self.node.name, timeout)
                timeout = self.node.reconnect_timeout
                async for line in self._attach(self.link):
                    yield line
        finally:
            if not self.finished and self.link is not None:
                await self.link.cancel(self.run_id)

    async def _attach(self, link: NodeLink) -> AsyncIterator[str]:
        """Follow the run on one connection; returns quietly if the link drops."""
        messages = link.run(self.run_id, self.command, self.cwd, self.after)
        try:
            async for message in messages:
                line = self._handle(message)
                if line is not None:
                    yield line
        except LinkLost:
            return

    def _handle(self, message: Mapping[str, Any]) -> str | None:
        if message["type"] == "exit":
            self.finished = True
            check_exit(self.node.name, int(message["code"]), str(message.get("stderr", "")))
            return None
        seq = int(message["seq"])
        if seq <= self.after:  # already delivered before a reconnect
            return None
        self.after = seq
        return str(message["line"])
