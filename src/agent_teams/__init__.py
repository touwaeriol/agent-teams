"""Composable multi-agent teams built on the Claude API."""

from .agent import Agent, Session
from .claude_code import ClaudeCodeWorker
from .config import build_team, load_team
from .context import RunContext
from .conversation import Conversation
from .errors import AgentTeamsError, ConfigError, LLMError, MaxStepsExceeded
from .hooks import (
    Event,
    Hooks,
    LLMReplied,
    RunFinished,
    RunStarted,
    TaskAssigned,
    TaskFinished,
    TaskStarted,
    ToolFinished,
    ToolStarted,
    WorkerProgress,
    trace,
)
from .leader import Leader
from .llm import LLM, AnthropicLLM, ScriptedLLM
from .nodes import LocalNode, Node, SSHNode
from .runnable import Runnable
from .tasks import TaskBoard
from .teams import Parallel, RoundRobin, Sequential, Team, supervisor
from .tools import Tool, Toolkit, as_tool, tool
from .types import Reply, Result, Task, ToolCall, ToolResult, ToolSpec

__all__ = [
    "LLM",
    "Agent",
    "AgentTeamsError",
    "AnthropicLLM",
    "ClaudeCodeWorker",
    "ConfigError",
    "Conversation",
    "Event",
    "Hooks",
    "LLMError",
    "LLMReplied",
    "Leader",
    "LocalNode",
    "MaxStepsExceeded",
    "Node",
    "Parallel",
    "Reply",
    "Result",
    "RoundRobin",
    "RunContext",
    "RunFinished",
    "RunStarted",
    "Runnable",
    "SSHNode",
    "ScriptedLLM",
    "Sequential",
    "Session",
    "Task",
    "TaskAssigned",
    "TaskBoard",
    "TaskFinished",
    "TaskStarted",
    "Team",
    "Tool",
    "ToolCall",
    "ToolFinished",
    "ToolResult",
    "ToolSpec",
    "ToolStarted",
    "Toolkit",
    "WorkerProgress",
    "as_tool",
    "build_team",
    "load_team",
    "supervisor",
    "tool",
    "trace",
]
