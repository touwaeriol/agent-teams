"""Declarative team definitions (YAML or JSON) validated with pydantic.

Example::

    llm: {model: claude-opus-5-5, effort: high}
    team:
      type: sequential
      name: writers
      members:
        - {name: researcher, instructions: "Collect facts."}
        - {name: writer, instructions: "Write the article."}

Node ``type`` is one of ``agent`` (default), ``sequential``, ``parallel``,
``round_robin`` and ``supervisor``. Tools are referenced as
``"package.module:attribute"`` (a :class:`Tool` or a plain function).
"""

from __future__ import annotations

import importlib
import json
from collections.abc import Callable, Mapping
from pathlib import Path
from typing import Annotated, Any, Literal

import yaml
from pydantic import BaseModel, ConfigDict, Discriminator, Field, Tag, ValidationError

from .agent import Agent
from .errors import ConfigError
from .llm.anthropic import AnthropicLLM
from .llm.base import LLM
from .runnable import Runnable
from .teams import SUPERVISOR_INSTRUCTIONS, Parallel, RoundRobin, Sequential, supervisor
from .tools import Tool, tool

LLMFactory = Callable[[Mapping[str, Any]], LLM]


def anthropic_factory(options: Mapping[str, Any]) -> LLM:
    return AnthropicLLM(**options)


class Builder:
    """Turns specs into runnables; shares one LLM instance per distinct config."""

    def __init__(self, llm_defaults: Mapping[str, Any], llm_factory: LLMFactory) -> None:
        self._defaults = dict(llm_defaults)
        self._factory = llm_factory
        self._llms: dict[str, LLM] = {}

    def llm(self, overrides: Mapping[str, Any] | None) -> LLM:
        options = {**self._defaults, **(overrides or {})}
        key = json.dumps(options, sort_keys=True, default=str)
        if key not in self._llms:
            self._llms[key] = self._create_llm(options)
        return self._llms[key]

    def _create_llm(self, options: Mapping[str, Any]) -> LLM:
        try:
            return self._factory(options)
        except TypeError as exc:
            raise ConfigError(f"invalid llm options {options}: {exc}") from exc

    @staticmethod
    def tools(refs: list[str]) -> list[Tool]:
        return [load_tool(ref) for ref in refs]


class _Node(BaseModel):
    model_config = ConfigDict(extra="forbid")

    name: str
    description: str = ""

    def build(self, builder: Builder) -> Runnable:
        raise NotImplementedError


class _LLMNode(_Node):
    instructions: str = ""
    tools: list[str] = Field(default_factory=list)
    llm: dict[str, Any] | None = None
    max_steps: int = Field(20, ge=1)


class AgentSpec(_LLMNode):
    type: Literal["agent"] = "agent"

    def build(self, builder: Builder) -> Runnable:
        return Agent(
            name=self.name,
            llm=builder.llm(self.llm),
            instructions=self.instructions,
            description=self.description,
            tools=builder.tools(self.tools),
            max_steps=self.max_steps,
        )


class SupervisorSpec(_LLMNode):
    type: Literal["supervisor"]
    instructions: str = SUPERVISOR_INSTRUCTIONS
    members: list[NodeSpec] = Field(min_length=1)

    def build(self, builder: Builder) -> Runnable:
        return supervisor(
            self.name,
            builder.llm(self.llm),
            [m.build(builder) for m in self.members],
            instructions=self.instructions,
            description=self.description,
            tools=builder.tools(self.tools),
            max_steps=self.max_steps,
        )


class _TeamNode(_Node):
    members: list[NodeSpec] = Field(min_length=1)

    def team_fields(self, builder: Builder) -> dict[str, Any]:
        members = [m.build(builder) for m in self.members]
        return {"name": self.name, "description": self.description, "members": members}


class SequentialSpec(_TeamNode):
    type: Literal["sequential"]

    def build(self, builder: Builder) -> Runnable:
        return Sequential(**self.team_fields(builder))


class ParallelSpec(_TeamNode):
    type: Literal["parallel"]
    aggregator: NodeSpec | None = None

    def build(self, builder: Builder) -> Runnable:
        aggregator = self.aggregator.build(builder) if self.aggregator else None
        return Parallel(**self.team_fields(builder), aggregator=aggregator)


class RoundRobinSpec(_TeamNode):
    type: Literal["round_robin"]
    rounds: int = Field(2, ge=1)
    finalizer: NodeSpec | None = None

    def build(self, builder: Builder) -> Runnable:
        finalizer = self.finalizer.build(builder) if self.finalizer else None
        return RoundRobin(**self.team_fields(builder), rounds=self.rounds, finalizer=finalizer)


def _node_type(value: Any) -> str:
    if isinstance(value, Mapping):
        return str(value.get("type", "agent"))
    return str(getattr(value, "type", "agent"))


NodeSpec = Annotated[
    Annotated[AgentSpec, Tag("agent")]
    | Annotated[SequentialSpec, Tag("sequential")]
    | Annotated[ParallelSpec, Tag("parallel")]
    | Annotated[RoundRobinSpec, Tag("round_robin")]
    | Annotated[SupervisorSpec, Tag("supervisor")],
    Discriminator(_node_type),
]


class TeamFile(BaseModel):
    model_config = ConfigDict(extra="forbid")

    llm: dict[str, Any] = Field(default_factory=dict)
    team: NodeSpec


for _model in (SupervisorSpec, _TeamNode, SequentialSpec, ParallelSpec, RoundRobinSpec, TeamFile):
    _model.model_rebuild()


def build_team(data: Mapping[str, Any], llm_factory: LLMFactory = anthropic_factory) -> Runnable:
    """Build a runnable from an already-parsed definition."""
    try:
        spec = TeamFile.model_validate(data)
    except ValidationError as exc:
        raise ConfigError(str(exc)) from exc
    return spec.team.build(Builder(spec.llm, llm_factory))


def load_team(path: str | Path, llm_factory: LLMFactory = anthropic_factory) -> Runnable:
    """Build a runnable from a YAML or JSON file."""
    data = yaml.safe_load(Path(path).read_text(encoding="utf-8"))
    if not isinstance(data, Mapping):
        raise ConfigError(f"{path}: expected a mapping at the top level")
    return build_team(data, llm_factory)


def load_tool(ref: str) -> Tool:
    """Import ``"package.module:attribute"`` and coerce it into a :class:`Tool`."""
    module_name, _, attribute = ref.partition(":")
    if not attribute:
        raise ConfigError(f"tool reference must look like 'module:attribute', got '{ref}'")
    try:
        target = getattr(importlib.import_module(module_name), attribute)
    except (ImportError, AttributeError) as exc:
        raise ConfigError(f"cannot import tool '{ref}': {exc}") from exc
    if isinstance(target, Tool):
        return target
    if callable(target):
        return tool(target)
    raise ConfigError(f"'{ref}' is neither a Tool nor a callable")
