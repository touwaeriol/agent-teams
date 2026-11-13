"""Tools: plain Python functions (or whole agents/teams) the model can call.

``@tool`` derives the JSON schema from the signature and validates arguments
with pydantic, so tool functions stay free of framework code. A parameter
annotated with :class:`RunContext` is injected rather than exposed.
"""

from __future__ import annotations

import asyncio
import inspect
import itertools
import json
import re
from collections.abc import Awaitable, Callable, Iterable, Mapping
from dataclasses import dataclass
from typing import TYPE_CHECKING, Annotated, Any, overload

from pydantic import BaseModel, ConfigDict, Field, create_model

from .context import RunContext
from .hooks import ToolFinished, ToolStarted
from .types import ToolCall, ToolResult, ToolSpec

if TYPE_CHECKING:
    from .runnable import Runnable

ToolHandler = Callable[[Mapping[str, Any], RunContext], Awaitable[str]]

_ARG_LINE = re.compile(r"^\s+(\w+)(?:\s*\([^)]*\))?:\s*(.+)$")
_ARGS_HEADER = re.compile(r"^Args:\s*$", re.MULTILINE)
_INVALID_NAME_CHARS = re.compile(r"[^a-zA-Z0-9_-]")
_VARIADIC = (inspect.Parameter.VAR_POSITIONAL, inspect.Parameter.VAR_KEYWORD)


@dataclass(frozen=True)
class Tool:
    """A named, described, schema-checked async callable."""

    name: str
    description: str
    input_schema: Mapping[str, Any]
    handler: ToolHandler

    @property
    def spec(self) -> ToolSpec:
        return ToolSpec(self.name, self.description, self.input_schema)


@overload
def tool(fn: Callable[..., Any], /) -> Tool: ...
@overload
def tool(
    *, name: str | None = None, description: str | None = None
) -> Callable[[Callable[..., Any]], Tool]: ...
def tool(
    fn: Callable[..., Any] | None = None,
    /,
    *,
    name: str | None = None,
    description: str | None = None,
) -> Tool | Callable[[Callable[..., Any]], Tool]:
    """Turn a sync or async function into a :class:`Tool`.

    Usable bare (``@tool``) or with overrides (``@tool(name="...")``).
    """

    def build(func: Callable[..., Any]) -> Tool:
        summary, arg_docs = _parse_docstring(func.__doc__)
        model, context_params = _arguments_model(func, arg_docs)
        return Tool(
            name=tool_name(name or func.__name__),
            description=description or summary or func.__name__,
            input_schema=_schema(model),
            handler=_handler(func, model, context_params),
        )

    return build if fn is None else build(fn)


def as_tool(runnable: Runnable, *, description: str | None = None) -> Tool:
    """Expose an agent or team as a tool, so another agent can delegate to it."""

    async def delegate(
        task: Annotated[str, Field(description="A complete, self-contained task.")],
        ctx: RunContext,
    ) -> str:
        return (await runnable.run(task, ctx)).output

    summary = description or runnable.description or f"Delegate a task to {runnable.name}."
    return tool(name=runnable.name, description=summary)(delegate)


def tool_name(name: str) -> str:
    """Coerce a name into the ``[a-zA-Z0-9_-]{1,64}`` form the API accepts."""
    return _INVALID_NAME_CHARS.sub("_", name)[:64] or "tool"


class Toolkit:
    """A set of tools with concurrent, failure-isolated execution."""

    def __init__(self, tools: Iterable[Tool] = ()) -> None:
        self._tools: dict[str, Tool] = {}
        for item in tools:
            self.add(item)

    def add(self, item: Tool) -> None:
        if item.name in self._tools:
            raise ValueError(f"duplicate tool name: {item.name}")
        self._tools[item.name] = item

    @property
    def specs(self) -> tuple[ToolSpec, ...]:
        return tuple(t.spec for t in self._tools.values())

    def __len__(self) -> int:
        return len(self._tools)

    async def execute(self, calls: Iterable[ToolCall], ctx: RunContext) -> tuple[ToolResult, ...]:
        return tuple(await asyncio.gather(*(self._execute_one(c, ctx) for c in calls)))

    async def _execute_one(self, call: ToolCall, ctx: RunContext) -> ToolResult:
        await ctx.emit(ToolStarted(ctx.path, call))
        result = await self._invoke(call, ctx)
        await ctx.emit(ToolFinished(ctx.path, call, result))
        return result

    async def _invoke(self, call: ToolCall, ctx: RunContext) -> ToolResult:
        found = self._tools.get(call.name)
        if found is None:
            return ToolResult(call.id, f"Unknown tool: {call.name}", is_error=True)
        try:
            content = await found.handler(call.arguments, ctx)
        except Exception as exc:  # reported to the model, which can recover
            return ToolResult(call.id, f"{type(exc).__name__}: {exc}", is_error=True)
        return ToolResult(call.id, content)


def _parse_docstring(doc: str | None) -> tuple[str, dict[str, str]]:
    """Split a Google-style docstring into (summary, {arg: description})."""
    parts = _ARGS_HEADER.split(inspect.cleandoc(doc or ""), maxsplit=1)
    if len(parts) == 1:
        return parts[0].strip(), {}
    lines = itertools.takewhile(lambda s: not s or s[0].isspace(), parts[1].splitlines()[1:])
    matches = (_ARG_LINE.match(line) for line in lines)
    return parts[0].strip(), {m[1]: m[2] for m in matches if m}


def _arguments_model(
    func: Callable[..., Any], arg_docs: Mapping[str, str]
) -> tuple[type[BaseModel], tuple[str, ...]]:
    hints = inspect.get_annotations(func, eval_str=True)
    params = [p for p in inspect.signature(func).parameters.values() if p.kind not in _VARIADIC]
    context_params = tuple(p.name for p in params if hints.get(p.name) is RunContext)
    fields: dict[str, Any] = {
        p.name: (hints.get(p.name, Any), _field(p.default, arg_docs.get(p.name)))
        for p in params
        if p.name not in context_params
    }
    config = ConfigDict(extra="forbid")
    return create_model(f"{func.__name__}_arguments", __config__=config, **fields), context_params


def _field(default: Any, doc: str | None) -> Any:
    value = ... if default is inspect.Parameter.empty else default
    return Field(value, description=doc) if doc else Field(value)


def _schema(model: type[BaseModel]) -> dict[str, Any]:
    schema = model.model_json_schema()
    schema.pop("title", None)
    return schema


def _handler(
    func: Callable[..., Any], model: type[BaseModel], context_params: tuple[str, ...]
) -> ToolHandler:
    async def handle(arguments: Mapping[str, Any], ctx: RunContext) -> str:
        kwargs = dict(model.model_validate(arguments)) | dict.fromkeys(context_params, ctx)
        if inspect.iscoroutinefunction(func):
            return _to_text(await func(**kwargs))
        return _to_text(await asyncio.to_thread(func, **kwargs))

    return handle


def _to_text(value: Any) -> str:
    if isinstance(value, str):
        return value
    if isinstance(value, BaseModel):
        return value.model_dump_json()
    return json.dumps(value, ensure_ascii=False, default=str)
