"""Console side of the node daemon protocol.

Each node daemon keeps one WebSocket open to the console (it dials out, so
nodes behind NAT work). JSON messages:

* node -> console: ``hello`` {node info}, ``output`` {run_id, seq, line},
  ``exit`` {run_id, code, stderr}
* console -> node: ``start`` {run_id, command, cwd, after, env?, repo?},
  ``cancel`` {run_id}

``start`` is idempotent per ``run_id``: if the daemon already has that run
(still running, finished, or only in its on-disk log) it replays the output
after ``after`` instead of starting again. That is what lets a run survive
network drops and console restarts.
"""

from __future__ import annotations

import asyncio
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


Hangup = Callable[[], Awaitable[None]]
EnvSource = Callable[[], Awaitable[Mapping[str, str]]]


class NodeLink:
    """One connected daemon; multiplexes many runs over its socket.

    ``key`` identifies the node (its ID); ``hangup`` closes the connection,
    e.g. when the node's token is revoked.
    """

    def __init__(
        self, key: str, send: Send, hello: Mapping[str, Any], hangup: Hangup | None = None
    ) -> None:
        self.key = key
        self.hello = dict(hello)
        self.connected_at = time.time()
        self._send = send
        self._hangup = hangup
        self._runs: dict[str, asyncio.Queue[Message]] = {}

    async def run(
        self,
        run_id: str,
        command: Sequence[str],
        cwd: str,
        after: int,
        setup: Mapping[str, Any] | None = None,
    ) -> AsyncIterator[Message]:
        """Start or re-attach to ``run_id``; yield its messages until ``exit``.

        ``setup`` may carry ``env`` (extra environment for the process) and
        ``repo`` ({url, branch}: clone it into ``cwd`` first if not there yet).
        """
        queue: asyncio.Queue[Message] = asyncio.Queue()
        self._runs[run_id] = queue
        try:
            start = {"command": list(command), "cwd": cwd, "after": after, **(setup or {})}
            await self._send({"type": "start", "run_id": run_id, **start})
            while (message := await queue.get()) is not _LOST:
                yield message
                if message["type"] == "exit":
                    return
            raise LinkLost(self.key)
        finally:
            if self._runs.get(run_id) is queue:
                del self._runs[run_id]

    async def cancel(self, run_id: str) -> None:
        with suppress(Exception):
            await self._send({"type": "cancel", "run_id": run_id})

    async def hang_up(self) -> None:
        if self._hangup is not None:
            with suppress(Exception):
                await self._hangup()

    def deliver(self, message: Mapping[str, Any]) -> None:
        queue = self._runs.get(str(message.get("run_id")))
        if queue is not None:
            queue.put_nowait(dict(message))

    def close(self) -> None:
        for queue in self._runs.values():
            queue.put_nowait(_LOST)


class NodeRegistry:
    """Which node daemons are connected, by node key. Authentication happens
    before :meth:`attach` (the web service checks the node's token)."""

    def __init__(self) -> None:
        self._links: dict[str, NodeLink] = {}
        self._online: dict[str, asyncio.Event] = {}

    def attach(self, link: NodeLink) -> None:
        previous = self._links.get(link.key)
        if previous is not None:
            previous.close()
        self._links[link.key] = link
        self._event(link.key).set()

    def detach(self, link: NodeLink) -> None:
        link.close()
        if self._links.get(link.key) is link:
            del self._links[link.key]
            self._event(link.key).clear()

    async def disconnect(self, key: str) -> None:
        """Drop a node's connection (its token was revoked or it was deleted)."""
        link = self._links.get(key)
        if link is not None:
            self.detach(link)
            await link.hang_up()

    async def link(self, key: str, timeout: float) -> NodeLink:
        """The node's current connection, waiting up to ``timeout`` seconds for it."""
        try:
            await asyncio.wait_for(self._event(key).wait(), timeout)
        except asyncio.TimeoutError:
            raise NodeOffline(f"node '{key}' has not connected within {timeout:.0f}s") from None
        return self._links[key]

    def status(self, key: str) -> dict[str, Any]:
        link = self._links.get(key)
        if link is None:
            return {"online": False}
        info = {k: v for k, v in link.hello.items() if k != "type"}
        return {**info, "online": True, "since": link.connected_at}

    def _event(self, key: str) -> asyncio.Event:
        return self._online.setdefault(key, asyncio.Event())


@dataclass
class RemoteNode(Node):
    """A machine whose daemon is connected to this console."""

    name: str
    registry: NodeRegistry = field(repr=False)
    workdir: str = ""  # relative to the daemon's --root
    key: str = ""  # registry key (the node's ID); defaults to the name
    env: Mapping[str, str] = field(default_factory=dict)  # extra environment for every run
    env_source: EnvSource | None = None  # more environment, fetched when each run starts
    repo: Mapping[str, str] | None = None  # {url, branch}: cloned into each employee's directory
    connect_timeout: float = 60.0
    reconnect_timeout: float = 3600.0
    resumable = True

    @property
    def registry_key(self) -> str:
        return self.key or self.name

    async def setup(self) -> dict[str, Any]:
        """What the daemon needs before starting a run: environment and repository."""
        env = {**self.env, **(await self.env_source() if self.env_source else {})}
        setup: dict[str, Any] = {}
        if env:
            setup["env"] = env
        if self.repo:
            setup["repo"] = dict(self.repo)
        return setup

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
    setup: dict[str, Any] = field(default_factory=dict)

    async def lines(self) -> AsyncIterator[str]:
        timeout = self.node.connect_timeout
        self.setup = await self.node.setup()
        try:
            while not self.finished:
                self.link = await self.node.registry.link(self.node.registry_key, timeout)
                timeout = self.node.reconnect_timeout
                async for line in self._attach(self.link):
                    yield line
        finally:
            if not self.finished and self.link is not None:
                await self.link.cancel(self.run_id)

    async def _attach(self, link: NodeLink) -> AsyncIterator[str]:
        """Follow the run on one connection; returns quietly if the link drops."""
        messages = link.run(self.run_id, self.command, self.cwd, self.after, self.setup)
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
