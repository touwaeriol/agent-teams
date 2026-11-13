from __future__ import annotations

import sys
from pathlib import Path

import pytest

from agent_teams.nodes import LocalNode, NodeCommandError, SSHNode, probe, remote_command

PY = sys.executable


async def test_local_node_streams_lines_in_workdir(tmp_path: Path) -> None:
    node = LocalNode("here", str(tmp_path))
    script = "import os; print('a'); print(os.getcwd())"
    lines = [line async for line in node.stream([PY, "-c", script])]
    assert lines == ["a", str(tmp_path)]


async def test_non_zero_exit_raises_with_stderr() -> None:
    node = LocalNode()
    script = "import sys; print('partial'); print('bad thing', file=sys.stderr); sys.exit(2)"
    seen: list[str] = []

    async def consume() -> None:
        async for line in node.stream([PY, "-c", script]):
            seen.append(line)

    with pytest.raises(NodeCommandError, match="exit 2: bad thing"):
        await consume()
    assert seen == ["partial"]


async def test_probe_returns_first_line() -> None:
    assert await probe(LocalNode(), [PY, "-c", "print('v1'); print('more')"]) == "v1"


def test_ssh_argv_quotes_the_remote_command() -> None:
    node = SSHNode(
        "b1", "10.0.0.5", user="dev", port=2222, identity_file="/k/id", workdir="/srv/my app"
    )
    argv, cwd = node.argv(["claude", "-p", "fix 'it' && rm -rf /"], node.workdir)

    assert cwd is None
    assert argv[:5] == ["ssh", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=accept-new"]
    assert argv[5:9] == ["-p", "2222", "-i", "/k/id"]
    assert argv[9] == "dev@10.0.0.5"
    assert argv[10] == "cd '/srv/my app' && exec claude -p 'fix '\"'\"'it'\"'\"' && rm -rf /'"


def test_ssh_defaults_use_ssh_config() -> None:
    argv, _ = SSHNode("b1", "build-box", options=["-o", "ConnectTimeout=5"]).argv(["true"], "")
    assert argv[-4:] == ["-o", "ConnectTimeout=5", "build-box", "exec true"]


def test_remote_command_without_workdir() -> None:
    assert remote_command(["echo", "a b"], "") == "exec echo 'a b'"
