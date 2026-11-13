"""JSON round-trip for conversation history, so a conversation survives restarts."""

from __future__ import annotations

from typing import Any

from ..types import AssistantTurn, ToolCall, ToolResult, ToolTurn, Turn, UserTurn


def dump_turn(turn: Turn) -> dict[str, Any]:
    match turn:
        case UserTurn(text=text):
            return {"kind": "user", "text": text}
        case AssistantTurn(text=text, tool_calls=calls, raw=raw):
            return {
                "kind": "assistant",
                "text": text,
                "tool_calls": [
                    {"id": c.id, "name": c.name, "arguments": dict(c.arguments)} for c in calls
                ],
                "raw": raw,
            }
        case ToolTurn(results=results):
            return {
                "kind": "tool",
                "results": [
                    {"call_id": r.call_id, "content": r.content, "is_error": r.is_error}
                    for r in results
                ],
            }
    raise TypeError(f"unsupported turn: {turn!r}")


def load_turn(data: dict[str, Any]) -> Turn:
    return _LOADERS[data["kind"]](data)


def _load_assistant(data: dict[str, Any]) -> Turn:
    calls = tuple(ToolCall(c["id"], c["name"], c["arguments"]) for c in data["tool_calls"])
    return AssistantTurn(data["text"], calls, data.get("raw"))


def _load_tool(data: dict[str, Any]) -> Turn:
    return ToolTurn(tuple(ToolResult(**r) for r in data["results"]))


_LOADERS = {
    "user": lambda data: UserTurn(data["text"]),
    "assistant": _load_assistant,
    "tool": _load_tool,
}
