"""Command line.

agent-teams chat team.yaml          # talk to the team lead interactively
agent-teams run team.yaml "task"    # one-shot run
agent-teams check team.yaml         # validate a definition
"""

from __future__ import annotations

import argparse
import asyncio
import sys
from collections.abc import Callable, Sequence

import anthropic

from .agent import Agent, Session
from .config import load_team
from .context import RunContext
from .errors import AgentTeamsError, ConfigError
from .hooks import Hooks, trace

EXIT_COMMANDS = frozenset({"/exit", "/quit"})
AUTH_HINT = "set ANTHROPIC_API_KEY, or run `ant auth login` once to sign in"

Reader = Callable[[], str | None]
Writer = Callable[[str], None]


def main(argv: Sequence[str] | None = None) -> int:
    args = _parser().parse_args(argv)
    try:
        return int(args.handler(args))
    except (AgentTeamsError, anthropic.AnthropicError) as exc:
        print(f"error: {describe_error(exc)}", file=sys.stderr)
        return 1


def describe_error(exc: Exception) -> str:
    if isinstance(exc, anthropic.AuthenticationError):
        return f"Claude rejected the credentials ({exc}); {AUTH_HINT}"
    return str(exc)


async def converse(session: Session, read: Reader, write: Writer) -> None:
    """A chat loop: ``/reset`` clears the history, ``/exit`` or EOF quits."""
    write(f"Talking to {session.agent.name}. /reset clears history, /exit quits.")
    while (line := await asyncio.to_thread(read)) is not None:
        message = line.strip()
        if message in EXIT_COMMANDS:
            return
        await _handle(session, message, write)


async def _handle(session: Session, message: str, write: Writer) -> None:
    if message == "/reset":
        session.reset()
        write("(history cleared)")
        return
    if message:
        write(f"{session.agent.name}: {await _reply(session, message)}")


async def _reply(session: Session, message: str) -> str:
    try:
        return (await session.send(message)).output
    except (AgentTeamsError, anthropic.AnthropicError) as exc:
        return f"[error] {describe_error(exc)}"


def _parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="agent-teams", description=__doc__, formatter_class=argparse.RawTextHelpFormatter
    )
    commands = parser.add_subparsers(required=True)

    chat = commands.add_parser("chat", help="talk to the team lead interactively")
    chat.add_argument("team", help="path to a YAML/JSON team definition")
    chat.add_argument("-v", "--verbose", action="store_true", help="trace every step to stderr")
    chat.set_defaults(handler=_chat)

    run = commands.add_parser("run", help="run a team on a task")
    run.add_argument("team", help="path to a YAML/JSON team definition")
    run.add_argument("task", help="the task text, or '-' to read it from stdin")
    run.add_argument("-v", "--verbose", action="store_true", help="trace every step to stderr")
    run.set_defaults(handler=_run)

    check = commands.add_parser("check", help="validate a team definition")
    check.add_argument("team")
    check.set_defaults(handler=_check)
    return parser


def _chat(args: argparse.Namespace) -> int:
    lead = load_team(args.team)
    if not isinstance(lead, Agent):
        raise ConfigError("chat needs an agent or a supervisor at the top of the team file")
    asyncio.run(converse(lead.session(_context(args.verbose)), _prompt, print))
    return 0


def _run(args: argparse.Namespace) -> int:
    team = load_team(args.team)
    task = sys.stdin.read() if args.task == "-" else args.task
    result = asyncio.run(team.run(task, _context(args.verbose)))
    print(result.output)
    return 0


def _check(args: argparse.Namespace) -> int:
    team = load_team(args.team)
    print(f"ok: {team.name}")
    return 0


def _context(verbose: bool) -> RunContext:
    return RunContext(trace(Hooks(), _stderr) if verbose else Hooks())


def _prompt() -> str | None:
    try:
        return input("you> ")
    except EOFError:
        return None


def _stderr(line: str) -> None:
    print(line, file=sys.stderr)
