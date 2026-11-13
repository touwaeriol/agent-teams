from __future__ import annotations

import io
from collections.abc import Iterator
from pathlib import Path

import httpx2 as httpx
import pytest
from anthropic import AuthenticationError

from agent_teams import Agent, ScriptedLLM, Sequential, cli
from agent_teams.llm import say
from agent_teams.runnable import Runnable
from agent_teams.types import AssistantTurn, Reply

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


async def test_converse_keeps_context_and_handles_commands() -> None:
    refusal = Reply(AssistantTurn(""), stop="refusal")
    lead = Agent("lead", ScriptedLLM([say("hello boss"), refusal, say("done")]))
    session = lead.session()
    out: list[str] = []

    await cli.converse(
        session, lines("hi", "", "bad", "/reset", "work", "/exit", "never"), out.append
    )

    assert out[1] == "lead: hello boss"
    assert out[2].startswith("lead: [error]")
    assert out[3] == "(history cleared)"
    assert out[4] == "lead: done"
    assert len(out) == 5
    assert len(session.history) == 2


def test_chat_command_runs_a_session(
    monkeypatch: pytest.MonkeyPatch, capsys: pytest.CaptureFixture[str]
) -> None:
    monkeypatch.setattr(cli, "load_team", scripted_team)
    monkeypatch.setattr(cli, "_prompt", lines("hi"))
    assert cli.main(["chat", "team.yaml"]) == 0
    assert "bot: final answer" in capsys.readouterr().out


def test_chat_requires_an_agent_at_the_top(
    monkeypatch: pytest.MonkeyPatch, capsys: pytest.CaptureFixture[str]
) -> None:
    team = Sequential("pipe", [Agent("a", ScriptedLLM([]))])
    monkeypatch.setattr(cli, "load_team", lambda _path: team)
    assert cli.main(["chat", "team.yaml"]) == 1
    assert "supervisor" in capsys.readouterr().err


def test_auth_errors_explain_how_to_sign_in() -> None:
    request = httpx.Request("POST", "https://api.anthropic.com/v1/messages")
    response = httpx.Response(401, request=request)
    error = AuthenticationError("invalid x-api-key", response=response, body=None)
    assert "ant auth login" in cli.describe_error(error)
