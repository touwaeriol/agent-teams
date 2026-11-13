from __future__ import annotations

from collections.abc import Mapping
from pathlib import Path
from typing import Any

import pytest

from agent_teams import Agent, ConfigError, Parallel, RoundRobin, ScriptedLLM, Sequential
from agent_teams.config import build_team, load_team, load_tool
from agent_teams.llm.base import LLM

EXAMPLES = Path(__file__).resolve().parents[1] / "examples"


class RecordingFactory:
    def __init__(self) -> None:
        self.options: list[dict[str, Any]] = []

    def __call__(self, options: Mapping[str, Any]) -> LLM:
        self.options.append(dict(options))
        return ScriptedLLM(["ok"])


def test_builds_nested_team_with_shared_llms() -> None:
    factory = RecordingFactory()
    team = build_team(
        {
            "llm": {"model": "m1"},
            "team": {
                "type": "sequential",
                "name": "pipe",
                "members": [
                    {"name": "a", "tools": ["tests.sample_tools:word_count"]},
                    {
                        "type": "parallel",
                        "name": "fan",
                        "members": [{"name": "b"}, {"name": "c", "llm": {"effort": "low"}}],
                        "aggregator": {"name": "judge"},
                    },
                    {
                        "type": "round_robin",
                        "name": "chat",
                        "rounds": 3,
                        "members": [{"name": "d"}],
                        "finalizer": {"name": "e"},
                    },
                    {"type": "supervisor", "name": "lead", "members": [{"name": "f"}]},
                ],
            },
        },
        llm_factory=factory,
    )

    assert isinstance(team, Sequential)
    a, fan, chat, lead = team.members
    assert isinstance(a, Agent)
    assert [s.name for s in a.toolkit.specs] == ["word_count"]
    assert isinstance(fan, Parallel)
    assert fan.aggregator is not None
    assert isinstance(chat, RoundRobin)
    assert chat.rounds == 3
    assert isinstance(lead, Agent)
    assert [s.name for s in lead.toolkit.specs] == ["f"]
    assert factory.options == [{"model": "m1"}, {"model": "m1", "effort": "low"}]


@pytest.mark.parametrize(
    "team",
    [
        {"type": "unknown", "name": "x"},
        {"name": "x", "surprise": True},
        {"type": "sequential", "name": "x", "members": []},
        {"type": "round_robin", "name": "x", "members": [{"name": "a"}], "rounds": 0},
        {"instructions": "no name"},
    ],
)
def test_invalid_specs_raise_config_error(team: dict[str, Any]) -> None:
    with pytest.raises(ConfigError):
        build_team({"team": team}, llm_factory=RecordingFactory())


def test_bad_llm_options_raise_config_error() -> None:
    with pytest.raises(ConfigError, match="llm options"):
        build_team({"llm": {"bogus": 1}, "team": {"name": "a"}})


def test_load_tool_accepts_functions_and_tools() -> None:
    assert load_tool("tests.sample_tools:word_count").name == "word_count"
    assert load_tool("tests.sample_tools:echo").name == "echo_tool"


@pytest.mark.parametrize(
    "ref",
    [
        "tests.sample_tools",
        "tests.sample_tools:missing",
        "no.such.module:x",
        "tests.sample_tools:NOT_CALLABLE",
    ],
)
def test_load_tool_rejects_bad_references(ref: str) -> None:
    with pytest.raises(ConfigError):
        load_tool(ref)


@pytest.mark.parametrize("path", sorted(EXAMPLES.glob("*.yaml")))
def test_example_files_are_valid(path: Path) -> None:
    assert load_team(path, llm_factory=RecordingFactory()).name


def test_load_team_rejects_non_mapping(tmp_path: Path) -> None:
    path = tmp_path / "team.yaml"
    path.write_text("- just a list\n", encoding="utf-8")
    with pytest.raises(ConfigError, match="mapping"):
        load_team(path)


def test_leader_shorthand_makes_a_supervisor_over_other_nodes() -> None:
    team = build_team(
        {
            "leader": "lead",
            "nodes": [
                {"name": "researcher", "description": "Collects facts."},
                {"name": "lead", "instructions": "Run the team."},
                {"type": "parallel", "name": "reviewers", "members": [{"name": "r1"}]},
            ],
        },
        llm_factory=RecordingFactory(),
    )

    assert isinstance(team, Agent)
    assert team.name == "lead"
    assert team.instructions.startswith("Run the team.")
    assert "researcher: Collects facts." in team.instructions
    assert [s.name for s in team.toolkit.specs] == ["researcher", "reviewers"]


def test_leader_without_instructions_gets_default_lead_prompt() -> None:
    team = build_team(
        {"leader": "lead", "nodes": [{"name": "lead"}, {"name": "worker"}]},
        llm_factory=RecordingFactory(),
    )
    assert isinstance(team, Agent)
    assert "break it into tasks" in team.instructions


@pytest.mark.parametrize(
    ("data", "fragment"),
    [
        ({"leader": "boss", "nodes": [{"name": "a"}, {"name": "b"}]}, "not one of the nodes"),
        (
            {
                "leader": "t",
                "nodes": [{"type": "sequential", "name": "t", "members": [{"name": "a"}]}],
            },
            "must be an agent",
        ),
        ({"leader": "a", "nodes": [{"name": "a"}]}, "members"),
        (
            {"leader": "a", "nodes": [{"name": "a"}, {"name": "b"}], "team": {"name": "x"}},
            "not both",
        ),
    ],
)
def test_invalid_leader_definitions(data: dict[str, Any], fragment: str) -> None:
    with pytest.raises(ConfigError, match=fragment):
        build_team(data, llm_factory=RecordingFactory())
