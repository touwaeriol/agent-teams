"""Nodes: machines that employees run on.

A node runs one command and streams its stdout line by line. Every employee
works in its own directory: ``<node workdir>/<employee>`` unless it sets one.

* ``RemoteNode`` (remote.py): a daemon on the machine dials in to the console;
  runs survive network drops and console restarts. The recommended kind.
* ``SSHNode``: agentless, one SSH session per run.
* ``LocalNode``: runs on the console host itself.
"""

from __future__ import annotations

import asyncio
import os
import posixpath
import shlex
from abc import ABC, abstractmethod
from collections.abc import AsyncIterator, Sequence
from contextlib import suppress
from dataclasses import dataclass, field

from .errors import AgentTeamsError

STDERR_TAIL = 2000


class NodeCommandError(AgentTeamsError):
    """A command on a node exited with a non-zero status."""


class Node(ABC):
    name: str
    workdir: str
    #: Whether re-running with the same ``key`` re-attaches to the earlier run.
    resumable: bool = False

    @abstractmethod
    def stream(self, command: Sequence[str], cwd: str, key: str = "") -> AsyncIterator[str]:
        """Run ``command`` in ``cwd``, yielding stdout lines; raise on non-zero exit."""

    def workdir_for(self, employee: str, workdir: str = "") -> str:
        """The employee's directory: its own setting, else one named after it."""
        return posixpath.join(self.workdir, workdir or employee)


class ProcessNode(Node):
    """A node backed by a local child process (directly or via ``ssh``)."""

    @abstractmethod
    def argv(self, command: Sequence[str], cwd: str) -> tuple[list[str], str | None]:
        """The local argv to execute, and the local working directory (if any)."""

    async def stream(self, command: Sequence[str], cwd: str, key: str = "") -> AsyncIterator[str]:
        argv, local_cwd = self.argv(command, cwd)
        if local_cwd:
            os.makedirs(local_cwd, exist_ok=True)
        process = await asyncio.create_subprocess_exec(
            *argv,
            cwd=local_cwd,
            stdin=asyncio.subprocess.DEVNULL,
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
            limit=16 * 1024 * 1024,  # stream-json lines can be large
        )
        stderr = asyncio.ensure_future(_read_all(process.stderr))
        try:
            async for line in _lines(process.stdout):
                yield line
            check_exit(self.name, await process.wait(), await stderr)
        finally:
            _kill(process)
            stderr.cancel()


@dataclass
class LocalNode(ProcessNode):
    name: str = "local"
    workdir: str = ""

    def argv(self, command: Sequence[str], cwd: str) -> tuple[list[str], str | None]:
        return list(command), cwd or None


@dataclass
class SSHNode(ProcessNode):
    """Runs commands through the system ``ssh`` client (keys, ~/.ssh/config, agent)."""

    name: str
    host: str
    user: str = ""
    port: int | None = None
    identity_file: str = ""
    workdir: str = ""
    options: Sequence[str] = field(default_factory=tuple)

    def argv(self, command: Sequence[str], cwd: str) -> tuple[list[str], str | None]:
        return [*self.ssh_args(), self.destination, remote_command(command, cwd)], None

    @property
    def destination(self) -> str:
        return f"{self.user}@{self.host}" if self.user else self.host

    def ssh_args(self) -> list[str]:
        port = ["-p", str(self.port)] if self.port else []
        identity = ["-i", self.identity_file] if self.identity_file else []
        defaults = ["-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=accept-new"]
        return ["ssh", *defaults, *port, *identity, *self.options]


def check_exit(node: str, code: int, stderr: str) -> None:
    if code != 0:
        raise NodeCommandError(f"{node}: exit {code}: {stderr[-STDERR_TAIL:].strip()}")


async def probe(node: Node, command: Sequence[str] = ("claude", "--version")) -> str:
    """Run a quick command on ``node`` and return its first output line."""
    lines = [line async for line in node.stream(command, "")]
    return lines[0] if lines else ""


def remote_command(command: Sequence[str], cwd: str) -> str:
    """A shell-safe command line for the remote login shell."""
    quoted = shlex.join(command)
    if not cwd:
        return f"exec {quoted}"
    directory = shlex.quote(cwd)
    return f"mkdir -p {directory} && cd {directory} && exec {quoted}"


async def _lines(reader: asyncio.StreamReader | None) -> AsyncIterator[str]:
    if reader is None:
        return
    while line := await reader.readline():
        yield line.decode("utf-8", errors="replace").rstrip("\r\n")


async def _read_all(reader: asyncio.StreamReader | None) -> str:
    return (await reader.read()).decode("utf-8", errors="replace") if reader else ""


def _kill(process: asyncio.subprocess.Process) -> None:
    if process.returncode is None:
        with suppress(ProcessLookupError):
            process.kill()
