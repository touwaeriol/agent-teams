from __future__ import annotations

import json
import sys
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

import pytest

from agent_teams import CodexWorker, Hooks, RunContext, WorkerProgress
from agent_teams.codex import CodexError, describe
from agent_teams.config import build_team, parse_team
from agent_teams.errors import ConfigError
from agent_teams.leader import Leader
from agent_teams.mcp_config import ToolEndpoint
from agent_teams.nodes import LocalNode, NodeCommandError
from agent_teams.remote import NodeRegistry
from agent_teams.server.accounts import MemoryAccounts
from agent_teams.server.models import NodeView
from agent_teams.server.workspaces import Workspaces

FAKE = f"{sys.executable} {Path(__file__).with_name('fake_codex.py')}"
ENDPOINT = ToolEndpoint("http://console/mcp", "s3cret", ("report_progress", "approve"))


def worker(tmp_path: Path, **options: Any) -> CodexWorker:
    options.setdefault("codex_command", FAKE)
    return CodexWorker("coder", LocalNode("here", str(tmp_path)), **options)


def payload(output: str) -> dict[str, Any]:
    data: dict[str, Any] = json.loads(output)
    return data


def test_command_is_non_interactive_json_with_the_prompt_last(tmp_path: Path) -> None:
    command = worker(tmp_path, model="gpt-x", extra_args=["--ephemeral"]).command("-do it")

    assert command[2:6] == ["exec", "--json", "--skip-git-repo-check", "--sandbox"]
    assert command[command.index("--sandbox") + 1] == "workspace-write"
    assert command[command.index("--model") + 1] == "gpt-x"
    assert command[-3:] == ["--ephemeral", "--", "-do it"]
    assert "-c" not in command  # no instructions, no tools


def test_instructions_become_a_toml_string_override(tmp_path: Path) -> None:
    command = worker(tmp_path, instructions='Say "hi"\n中文').command("x")

    assert command[command.index("-c") + 1] == 'developer_instructions="Say \\"hi\\"\\n中文"'


def test_with_tools_adds_the_mcp_server_and_hides_the_approval_tool(tmp_path: Path) -> None:
    plain = worker(tmp_path)
    equipped = plain.with_tools(ENDPOINT, "approve")

    overrides = [
        equipped.command("x")[i + 1] for i, a in enumerate(equipped.command("x")) if a == "-c"
    ]
    assert overrides == [
        'mcp_servers.team.url="http://console/mcp"',
        'mcp_servers.team.http_headers={"Authorization" = "Bearer s3cret"}',
        "mcp_servers.team.tool_timeout_sec=86400",
        'mcp_servers.team.disabled_tools=["approve"]',
    ]
    assert plain.endpoint is None  # a copy, the original is untouched
    assert "disabled_tools" not in " ".join(plain.with_tools(ENDPOINT).command("x"))


async def test_runs_codex_and_returns_the_last_message(tmp_path: Path) -> None:
    progress: list[str] = []
    hooks = Hooks()
    hooks.on(WorkerProgress, lambda e: progress.append(f"{e.source} {e.text}"))

    result = await worker(tmp_path).run("add a test", RunContext(hooks, ("lead",)))

    data = payload(result.output)
    assert data["cwd"] == str(tmp_path / "coder")
    assert data["argv"][-2:] == ["--", "add a test"]
    assert progress[:3] == [
        "lead/coder thread th-1",
        "lead/coder $ /bin/bash -lc ls",
        "lead/coder not json noise",
    ]
    assert "lead/coder Looking at the repo." in progress
    assert progress[-1] == "lead/coder finished: 30 in / 15 out tokens"


async def test_overrides_reach_the_cli(tmp_path: Path) -> None:
    result = await worker(tmp_path, instructions="be brief").with_tools(ENDPOINT).run("x")

    config = payload(result.output)["config"]
    assert config["developer_instructions"] == "be brief"
    assert config["mcp_servers.team.http_headers"] == {"Authorization": "Bearer s3cret"}


async def test_a_failed_turn_raises_its_message(tmp_path: Path) -> None:
    progress: list[str] = []
    hooks = Hooks()
    hooks.on(WorkerProgress, lambda e: progress.append(e.text))

    with pytest.raises(CodexError, match="coder: 401 Unauthorized"):
        await worker(tmp_path).run("fail", RunContext(hooks))
    assert "error: Reconnecting... 1/5 (401 Unauthorized)" in progress


async def test_a_crash_without_events_raises_the_exit_status(tmp_path: Path) -> None:
    with pytest.raises(NodeCommandError, match="exit 3: boom on stderr"):
        await worker(tmp_path).run("crash")


async def test_exiting_cleanly_without_a_finished_turn_is_an_error(tmp_path: Path) -> None:
    script = tmp_path / "quiet.py"
    script.write_text('print(\'{"type": "thread.started", "thread_id": "t"}\')\n')
    quiet = worker(tmp_path, codex_command=f"{sys.executable} {script}")

    with pytest.raises(CodexError, match="without finishing the turn"):
        await quiet.run("x")


@pytest.mark.parametrize(
    ("event", "lines"),
    [
        ({"type": "turn.started"}, []),
        ({"type": "turn.failed", "error": {"message": "nope"}}, ["failed: nope"]),
        (
            {"type": "item.completed", "item": {"type": "error", "message": "slow model"}},
            ["warning: slow model"],
        ),
        (
            {
                "type": "item.completed",
                "item": {
                    "type": "command_execution",
                    "status": "failed",
                    "exit_code": 1,
                    "aggregated_output": "bwrap: denied\n",
                },
            },
            ["command failed (exit 1): bwrap: denied"],
        ),
        (
            {
                "type": "item.started",
                "item": {
                    "type": "mcp_tool_call",
                    "server": "team",
                    "tool": "ask_lead",
                    "arguments": {"question": "?"},
                },
            },
            ['team.ask_lead {"question": "?"}'],
        ),
        (
            {
                "type": "item.completed",
                "item": {"type": "mcp_tool_call", "tool": "ask_lead", "error": {"message": "gone"}},
            },
            ["ask_lead failed: {'message': 'gone'}"],
        ),
        (
            {"type": "item.completed", "item": {"type": "file_change", "changes": [{}, {}]}},
            ["changed 2 file(s)"],
        ),
        ({"type": "item.completed", "item": {"type": "todo_list"}}, []),
        ({"type": "something.new"}, []),
    ],
)
def test_describe_events(event: dict[str, Any], lines: list[str]) -> None:
    assert describe(event) == lines


def test_config_builds_codex_employees(tmp_path: Path) -> None:
    team = build_team(
        {
            "nodes": [{"name": "here", "local": True, "workdir": str(tmp_path)}],
            "leader": "lead",
            "employees": [
                {"name": "lead", "type": "claude_code", "node": "here"},
                {
                    "name": "cx",
                    "type": "codex",
                    "node": "here",
                    "sandbox": "read-only",
                    "can_delegate": True,
                },
            ],
        }
    )

    assert isinstance(team, Leader)
    member = team.members[0]
    assert isinstance(member, CodexWorker)
    assert (member.sandbox, member.can_delegate, member.node.name) == ("read-only", True, "here")


def test_config_rejects_bad_sandbox_and_codex_leaders() -> None:
    with pytest.raises(ConfigError, match="sandbox"):
        parse_team({"team": {"name": "cx", "type": "codex", "node": "n", "sandbox": "yolo"}})
    with pytest.raises(ConfigError, match="must be an agent or claude_code"):
        parse_team({"leader": "cx", "employees": [{"name": "cx", "type": "codex", "node": "n"}]})


PLATFORM_TEAM = """\
leader: lead
employees:
  - {name: lead, type: %s, node: box}
  - {name: cx, type: codex, node: box}
"""


def test_platform_allows_codex_employees_but_not_a_codex_leader() -> None:
    spaces = Workspaces(MemoryAccounts(), NodeRegistry())
    nodes = [NodeView(id="n1", name="box", created_at=datetime.now(timezone.utc))]

    lead = spaces.build(PLATFORM_TEAM % "claude_code", nodes)
    assert isinstance(lead.members[0], CodexWorker)
    assert lead.members[0].node.resumable  # a daemon node
    with pytest.raises(ConfigError):
        spaces.build(PLATFORM_TEAM % "codex", nodes)
