"""Composable multi-agent teams built on the Claude API."""

from .agent import Agent
from .config import build_team, load_team
from .context import RunContext
from .errors import AgentTeamsError, ConfigError, LLMError, MaxStepsExceeded
from .hooks import (
    Event,
    Hooks,
    LLMReplied,
    RunFinished,
    RunStarted,
    ToolFinished,
    ToolStarted,
    trace,
)
from .llm import LLM, AnthropicLLM, ScriptedLLM
from .runnable import Runnable
from .teams import Parallel, RoundRobin, Sequential, Team, supervisor
from .tools import Tool, Toolkit, as_tool, tool
from .types import Reply, Result, ToolCall, ToolResult, ToolSpec

__all__ = [
    "LLM",
    "Agent",
    "AgentTeamsError",
    "AnthropicLLM",
    "ConfigError",
    "Event",
    "Hooks",
    "LLMError",
    "LLMReplied",
    "MaxStepsExceeded",
    "Parallel",
    "Reply",
    "Result",
    "RoundRobin",
    "RunContext",
    "RunFinished",
    "RunStarted",
    "Runnable",
    "ScriptedLLM",
    "Sequential",
    "Team",
    "Tool",
    "ToolCall",
    "ToolFinished",
    "ToolResult",
    "ToolSpec",
    "ToolStarted",
    "Toolkit",
    "as_tool",
    "build_team",
    "load_team",
    "supervisor",
    "tool",
    "trace",
]
