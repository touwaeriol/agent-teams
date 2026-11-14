"""API and storage records."""

from __future__ import annotations

from datetime import datetime
from typing import Any, Literal

from pydantic import BaseModel

from ..types import FINAL_STATUSES, Task, TaskStatus

Role = Literal["user", "leader", "error"]


class ConversationView(BaseModel):
    id: str
    title: str
    created_at: datetime
    project_id: str | None = None


class MessageView(BaseModel):
    id: int
    role: Role
    content: str
    created_at: datetime


class TaskView(BaseModel):
    id: str
    assignee: str
    instruction: str
    depends_on: list[str]
    status: TaskStatus
    output: str

    @property
    def finished(self) -> bool:
        return self.status in FINAL_STATUSES

    def to_task(self) -> Task:
        return Task(
            self.id,
            self.assignee,
            self.instruction,
            tuple(self.depends_on),
            self.status,
            self.output,
        )

    @classmethod
    def of(cls, task: Task) -> TaskView:
        return cls(
            id=task.id,
            assignee=task.assignee,
            instruction=task.instruction,
            depends_on=list(task.depends_on),
            status=task.status,
            output=task.output,
        )


class NewConversation(BaseModel):
    title: str = ""
    project_id: str | None = None


class NewMessage(BaseModel):
    content: str


class Login(BaseModel):
    token: str


UserRole = Literal["admin", "user"]


class UserView(BaseModel):
    id: str
    email: str
    role: UserRole
    active: bool
    max_nodes: int | None
    created_at: datetime


class NodeView(BaseModel):
    id: str
    name: str
    created_at: datetime


class PlatformSettings(BaseModel):
    registration_open: bool = True
    default_max_nodes: int = 5


class Credentials(BaseModel):
    email: str
    password: str


class NewNode(BaseModel):
    name: str


class TeamConfig(BaseModel):
    config: str = ""
    team: dict[str, Any] | None = None  # structured form; wins over ``config``


class UserUpdate(BaseModel):
    role: UserRole | None = None
    active: bool | None = None
    max_nodes: int | None = None
    clear_max_nodes: bool = False


GitProvider = Literal["github"]


class GitConnectionView(BaseModel):
    """A saved Git credential; the token itself never leaves the server."""

    id: str
    name: str
    provider: GitProvider
    login: str
    created_at: datetime


class NewGitConnection(BaseModel):
    name: str = ""
    provider: GitProvider = "github"
    token: str


class ProjectView(BaseModel):
    id: str
    name: str
    description: str
    context: str
    repo: str | None  # owner/repo
    branch: str
    git_connection_id: str | None
    created_at: datetime
    updated_at: datetime


class ProjectInput(BaseModel):
    name: str
    description: str = ""
    context: str = ""
    repo: str | None = None  # GitHub URL, git@ URL or owner/repo
    branch: str = ""
    git_connection_id: str | None = None


class ProjectUpdate(BaseModel):
    name: str | None = None
    description: str | None = None
    context: str | None = None
    repo: str | None = None
    branch: str | None = None
    git_connection_id: str | None = None
    clear_repo: bool = False
