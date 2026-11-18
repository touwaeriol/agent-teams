#!/usr/bin/env node
// Fake `codex app-server`: JSON-RPC over stdio, enough of the protocol for the daemon tests.
// Records every request in $FAKE_CODEX_LOG; talks to the platform MCP URL with the real MCP client.
import { appendFileSync } from "node:fs";
import { createInterface } from "node:readline";
import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { StreamableHTTPClientTransport } from "@modelcontextprotocol/sdk/client/streamableHttp.js";

const threads = new Map();
const pending = new Map();
let nextServerId = 1000;
/** Set by a turn whose text has "steerfail": turn/steer is refused, as after the turn ended. */
let steerFails = false;

const write = (msg) => process.stdout.write(JSON.stringify(msg) + "\n");
const log = (entry) => process.env.FAKE_CODEX_LOG && appendFileSync(process.env.FAKE_CODEX_LOG, JSON.stringify(entry) + "\n");

function serverRequest(method, params) {
  const id = nextServerId++;
  write({ id, method, params });
  return new Promise((resolve) => pending.set(id, resolve));
}

async function mcp(threadId) {
  const url = threads.get(threadId).config.mcp_servers.platform.url;
  const client = new Client({ name: "fake-codex", version: "1" });
  await client.connect(new StreamableHTTPClientTransport(new URL(url)));
  return client;
}

async function runTurn(threadId, turnId, text) {
  const note = (method, params) => write({ method, params: { threadId, turnId, ...params } });
  note("turn/started", { turn: { id: turnId, status: "inProgress", items: [] } });
  let reply = `echo: ${text.replace(/^<!-- m_[^>]* -->\n/, "")}`;
  if (text.includes("approve")) {
    note("item/started", { item: { type: "commandExecution", id: "item_1", command: "rm -rf build", status: "inProgress" }, startedAtMs: Date.now() });
    const res = await serverRequest("item/commandExecution/requestApproval", { threadId, turnId, itemId: "item_1", command: "rm -rf build", cwd: "/x", startedAtMs: Date.now() });
    reply = `decision: ${res.decision}`;
    note("item/completed", { item: { type: "commandExecution", id: "item_1", command: "rm -rf build", status: res.decision === "decline" ? "declined" : "completed" }, completedAtMs: Date.now() });
  }
  if (text.includes("tool")) {
    const client = await mcp(threadId);
    const r = await client.callTool({ name: "complete_task", arguments: { task_id: "t3", summary: "codex done" } });
    reply = `tool: ${r.content.map((c) => c.text).join("")}`;
    await client.close();
  }
  if (text.includes("steerfail")) steerFails = true;
  if (text.includes("autocompact")) {
    // the runtime compacts mid-turn by itself; the daemon may steer the anchor in
    compaction(threadId, turnId);
    await new Promise((r) => setTimeout(r, 100));
  }
  note("item/completed", { item: { type: "agentMessage", id: "msg_1", text: reply }, completedAtMs: Date.now() });
  usage(threadId, turnId, 12000);
  note("turn/completed", { turn: { id: turnId, status: "completed", items: [], error: null } });
}

function usage(threadId, turnId, tokens) {
  const b = { totalTokens: tokens, inputTokens: tokens - 100, cachedInputTokens: 0, cacheWriteInputTokens: 0, outputTokens: 100, reasoningOutputTokens: 0 };
  write({ method: "thread/tokenUsage/updated", params: { threadId, turnId, tokenUsage: { total: b, last: b, modelContextWindow: 272000 } } });
}

function compaction(threadId, turnId) {
  const note = (method, params) => write({ method, params: { threadId, turnId, ...params } });
  note("item/started", { item: { type: "contextCompaction", id: "cc_1" }, startedAtMs: Date.now() });
  note("item/completed", { item: { type: "contextCompaction", id: "cc_1" }, completedAtMs: Date.now() });
  note("thread/compacted", {});
}

/** thread/compact/start: a turn holding only the compaction, like the app-server. */
async function compactTurn(threadId) {
  const turnId = `turn_${Math.random().toString(36).slice(2, 8)}`;
  const note = (method, params) => write({ method, params: { threadId, turnId, ...params } });
  note("turn/started", { turn: { id: turnId, status: "inProgress", items: [] } });
  compaction(threadId, turnId);
  usage(threadId, turnId, 3000);
  note("turn/completed", { turn: { id: turnId, status: "completed", items: [], error: null } });
}

const handlers = {
  initialize: () => ({ userAgent: "fake-codex/0.169.0" }),
  "thread/start": (p) => {
    const id = `th_${threads.size + 1}`;
    threads.set(id, p);
    // Like the real app-server: no model → its own default; model "" is rejected.
    if (p.model === "") throw new Error("No models are available. Set `model` explicitly or check your model catalog configuration.");
    return { thread: { id }, model: p.model ?? "fake-codex-default" };
  },
  "thread/resume": (p) => {
    if (p.threadId === "th_missing") throw new Error("thread not found");
    threads.set(p.threadId, p);
    return { thread: { id: p.threadId }, model: p.model ?? "fake-codex-default" };
  },
  "mcpServerStatus/list": async (p) => {
    const client = await mcp(p.threadId);
    const { tools } = await client.listTools();
    await client.close();
    return { data: [{ name: "platform", tools: Object.fromEntries(tools.map((t) => [t.name, t])), toolsError: null }], nextCursor: null };
  },
  "turn/start": (p) => {
    const turnId = `turn_${Math.random().toString(36).slice(2, 8)}`;
    setTimeout(() => void runTurn(p.threadId, turnId, p.input[0].text), 20);
    return { turn: { id: turnId, status: "inProgress", items: [] } };
  },
  "turn/steer": (p) => {
    if (steerFails) throw new Error("no active turn to steer");
    return { turnId: p.expectedTurnId };
  },
  "thread/compact/start": (p) => {
    // late enough for a message sent right after to run its own turn first
    setTimeout(() => void compactTurn(p.threadId), 300);
    return {};
  },
  "thread/read": (p) => ({ thread: { id: p.threadId, turns: [{ items: [{ type: "userMessage", content: [{ type: "text", text: "<!-- m_seen -->\nold" }] }] }] } }),
  "turn/interrupt": () => ({}),
};

createInterface({ input: process.stdin }).on("line", async (line) => {
  const msg = JSON.parse(line);
  if (msg.id !== undefined && !msg.method) return pending.get(msg.id)?.(msg.result), pending.delete(msg.id);
  if (msg.method) log({ method: msg.method, params: msg.params });
  if (msg.id === undefined) return;
  try {
    write({ id: msg.id, result: await handlers[msg.method](msg.params) });
  } catch (err) {
    write({ id: msg.id, error: { code: -32000, message: err.message } });
  }
});
