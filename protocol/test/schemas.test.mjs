import { test } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { join } from "node:path";
import { ROOT, messageSchemas, readJson, schemaFiles, loadSchema, schemaId, toolSchemas } from "../scripts/lib.mjs";
import {
  bodyValidator, createAjv, describeErrors, messageValidator, officeValidator, toolValidator,
} from "../scripts/validator.mjs";

const ajv = createAjv();
const frames = readJson(join(ROOT, "examples/messages.json"));
const invalidFrames = readJson(join(ROOT, "examples/invalid-messages.json"));
const toolExamples = readJson(join(ROOT, "examples/tools.json"));

// Message types protocol.md defines (sections 2-9); run.* and protocol 1 are out of scope (architecture.md 14).
const EXPECTED_TYPES = {
  console_to_node: [
    "welcome", "session.open", "session.release", "session.send", "session.compact", "session.interrupt", "session.set_mode",
    "session.close", "session.watch", "approval.resolve", "tool.result", "file.download", "git.prepare", "git.status", "git.push",
    "git.cleanup", "git.purge", "cred.response", "node.config", "node.upgrade", "node.drain", "node.usage_refresh",
  ],
  node_to_console: [
    "hello", "sessions.report", "session.opened", "session.delivered", "session.compacted", "session.interrupted", "session.closed",
    "session.event", "session.state", "stream.delta", "approval.request", "approval.ack", "approval.cancel",
    "tool.call", "tool.cancel", "file.chunk", "git.result", "cred.request", "node.stats", "node.usage", "node.upgrade_result", "log",
  ],
  both: ["ack", "error"],
};

// Platform tool sets per tier (architecture.md 6.3).
const EXPECTED_TOOLS = {
  coordinator: ["open_thread", "list_threads", "thread_status", "message_thread", "forward_to_thread", "ask_user", "remember", "set_status",
    "memory_list", "memory_read", "memory_write", "memory_delete", "list_deliverables"],
  lead: [
    "update_plan", "assign_task", "list_tasks", "cancel_task", "reply_to", "review_permission", "broadcast", "send_message", "ask_user",
    "enqueue_merge", "open_pr", "finish", "publish_deliverable", "publish_file", "set_status", "tell_user",
    "memory_list", "memory_read", "memory_write", "memory_delete", "list_deliverables", "read_deliverable",
  ],
  member: [
    "complete_task", "push_branch", "report_progress", "ask_lead", "ask_user", "send_message", "read_messages",
    "list_team", "get_task", "request_review", "publish_deliverable", "publish_file", "set_status", "assign_subtask", "wait_task",
    "memory_list", "memory_read", "list_deliverables", "read_deliverable",
  ],
};
const BLOCKING = { member: ["ask_lead", "ask_user", "request_review", "wait_task"], lead: [], coordinator: [] };

function sorted(list) {
  return [...list].sort();
}

test("source schemas are formatted and generated files are fresh", () => {
  for (const script of ["fmt-schemas.mjs", "bundle.mjs", "gen-ts.mjs"]) {
    execFileSync(process.execPath, [join(ROOT, "scripts", script), "--check"], { stdio: "pipe" });
  }
});

test("every schema $id matches its path", () => {
  for (const rel of [...schemaFiles("node"), ...schemaFiles("tools")]) {
    assert.equal(loadSchema(rel).$id, schemaId(rel), rel);
  }
});

test("office schema is the copy of docs/v2/office-layout.schema.json", () => {
  const docs = readJson(join(ROOT, "../docs/v2/office-layout.schema.json"));
  assert.deepEqual(loadSchema("office/office-layout.schema.json"), docs);
});

test("message types and directions match protocol.md", () => {
  const actual = { console_to_node: [], node_to_console: [], both: [] };
  for (const m of messageSchemas()) actual[m.schema["x-direction"]].push(m.type);
  for (const dir of Object.keys(EXPECTED_TYPES)) assert.deepEqual(sorted(actual[dir]), sorted(EXPECTED_TYPES[dir]), dir);
});

test("reliability matches protocol.md 2.1", () => {
  const unreliable = ["hello", "welcome", "ack", "error", "stream.delta", "log", "node.stats", "node.usage", "node.usage_refresh", "cred.request", "cred.response"];
  for (const m of messageSchemas()) assert.equal(m.schema["x-reliable"], !unreliable.includes(m.type), m.type);
});

test("every message type has a valid example frame", () => {
  const validate = messageValidator(ajv);
  const covered = new Set();
  for (const frame of frames) {
    assert.ok(validate(frame), `${frame.type}: ${describeErrors(validate)}`);
    const body = bodyValidator(ajv, frame.type);
    assert.ok(body(frame.body), `${frame.type} body: ${describeErrors(body)}`);
    covered.add(frame.type);
  }
  const missing = messageSchemas().map((m) => m.type).filter((t) => !covered.has(t));
  assert.deepEqual(missing, []);
});

test("invalid frames are rejected", () => {
  const validate = messageValidator(ajv);
  for (const { why, frame } of invalidFrames) assert.equal(validate(frame), false, `accepted: ${why}`);
});

test("tool tiers and blocking flags match architecture.md 6.3", () => {
  const byTier = { coordinator: [], lead: [], member: [] };
  const blocking = { coordinator: [], lead: [], member: [] };
  for (const { name, schema } of toolSchemas()) {
    for (const tier of schema["x-tool"].tiers) byTier[tier].push(name);
    for (const tier of schema["x-tool"].blocking) blocking[tier].push(name);
  }
  for (const tier of Object.keys(EXPECTED_TOOLS)) {
    assert.deepEqual(sorted(byTier[tier]), sorted(EXPECTED_TOOLS[tier]), tier);
    assert.deepEqual(sorted(blocking[tier]), sorted(BLOCKING[tier]), `${tier} blocking`);
  }
});

test("tool examples: valid inputs pass, invalid ones fail, every tool covered", () => {
  const names = toolSchemas().map((t) => t.name);
  assert.deepEqual(sorted(Object.keys(toolExamples.valid)), sorted(names));
  for (const [name, inputs] of Object.entries(toolExamples.valid)) {
    const validate = toolValidator(ajv, name);
    for (const input of inputs) assert.ok(validate(input), `${name}: ${describeErrors(validate)}`);
  }
  for (const [name, inputs] of Object.entries(toolExamples.invalid)) {
    const validate = toolValidator(ajv, name);
    for (const input of inputs) assert.equal(validate(input), false, `${name} accepted ${JSON.stringify(input)}`);
  }
});

test("manifest input_schema is self-contained and accepts the same examples", () => {
  const manifest = readJson(join(ROOT, "gen/manifest.json"));
  for (const tool of manifest.tools) {
    assert.ok(!JSON.stringify(tool.input_schema).includes("$ref"), `${tool.name} has $ref`);
    const validate = ajv.compile(tool.input_schema);
    for (const input of toolExamples.valid[tool.name]) assert.ok(validate(input), `${tool.name}: ${describeErrors(validate)}`);
  }
});

test("the office example validates", () => {
  const validate = officeValidator(ajv);
  assert.ok(validate(readJson(join(ROOT, "../docs/v2/office.example.json"))), describeErrors(validate));
});
