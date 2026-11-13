from __future__ import annotations

import io
from pathlib import Path

import pytest

from agent_teams import Agent, ScriptedLLM, cli
from agent_teams.runnable import Runnable

EXAMPLE = Path(__file__).resolve().parents[1] / "examples" / "writing_team.yaml"


def scripted_team(_path: str) -> Runnable:
    return Agent("bot", ScriptedLLM(["final answer"]))


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


def test_check_validates_files(capsys: pytest.CaptureFixture[str]) -> None:
    assert cli.main(["check", str(EXAMPLE)]) == 0
    assert capsys.readouterr().out.startswith("ok: ")


def test_errors_are_reported(tmp_path: Path, capsys: pytest.CaptureFixture[str]) -> None:
    bad = tmp_path / "bad.yaml"
    bad.write_text("team: {type: nope, name: x}\n", encoding="utf-8")
    assert cli.main(["check", str(bad)]) == 1
    assert "error:" in capsys.readouterr().err
