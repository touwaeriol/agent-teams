"""A non-blocking conversation: anyone can post, one consumer answers in order.

The user and the task board both post messages; :meth:`Conversation.reply`
answers them one at a time against a single :class:`Session`, so the history
stays consistent while posting never waits for the agent.
"""

from __future__ import annotations

import asyncio
from typing import TYPE_CHECKING

from .tasks import TaskBoard, task_report
from .types import Result, Task

if TYPE_CHECKING:
    from .agent import Session


class Conversation:
    def __init__(self, session: Session, board: TaskBoard | None = None) -> None:
        self.session = session
        self.board = board
        self._inbox: asyncio.Queue[str] = asyncio.Queue()
        self._pending = 0
        self._settled = asyncio.Event()
        self._settled.set()
        if board is not None:
            board.listeners.append(self._on_task_finished)

    @property
    def name(self) -> str:
        return self.session.agent.name

    @property
    def idle(self) -> bool:
        """No unanswered messages and no background work in flight."""
        return self._pending == 0 and not (self.board and self.board.busy)

    def post(self, message: str) -> None:
        """Queue a message without waiting for the answer."""
        self._pending += 1
        self._settled.clear()
        self._inbox.put_nowait(message)

    async def reply(self) -> Result:
        """Wait for the next message and answer it."""
        message = await self._inbox.get()
        try:
            return await self.session.send(message)
        finally:
            self._pending -= 1
            if self.idle:
                self._settled.set()

    async def drain(self) -> None:
        """Wait until every message is answered and background work is done.

        Someone else must be consuming :meth:`reply` meanwhile. Background work
        always ends with a report message, so settling is checked after replies.
        """
        await self._settled.wait()

    def reset(self) -> None:
        self.session.reset()

    def status(self) -> str:
        return self.board.summary() if self.board else "No task board."

    def close(self) -> None:
        if self.board is not None:
            self.board.close()

    def _on_task_finished(self, task: Task) -> None:
        self.post(task_report(task))
