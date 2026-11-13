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

from .agent import Agent
from .config import load_team
from .context import RunContext
from .conversation import Conversation
from .errors import AgentTeamsError, ConfigError
from .hooks import Hooks, trace
from .leader import Leader

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


async def converse(conversation: Conversation, read: Reader, write: Writer) -> None:
    """A non-blocking chat loop.

    Typing never waits for the leader: messages queue up while replies (and
    reports from background tasks) are printed as they arrive. ``/tasks`` shows
    the task board, ``/reset`` clears the history, ``/exit`` quits at once and
    EOF quits after all pending work is finished.
    """
    write(f"Talking to {conversation.name}. /tasks shows progress, /reset clears, /exit quits.")
    responder = asyncio.create_task(_respond_forever(conversation, write))
    try:
        if await _read_messages(conversation, read, write):
            await conversation.drain()
    finally:
        responder.cancel()
        conversation.close()


async def _read_messages(conversation: Conversation, read: Reader, write: Writer) -> bool:
    """Feed input lines to the conversation; True on EOF, False on /exit."""
    while (line := await asyncio.to_thread(read)) is not None:
        message = line.strip()
        if message in EXIT_COMMANDS:
            return False
        _handle(conversation, message, write)
    return True


def _handle(conversation: Conversation, message: str, write: Writer) -> None:
    command = _COMMANDS.get(message)
    if command is not None:
        write(command(conversation))
        return
    if message:
        conversation.post(message)


def _reset(conversation: Conversation) -> str:
    conversation.reset()
    return "(history cleared)"


_COMMANDS: dict[str, Callable[[Conversation], str]] = {
    "/tasks": Conversation.status,
    "/reset": _reset,
}


async def _respond_forever(conversation: Conversation, write: Writer) -> None:
    while True:
        write(f"{conversation.name}: {await _reply(conversation)}")


async def _reply(conversation: Conversation) -> str:
    try:
        return (await conversation.reply()).output
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
    if not isinstance(lead, (Leader, Agent)):
        raise ConfigError("chat needs a leader or an agent at the top of the team file")
    asyncio.run(_chat_with(lead, _context(args.verbose)))
    return 0


async def _chat_with(lead: Leader | Agent, ctx: RunContext) -> None:
    await converse(lead.open(ctx), _prompt, print)


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
