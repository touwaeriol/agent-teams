"""Shared state of the running service, reachable from every request."""

from __future__ import annotations

from dataclasses import dataclass, field

from fastapi import Request, WebSocket

from ..remote import NodeRegistry
from .accounts import AccountStore
from .broker import Broker
from .github import GitHub, GitHubAPI
from .hub import Hub
from .mcp_bridge import ToolBridge
from .projects import MemoryProjects, ProjectStore
from .secrets import SecretBox
from .store import Store
from .workspaces import Workspaces


@dataclass
class Services:
    """External dependencies, created once at startup."""

    store: Store
    accounts: AccountStore
    broker: Broker
    public_url: str
    admin_emails: frozenset[str] = field(default_factory=frozenset)
    projects: ProjectStore = field(default_factory=MemoryProjects)
    secrets: SecretBox = field(default_factory=lambda: SecretBox(""))
    github: GitHub = field(default_factory=GitHubAPI)


@dataclass
class Platform:
    services: Services
    registry: NodeRegistry
    bridge: ToolBridge
    workspaces: Workspaces
    hub: Hub

    @classmethod
    def assemble(cls, services: Services) -> Platform:
        registry = NodeRegistry()
        bridge = ToolBridge(services.public_url)
        workspaces = Workspaces(services.accounts, registry, services.projects, services.secrets)
        hub = Hub(workspaces.lead, services.store, services.broker, registry, bridge)
        return cls(services, registry, bridge, workspaces, hub)

    @property
    def accounts(self) -> AccountStore:
        return self.services.accounts

    @property
    def projects(self) -> ProjectStore:
        return self.services.projects

    @property
    def public_url(self) -> str:
        return self.services.public_url.rstrip("/")


def platform(request: Request | WebSocket) -> Platform:
    state: Platform = request.app.state.platform
    return state
