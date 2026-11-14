from __future__ import annotations

import sys
from collections.abc import Mapping
from pathlib import Path
from typing import Any

import pytest
import yaml

from agent_teams import Agent, ConfigError, Leader, Parallel, RoundRobin, ScriptedLLM, Sequential
from agent_teams.claude_code import ClaudeCodeWorker
from agent_teams.config import build_team, load_nodes, load_team, load_tool
from agent_teams.llm.base import LLM
from agent_teams.nodes import LocalNode, SSHNode
from agent_teams.remote import NodeRegistry, RemoteNode, node_token

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
                    {"type": "leader", "name": "boss", "members": [{"name": "g"}], "max_turns": 5},
                ],
            },
        },
        llm_factory=factory,
    )

    assert isinstance(team, Sequential)
    a, fan, chat, lead, boss = team.members
    assert isinstance(a, Agent)
    assert [s.name for s in a.toolkit.specs] == ["word_count"]
    assert isinstance(fan, Parallel)
    assert fan.aggregator is not None
    assert isinstance(chat, RoundRobin)
    assert chat.rounds == 3
    assert isinstance(lead, Agent)
    assert [s.name for s in lead.toolkit.specs] == ["f"]
    assert isinstance(boss, Leader)
    assert boss.max_turns == 5
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


def test_leader_shorthand_makes_a_leader_over_other_nodes() -> None:
    team = build_team(
        {
            "leader": "lead",
            "employees": [
                {"name": "researcher", "description": "Collects facts."},
                {"name": "lead", "instructions": "Run the team."},
                {"type": "parallel", "name": "reviewers", "members": [{"name": "r1"}]},
            ],
        },
        llm_factory=RecordingFactory(),
    )

    assert isinstance(team, Leader)
    assert team.name == "lead"
    assert team.instructions == "Run the team."
    assert [m.name for m in team.members] == ["researcher", "reviewers"]


def test_leader_without_instructions_gets_default_lead_prompt() -> None:
    team = build_team(
        {"leader": "lead", "employees": [{"name": "lead"}, {"name": "worker"}]},
        llm_factory=RecordingFactory(),
    )
    assert isinstance(team, Leader)
    assert "assign_task" in team.instructions


@pytest.mark.parametrize(
    ("data", "fragment"),
    [
        (
            {"leader": "boss", "employees": [{"name": "a"}, {"name": "b"}]},
            "not one of the employees",
        ),
        (
            {
                "leader": "t",
                "employees": [{"type": "sequential", "name": "t", "members": [{"name": "a"}]}],
            },
            "must be an agent",
        ),
        ({"leader": "a", "employees": [{"name": "a"}]}, "members"),
        (
            {"leader": "a", "employees": [{"name": "a"}, {"name": "b"}], "team": {"name": "x"}},
            "not both",
        ),
    ],
)
def test_invalid_leader_definitions(data: dict[str, Any], fragment: str) -> None:
    with pytest.raises(ConfigError, match=fragment):
        build_team(data, llm_factory=RecordingFactory())


FAKE_CLAUDE = f"{sys.executable} {Path(__file__).with_name('fake_claude.py')}"


def multi_node_team(tmp_path: Path) -> dict[str, Any]:
    return {
        "nodes": [
            {"name": "build-1", "host": "10.0.0.11", "user": "dev", "workdir": "/srv/app"},
            {"name": "here", "local": True, "workdir": str(tmp_path)},
        ],
        "leader": "lead",
        "employees": [
            {"name": "lead"},
            {
                "name": "backend",
                "type": "claude_code",
                "node": "build-1",
                "description": "Backend engineer.",
                "allowed_tools": ["Bash(pytest *)"],
                "permission_mode": "dontAsk",
            },
            {
                "name": "frontend",
                "type": "claude_code",
                "node": "here",
                "claude_command": FAKE_CLAUDE,
            },
        ],
    }


def test_claude_code_employees_run_on_their_nodes(tmp_path: Path) -> None:
    team = build_team(multi_node_team(tmp_path), llm_factory=RecordingFactory())

    assert isinstance(team, Leader)
    backend, frontend = team.members
    assert isinstance(backend, ClaudeCodeWorker)
    assert isinstance(backend.node, SSHNode)
    assert backend.node.destination == "dev@10.0.0.11"
    assert backend.permission_mode == "dontAsk"
    assert backend.description == "Backend engineer."
    assert isinstance(frontend, ClaudeCodeWorker)
    assert isinstance(frontend.node, LocalNode)
    assert frontend.description == "Claude Code on node here"


@pytest.mark.parametrize(
    ("change", "fragment"),
    [
        ({"node": "nowhere"}, "unknown node 'nowhere'"),
        ({"permission_mode": "yolo"}, "permission_mode"),
    ],
)
def test_invalid_claude_code_employees(
    tmp_path: Path, change: dict[str, Any], fragment: str
) -> None:
    data = multi_node_team(tmp_path)
    data["employees"][1].update(change)
    with pytest.raises(ConfigError, match=fragment):
        build_team(data, llm_factory=RecordingFactory())


@pytest.mark.parametrize(
    ("nodes", "fragment"),
    [
        ([{"name": "x", "host": "h", "local": True}], "cannot have both"),
        ([{"name": "x", "local": True}, {"name": "x", "local": True}], "unique"),
    ],
)
def test_invalid_nodes(nodes: list[dict[str, Any]], fragment: str) -> None:
    with pytest.raises(ConfigError, match=fragment):
        build_team({"nodes": nodes, "team": {"name": "a"}}, llm_factory=RecordingFactory())


def test_load_nodes(tmp_path: Path) -> None:
    path = tmp_path / "team.yaml"
    path.write_text(yaml.safe_dump(multi_node_team(tmp_path)), encoding="utf-8")
    assert [n.name for n in load_nodes(path)] == ["build-1", "here"]


def test_nodes_without_host_are_daemon_nodes() -> None:
    registry = NodeRegistry("secret")
    team = build_team(
        {
            "nodes": [{"name": "gpu-box", "workdir": "agents", "revision": 2}],
            "leader": "lead",
            "employees": [
                {"name": "lead"},
                {"name": "coder", "type": "claude_code", "node": "gpu-box"},
            ],
        },
        llm_factory=RecordingFactory(),
        registry=registry,
    )
    assert isinstance(team, Leader)
    (coder,) = team.members
    assert isinstance(coder, ClaudeCodeWorker)
    assert isinstance(coder.node, RemoteNode)
    assert coder.resumable
    assert coder.directory == "agents/coder"
    assert registry.verify("gpu-box", node_token("secret", "gpu-box", 2))
