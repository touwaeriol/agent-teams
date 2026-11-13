"""API and storage records."""

from __future__ import annotations

from datetime import datetime
from typing import Literal

from pydantic import BaseModel

from ..types import Task, TaskStatus

Role = Literal["user", "leader", "error"]


class ConversationView(BaseModel):
    id: str
    title: str
    created_at: datetime


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


class NewMessage(BaseModel):
    content: str


class Login(BaseModel):
    token: str
