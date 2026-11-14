"""Command line.

agent-teams chat team.yaml          # talk to the team lead interactively
agent-teams run team.yaml "task"    # one-shot run
agent-teams check team.yaml         # validate a definition
agent-teams nodes team.yaml         # check SSH/local nodes
agent-teams serve                   # the multi-user web service ([server] extra)
agent-teams node --console URL      # the node daemon, on each worker ([node] extra)
"""

from __future__ import annotations

import argparse
import asyncio
import logging
import os
import socket
import sys
from collections.abc import Callable, Sequence
from pathlib import Path

import anthropic

from .agent import Agent
from .config import load_nodes, load_team
from .context import RunContext
from .conversation import Conversation
from .errors import AgentTeamsError, ConfigError, describe_error
from .hooks import Hooks, trace
from .leader import Leader
from .nodes import Node, probe

EXIT_COMMANDS = frozenset({"/exit", "/quit"})

Reader = Callable[[], str | None]
Writer = Callable[[str], None]


def main(argv: Sequence[str] | None = None) -> int:
    args = _parser().parse_args(argv)
    # Like uvicorn: tool references ("module:attr") resolve from the current directory.
    if "" not in sys.path and os.getcwd() not in sys.path:
        sys.path.insert(0, os.getcwd())
    try:
        return int(args.handler(args))
    except (AgentTeamsError, anthropic.AnthropicError) as exc:
        print(f"error: {describe_error(exc)}", file=sys.stderr)
        return 1


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

    serve = commands.add_parser("serve", help="run the web service (needs the [server] extra)")
    serve.add_argument("--host", default=os.environ.get("HOST", "0.0.0.0"))
    serve.add_argument("--port", type=int, default=int(os.environ.get("PORT", "8000")))
    serve.set_defaults(handler=_serve)

    daemon = commands.add_parser("node", help="run the node daemon on a worker machine")
    daemon.add_argument(
        "--console",
        default=os.environ.get("AGENT_TEAMS_CONSOLE"),
        required=not os.environ.get("AGENT_TEAMS_CONSOLE"),
        help="console URL, e.g. http://console:3001",
    )
    daemon.add_argument(
        "--name",
        default=os.environ.get("AGENT_TEAMS_NODE", socket.gethostname()),
        help="shown in logs; the console identifies the node by its token",
    )
    daemon.add_argument(
        "--root",
        default=os.environ.get("AGENT_TEAMS_ROOT", os.path.expanduser("~/agent-teams")),
        help="employees work only inside this directory",
    )
    daemon.add_argument(
        "--allow",
        action="append",
        help="executable employees may run (repeatable; default: claude, codex)",
    )
    daemon.add_argument(
        "--max-runs", type=int, default=int(os.environ.get("AGENT_TEAMS_MAX_RUNS", "4"))
    )
    daemon.add_argument(
        "--state",
        default=os.environ.get(
            "AGENT_TEAMS_STATE", os.path.expanduser("~/.local/state/agent-teams")
        ),
    )
    daemon.set_defaults(handler=_daemon)

    nodes = commands.add_parser(
        "nodes", help="check SSH/local nodes: reachable and claude installed"
    )
    nodes.add_argument("team")
    nodes.set_defaults(handler=_nodes)

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


def _serve(args: argparse.Namespace) -> int:
    # Imported here: the web stack is an optional extra.
    import uvicorn  # noqa: PLC0415

    from .server.app import Settings, create_app  # noqa: PLC0415

    settings = Settings.from_env()
    uvicorn.run(create_app(settings.services), host=args.host, port=args.port)
    return 0


def _daemon(args: argparse.Namespace) -> int:
    from .daemon import DaemonConfig, run_daemon  # noqa: PLC0415 - optional [node] extra

    token = os.environ.get("AGENT_TEAMS_NODE_TOKEN", "")
    if not token:
        raise ConfigError("set AGENT_TEAMS_NODE_TOKEN (shown when you add the node in the console)")
    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
    config = DaemonConfig(
        console=args.console,
        name=args.name,
        token=token,
        root=Path(args.root),
        state=Path(args.state),
        allow=frozenset(args.allow or ("claude", "codex")),
        max_runs=args.max_runs,
    )
    run_daemon(config)
    return 0


def _nodes(args: argparse.Namespace) -> int:
    nodes = load_nodes(args.team)
    if not nodes:
        print("no SSH or local nodes defined (daemon nodes are managed in the web console)")
        return 0
    results = asyncio.run(_probe_all(nodes))
    for node, outcome in zip(nodes, results, strict=True):
        print(f"{node.name}: {outcome}")
    return 0 if all(r.startswith("ok") for r in results) else 1


async def _probe_all(nodes: Sequence[Node]) -> list[str]:
    return list(await asyncio.gather(*(_probe_one(n) for n in nodes)))


async def _probe_one(node: Node) -> str:
    try:
        return f"ok ({await probe(node)})"
    except (AgentTeamsError, OSError) as exc:
        return f"FAILED {describe_error(exc)}"


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
