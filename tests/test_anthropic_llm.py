from __future__ import annotations

from types import TracebackType
from typing import Any

from anthropic.types.beta import BetaMessage

from agent_teams import AnthropicLLM
from agent_teams.llm.anthropic import FALLBACK_BETA, to_message, to_reply
from agent_teams.types import (
    AssistantTurn,
    ToolCall,
    ToolResult,
    ToolSpec,
    ToolTurn,
    UserTurn,
)

SPEC = ToolSpec("lookup", "Look things up.", {"type": "object", "properties": {}})


def message(content: list[dict[str, Any]], stop: str = "end_turn", **extra: Any) -> BetaMessage:
    return BetaMessage.model_validate(
        {
            "id": "msg_1",
            "type": "message",
            "role": "assistant",
            "model": "claude-opus-5-5",
            "content": content,
            "stop_reason": stop,
            "usage": {"input_tokens": 1, "output_tokens": 1},
            **extra,
        }
    )


def test_request_params_defaults() -> None:
    llm = AnthropicLLM()
    params = llm.request_params("be brief", [UserTurn("hi")], [SPEC])

    assert params["model"] == "claude-opus-5-5"
    assert params["system"] == "be brief"
    assert params["thinking"] == {"type": "adaptive"}
    assert params["output_config"] == {"effort": "high"}
    assert params["betas"] == [FALLBACK_BETA]
    assert params["fallbacks"] == "default"
    assert params["messages"] == [{"role": "user", "content": "hi"}]
    assert params["tools"][0]["eager_input_streaming"] is True


def test_request_params_drop_empty_values_and_apply_options() -> None:
    llm = AnthropicLLM(effort=None, thinking=None, fallbacks=False, options={"max_tokens": 10})
    params = llm.request_params("", [UserTurn("hi")], [])

    assert set(params) == {"model", "max_tokens", "messages"}
    assert params["max_tokens"] == 10


def test_turns_convert_to_native_messages() -> None:
    call = ToolCall("tu_1", "lookup", {"q": "x"})
    assert to_message(AssistantTurn("t", (call,))) == {
        "role": "assistant",
        "content": [
            {"type": "text", "text": "t"},
            {"type": "tool_use", "id": "tu_1", "name": "lookup", "input": {"q": "x"}},
        ],
    }
    raw = [{"type": "thinking", "thinking": "", "signature": "sig"}]
    assert to_message(AssistantTurn("", raw=raw)) == {"role": "assistant", "content": raw}
    assert to_message(ToolTurn((ToolResult("tu_1", "bad", is_error=True),))) == {
        "role": "user",
        "content": [
            {"type": "tool_result", "tool_use_id": "tu_1", "content": "bad", "is_error": True}
        ],
    }


def test_reply_keeps_raw_blocks_and_extracts_tool_calls() -> None:
    reply = to_reply(
        message(
            [
                {"type": "thinking", "thinking": "", "signature": "sig"},
                {"type": "text", "text": "Let me check."},
                {"type": "tool_use", "id": "tu_1", "name": "lookup", "input": {"q": "x"}},
            ],
            stop="tool_use",
        )
    )

    assert reply.stop == "tool_use"
    assert reply.text == "Let me check."
    assert reply.tool_calls == (ToolCall("tu_1", "lookup", {"q": "x"}),)
    assert [b["type"] for b in reply.turn.raw] == ["thinking", "text", "tool_use"]


def test_blocks_before_a_fallback_are_dropped_except_text() -> None:
    fallback = {
        "type": "fallback",
        "from": {"model": "claude-opus-5-5"},
        "to": {"model": "claude-opus-4-8"},
        "trigger": {"type": "refusal", "category": "cyber"},
    }
    reply = to_reply(
        message(
            [
                {"type": "thinking", "thinking": "", "signature": "sig"},
                {"type": "text", "text": "Part one. "},
                {"type": "tool_use", "id": "tu_0", "name": "lookup", "input": {}},
                fallback,
                {"type": "text", "text": "Part two."},
            ]
        )
    )

    assert reply.text == "Part one. Part two."
    assert reply.tool_calls == ()
    assert [b["type"] for b in reply.turn.raw] == ["text", "fallback", "text"]
    assert reply.turn.raw[1]["from"] == {"model": "claude-opus-5-5"}


def test_refusal_carries_detail() -> None:
    reply = to_reply(
        message(
            [],
            stop="refusal",
            stop_details={"type": "refusal", "category": "cyber", "explanation": "no"},
        )
    )
    assert reply.stop == "refusal"
    assert reply.detail == "cyber: no"


class FakeStream:
    def __init__(self, final: BetaMessage) -> None:
        self.final = final

    async def __aenter__(self) -> FakeStream:
        return self

    async def __aexit__(
        self,
        exc_type: type[BaseException] | None,
        exc: BaseException | None,
        tb: TracebackType | None,
    ) -> None:
        return None

    async def get_final_message(self) -> BetaMessage:
        return self.final


class FakeMessages:
    def __init__(self, final: BetaMessage) -> None:
        self.final = final
        self.params: dict[str, Any] = {}

    def stream(self, **params: Any) -> FakeStream:
        self.params = params
        return FakeStream(self.final)


class FakeClient:
    def __init__(self, final: BetaMessage) -> None:
        self.beta = type("Beta", (), {"messages": FakeMessages(final)})()


async def test_complete_streams_and_normalizes() -> None:
    client = FakeClient(message([{"type": "text", "text": "done"}]))
    llm = AnthropicLLM(client=client)  # type: ignore[arg-type]

    reply = await llm.complete("sys", [UserTurn("hi")], [])

    assert reply.text == "done"
    assert reply.stop == "end"
    assert client.beta.messages.params["system"] == "sys"
