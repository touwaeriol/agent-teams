#!/usr/bin/env node
// Fake `agy`: `--version`, and `acp --process-per-session` speaking ACP (JSON-RPC over stdio) the
// way the Agent Client Protocol describes it, enough for the daemon tests. Records the start
// (argv, a few env names) and every incoming message in $FAKE_AGY_LOG; talks to the platform MCP
// URL with the real MCP client. $FAKE_AGY_AUTH=none: session/new answers -32000 (not logged in).
import { appendFileSync } from "node:fs";
import { createInterface } from "node:readline";
import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { StreamableHTTPClientTransport } from "@modelcontextprotocol/sdk/client/streamableHttp.js";

const argv = process.argv.slice(2);
if (argv.includes("--version")) {
  if (process.env.FAKE_AGY_NO_VERSION) process.exit(1);
  process.stdout.write("1.5.14\n");
  process.exit(0);
}

const log = (entry) => process.env.FAKE_AGY_LOG && appendFileSync(process.env.FAKE_AGY_LOG, JSON.stringify(entry) + "\n");
const write = (msg) => process.stdout.write(JSON.stringify({ jsonrpc: "2.0", ...msg }) + "\n");
const env = {};
for (const k of ["HOME", "AGY_CLI_DISABLE_AUTO_UPDATE", "GEMINI_API_KEY", "AT_CRED_SOCK", "AT_SSH_CONFIG", "GIT_AUTHOR_NAME"]) if (process.env[k] !== undefined) env[k] = process.env[k];
log({ method: "__start", argv, cwd: process.cwd(), env });

const sessions = new Map();
const pending = new Map();
let nextId = 1000;
let cancelled = false;

function request(method, params) {
  const id = nextId++;
  write({ id, method, params });
  return new Promise((resolve) => pending.set(id, resolve));
}

const update = (sessionId, u) => write({ method: "session/update", params: { sessionId, update: u } });

async function callPlatform(sessionId, name, args) {
  const url = sessions.get(sessionId).mcpServers.find((s) => s.name === "platform").url;
  const client = new Client({ name: "fake-agy", version: "1" });
  await client.connect(new StreamableHTTPClientTransport(new URL(url)));
  const r = await client.callTool({ name, arguments: args });
  await client.close();
  return r.content.map((c) => c.text).join("");
}

async function permission(sessionId, toolCall) {
  const options = [
    { optionId: "allow-once", name: "Allow", kind: "allow_once" },
    { optionId: "allow-always", name: "Always allow", kind: "allow_always" },
    { optionId: "reject-once", name: "Reject", kind: "reject_once" },
  ];
  const r = await request("session/request_permission", { sessionId, toolCall, options });
  return r.outcome.outcome === "selected" ? r.outcome.optionId : "cancelled";
}

async function prompt(p) {
  const sid = p.sessionId;
  const text = p.prompt.find((b) => b.type === "text")?.text ?? "";
  cancelled = false;
  if (text.includes("quota")) throw Object.assign(new Error("RESOURCE_EXHAUSTED: quota exceeded for model"), { code: -32603 });
  if (text.startsWith("/compact")) {
    update(sid, { sessionUpdate: "agent_message_chunk", content: { type: "text", text: "compacted" } });
    return { stopReason: "end_turn" };
  }
  update(sid, { sessionUpdate: "agent_thought_chunk", content: { type: "text", text: "thinking…" } });
  let reply = `echo: ${text}`;
  if (text.includes("plan")) {
    update(sid, { sessionUpdate: "plan", entries: [{ content: "read the code", priority: "high", status: "completed" }, { content: "write tests", priority: "medium", status: "in_progress" }] });
  }
  if (text.includes("tool")) {
    update(sid, { sessionUpdate: "tool_call", toolCallId: "call_p", title: "platform/complete_task", kind: "other", status: "in_progress", rawInput: { task_id: "t3" } });
    reply = `tool: ${await callPlatform(sid, "complete_task", { task_id: "t3", summary: "agy done" })}`;
    update(sid, { sessionUpdate: "tool_call_update", toolCallId: "call_p", status: "completed" });
  }
  if (text.includes("write")) {
    const tc = { toolCallId: "call_w", title: "Write notes.txt", kind: "edit", status: "pending", locations: [{ path: `${process.cwd()}/notes.txt` }], rawInput: { file_path: `${process.cwd()}/notes.txt`, content: "x" } };
    update(sid, { sessionUpdate: "tool_call", ...tc });
    const choice = await permission(sid, tc);
    update(sid, { sessionUpdate: "tool_call_update", toolCallId: "call_w", status: choice.startsWith("allow") ? "completed" : "failed" });
    reply = `permission: ${choice}`;
  }
  const run = /run (.+)$/.exec(text);
  if (run) {
    const tc = { toolCallId: "call_x", title: run[1], kind: "execute", status: "pending", rawInput: { command: run[1] } };
    update(sid, { sessionUpdate: "tool_call", ...tc });
    const choice = await permission(sid, tc);
    if (choice.startsWith("allow")) update(sid, { sessionUpdate: "tool_call_update", toolCallId: "call_x", status: "in_progress" });
    update(sid, { sessionUpdate: "tool_call_update", toolCallId: "call_x", status: choice.startsWith("allow") ? "completed" : "failed" });
    reply = `permission: ${choice}`;
  }
  if (text.includes("sneaky")) {
    // a write that never asked (as if agy's settings auto-approved it)
    update(sid, { sessionUpdate: "tool_call", toolCallId: "call_s", title: "Edit main.go", kind: "edit", status: "in_progress", locations: [{ path: `${process.cwd()}/main.go` }] });
    for (let i = 0; i < 50 && !cancelled; i++) await new Promise((r) => setTimeout(r, 20));
    return { stopReason: cancelled ? "cancelled" : "end_turn" };
  }
  if (text.includes("hold")) {
    for (let i = 0; i < 250 && !cancelled; i++) await new Promise((r) => setTimeout(r, 20));
    return { stopReason: cancelled ? "cancelled" : "end_turn" };
  }
  update(sid, { sessionUpdate: "agent_message_chunk", content: { type: "text", text: reply.slice(0, 6) } });
  update(sid, { sessionUpdate: "agent_message_chunk", content: { type: "text", text: reply.slice(6) } });
  return { stopReason: cancelled ? "cancelled" : "end_turn" };
}

const authError = () => Object.assign(new Error("Authentication required"), { code: -32000 });

const handlers = {
  initialize: () => ({
    protocolVersion: 1,
    agentCapabilities: {
      loadSession: true, promptCapabilities: { image: true, audio: false, embeddedContext: true },
      mcpCapabilities: { http: argv.includes("--process-per-session"), sse: false },
      sessionCapabilities: process.env.FAKE_AGY_NO_RESUME ? {} : { resume: {}, close: {}, load: {}, list: {} },
      _meta: { antigravity: { tier: true, systemPrompt: { append: true } } },
    },
    agentInfo: { name: "Antigravity ACP Server", version: "1.5.14" },
    authMethods: [{ id: "oauth-personal", name: "Google" }],
  }),
  "session/new": (p) => {
    if (process.env.FAKE_AGY_AUTH === "none") throw authError();
    const sessionId = `agy-sess-${sessions.size + 1}`;
    sessions.set(sessionId, p);
    return { sessionId, models: { currentModelId: "gemini-default", availableModels: [{ modelId: "gemini-default", name: "Default" }, { modelId: "gemini-4-neon-high", name: "Neon" }] } };
  },
  "session/resume": (p) => {
    if (p.sessionId === "agy-missing") throw Object.assign(new Error("session not found"), { code: -32602 });
    sessions.set(p.sessionId, p);
    return { models: { currentModelId: "gemini-default" } };
  },
  "session/load": (p) => {
    if (p.sessionId === "agy-missing") throw Object.assign(new Error("session not found"), { code: -32602 });
    sessions.set(p.sessionId, p);
    update(p.sessionId, { sessionUpdate: "user_message_chunk", content: { type: "text", text: "old question" } });
    update(p.sessionId, { sessionUpdate: "agent_message_chunk", content: { type: "text", text: "old answer" } });
    return {};
  },
  "session/set_model": (p) => {
    if (p.modelId === "bad-model") throw Object.assign(new Error(`unknown model ${p.modelId}`), { code: -32602 });
    return {};
  },
  "session/prompt": prompt,
  "session/close": () => ({}),
};

createInterface({ input: process.stdin }).on("line", async (line) => {
  const msg = JSON.parse(line);
  if (msg.id !== undefined && !msg.method) return pending.get(msg.id)?.(msg.result), pending.delete(msg.id);
  log({ method: msg.method, params: msg.params });
  if (msg.method === "session/cancel") {
    cancelled = true;
    return;
  }
  if (msg.id === undefined) return;
  try {
    const h = handlers[msg.method];
    if (!h) throw Object.assign(new Error(`method not found: ${msg.method}`), { code: -32601 });
    write({ id: msg.id, result: await h(msg.params) });
  } catch (err) {
    write({ id: msg.id, error: { code: err.code ?? -32603, message: err.message } });
  }
});
