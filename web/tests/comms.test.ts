// Run with: cd web && node --test tests/
import assert from "node:assert/strict";
import { test } from "node:test";
import { activityFeed, answerLabel, approvalBody, chatTimeline, pendingForUser, recipientLabel, taskTree, upsertById } from "../src/lib/comms.ts";
import type { Mail, Message, Question, Task } from "../src/lib/api.ts";

const task = (id: string, parent = ""): Task => ({ id, assignee: "dev", instruction: id, depends_on: [], status: "pending", output: "", parent });
const question = (patch: Partial<Question>): Question => ({
  id: "q1", asker: "dev-a", task_id: "t1", audience: "user", kind: "question", text: "?", options: [], detail: {},
  answer: null, status: "open", created_at: "2026-11-14T10:00:00Z", answered_at: null, ...patch,
});

test("taskTree nests sub-tasks under their parent", () => {
  const tree = taskTree([task("t1"), task("t2"), task("t3", "t1"), task("t4", "t3")]);
  assert.deepEqual(tree.map((n) => [n.task.id, n.depth]), [["t1", 0], ["t3", 1], ["t4", 2], ["t2", 0]]);
});

test("taskTree keeps orphans and cycles visible", () => {
  const tree = taskTree([task("t1", "gone"), task("a", "b"), task("b", "a")]);
  assert.deepEqual(tree.map((n) => n.task.id).sort(), ["a", "b", "t1"]);
});

test("pendingForUser keeps open user questions only", () => {
  const list = [question({ id: "q1" }), question({ id: "q2", audience: "lead" }), question({ id: "q3", status: "answered" })];
  assert.deepEqual(pendingForUser(list).map((q) => q.id), ["q1"]);
  assert.deepEqual(pendingForUser(undefined), []);
});

test("upsertById replaces in place and appends new", () => {
  const list = [{ id: 1, v: "a" }, { id: 2, v: "b" }];
  assert.deepEqual(upsertById(list, { id: 1, v: "c" }), [{ id: 1, v: "c" }, { id: 2, v: "b" }]);
  assert.equal(upsertById(list, { id: 3, v: "d" }).length, 3);
});

test("chatTimeline interleaves user questions with messages by time", () => {
  const messages: Message[] = [
    { id: 1, role: "user", content: "hi", created_at: "2026-11-14T09:00:00Z" },
    { id: 2, role: "leader", content: "ok", created_at: "2026-11-14T11:00:00Z" },
  ];
  const items = chatTimeline(messages, [question({}), question({ id: "q2", audience: "lead" })]);
  assert.deepEqual(items.map((i) => (i.kind === "message" ? i.message.id : i.question.id)), [1, "q1", 2]);
});

test("activityFeed mixes lines and mail by time", () => {
  const mail: Mail[] = [{ id: 1, sender: "dev-a", recipient: "dev-b", body: "x", created_at: "2026-11-14T10:00:00Z" }];
  const items = activityFeed([{ at: Date.parse("2026-11-14T09:00:00Z"), text: "a" }, { at: Date.parse("2026-11-14T11:00:00Z"), text: "b" }], mail);
  assert.deepEqual(items.map((i) => i.kind), ["line", "mail", "line"]);
});

test("recipientLabel names broadcast and lead", () => {
  assert.equal(recipientLabel("*"), "全体");
  assert.equal(recipientLabel("lead"), "负责人");
  assert.equal(recipientLabel("dev-b"), "dev-b");
});

test("approvalBody shows a shell command or the input as JSON", () => {
  const bash = approvalBody(question({ kind: "approval", detail: { tool_name: "Bash", input: { command: "rm -rf x" } } }));
  assert.equal(bash.tool, "Bash");
  assert.equal(bash.command, "rm -rf x");
  const edit = approvalBody(question({ kind: "approval", detail: { tool_name: "Edit", input: { file_path: "a" } } }));
  assert.equal(edit.command, null);
  assert.match(edit.input, /"file_path": "a"/);
});

test("answerLabel words approval answers", () => {
  assert.equal(answerLabel(question({ kind: "approval", status: "answered", answer: "allow_always" })), "已总是允许");
  assert.equal(answerLabel(question({ kind: "approval", status: "answered", answer: "deny" })), "已拒绝");
  assert.equal(answerLabel(question({ status: "answered", answer: "用 A" })), "用 A");
  assert.equal(answerLabel(question({ status: "cancelled" })), "已取消");
});

import { canLead, memberType, switchMemberType } from "../src/lib/team.ts";

test("switchMemberType drops the other type's settings", () => {
  const claude = { name: "dev", type: "claude_code", node: "n1", permission_mode: "plan", allowed_tools: ["Bash"], max_turns: 3, can_delegate: true };
  const codex = switchMemberType(claude, "codex");
  assert.deepEqual(codex, { name: "dev", type: "codex", node: "n1", can_delegate: true });
  assert.equal(memberType(codex), "codex");
  assert.equal(canLead(codex), false);
  const back = switchMemberType({ ...codex, sandbox: "read-only" }, "claude_code");
  assert.deepEqual(back, { name: "dev", type: "claude_code", node: "n1", can_delegate: true });
  assert.equal(canLead({ name: "x" }), true);
});

test("switchMemberType drops codex_command and per-type CLI flags", () => {
  const codex = { name: "dev", type: "codex", node: "n1", codex_command: "/opt/codex", extra_args: ["--full-auto"], model: "m" };
  assert.deepEqual(switchMemberType(codex, "claude_code"), { name: "dev", type: "claude_code", node: "n1", model: "m" });
  const claude = { name: "dev", type: "claude_code", node: "n1", claude_command: "c", extra_args: ["--verbose"] };
  assert.deepEqual(switchMemberType(claude, "codex"), { name: "dev", type: "codex", node: "n1" });
  assert.deepEqual(switchMemberType(claude, "claude_code"), claude);
});
