"""Command line: ``agent-teams run team.yaml "task"``."""

from __future__ import annotations

import argparse
import asyncio
import sys
from collections.abc import Sequence

from .config import load_team
from .context import RunContext
from .errors import AgentTeamsError
from .hooks import Hooks, trace


def main(argv: Sequence[str] | None = None) -> int:
    args = _parser().parse_args(argv)
    try:
        return int(args.handler(args))
    except AgentTeamsError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1


def _parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(prog="agent-teams", description=__doc__)
    commands = parser.add_subparsers(required=True)

    run = commands.add_parser("run", help="run a team on a task")
    run.add_argument("team", help="path to a YAML/JSON team definition")
    run.add_argument("task", help="the task text, or '-' to read it from stdin")
    run.add_argument("-v", "--verbose", action="store_true", help="trace every step to stderr")
    run.set_defaults(handler=_run)

    check = commands.add_parser("check", help="validate a team definition")
    check.add_argument("team")
    check.set_defaults(handler=_check)
    return parser


def _run(args: argparse.Namespace) -> int:
    team = load_team(args.team)
    task = sys.stdin.read() if args.task == "-" else args.task
    hooks = trace(Hooks(), _stderr) if args.verbose else Hooks()
    result = asyncio.run(team.run(task, RunContext(hooks)))
    print(result.output)
    return 0


def _check(args: argparse.Namespace) -> int:
    team = load_team(args.team)
    print(f"ok: {team.name}")
    return 0


def _stderr(line: str) -> None:
    print(line, file=sys.stderr)
