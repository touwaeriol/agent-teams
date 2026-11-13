"""Provider-neutral data types shared by agents, tools, teams and LLM backends."""

from __future__ import annotations

from collections.abc import Mapping
from dataclasses import dataclass, field
from typing import Any, Literal, TypeAlias

StopReason: TypeAlias = Literal["end", "tool_use", "max_tokens", "refusal", "other"]


@dataclass(frozen=True)
class ToolCall:
    """A request from the model to invoke a tool."""

    id: str
    name: str
    arguments: Mapping[str, Any]


@dataclass(frozen=True)
class ToolResult:
    """The outcome of a tool call, sent back to the model."""

    call_id: str
    content: str
    is_error: bool = False


@dataclass(frozen=True)
class ToolSpec:
    """What the model needs to know to call a tool."""

    name: str
    description: str
    input_schema: Mapping[str, Any]


@dataclass(frozen=True)
class UserTurn:
    """Plain text from the user (or from an upstream agent)."""

    text: str


@dataclass(frozen=True)
class AssistantTurn:
    """A model reply.

    ``raw`` holds the provider-native content so it can be replayed unchanged
    (e.g. thinking blocks must be passed back exactly as received).
    """

    text: str
    tool_calls: tuple[ToolCall, ...] = ()
    raw: Any = None


@dataclass(frozen=True)
class ToolTurn:
    """All results for the tool calls of the preceding assistant turn."""

    results: tuple[ToolResult, ...]


Turn: TypeAlias = UserTurn | AssistantTurn | ToolTurn


@dataclass(frozen=True)
class Reply:
    """A normalized LLM response."""

    turn: AssistantTurn
    stop: StopReason
    detail: str = ""

    @property
    def text(self) -> str:
        return self.turn.text

    @property
    def tool_calls(self) -> tuple[ToolCall, ...]:
        return self.turn.tool_calls


@dataclass(frozen=True)
class Result:
    """The output of running an agent or a team; ``children`` keeps the trace."""

    name: str
    output: str
    children: tuple[Result, ...] = field(default=())


TaskStatus: TypeAlias = Literal["pending", "running", "done", "failed", "cancelled"]
FINAL_STATUSES: frozenset[TaskStatus] = frozenset({"done", "failed", "cancelled"})


@dataclass(frozen=True)
class Task:
    """A unit of work the leader handed to a team member (immutable snapshot)."""

    id: str
    assignee: str
    instruction: str
    depends_on: tuple[str, ...] = ()
    status: TaskStatus = "pending"
    output: str = ""

    @property
    def finished(self) -> bool:
        return self.status in FINAL_STATUSES
