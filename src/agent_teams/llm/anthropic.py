"""Claude backend built on the official ``anthropic`` SDK."""

from __future__ import annotations

from collections.abc import Mapping, Sequence
from dataclasses import dataclass, field
from functools import cached_property
from typing import Any

from anthropic import AsyncAnthropic
from anthropic.types.beta import BetaContentBlock, BetaMessage

from ..types import (
    AssistantTurn,
    Reply,
    StopReason,
    ToolCall,
    ToolResult,
    ToolSpec,
    ToolTurn,
    Turn,
    UserTurn,
)

DEFAULT_MODEL = "claude-opus-5-5"
FALLBACK_BETA = "server-side-fallback-2026-07-01"

_STOP_REASONS: Mapping[str, StopReason] = {
    "end_turn": "end",
    "stop_sequence": "end",
    "tool_use": "tool_use",
    "max_tokens": "max_tokens",
    "model_context_window_exceeded": "max_tokens",
    "refusal": "refusal",
}


@dataclass
class AnthropicLLM:
    """Streams each request (safe for long outputs) and returns the final message.

    Defaults: adaptive thinking, ``high`` effort, and server-side refusal
    fallbacks (``fallbacks="default"``). Anything else the Messages API accepts
    can be passed through ``options`` without subclassing.
    """

    model: str = DEFAULT_MODEL
    max_tokens: int = 64000
    effort: str | None = "high"
    thinking: Mapping[str, Any] | None = field(default_factory=lambda: {"type": "adaptive"})
    fallbacks: bool = True
    options: Mapping[str, Any] = field(default_factory=dict)
    client: AsyncAnthropic | None = field(default=None, repr=False)

    @cached_property
    def _client(self) -> AsyncAnthropic:
        return self.client or AsyncAnthropic()

    async def complete(
        self, system: str, history: Sequence[Turn], tools: Sequence[ToolSpec]
    ) -> Reply:
        params = self.request_params(system, history, tools)
        async with self._client.beta.messages.stream(**params) as stream:
            message = await stream.get_final_message()
        return to_reply(message)

    def request_params(
        self, system: str, history: Sequence[Turn], tools: Sequence[ToolSpec]
    ) -> dict[str, Any]:
        optional = {
            "system": system,
            "tools": [to_tool_param(t) for t in tools],
            "thinking": self.thinking,
            "output_config": {"effort": self.effort} if self.effort else None,
            "betas": [FALLBACK_BETA] if self.fallbacks else None,
            "fallbacks": "default" if self.fallbacks else None,
        }
        return {
            "model": self.model,
            "max_tokens": self.max_tokens,
            "messages": [to_message(turn) for turn in history],
            **{key: value for key, value in optional.items() if value},
            **self.options,
        }


def to_tool_param(spec: ToolSpec) -> dict[str, Any]:
    # Tool inputs stream as generated; the Toolkit validates them before running.
    return {
        "name": spec.name,
        "description": spec.description,
        "input_schema": dict(spec.input_schema),
        "eager_input_streaming": True,
    }


def to_message(turn: Turn) -> dict[str, Any]:
    match turn:
        case UserTurn(text=text):
            return {"role": "user", "content": text}
        case AssistantTurn(raw=raw) if raw is not None:
            return {"role": "assistant", "content": raw}
        case AssistantTurn():
            return {"role": "assistant", "content": _assistant_blocks(turn)}
        case ToolTurn(results=results):
            return {"role": "user", "content": [_tool_result(r) for r in results]}
    raise TypeError(f"unsupported turn: {turn!r}")


def to_reply(message: BetaMessage) -> Reply:
    blocks = _echo_blocks(message.content)
    text = "".join(b.text for b in blocks if b.type == "text")
    calls = tuple(
        ToolCall(b.id, b.name, b.input if isinstance(b.input, dict) else {})
        for b in blocks
        if b.type == "tool_use"
    )
    raw = [b.to_dict() for b in blocks]
    stop = _STOP_REASONS.get(message.stop_reason or "", "other")
    return Reply(AssistantTurn(text, calls, raw), stop, _stop_detail(message))


def _echo_blocks(content: Sequence[BetaContentBlock]) -> list[BetaContentBlock]:
    """Drop what a declining model produced before a mid-output fallback.

    Only text survives from before the last ``fallback`` block; the block
    itself and everything after it are kept in place.
    """
    boundary = max((i for i, b in enumerate(content) if b.type == "fallback"), default=0)
    return [b for i, b in enumerate(content) if i >= boundary or b.type == "text"]


def _assistant_blocks(turn: AssistantTurn) -> list[dict[str, Any]]:
    text = [{"type": "text", "text": turn.text}] if turn.text else []
    calls = [
        {"type": "tool_use", "id": c.id, "name": c.name, "input": dict(c.arguments)}
        for c in turn.tool_calls
    ]
    return text + calls


def _tool_result(result: ToolResult) -> dict[str, Any]:
    return {
        "type": "tool_result",
        "tool_use_id": result.call_id,
        "content": result.content,
        "is_error": result.is_error,
    }


def _stop_detail(message: BetaMessage) -> str:
    details = message.stop_details
    return f"{details.category}: {details.explanation}" if details else ""
