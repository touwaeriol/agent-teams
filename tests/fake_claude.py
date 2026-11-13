"""Stands in for the `claude` CLI in tests: prints stream-json like `claude -p`.

The prompt (after -p) selects the behaviour: "fail" -> error result,
"crash" -> non-zero exit without a result, "slow:<pidfile>" -> sleep after
writing its pid, anything else -> success. The final result echoes argv.
"""

import json
import os
import sys
import time
from typing import Any

argv = sys.argv[1:]
prompt = argv[argv.index("-p") + 1]


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
        "result": json.dumps({"cwd": os.getcwd(), "argv": argv}),
    }
)
