"""Employee and leader team tools on a task board with comms."""

from __future__ import annotations

import asyncio
import itertools
import json
from collections.abc import Awaitable, Callable, Mapping
from dataclasses import dataclass, field
from typing import Any

import pytest

from agent_teams import Hooks, RunContext, TaskBoard, Toolkit
from agent_teams.comms import Comms
from agent_teams.conversation import Conversation
from agent_teams.hooks import WorkerProgress
from agent_teams.mcp_config import ToolEndpoint
from agent_teams.runnable import Runnable
from agent_teams.tasks import REATTACHED_NOTE
from agent_teams.team_tools import leader_tools
from agent_teams.types import Result, Task, ToolCall

from .fakes import Worker

Call = Callable[[str, Mapping[str, Any]], Awaitable[str]]
Script = Callable[[Call, str], Awaitable[str]]


class FakeServer:
    """Stands in for the MCP bridge: keeps the toolkits by token."""

    def __init__(self) -> None:
        self.bindings: dict[str, tuple[Toolkit, RunContext]] = {}
        self.exposed: list[ToolEndpoint] = []
        self._tokens = itertools.count(1)

    def expose(self, toolkit: Toolkit, ctx: RunContext) -> ToolEndpoint:
        token = f"tok{next(self._tokens)}"
        self.bindings[token] = (toolkit, ctx)
        endpoint = ToolEndpoint("http://console/mcp", token, tuple(s.name for s in toolkit.specs))
        self.exposed.append(endpoint)
        return endpoint

    def revoke(self, token: str) -> None:
        self.bindings.pop(token, None)

    async def call(self, token: str, name: str, arguments: Mapping[str, Any]) -> str:
        toolkit, ctx = self.bindings[token]
        (result,) = await toolkit.execute([ToolCall("c", name, arguments)], ctx)
        return result.content


@dataclass(eq=False)
class Employee(Runnable):
    """A member that can use team tools; its ``script`` plays the agent."""

    name: str
    server: FakeServer
    script: Script
    description: str = ""
    can_delegate: bool = False
    prompts: list[str] = field(default_factory=list)
    approval_tools: list[str | None] = field(default_factory=list)

    def with_tools(self, endpoint: ToolEndpoint, approval_tool: str | None = None) -> Runnable:
        self.approval_tools.append(approval_tool)
        return _Equipped(self, endpoint)

    async def _run(self, task: str, ctx: RunContext) -> Result:
        raise AssertionError("an employee on a board with comms always runs with tools")


@dataclass(eq=False)
class _Equipped(Runnable):
    employee: Employee
    endpoint: ToolEndpoint

    def __post_init__(self) -> None:
        self.name = self.employee.name
        self.description = self.employee.description

    async def _run(self, task: str, ctx: RunContext) -> Result:
        self.employee.prompts.append(task)

        async def call(name: str, arguments: Mapping[str, Any]) -> str:
            return await self.employee.server.call(self.endpoint.token, name, arguments)

        return Result(self.name, await self.employee.script(call, task))


def calling(name: str, **arguments: Any) -> Script:
    async def script(call: Call, _prompt: str) -> str:
        return await call(name, arguments)

    return script


def board_of(*members: Runnable, hooks: Hooks | None = None) -> tuple[TaskBoard, Comms]:
    comms = Comms()
    server = next(m.server for m in members if isinstance(m, Employee))
    board = TaskBoard(
        members, RunContext(hooks or Hooks(), ("lead",)), comms=comms, tool_server=server
    )
    return board, comms


async def open_question(comms: Comms) -> str:
    while not [q for q in comms.questions if q.status == "open"]:
        await asyncio.sleep(0.01)
    return next(q.id for q in comms.questions if q.status == "open")


async def test_the_toolkit_lives_as_long_as_the_task() -> None:
    server = FakeServer()
    progress: list[WorkerProgress] = []
    hooks = Hooks()
    hooks.on(WorkerProgress, progress.append)

    async def script(call: Call, _prompt: str) -> str:
        await call("report_progress", {"text": "half way"})
        return await call("list_team", {})

    dev = Employee("dev", server, script, description="writes code")
    board, comms = board_of(dev, Worker("qa"), hooks=hooks)
    await comms.send("lead", "dev", "use branch feat/x")

    task = await board.assign("dev", "build it")
    await board.wait()

    (endpoint,) = server.exposed
    assert "assign_subtask" not in endpoint.tools
    assert {"report_progress", "ask_lead", "ask_user", "approve", "request_review"} <= set(
        endpoint.tools
    )
    assert server.bindings == {}  # revoked once the task ended
    assert dev.approval_tools == ["approve"]
    (prompt,) = dev.prompts
    assert prompt.startswith("build it\n\n---\nYour team:\n- lead: the team lead\n- qa: qa worker")
    assert "[from lead] use branch feat/x" in prompt
    assert "report_progress" in prompt
    assert await comms.unread("dev") == []
    assert [(p.path, p.text) for p in progress] == [(("lead", "dev"), "half way")]
    output = board.get(task.id).output
    assert "- dev (you) (Employee): writes code; working on t1" in output
    assert "- qa (Worker): qa worker" in output


async def test_members_without_tools_or_boards_without_comms_run_plainly() -> None:
    server = FakeServer()
    plain = TaskBoard([Employee("dev", server, calling("list_team"))], RunContext())
    worker = Worker("qa")
    board, _comms = board_of(Employee("x", server, calling("list_team")), worker)

    await plain.assign("dev", "x")
    await plain.wait()
    await board.assign("qa", "check")
    await board.wait()

    assert "always runs with tools" in plain.get("t1").output
    assert worker.tasks == ["check"]
    assert server.exposed == []


async def test_ask_lead_waits_for_reply_to() -> None:
    server = FakeServer()
    board, comms = board_of(Employee("dev", server, calling("ask_lead", question="sqlite?")))
    posted: list[str] = []
    comms.notify = posted.append
    reply_to = {t.name: t for t in leader_tools(board, comms)}["reply_to"]

    task = await board.assign("dev", "store data")
    question = await open_question(comms)
    assert board.get(task.id).status == "running"
    answer = await reply_to.handler(
        {"question_id": question, "answer": "use postgres"}, RunContext()
    )
    await board.wait()

    assert answer == "answered q1"
    assert posted == ["[Question q1 from dev · t1]\nsqlite?\n(answer with reply_to)"]
    assert board.get(task.id).output == "use postgres"


async def test_ask_user_and_approve_wait_for_the_user() -> None:
    server = FakeServer()

    async def script(call: Call, _prompt: str) -> str:
        color = await call("ask_user", {"question": "color?", "options": ["red", "blue"]})
        bash = {"tool_name": "Bash", "input": {"command": "rm -rf build"}, "tool_use_id": "x"}
        first = await call("approve", bash)
        second = await call("approve", bash)
        return "\n".join([color, first, second])

    board, comms = board_of(Employee("dev", server, script))
    task = await board.assign("dev", "paint")
    await comms.answer(await open_question(comms), "blue")
    approval = await open_question(comms)
    await comms.answer(approval, "allow_always")
    await board.wait()

    color, first, second = board.get(task.id).output.splitlines()
    assert color == "blue"
    allowed = {"behavior": "allow", "updatedInput": {"command": "rm -rf build"}}
    assert json.loads(first) == json.loads(second) == allowed
    question = comms.question(approval)
    assert (question.kind, question.task_id, question.audience) == ("approval", "t1", "user")


async def test_denied_approval() -> None:
    server = FakeServer()
    script = calling("approve", tool_name="Write", input={"file_path": "a"})
    board, comms = board_of(Employee("dev", server, script))
    task = await board.assign("dev", "write")
    await comms.answer(await open_question(comms), "deny")
    await board.wait()

    assert json.loads(board.get(task.id).output) == {
        "behavior": "deny",
        "message": "用户拒绝了这个操作",
    }


async def test_messages_between_employees() -> None:
    server = FakeServer()

    async def sender(call: Call, _prompt: str) -> str:
        outcomes = [
            await call("send_message", {"to": "b", "text": "hello b"}),
            await call("send_message", {"to": "a", "text": "me"}),
            await call("send_message", {"to": "ghost", "text": "?"}),
            await call("send_message", {"to": "lead", "text": "fyi"}),
        ]
        return "\n".join(outcomes)

    async def reader(call: Call, _prompt: str) -> str:
        return await call("read_messages", {}) + "|" + await call("read_messages", {})

    listener = Employee("b", server, reader)
    board, comms = board_of(Employee("a", server, sender), listener)
    posted: list[str] = []
    comms.notify = posted.append
    sent = await board.assign("a", "talk")
    await board.wait()
    read = await board.assign("b", "listen")
    await board.wait()

    outcomes = board.get(sent.id).output.splitlines()
    assert outcomes[0] == "sent"
    assert "yourself" in outcomes[1]
    assert "unknown member 'ghost'" in outcomes[2]
    assert posted == ["[Message from a]\nfyi"]
    # b got the message in its briefing already, so read_messages finds nothing new
    assert board.get(read.id).output == "No new messages.|No new messages."
    assert "[from a] hello b" in listener.prompts[0]


async def test_get_task() -> None:
    server = FakeServer()
    board, _comms = board_of(Employee("dev", server, calling("get_task", task_id="t1")))
    task = await board.assign("dev", "describe yourself")
    await board.wait()

    assert board.get(task.id).output.startswith("t1 · dev · running\n\nInstruction:\n")


async def test_request_review_blocks_until_the_review_is_done() -> None:
    server = FakeServer()
    gate = asyncio.Event()
    reviewer = Worker("qa", gate)

    async def script(call: Call, _prompt: str) -> str:
        mine = await call("request_review", {"reviewer": "dev", "summary": "x"})
        review = await call("request_review", {"reviewer": "qa", "summary": "see PR 7"})
        return f"{mine}\n{review}"

    board, _comms = board_of(Employee("dev", server, script), reviewer)
    finished: list[str] = []
    board.listeners.append(lambda t: finished.append(t.id))
    task = await board.assign("dev", "ship it")
    while len(board.tasks) < 2:
        await asyncio.sleep(0.01)
    await asyncio.sleep(0.05)

    review = board.get("t2")
    assert board.get(task.id).status == "running"
    assert (review.parent, review.assignee) == ("t1", "qa")
    assert reviewer.tasks == ["Review requested by dev (task t1):\n\nsee PR 7"]
    gate.set()
    await board.wait()

    mine, rest = board.get(task.id).output.split("\n", 1)
    assert "cannot review your own work" in mine
    assert rest == "[Review t2 · qa · done]\nqa finished"
    assert finished == ["t2", "t1"]


async def test_delegating_employees_assign_and_wait_for_subtasks() -> None:
    server = FakeServer()

    async def script(call: Call, _prompt: str) -> str:
        assigned = await call("assign_subtask", {"assignee": "helper", "instruction": "part"})
        task_id = assigned.split()[0]
        foreign = await call("wait_task", {"task_id": "t1"})
        return f"{assigned}\n{foreign}\n{await call('wait_task', {'task_id': task_id})}"

    lead = Employee("lead_dev", server, script, can_delegate=True)
    board, _comms = board_of(lead, Worker("helper"))
    task = await board.assign("lead_dev", "big job")
    await board.wait()

    assigned, foreign, result = board.get(task.id).output.split("\n", 2)
    assert assigned == "t2 assigned to helper; use wait_task to get its result."
    assert "t1 is not one of your subtasks" in foreign
    assert result == "[Task t2 · helper · done]\nhelper finished"
    assert board.get("t2").parent == "t1"
    assert {"assign_subtask", "wait_task"} <= set(server.exposed[0].tools)
    assert "assign_subtask" in lead.prompts[0]


class Recorder:
    name = "lead"

    def __init__(self) -> None:
        self.sent: list[str] = []

    async def send(self, message: str) -> Result:
        self.sent.append(message)
        return Result("lead", "ok")

    def reset(self) -> None:
        return None

    def snapshot(self) -> dict[str, Any]:
        return {}

    def restore(self, state: Mapping[str, Any]) -> None:
        return None


async def test_subtasks_do_not_report_to_the_leader() -> None:
    server = FakeServer()
    script = calling("request_review", reviewer="qa", summary="look")
    board, _comms = board_of(Employee("dev", server, script), Worker("qa"))
    session = Recorder()
    conversation = Conversation(session, board)

    await board.assign("dev", "work")
    await board.wait()
    await conversation.reply()

    assert [m.splitlines()[0] for m in session.sent] == ["[Task t1 · dev · done]"]
    assert conversation.idle


async def test_subtasks_left_behind_are_cancelled_when_the_parent_ends() -> None:
    server = FakeServer()
    helper = Worker("helper", asyncio.Event())
    script = calling("assign_subtask", assignee="helper", instruction="part")
    lead = Employee("lead_dev", server, script, can_delegate=True)
    board, _comms = board_of(lead, helper)

    await board.assign("lead_dev", "big job")
    await board.wait()
    await board.wait()

    assert board.get("t1").status == "done"
    assert board.get("t2").status == "cancelled"


async def test_cancelling_the_requester_cancels_its_review() -> None:
    server = FakeServer()
    board, _comms = board_of(
        Employee("dev", server, calling("request_review", reviewer="qa", summary="x")),
        Worker("qa", asyncio.Event()),
    )
    task = await board.assign("dev", "ship it")
    while len(board.tasks) < 2:
        await asyncio.sleep(0.01)

    board.cancel(task.id)
    await board.wait()

    assert [t.status for t in board.tasks] == ["cancelled", "cancelled"]


async def test_orphaned_subtasks_report_to_the_leader() -> None:
    """After a restart the parent may be gone; its adopted subtask reports to the leader."""
    board, _comms = board_of(Employee("dev", FakeServer(), calling("list_team")), Worker("qa"))
    session = Recorder()
    conversation = Conversation(session, board)

    await board.adopt(Task("t2", "qa", "review", parent="t1"))
    await board.wait()
    await conversation.reply()

    assert [m.splitlines()[0] for m in session.sent] == ["[Task t2 · qa · done]"]


async def test_adopted_equipped_tasks_warn_that_their_tools_stopped_working() -> None:
    board, _comms = board_of(Employee("dev", FakeServer(), calling("list_team")), Worker("qa"))

    await board.adopt(Task("t1", "dev", "build", status="running"))
    await board.adopt(Task("t2", "qa", "check", status="running"))
    await board.wait()

    assert board.get("t1").output.endswith(REATTACHED_NOTE)
    assert board.get("t2").output == "qa finished"  # no team tools, nothing to warn about


async def test_finished_tasks_cancel_their_open_questions() -> None:
    server = FakeServer()
    board, comms = board_of(Employee("dev", server, calling("ask_user", question="?")))
    task = await board.assign("dev", "ask")
    question = await open_question(comms)

    board.cancel(task.id)
    await board.wait()
    while comms.question(question).status == "open":
        await asyncio.sleep(0.01)

    assert comms.question(question).status == "cancelled"


async def test_leader_tools() -> None:
    server = FakeServer()
    board, comms = board_of(Employee("dev", server, calling("read_messages")))
    posted: list[str] = []
    comms.notify = posted.append
    tools = {t.name: t for t in leader_tools(board, comms)}
    ctx = RunContext()

    assert await tools["broadcast"].handler({"text": "all hands"}, ctx) == "sent to everyone"
    assert await tools["send_message"].handler({"to": "dev", "text": "hi dev"}, ctx) == "sent"
    with pytest.raises(ValueError, match="unknown member"):
        await tools["send_message"].handler({"to": "nobody", "text": "?"}, ctx)
    steps = [{"title": "plan", "status": "done"}, {"title": "build"}]
    assert await tools["update_plan"].handler({"steps": steps}, ctx) == "plan updated (2 steps)"
    asked = await tools["ask_user"].handler({"question": "go?", "options": ["yes"]}, ctx)
    with pytest.raises(ValueError, match="for the user"):
        await tools["reply_to"].handler({"question_id": "q1", "answer": "x"}, ctx)
    await comms.answer("q1", "yes")

    assert asked.startswith("q1 asked")
    assert comms.plan == [{"title": "plan", "status": "done"}, {"title": "build", "status": "todo"}]
    assert [(m.recipient, m.body) for m in comms.mail] == [("*", "all hands"), ("dev", "hi dev")]
    assert posted == ["[Answer to q1] yes"]
    assert comms.question("q1").options == ("yes",)
