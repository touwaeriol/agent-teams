"""The node daemon: ``agent-teams node``.

Runs on each worker machine, dials out to the console over WebSocket and
starts employees (Claude Code, Codex, ...) on request. It only runs allowed
executables, only inside ``--root``, and keeps every run's output in memory
and in a log file, so a run outlives network drops and console restarts and
can be replayed from any point. Needs the ``[node]`` extra.
"""

from __future__ import annotations

import asyncio
import hashlib
import json
import logging
import os
import signal
import socket
import time
from collections.abc import Awaitable, Callable, Iterable, Mapping
from contextlib import suppress
from dataclasses import dataclass, field
from pathlib import Path
from typing import IO, Any
from urllib.parse import urlencode, urlsplit, urlunsplit

from websockets.asyncio.client import ClientConnection, connect
from websockets.exceptions import WebSocketException

from .remote import PROTOCOL_VERSION, Message

logger = logging.getLogger(__name__)

STDERR_TAIL = 2000
KILL_GRACE_SECONDS = 5.0
MEMORY_RETENTION_SECONDS = 3600.0
LOG_RETENTION_SECONDS = 7 * 24 * 3600.0
MAX_BACKOFF_SECONDS = 30.0
REJECTED, NOT_FOUND, LOST, STOPPED, CANCELLED = 126, 127, 137, 143, 130
CHECKOUT_FAILED = 128

Send = Callable[[Message], Awaitable[None]]


@dataclass(frozen=True)
class DaemonConfig:
    console: str
    name: str
    token: str
    root: Path
    state: Path
    allow: frozenset[str] = frozenset({"claude", "codex"})
    max_runs: int = 4

    @property
    def url(self) -> str:
        parts = urlsplit(self.console)
        scheme = {"https": "wss", "http": "ws"}.get(parts.scheme, parts.scheme)
        path = parts.path.rstrip("/") + "/nodes/connect"
        return urlunsplit((scheme, parts.netloc, path, urlencode({"name": self.name}), ""))


class Policy:
    """What the console may ask this node to run."""

    def __init__(self, root: Path, allow: Iterable[str]) -> None:
        self.root = root.resolve()
        self.allow = frozenset(allow)

    def check(self, command: list[str], cwd: str) -> Path:
        """The resolved working directory; raises ``PermissionError`` if not allowed."""
        if not command or Path(command[0]).name not in self.allow:
            raise PermissionError(
                f"executable not allowed: {command[:1]}; allowed {sorted(self.allow)}"
            )
        directory = (self.root / cwd).resolve()
        if directory != self.root and self.root not in directory.parents:
            raise PermissionError(f"working directory outside {self.root}: {cwd}")
        return directory


class Run:
    """One employee run: output lines (seq = index + 1), exit status, log file."""

    def __init__(self, run_id: str, log_path: Path) -> None:
        self.id = run_id
        self.lines: list[str] = []
        self.exit: tuple[int, str] | None = None
        self.finished_at = 0.0
        self.process: asyncio.subprocess.Process | None = None
        self.cancelled = False
        self._log_path = log_path
        self._log: IO[str] | None = None
        self._changed = asyncio.Event()

    @classmethod
    def load(cls, run_id: str, log_path: Path) -> Run:
        """Rebuild a run from its log; one without an exit record died with the daemon."""
        run = cls(run_id, log_path)
        for record in map(json.loads, log_path.read_text(encoding="utf-8").splitlines()):
            if "exit" in record:
                run.exit = (int(record["exit"]), str(record.get("stderr", "")))
            else:
                run.lines.append(str(record["line"]))
        if run.exit is None:
            run.finish(LOST, "the node daemon restarted while this run was in progress")
        run.finished_at = time.time()
        return run

    def append(self, line: str) -> None:
        self.lines.append(line)
        self._write({"seq": len(self.lines), "line": line})
        self._notify()

    def finish(self, code: int, stderr: str) -> None:
        if self.exit is not None:
            return
        self.exit = (code, stderr[-STDERR_TAIL:])
        self.finished_at = time.time()
        self._write({"exit": code, "stderr": self.exit[1]})
        if self._log is not None:
            self._log.close()
        self._notify()

    async def follow(self, after: int, send: Send) -> None:
        """Send output after ``after`` and then the exit, as it happens."""
        seq = max(after, 0)
        while True:
            changed = self._changed
            while seq < len(self.lines):
                seq += 1
                await send(
                    {"type": "output", "run_id": self.id, "seq": seq, "line": self.lines[seq - 1]}
                )
            if self.exit is not None:
                code, stderr = self.exit
                await send({"type": "exit", "run_id": self.id, "code": code, "stderr": stderr})
                return
            await changed.wait()

    def _write(self, record: Mapping[str, Any]) -> None:
        if self._log is None:
            self._log_path.parent.mkdir(parents=True, exist_ok=True)
            self._log = self._log_path.open("a", encoding="utf-8")
        self._log.write(json.dumps(record, ensure_ascii=False) + "\n")
        self._log.flush()

    def _notify(self) -> None:
        self._changed.set()
        self._changed = asyncio.Event()


class Runner:
    """Starts, tracks, re-attaches and cancels runs on this machine."""

    def __init__(self, policy: Policy, state: Path, max_runs: int) -> None:
        self.policy = policy
        self.state = state
        self._runs: dict[str, Run] = {}
        self._slots = asyncio.Semaphore(max_runs)
        self._tasks: set[asyncio.Task[None]] = set()

    @property
    def active(self) -> list[str]:
        return [run.id for run in self._runs.values() if run.exit is None]

    def ensure(self, run_id: str, command: list[str], cwd: str, setup: Setup | None = None) -> Run:
        """The run with this ID: existing (memory or log) or newly started."""
        self._forget_old()
        if run_id in self._runs:
            return self._runs[run_id]
        path = self._log_path(run_id)
        if path.exists():
            run = Run.load(run_id, path)
        else:
            run = self._start(run_id, path, command, cwd, setup or Setup())
        self._runs[run_id] = run
        return run

    def cancel(self, run_id: str) -> None:
        run = self._runs.get(run_id)
        if run is None or run.exit is not None:
            return
        run.cancelled = True
        if run.process is not None:
            _terminate(run.process)

    def stop(self) -> None:
        """Kill everything still running (daemon shutdown)."""
        for run in self._runs.values():
            if run.process is not None and run.exit is None:
                _terminate(run.process, grace=0)
                run.finish(STOPPED, "the node daemon stopped")

    def purge_logs(self, max_age: float = LOG_RETENTION_SECONDS) -> None:
        cutoff = time.time() - max_age
        for path in (self.state / "runs").glob("*.jsonl"):
            if path.stat().st_mtime < cutoff:
                path.unlink(missing_ok=True)

    def _start(self, run_id: str, path: Path, command: list[str], cwd: str, setup: Setup) -> Run:
        run = Run(run_id, path)
        try:
            directory = self.policy.check(command, cwd)
        except PermissionError as exc:
            run.finish(REJECTED, f"rejected by node policy: {exc}")
            return run
        task = asyncio.create_task(self._execute(run, command, directory, setup))
        self._tasks.add(task)
        task.add_done_callback(self._tasks.discard)
        return run

    async def _execute(self, run: Run, command: list[str], directory: Path, setup: Setup) -> None:
        async with self._slots:
            if run.cancelled:
                run.finish(CANCELLED, "cancelled before it started")
                return
            await asyncio.to_thread(directory.mkdir, parents=True, exist_ok=True)
            problem = await _checkout(run, directory, setup)
            if problem:
                run.finish(CHECKOUT_FAILED, problem)
                return
            try:
                run.process = await _spawn(command, directory, setup.env)
            except OSError as exc:
                run.finish(NOT_FOUND, f"cannot start {command[0]}: {exc}")
                return
            await _pump(run, run.process)

    def _forget_old(self) -> None:
        cutoff = time.time() - MEMORY_RETENTION_SECONDS
        stale = [k for k, r in self._runs.items() if r.exit is not None and r.finished_at < cutoff]
        for run_id in stale:
            del self._runs[run_id]

    def _log_path(self, run_id: str) -> Path:
        digest = hashlib.sha256(run_id.encode()).hexdigest()[:32]
        return self.state / "runs" / f"{digest}.jsonl"


@dataclass(frozen=True)
class Setup:
    """Per-run preparation sent with ``start``: extra environment and a repository."""

    env: Mapping[str, str] = field(default_factory=dict)
    repo: Mapping[str, str] | None = None

    @classmethod
    def of(cls, message: Mapping[str, Any]) -> Setup:
        env = message.get("env")
        repo = message.get("repo")
        return cls(
            {str(k): str(v) for k, v in env.items()} if isinstance(env, dict) else {},
            {str(k): str(v) for k, v in repo.items()} if isinstance(repo, dict) else None,
        )


async def _checkout(run: Run, directory: Path, setup: Setup) -> str:
    """Clone the run's repository into ``directory`` unless it is already there.

    Returns a problem description, or an empty string when ready.
    """
    repo = setup.repo
    if not repo or not repo.get("url"):
        return ""
    state = await asyncio.to_thread(_directory_state, directory)
    if state == "repo":
        return ""
    if state == "other":
        return f"cannot clone {repo['url']}: {directory} is not empty and not a git repository"
    branch = ["--branch", repo["branch"]] if repo.get("branch") else []
    command = ["git", "clone", *branch, "--", repo["url"], "."]
    code, stderr = await _run_quietly(command, directory, setup.env)
    if code != 0:
        return f"git clone {repo['url']} failed ({code}): {stderr[-STDERR_TAIL:]}"
    run.append(f"cloned {repo['url']} into {directory}")
    return ""


def _directory_state(directory: Path) -> str:
    """``repo`` (has .git), ``empty`` or ``other``."""
    if (directory / ".git").exists():
        return "repo"
    return "other" if any(directory.iterdir()) else "empty"


async def _run_quietly(
    command: list[str], directory: Path, env: Mapping[str, str]
) -> tuple[int, str]:
    try:
        process = await asyncio.create_subprocess_exec(
            *command,
            cwd=directory,
            env={**os.environ, **env},
            stdin=asyncio.subprocess.DEVNULL,
            stdout=asyncio.subprocess.DEVNULL,
            stderr=asyncio.subprocess.PIPE,
        )
    except OSError as exc:
        return NOT_FOUND, f"cannot start {command[0]}: {exc}"
    _, stderr = await process.communicate()
    return int(process.returncode or 0), stderr.decode("utf-8", errors="replace")


async def _spawn(
    command: list[str], directory: Path, env: Mapping[str, str] | None = None
) -> asyncio.subprocess.Process:
    return await asyncio.create_subprocess_exec(
        *command,
        cwd=directory,
        env={**os.environ, **env} if env else None,
        stdin=asyncio.subprocess.DEVNULL,
        stdout=asyncio.subprocess.PIPE,
        stderr=asyncio.subprocess.PIPE,
        start_new_session=True,  # own process group, so cancel kills its children too
        limit=16 * 1024 * 1024,
    )


async def _pump(run: Run, process: asyncio.subprocess.Process) -> None:
    stderr = asyncio.ensure_future(process.stderr.read() if process.stderr else _empty())
    assert process.stdout is not None
    while raw := await process.stdout.readline():
        run.append(raw.decode("utf-8", errors="replace").rstrip("\r\n"))
    code = await process.wait()
    run.finish(code, (await stderr).decode("utf-8", errors="replace"))


async def _empty() -> bytes:
    return b""


def _terminate(process: asyncio.subprocess.Process, grace: float = KILL_GRACE_SECONDS) -> None:
    _signal_group(process, signal.SIGTERM if grace else signal.SIGKILL)
    if grace:
        asyncio.get_running_loop().call_later(grace, _signal_group, process, signal.SIGKILL)


def _signal_group(process: asyncio.subprocess.Process, sig: signal.Signals) -> None:
    if process.returncode is None:
        with suppress(ProcessLookupError, PermissionError):
            os.killpg(process.pid, sig)


@dataclass
class Daemon:
    config: DaemonConfig
    runner: Runner = field(init=False)
    connection: ClientConnection | None = field(init=False, default=None)
    _claude_version: str = field(init=False, default="")

    def __post_init__(self) -> None:
        policy = Policy(self.config.root, self.config.allow)
        self.runner = Runner(policy, self.config.state, self.config.max_runs)

    async def serve_forever(self) -> None:
        self.runner.purge_logs()
        self._claude_version = await _version("claude")
        delay = 1.0
        try:
            while True:
                delay = await self._session(delay)
                await asyncio.sleep(delay)
        finally:
            self.runner.stop()

    async def _session(self, delay: float) -> float:
        """One connection; returns the delay before reconnecting."""
        headers = {"Authorization": f"Bearer {self.config.token}"}
        try:
            async with connect(self.config.url, additional_headers=headers, max_size=None) as ws:
                logger.info("connected to %s as %s", self.config.console, self.config.name)
                await self._converse(ws)
            return 1.0
        except (OSError, WebSocketException) as exc:
            logger.warning("console connection failed: %s; retrying in %.0fs", exc, delay)
            return min(delay * 2, MAX_BACKOFF_SECONDS)

    async def _converse(self, ws: ClientConnection) -> None:
        lock = asyncio.Lock()

        async def send(message: Message) -> None:
            async with lock:
                await ws.send(json.dumps(message, ensure_ascii=False))

        followers: set[asyncio.Task[None]] = set()
        self.connection = ws
        await send(self.hello())
        try:
            async for raw in ws:
                self._dispatch(json.loads(raw), send, followers)
        finally:
            self.connection = None
            for task in followers:
                task.cancel()

    def hello(self) -> Message:
        return {
            "type": "hello",
            "protocol": PROTOCOL_VERSION,
            "host": socket.gethostname(),
            "root": str(self.runner.policy.root),
            "allow": sorted(self.runner.policy.allow),
            "max_runs": self.config.max_runs,
            "active": self.runner.active,
            "claude": self._claude_version,
        }

    def _dispatch(self, message: Message, send: Send, followers: set[asyncio.Task[None]]) -> None:
        if message.get("type") == "cancel":
            self.runner.cancel(str(message["run_id"]))
            return
        if message.get("type") != "start":
            return
        run = self.runner.ensure(
            str(message["run_id"]),
            list(message["command"]),
            str(message["cwd"]),
            Setup.of(message),
        )
        task = asyncio.create_task(run.follow(int(message.get("after", 0)), send))
        followers.add(task)
        task.add_done_callback(followers.discard)


async def _version(executable: str) -> str:
    try:
        process = await asyncio.create_subprocess_exec(
            executable,
            "--version",
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.DEVNULL,
        )
        output, _ = await asyncio.wait_for(process.communicate(), 20)
    except (OSError, asyncio.TimeoutError):
        return "not found"
    return output.decode().strip().splitlines()[0] if output.strip() else "unknown"


def run_daemon(config: DaemonConfig) -> None:
    """Blocking entry point; stops cleanly on SIGINT/SIGTERM."""

    async def main() -> None:
        task = asyncio.current_task()
        loop = asyncio.get_running_loop()
        for sig in (signal.SIGINT, signal.SIGTERM):
            if task is not None:
                loop.add_signal_handler(sig, task.cancel)
        with suppress(asyncio.CancelledError):
            await Daemon(config).serve_forever()

    asyncio.run(main())
