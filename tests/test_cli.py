from __future__ import annotations

import asyncio
import io
import threading
from collections.abc import Iterator
from pathlib import Path

import httpx2 as httpx
import pytest
from anthropic import AuthenticationError

from agent_teams import Agent, Leader, LocalNode, ScriptedLLM, Sequential, SSHNode, cli
from agent_teams.errors import describe_error
from agent_teams.llm import call, say
from agent_teams.runnable import Runnable
from agent_teams.types import AssistantTurn, Reply

from .fakes import Worker

EXAMPLES = Path(__file__).resolve().parents[1] / "examples"


def scripted_team(_path: str) -> Runnable:
    return Agent("bot", ScriptedLLM(["final answer"]))


def lines(*items: str) -> cli.Reader:
    feed: Iterator[str] = iter(items)
    return lambda: next(feed, None)


def test_run_prints_output(
    monkeypatch: pytest.MonkeyPatch, capsys: pytest.CaptureFixture[str]
) -> None:
    monkeypatch.setattr(cli, "load_team", scripted_team)
    assert cli.main(["run", "team.yaml", "do it", "--verbose"]) == 0
    captured = capsys.readouterr()
    assert captured.out.strip() == "final answer"
    assert "[bot] ▶ start: do it" in captured.err


def test_run_reads_task_from_stdin(
    monkeypatch: pytest.MonkeyPatch, capsys: pytest.CaptureFixture[str]
) -> None:
    monkeypatch.setattr(cli, "load_team", scripted_team)
    monkeypatch.setattr("sys.stdin", io.StringIO("from stdin"))
    assert cli.main(["run", "team.yaml", "-"]) == 0
    assert capsys.readouterr().out.strip() == "final answer"


@pytest.mark.parametrize("name", ["writing_team.yaml", "company.yaml"])
def test_check_validates_files(name: str, capsys: pytest.CaptureFixture[str]) -> None:
    assert cli.main(["check", str(EXAMPLES / name)]) == 0
    assert capsys.readouterr().out.startswith("ok: ")


def test_errors_are_reported(tmp_path: Path, capsys: pytest.CaptureFixture[str]) -> None:
    bad = tmp_path / "bad.yaml"
    bad.write_text("team: {type: nope, name: x}\n", encoding="utf-8")
    assert cli.main(["check", str(bad)]) == 1
    assert "error:" in capsys.readouterr().err


class Console:
    """Feeds each line only once ``after`` outputs exist; ``None`` means EOF."""

    def __init__(self, *steps: tuple[int, str | None]) -> None:
        self.steps = list(steps)
        self.out: list[str] = []
        self._changed = threading.Condition()

    def write(self, line: str) -> None:
        with self._changed:
            self.out.append(line)
            self._changed.notify_all()

    def read(self) -> str | None:
        if not self.steps:
            return None
        after, line = self.steps.pop(0)
        with self._changed:
            self._changed.wait_for(lambda: len(self.out) >= after, timeout=5)
        return line


def poem_leader(gate: asyncio.Event | None = None) -> Leader:
    llm = ScriptedLLM(
        [
            call("assign_task", {"assignee": "writer", "instruction": "poem"}),
            say("on it"),
            say("still working"),
            say("here is the poem"),
        ]
    )
    return Leader("lead", llm, [Worker("writer", gate)])


async def test_converse_keeps_context_and_handles_commands() -> None:
    refusal = Reply(AssistantTurn(""), stop="refusal")
    conversation = Agent("lead", ScriptedLLM([say("hello boss"), refusal, say("done")])).open()
    console = Console(
        (1, "hi"), (2, ""), (2, "bad"), (3, "/reset"), (4, "work"), (5, "/exit"), (5, "never")
    )

    await cli.converse(conversation, console.read, console.write)

    assert console.out[1:] == [
        "lead: hello boss",
        console.out[2],
        "(history cleared)",
        "lead: done",
    ]
    assert console.out[2].startswith("lead: [error]")
    assert len(conversation.session.snapshot()["history"]) == 2


async def test_leader_keeps_talking_while_tasks_run_and_eof_waits_for_them() -> None:
    gate = asyncio.Event()
    console = Console((1, "write a poem"), (2, "status?"), (3, None))

    def write(line: str) -> None:
        console.write(line)
        if line == "lead: still working":
            gate.set()

    await cli.converse(poem_leader(gate).open(), console.read, write)

    assert console.out[1:] == ["lead: on it", "lead: still working", "lead: here is the poem"]


async def test_tasks_command_shows_the_board() -> None:
    console = Console((1, "go"), (2, "/tasks"), (3, "/exit"))
    await cli.converse(poem_leader(asyncio.Event()).open(), console.read, console.write)
    assert console.out[2] == "t1 [running] writer: poem"


async def test_tasks_command_without_a_board() -> None:
    console = Console((1, "/tasks"), (2, "/exit"))
    await cli.converse(Agent("a", ScriptedLLM([])).open(), console.read, console.write)
    assert console.out[1] == "No task board."


def test_chat_command_runs_until_work_is_done(
    monkeypatch: pytest.MonkeyPatch, capsys: pytest.CaptureFixture[str]
) -> None:
    monkeypatch.setattr(cli, "load_team", lambda _path: poem_leader())
    monkeypatch.setattr(cli, "_prompt", lines("write a poem"))
    assert cli.main(["chat", "team.yaml"]) == 0
    out = capsys.readouterr().out
    assert "lead: on it" in out
    assert "lead: still working" in out


def test_chat_requires_a_leader_or_agent_at_the_top(
    monkeypatch: pytest.MonkeyPatch, capsys: pytest.CaptureFixture[str]
) -> None:
    team = Sequential("pipe", [Agent("a", ScriptedLLM([]))])
    monkeypatch.setattr(cli, "load_team", lambda _path: team)
    assert cli.main(["chat", "team.yaml"]) == 1
    assert "leader" in capsys.readouterr().err


def test_auth_errors_explain_how_to_sign_in() -> None:
    request = httpx.Request("POST", "https://api.anthropic.com/v1/messages")
    response = httpx.Response(401, request=request)
    error = AuthenticationError("invalid x-api-key", response=response, body=None)
    assert "ant auth login" in describe_error(error)


def test_nodes_command_reports_each_node(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch, capsys: pytest.CaptureFixture[str]
) -> None:
    good = LocalNode("good", str(tmp_path))
    bad = SSHNode("bad", "127.0.0.1", port=1, options=["-o", "ConnectTimeout=3"])
    monkeypatch.setattr(cli, "load_nodes", lambda _path: [good, bad])

    assert cli.main(["nodes", "team.yaml"]) == 1

    out = capsys.readouterr().out.splitlines()
    assert out[1].startswith("bad: FAILED")
    assert out[0].startswith("good: ")


def test_nodes_command_without_nodes(
    monkeypatch: pytest.MonkeyPatch, capsys: pytest.CaptureFixture[str]
) -> None:
    monkeypatch.setattr(cli, "load_nodes", lambda _path: [])
    assert cli.main(["nodes", "team.yaml"]) == 0
    assert capsys.readouterr().out.startswith("no SSH or local nodes defined")


def test_node_daemon_requires_its_token(
    monkeypatch: pytest.MonkeyPatch, capsys: pytest.CaptureFixture[str]
) -> None:
    monkeypatch.delenv("AGENT_TEAMS_NODE_TOKEN", raising=False)
    assert cli.main(["node", "--console", "http://c:3001", "--name", "n1"]) == 1
    assert "AGENT_TEAMS_NODE_TOKEN" in capsys.readouterr().err
