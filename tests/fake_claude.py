"""Stands in for the `claude` CLI in tests: prints stream-json like `claude -p`.

The prompt (after -p) selects the behaviour: "fail" -> error result,
"crash" -> non-zero exit without a result, "slow:<pidfile>" -> sleep after
writing its pid, "tick:<n>" -> n progress lines 0.15s apart, anything else ->
success. The final result echoes argv and cwd. If FAKE_CLAUDE_MARKER is set,
each start appends a line to that file (to count how often it really ran).

Leader mode: "delegate:<assignee>:<instruction>" calls the assign_task tool of
the MCP server given in --mcp-config (as Claude Code would) and reports the
tool's answer; a "[Task ..." report is answered with "report: <first line>".
"""

import asyncio
import json
import os
import sys
import time
from typing import Any

import httpx2
from mcp import ClientSession
from mcp.client.streamable_http import streamable_http_client

argv = sys.argv[1:]
prompt = argv[argv.index("-p") + 1]

if marker := os.environ.get("FAKE_CLAUDE_MARKER"):
    with open(marker, "a") as handle:
        handle.write(f"{os.getpid()}\n")


def emit(event: dict[str, Any]) -> None:
    print(json.dumps(event), flush=True)


emit({"type": "system", "subtype": "init", "session_id": "s-1", "cwd": os.getcwd()})
emit(
    {
        "type": "assistant",
        "message": {
            "content": [
                {"type": "text", "text": "Looking at the repo."},
                {"type": "tool_use", "name": "Bash", "input": {"command": "ls"}},
            ]
        },
    }
)
emit({"type": "user", "message": {"content": [{"type": "tool_result", "content": "a.py"}]}})
print("not json noise", flush=True)

if prompt == "crash":
    print("boom on stderr", file=sys.stderr)
    sys.exit(3)
if prompt.startswith("slow:"):
    with open(prompt.split(":", 1)[1], "w") as handle:
        handle.write(str(os.getpid()))
    time.sleep(30)


async def call_mcp_tool(name: str, arguments: dict[str, Any]) -> str:
    config = json.loads(argv[argv.index("--mcp-config") + 1])["mcpServers"]["team"]
    async with (
        httpx2.AsyncClient(headers=config["headers"]) as http,
        streamable_http_client(config["url"], http_client=http) as (read, write),
        ClientSession(read, write) as session,
    ):
        await session.initialize()
        result = await session.call_tool(name, arguments)
        return str(result.content[0].text)  # type: ignore[union-attr]


def finish(text: str) -> None:
    emit(
        {"type": "result", "subtype": "success", "is_error": False, "num_turns": 1, "result": text}
    )
    sys.exit(0)


if prompt.startswith("delegate:"):
    _, assignee, instruction = prompt.split(":", 2)
    answer = asyncio.run(
        call_mcp_tool("assign_task", {"assignee": assignee, "instruction": instruction})
    )
    session = argv[argv.index("--session-id") + 1] if "--session-id" in argv else "resumed"
    finish(f"delegated ({session}): {answer}")
if prompt.startswith("[Task"):
    resumed = argv[argv.index("--resume") + 1] if "--resume" in argv else "no session"
    finish(f"report ({resumed}): {prompt.splitlines()[0]}")
if prompt.startswith("tick:"):
    for number in range(int(prompt.split(":", 1)[1])):
        emit(
            {
                "type": "assistant",
                "message": {"content": [{"type": "text", "text": f"tick {number}"}]},
            }
        )
        time.sleep(0.15)
if prompt == "fail":
    emit(
        {
            "type": "result",
            "subtype": "error_max_turns",
            "is_error": True,
            "errors": ["too many turns"],
        }
    )
    sys.exit(1)

emit(
    {
        "type": "result",
        "subtype": "success",
        "is_error": False,
        "num_turns": 2,
        "total_cost_usd": 0.0123,
        "result": json.dumps(
            {"cwd": os.getcwd(), "argv": argv, "gh_token": os.environ.get("GH_TOKEN", "")}
        ),
    }
)
