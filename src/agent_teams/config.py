"""Declarative team definitions (YAML or JSON) validated with pydantic.

Example::

    llm: {model: claude-opus-5-5, effort: high}
    team:
      type: sequential
      name: writers
      members:
        - {name: researcher, instructions: "Collect facts."}
        - {name: writer, instructions: "Write the article."}

Leader shorthand: list the employees and pick one agent as the leader; it gets
a non-blocking task board over all the other employees. ``nodes`` are machines
that ``claude_code`` employees run on (over SSH, or the server itself)::

    nodes:
      - {name: build-1, host: 10.0.0.11, user: dev, workdir: /srv/app}
      - {name: here, local: true, workdir: /srv/app}
    leader: lead
    employees:
      - {name: lead, instructions: "Plan the work and delegate it."}
      - {name: researcher, description: "Collects facts."}
      - {name: backend, type: claude_code, node: build-1, description: "Backend engineer."}

Member ``type`` is one of ``agent`` (default), ``claude_code``, ``leader``,
``sequential``, ``parallel``, ``round_robin`` and ``supervisor``. Tools are
referenced as ``"package.module:attribute"`` (a :class:`Tool` or a function).
"""

from __future__ import annotations

import importlib
import json
from collections.abc import Callable, Mapping
from pathlib import Path
from typing import Annotated, Any, Literal

import yaml
from pydantic import (
    BaseModel,
    ConfigDict,
    Discriminator,
    Field,
    Tag,
    ValidationError,
    model_validator,
)

from .agent import Agent
from .claude_code import ClaudeCodeWorker
from .errors import ConfigError
from .leader import LEADER_INSTRUCTIONS, Leader
from .llm.anthropic import AnthropicLLM
from .llm.base import LLM
from .nodes import LocalNode, Node, SSHNode
from .remote import NodeRegistry, RemoteNode
from .runnable import Runnable
from .teams import SUPERVISOR_INSTRUCTIONS, Parallel, RoundRobin, Sequential, supervisor
from .tools import Tool, tool

LLMFactory = Callable[[Mapping[str, Any]], LLM]


def anthropic_factory(options: Mapping[str, Any]) -> LLM:
    return AnthropicLLM(**options)


class Builder:
    """Turns specs into runnables; shares one LLM instance per distinct config."""

    def __init__(
        self,
        llm_defaults: Mapping[str, Any],
        llm_factory: LLMFactory,
        nodes: Mapping[str, Node] | None = None,
    ) -> None:
        self._defaults = dict(llm_defaults)
        self._factory = llm_factory
        self._llms: dict[str, LLM] = {}
        self._nodes = dict(nodes or {})

    def node(self, name: str) -> Node:
        if name not in self._nodes:
            raise ConfigError(f"unknown node '{name}'; defined nodes: {list(self._nodes)}")
        return self._nodes[name]

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


class _Member(BaseModel):
    model_config = ConfigDict(extra="forbid")

    name: str
    description: str = ""

    def build(self, builder: Builder) -> Runnable:
        raise NotImplementedError


class _LLMMember(_Member):
    instructions: str = ""
    tools: list[str] = Field(default_factory=list)
    llm: dict[str, Any] | None = None
    max_steps: int = Field(20, ge=1)


class AgentSpec(_LLMMember):
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


class ClaudeCodeSpec(_Member):
    type: Literal["claude_code"]
    node: str
    instructions: str = ""
    workdir: str = ""
    model: str = ""
    permission_mode: Literal[
        "acceptEdits", "auto", "bypassPermissions", "default", "dontAsk", "manual", "plan"
    ] = "acceptEdits"
    allowed_tools: list[str] = Field(default_factory=list)
    disallowed_tools: list[str] = Field(default_factory=list)
    max_turns: int | None = Field(None, ge=1)
    claude_command: str = "claude"
    extra_args: list[str] = Field(default_factory=list)

    def build(self, builder: Builder) -> Runnable:
        return ClaudeCodeWorker(
            **self.model_dump(exclude={"type", "node"}), node=builder.node(self.node)
        )


class SupervisorSpec(_LLMMember):
    type: Literal["supervisor"]
    instructions: str = SUPERVISOR_INSTRUCTIONS
    members: list[MemberSpec] = Field(min_length=1)

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


class LeaderSpec(_LLMMember):
    type: Literal["leader"]
    instructions: str = LEADER_INSTRUCTIONS
    members: list[MemberSpec] = Field(min_length=1)
    max_turns: int = Field(50, ge=1)

    def build(self, builder: Builder) -> Runnable:
        return Leader(
            name=self.name,
            llm=builder.llm(self.llm),
            members=[m.build(builder) for m in self.members],
            instructions=self.instructions,
            description=self.description,
            tools=builder.tools(self.tools),
            max_steps=self.max_steps,
            max_turns=self.max_turns,
        )


class _TeamMember(_Member):
    members: list[MemberSpec] = Field(min_length=1)

    def team_fields(self, builder: Builder) -> dict[str, Any]:
        members = [m.build(builder) for m in self.members]
        return {"name": self.name, "description": self.description, "members": members}


class SequentialSpec(_TeamMember):
    type: Literal["sequential"]

    def build(self, builder: Builder) -> Runnable:
        return Sequential(**self.team_fields(builder))


class ParallelSpec(_TeamMember):
    type: Literal["parallel"]
    aggregator: MemberSpec | None = None

    def build(self, builder: Builder) -> Runnable:
        aggregator = self.aggregator.build(builder) if self.aggregator else None
        return Parallel(**self.team_fields(builder), aggregator=aggregator)


class RoundRobinSpec(_TeamMember):
    type: Literal["round_robin"]
    rounds: int = Field(2, ge=1)
    finalizer: MemberSpec | None = None

    def build(self, builder: Builder) -> Runnable:
        finalizer = self.finalizer.build(builder) if self.finalizer else None
        return RoundRobin(**self.team_fields(builder), rounds=self.rounds, finalizer=finalizer)


def _member_type(value: Any) -> str:
    if isinstance(value, Mapping):
        return str(value.get("type", "agent"))
    return str(getattr(value, "type", "agent"))


MemberSpec = Annotated[
    Annotated[AgentSpec, Tag("agent")]
    | Annotated[SequentialSpec, Tag("sequential")]
    | Annotated[ParallelSpec, Tag("parallel")]
    | Annotated[RoundRobinSpec, Tag("round_robin")]
    | Annotated[SupervisorSpec, Tag("supervisor")]
    | Annotated[LeaderSpec, Tag("leader")]
    | Annotated[ClaudeCodeSpec, Tag("claude_code")],
    Discriminator(_member_type),
]


class NodeConfig(BaseModel):
    """A machine. By default its daemon dials in (``agent-teams node``); with
    ``host`` it is reached over SSH instead, with ``local: true`` it is the
    console host itself."""

    model_config = ConfigDict(extra="forbid")

    name: str
    local: bool = False
    host: str = ""
    user: str = ""
    port: int | None = None
    identity_file: str = ""
    workdir: str = ""
    ssh_options: list[str] = Field(default_factory=list)
    revision: int = Field(1, ge=1)

    @model_validator(mode="after")
    def _one_kind(self) -> NodeConfig:
        if self.local and self.host:
            raise ValueError(f"node '{self.name}' cannot have both 'host' and 'local: true'")
        return self

    @property
    def is_daemon(self) -> bool:
        return not (self.local or self.host)

    def build(self, registry: NodeRegistry) -> Node:
        if self.is_daemon:
            registry.declare(self.name, self.revision)
            return RemoteNode(self.name, registry, self.workdir)
        if self.local:
            return LocalNode(self.name, self.workdir)
        return SSHNode(
            self.name,
            self.host,
            self.user,
            self.port,
            self.identity_file,
            self.workdir,
            tuple(self.ssh_options),
        )


class TeamFile(BaseModel):
    model_config = ConfigDict(extra="forbid")

    llm: dict[str, Any] = Field(default_factory=dict)
    nodes: list[NodeConfig] = Field(default_factory=list)
    team: MemberSpec

    @model_validator(mode="after")
    def _unique_nodes(self) -> TeamFile:
        names = [n.name for n in self.nodes]
        if len(names) != len(set(names)):
            raise ValueError(f"node names must be unique: {names}")
        return self

    def build_nodes(self, registry: NodeRegistry) -> dict[str, Node]:
        return {n.name: n.build(registry) for n in self.nodes}

    @model_validator(mode="before")
    @classmethod
    def _expand_leader(cls, data: Any) -> Any:
        if not isinstance(data, Mapping) or "leader" not in data:
            return data
        rest = {k: v for k, v in data.items() if k not in ("leader", "employees")}
        if "team" in rest:
            raise ValueError("use either 'team' or 'leader' + 'employees', not both")
        employees = list(data.get("employees") or [])
        return {**rest, "team": _promote_leader(str(data["leader"]), employees)}


def _promote_leader(leader: str, employees: list[Any]) -> dict[str, Any]:
    """Turn the chosen agent into a leader over every other employee."""
    chosen = next(
        (e for e in employees if isinstance(e, Mapping) and e.get("name") == leader), None
    )
    if chosen is None:
        raise ValueError(f"leader '{leader}' is not one of the employees")
    if chosen.get("type", "agent") != "agent":
        raise ValueError(f"leader '{leader}' must be an agent")
    members = [e for e in employees if e is not chosen]
    return {**chosen, "type": "leader", "members": members}


for _model in (
    LeaderSpec,
    SupervisorSpec,
    _TeamMember,
    SequentialSpec,
    ParallelSpec,
    RoundRobinSpec,
    TeamFile,
):
    _model.model_rebuild()


def parse_team(data: Mapping[str, Any]) -> TeamFile:
    try:
        return TeamFile.model_validate(data)
    except ValidationError as exc:
        raise ConfigError(str(exc)) from exc


def build_team(
    data: Mapping[str, Any],
    llm_factory: LLMFactory = anthropic_factory,
    registry: NodeRegistry | None = None,
) -> Runnable:
    """Build a runnable from an already-parsed definition.

    Daemon nodes connect to ``registry`` (the web service owns it); without one
    they can never come online.
    """
    spec = parse_team(data)
    nodes = spec.build_nodes(registry or NodeRegistry())
    return spec.team.build(Builder(spec.llm, llm_factory, nodes))


def read_team_file(path: str | Path) -> Mapping[str, Any]:
    data = yaml.safe_load(Path(path).read_text(encoding="utf-8"))
    if not isinstance(data, Mapping):
        raise ConfigError(f"{path}: expected a mapping at the top level")
    return data


def load_team(
    path: str | Path,
    llm_factory: LLMFactory = anthropic_factory,
    registry: NodeRegistry | None = None,
) -> Runnable:
    """Build a runnable from a YAML or JSON file."""
    return build_team(read_team_file(path), llm_factory, registry)


def load_nodes(path: str | Path, registry: NodeRegistry | None = None) -> list[Node]:
    """The machines defined in a team file."""
    nodes = parse_team(read_team_file(path)).build_nodes(registry or NodeRegistry())
    return list(nodes.values())


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
