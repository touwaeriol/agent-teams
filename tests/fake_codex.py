"""Stands in for the `codex` CLI in tests: prints JSONL like `codex exec --json`.

The event shapes copy a real `codex exec --json` run (codex-cli 0.169.0).
The prompt (the argument after `--`) selects the behaviour: "fail" -> a
failed turn and exit 1, "crash" -> exit 3 without events, "tool:<name>:<json
arguments>" -> calls that tool on the MCP server given by the
`-c mcp_servers.team.*` overrides and answers with the tool's reply,
anything else -> success. The default answer echoes argv, cwd and the
parsed `-c` overrides as JSON.
"""

import asyncio
import json
import os
import sys
from typing import Any

import httpx2
from mcp import ClientSession
from mcp.client.streamable_http import streamable_http_client

argv = sys.argv[1:]
prompt = argv[argv.index("--") + 1]


def toml_value(text: str) -> Any:
    """Enough TOML for the overrides CodexWorker writes."""
    if text.startswith("{"):
        return json.loads(text.replace(" = ", ": "))
    return json.loads(text)


def overrides() -> dict[str, Any]:
    pairs = [argv[i + 1] for i, arg in enumerate(argv[: argv.index("--")]) if arg == "-c"]
    return {key: toml_value(value) for key, value in (p.split("=", 1) for p in pairs)}


def emit(event: dict[str, Any]) -> None:
    print(json.dumps(event), flush=True)


def item(kind: str, number: int, **fields: Any) -> dict[str, Any]:
    return {"id": f"item_{number}", "type": kind, **fields}


async def call_mcp_tool(name: str, arguments: dict[str, Any]) -> str:
    config = overrides()
    headers = config["mcp_servers.team.http_headers"]
    async with (
        httpx2.AsyncClient(headers=headers) as http,
        streamable_http_client(config["mcp_servers.team.url"], http_client=http) as (read, write),
        ClientSession(read, write) as session,
    ):
        await session.initialize()
        result = await session.call_tool(name, arguments)
        return str(result.content[0].text)  # type: ignore[union-attr]


def finish(text: str) -> None:
    emit({"type": "item.completed", "item": item("agent_message", 9, text=text)})
    emit({"type": "turn.completed", "usage": {"input_tokens": 30, "output_tokens": 15}})
    sys.exit(0)


if prompt == "crash":
    print("boom on stderr", file=sys.stderr)
    sys.exit(3)

emit({"type": "thread.started", "thread_id": "th-1"})
emit({"type": "turn.started"})
command = item("command_execution", 1, command="/bin/bash -lc ls", aggregated_output="")
emit({"type": "item.started", "item": {**command, "exit_code": None, "status": "in_progress"}})
emit({"type": "item.completed", "item": {**command, "exit_code": 0, "status": "completed"}})
print("not json noise", flush=True)

if prompt == "fail":
    emit({"type": "error", "message": "Reconnecting... 1/5 (401 Unauthorized)"})
    emit({"type": "turn.failed", "error": {"message": "401 Unauthorized"}})
    sys.exit(1)
if prompt.startswith("tool:"):
    _, name, arguments = prompt.split(":", 2)
    call = item("mcp_tool_call", 2, server="team", tool=name, arguments=json.loads(arguments))
    emit({"type": "item.started", "item": {**call, "status": "in_progress"}})
    answer = asyncio.run(call_mcp_tool(name, json.loads(arguments)))
    emit({"type": "item.completed", "item": {**call, "status": "completed"}})
    finish(f"tool said: {answer}")

emit({"type": "item.completed", "item": item("agent_message", 2, text="Looking at the repo.")})
finish(json.dumps({"cwd": os.getcwd(), "argv": argv, "config": overrides()}))
