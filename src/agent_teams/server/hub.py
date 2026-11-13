"""Live conversations behind the HTTP API.

The hub owns one :class:`Conversation` per active chat and one responder task
that answers queued messages. Everything observable is written to the store
and published to the broker, so browsers can reconnect and the server can
restart without losing conversations.
"""

from __future__ import annotations

import asyncio
import uuid
from dataclasses import dataclass

import anthropic

from ..agent import Agent
from ..context import RunContext
from ..conversation import Conversation
from ..errors import AgentTeamsError, describe_error
from ..hooks import Event, Hooks, TaskAssigned, TaskFinished, TaskStarted, format_event
from ..leader import Leader
from .broker import Broker
from .models import ConversationView, MessageView, Role, TaskView
from .serde import dump_turn, load_turn
from .store import Store


class UnknownConversation(KeyError):  # noqa: N818 - mirrors KeyError
    """No conversation with that ID."""


@dataclass
class _Live:
    conversation: Conversation
    responder: asyncio.Task[None]


class Hub:
    def __init__(self, lead: Leader | Agent, store: Store, broker: Broker) -> None:
        self.lead = lead
        self.store = store
        self.broker = broker
        self._live: dict[str, _Live] = {}
        self._opening = asyncio.Lock()

    async def create(self, title: str = "") -> ConversationView:
        return await self.store.create_conversation(uuid.uuid4().hex[:12], title)

    async def post(self, conversation_id: str, content: str) -> MessageView:
        """Record the user's message and queue it for the leader; never waits for a reply."""
        live = await self._ensure_live(conversation_id)
        message = await self._record(conversation_id, "user", content)
        live.conversation.post(content)
        return message

    async def messages(self, conversation_id: str) -> list[MessageView]:
        await self.check(conversation_id)
        return await self.store.messages(conversation_id)

    async def tasks(self, conversation_id: str) -> list[TaskView]:
        await self.check(conversation_id)
        return await self.store.tasks(conversation_id)

    async def close(self) -> None:
        for live in self._live.values():
            live.responder.cancel()
            live.conversation.close()
        self._live.clear()

    async def check(self, conversation_id: str) -> None:
        """Raise :class:`UnknownConversation` unless the conversation exists."""
        if not await self.store.exists(conversation_id):
            raise UnknownConversation(conversation_id)

    async def _ensure_live(self, conversation_id: str) -> _Live:
        async with self._opening:
            if conversation_id not in self._live:
                self._live[conversation_id] = await self._resume(conversation_id)
            return self._live[conversation_id]

    async def _resume(self, conversation_id: str) -> _Live:
        await self.check(conversation_id)
        await self.store.interrupt_unfinished(conversation_id)
        first_task = len(await self.store.tasks(conversation_id)) + 1
        conversation = self._open(RunContext(self._hooks(conversation_id)), first_task)
        history = await self.store.history(conversation_id)
        conversation.session.history.extend(load_turn(turn) for turn in history)
        responder = asyncio.create_task(self._respond_forever(conversation_id, conversation))
        return _Live(conversation, responder)

    def _open(self, ctx: RunContext, first_task: int) -> Conversation:
        if isinstance(self.lead, Leader):
            return self.lead.open(ctx, first_task=first_task)
        return self.lead.open(ctx)

    async def _respond_forever(self, conversation_id: str, conversation: Conversation) -> None:
        while True:
            role, content = await _answer(conversation)
            await self._record(conversation_id, role, content)
            turns = [dump_turn(turn) for turn in conversation.session.history]
            await self.store.save_history(conversation_id, turns)

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


async def _answer(conversation: Conversation) -> tuple[Role, str]:
    try:
        return "leader", (await conversation.reply()).output
    except (AgentTeamsError, anthropic.AnthropicError) as exc:
        return "error", describe_error(exc)
