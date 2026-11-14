"""Live conversations behind the HTTP API, for many users.

Every conversation has an owner; its leader comes from that owner's team.
The hub keeps one :class:`Conversation` and one responder task per active
chat. Everything observable is written to the store and published to the
broker, so browsers can reconnect and the server can restart without losing
conversations.
"""

from __future__ import annotations

import asyncio
import logging
import uuid
from collections.abc import Awaitable, Callable
from dataclasses import dataclass, field

import anthropic

from ..agent import Agent
from ..claude_session import ToolEndpoint, ToolServer
from ..context import RunContext
from ..conversation import Conversation
from ..errors import AgentTeamsError, describe_error
from ..hooks import Event, Hooks, TaskAssigned, TaskFinished, TaskStarted, format_event
from ..leader import Leader
from ..remote import NodeRegistry
from ..tools import Toolkit
from .broker import Broker
from .models import ConversationView, MessageView, Role, TaskView
from .store import INTERRUPTED, Store

logger = logging.getLogger(__name__)

LeadProvider = Callable[[str], Awaitable[Leader | Agent]]


class UnknownConversation(KeyError):  # noqa: N818 - mirrors KeyError
    """No conversation with that ID for this user."""


class _ScopedTools:
    """Hands out MCP endpoints for one conversation and revokes them together."""

    def __init__(self, server: ToolServer | None) -> None:
        self._server = server
        self.tokens: list[str] = []

    def expose(self, toolkit: Toolkit, ctx: RunContext) -> ToolEndpoint:
        if self._server is None:
            raise AgentTeamsError("this service has no MCP endpoint for Claude Code leaders")
        endpoint = self._server.expose(toolkit, ctx)
        self.tokens.append(endpoint.token)
        return endpoint


@dataclass
class _Live:
    owner: str
    conversation: Conversation
    responder: asyncio.Task[None]
    tools: _ScopedTools = field(repr=False)


class Hub:
    def __init__(
        self,
        leads: LeadProvider,
        store: Store,
        broker: Broker,
        registry: NodeRegistry | None = None,
        tool_server: ToolServer | None = None,
    ) -> None:
        self.leads = leads
        self.store = store
        self.broker = broker
        self.registry = registry or NodeRegistry()
        self.tool_server = tool_server
        self._live: dict[str, _Live] = {}
        self._opening = asyncio.Lock()

    async def create(self, user_id: str, title: str = "") -> ConversationView:
        return await self.store.create_conversation(uuid.uuid4().hex[:12], title, user_id)

    async def conversations(self, user_id: str) -> list[ConversationView]:
        return await self.store.conversations(user_id)

    async def post(self, user_id: str, conversation_id: str, content: str) -> MessageView:
        """Record the user's message and queue it for the leader; never waits for a reply."""
        await self.check(user_id, conversation_id)
        live = await self._ensure_live(conversation_id, user_id)
        message = await self._record(conversation_id, "user", content)
        live.conversation.post(content)
        return message

    async def messages(self, user_id: str, conversation_id: str) -> list[MessageView]:
        await self.check(user_id, conversation_id)
        return await self.store.messages(conversation_id)

    async def tasks(self, user_id: str, conversation_id: str) -> list[TaskView]:
        await self.check(user_id, conversation_id)
        return await self.store.tasks(conversation_id)

    async def check(self, user_id: str, conversation_id: str) -> None:
        """Raise :class:`UnknownConversation` unless ``user_id`` owns the conversation."""
        if await self.store.owner(conversation_id) != user_id:
            raise UnknownConversation(conversation_id)

    async def recover(self) -> None:
        """After a restart: resume every conversation that still has unfinished tasks."""
        for conversation_id in await self.store.unfinished_conversations():
            owner = await self.store.owner(conversation_id)
            if owner is None:
                continue
            try:
                await self._ensure_live(conversation_id, owner)
            except AgentTeamsError as exc:
                logger.warning("cannot resume conversation %s: %s", conversation_id, exc)

    async def close(self) -> None:
        for conversation_id in list(self._live):
            self._close(conversation_id)

    async def close_user(self, user_id: str) -> None:
        """End a user's live conversations (e.g. the account was disabled)."""
        for conversation_id in [c for c, live in self._live.items() if live.owner == user_id]:
            self._close(conversation_id)

    def _close(self, conversation_id: str) -> None:
        live = self._live.pop(conversation_id)
        live.responder.cancel()
        live.conversation.close()
        for token in live.tools.tokens:
            revoke = getattr(self.tool_server, "revoke", None)
            if revoke is not None:
                revoke(token)

    async def _ensure_live(self, conversation_id: str, owner: str) -> _Live:
        async with self._opening:
            if conversation_id not in self._live:
                self._live[conversation_id] = await self._resume(conversation_id, owner)
            return self._live[conversation_id]

    async def _resume(self, conversation_id: str, owner: str) -> _Live:
        lead = await self.leads(owner)
        tasks = await self.store.tasks(conversation_id)
        ctx = RunContext(self._hooks(conversation_id), run_key=conversation_id)
        tools = _ScopedTools(self.tool_server)
        conversation = _open(lead, ctx, len(tasks) + 1, tools)
        conversation.session.restore(await self.store.history(conversation_id))
        await self._recover(conversation_id, conversation, [t for t in tasks if not t.finished])
        responder = asyncio.create_task(self._respond_forever(conversation_id, conversation))
        return _Live(owner, conversation, responder, tools)

    async def _recover(
        self, conversation_id: str, conversation: Conversation, unfinished: list[TaskView]
    ) -> None:
        """Re-attach running tasks on daemon nodes; mark everything else interrupted."""
        board = conversation.board
        for view in unfinished:
            if board is not None and view.status == "running" and board.is_resumable(view.assignee):
                await board.adopt(view.to_task())
                continue
            interrupted = view.model_copy(update={"status": "failed", "output": INTERRUPTED})
            await self.store.save_task(conversation_id, interrupted.to_task())

    async def _respond_forever(self, conversation_id: str, conversation: Conversation) -> None:
        while True:
            role, content = await _answer(conversation)
            await self._record(conversation_id, role, content)
            await self.store.save_history(conversation_id, conversation.session.snapshot())

    async def _record(self, conversation_id: str, role: Role, content: str) -> MessageView:
        message = await self.store.add_message(conversation_id, role, content)
        event = {"type": "message", "message": message.model_dump(mode="json")}
        await self.broker.publish(conversation_id, event)
        return message

    def _hooks(self, conversation_id: str) -> Hooks:
        async def on_task(event: TaskAssigned | TaskStarted | TaskFinished) -> None:
            await self.store.save_task(conversation_id, event.task)
            view = TaskView.of(event.task).model_dump(mode="json")
            await self.broker.publish(conversation_id, {"type": "task", "task": view})

        async def on_activity(event: Event) -> None:
            line = format_event(event)
            if line is not None:
                await self.broker.publish(conversation_id, {"type": "activity", "text": line})

        hooks = Hooks()
        hooks.on(TaskAssigned, on_task)
        hooks.on(TaskStarted, on_task)
        hooks.on(TaskFinished, on_task)
        hooks.on(Event, on_activity)
        return hooks


def _open(
    lead: Leader | Agent, ctx: RunContext, first_task: int, tools: ToolServer
) -> Conversation:
    if isinstance(lead, Leader):
        return lead.open(ctx, first_task=first_task, tool_server=tools)
    return lead.open(ctx)


async def _answer(conversation: Conversation) -> tuple[Role, str]:
    try:
        return "leader", (await conversation.reply()).output
    except (AgentTeamsError, anthropic.AnthropicError) as exc:
        return "error", describe_error(exc)
