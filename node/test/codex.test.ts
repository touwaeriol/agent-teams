import { readFileSync, rmSync } from "node:fs";
import { join } from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import { startHarness, type Harness } from "./helpers/daemon.js";

let h: Harness | undefined;
let logFile = "";
afterEach(async () => {
  if (!h) return;
  const invalid = [...h!.console.invalid];
  await h.close();
  h = undefined;
  rmSync(logFile, { force: true });
  expect(invalid).toEqual([]);
});

const FAKE = join(import.meta.dirname, "helpers", "fake-codex.mjs");

function codexOpen(id: string, resume: Record<string, unknown> | null = null) {
  return {
    session_id: id, open_gen: 1, employee: { name: "dev-1", role: "dev", tier: "member", project_id: "p1", thread_no: 3 },
    runtime: "codex", cwd: `projects/p1/threads/3/${id}`, resume, claude: null,
    codex: { model: "gpt-5.5-codex", sandbox: "workspace-write", approval_policy: "on-request", developer_instructions: "be careful", config: { sandbox_workspace_write: { network_access: true } } },
    platform_tools: [{ name: "complete_task", description: "Finish", blocking: false, input_schema: { type: "object", required: ["task_id", "summary"], properties: { task_id: { type: "string" }, summary: { type: "string" } } } }],
    env: { LANG: "C.UTF-8" },
  };
}

async function startCodex() {
  logFile = join(process.env.TMPDIR ?? "/tmp", `fake-codex-${process.pid}-${Date.now()}.jsonl`);
  process.env.FAKE_CODEX_LOG = logFile;
  h = await startHarness({ codexCommand: [process.execPath, FAKE], passEnv: ["FAKE_CODEX_LOG"] });
  return () => readFileSync(logFile, "utf8").trim().split("\n").map((l) => JSON.parse(l) as { method: string; params: Record<string, any> });
}

describe("codex sessions over app-server", () => {
  it("adds the plugins' MCP servers to the thread config, pointing into the written plugin", async () => {
    const calls = await startCodex();
    const plugins = [{ name: "tinyplug", version: 4, files: [{ path: ".claude-plugin/plugin.json", content: '{"name":"tinyplug"}' }, { path: "s.mjs", content: "//" }],
      mcp_servers: { calc: { type: "stdio", command: "node", args: ["${CLAUDE_PLUGIN_ROOT}/s.mjs"] } } }];
    h!.console.command("session.open", { ...codexOpen("s_P1"), plugins });
    await h!.console.waitType("session.opened");
    const servers = calls().find((c) => c.method === "thread/start")!.params.config.mcp_servers;
    const dir = join(h!.opts.root, "agent-plugins", "p1", "3", "dev-1", "tinyplug@4");
    expect(servers.plugin_tinyplug_calc).toEqual({ command: "node", args: [join(dir, "s.mjs")] });
    expect(readFileSync(join(dir, "s.mjs"), "utf8")).toBe("//");
    expect(servers.platform.url).toMatch(/^http:/);
  });

  it("starts a thread with the loopback platform MCP config and runs a turn", async () => {
    const calls = await startCodex();
    h!.console.command("session.open", codexOpen("s_X1"));
    const opened = await h!.console.waitType("session.opened");
    expect(opened.body).toMatchObject({ codex_thread_id: "th_1", init: { tools: ["mcp__platform__complete_task"], mcp_servers: [{ name: "platform", status: "connected" }] } });
    const startParams = calls().find((c) => c.method === "thread/start")!.params;
    expect(startParams).toMatchObject({ model: "gpt-5.5-codex", sandbox: "workspace-write", approvalPolicy: "on-request", developerInstructions: "be careful" });
    expect(startParams.config.mcp_servers.platform).toMatchObject({ default_tools_approval_mode: "approve", tool_timeout_sec: 86400 });
    expect(startParams.config.mcp_servers.platform.url).toMatch(/^http:\/\/127\.0\.0\.1:\d+\/mcp\/s_X1\/[A-Za-z0-9_-]{43}$/);
    expect(startParams.config.sandbox_workspace_write).toEqual({ network_access: true });
    h!.console.command("session.release", { session_id: "s_X1", open_gen: 1 });
    h!.console.command("session.send", { session_id: "s_X1", msg_id: "m_c1", text: "hello codex" });
    const d = await h!.console.waitType("session.delivered", (b) => b.msg_id === "m_c1");
    expect(d.body.turn_id).toMatch(/^t_turn_/);
    const text = await h!.console.waitType("session.event", (b) => b.kind === "text" && b.final);
    expect(text.body.text).toBe("echo: hello codex");
    expect(calls().find((c) => c.method === "turn/start")!.params.input[0].text).toBe("<!-- m_c1 -->\nhello codex");
    await h!.console.waitType("session.state", (b) => b.state === "active" && b.session_id === "s_X1");
  });

  it("leaves an empty model out of thread/start so Codex picks its own default, and reports that model", async () => {
    const calls = await startCodex();
    const open = codexOpen("s_XE");
    h!.console.command("session.open", { ...open, codex: { ...open.codex, model: "" } });
    const opened = await h!.console.waitType("session.opened");
    expect(opened.body.init.model).toBe("fake-codex-default");
    expect("model" in calls().find((c) => c.method === "thread/start")!.params).toBe(false);
    h!.console.command("session.release", { session_id: "s_XE", open_gen: 1 });
    h!.console.command("session.send", { session_id: "s_XE", msg_id: "m_e1", text: "hi" });
    expect((await h!.console.waitType("session.event", (b) => b.kind === "turn_ended")).body.model).toBe("fake-codex-default");
  });

  it("runs the next turn with the model set by session.set_mode and reports it", async () => {
    const calls = await startCodex();
    h!.console.command("session.open", codexOpen("s_XM"));
    await h!.console.waitType("session.opened");
    h!.console.command("session.release", { session_id: "s_XM", open_gen: 1 });
    h!.console.command("session.send", { session_id: "s_XM", msg_id: "m_x1", text: "one" });
    expect((await h!.console.waitType("session.event", (b) => b.kind === "turn_ended")).body.model).toBe("gpt-5.5-codex");
    h!.console.command("session.set_mode", { session_id: "s_XM", model: "gpt-mini-x" });
    h!.console.command("session.send", { session_id: "s_XM", msg_id: "m_x2", text: "two" });
    await h!.console.waitType("session.event", (b) => b.kind === "turn_ended" && b.model === "gpt-mini-x");
    const turns = calls().filter((c) => c.method === "turn/start");
    expect(turns.map((t) => t.params.model)).toEqual([undefined, "gpt-mini-x"]);
  });

  it("forwards command approvals and platform tool calls", async () => {
    await startCodex();
    h!.console.command("session.open", codexOpen("s_X2"));
    await h!.console.waitType("session.opened");
    h!.console.command("session.release", { session_id: "s_X2", open_gen: 1 });
    h!.console.command("session.send", { session_id: "s_X2", msg_id: "m_c2", text: "please approve" });
    const req = await h!.console.waitType("approval.request");
    expect(req.body).toMatchObject({ source: "codex_command", tool_name: "shell", input: { command: "rm -rf build" }, tool_use_id: "item_1" });
    h!.console.command("approval.resolve", { approval_id: req.body.approval_id, decision: "allow_always", decided_by: "user:1" });
    expect((await h!.console.waitType("session.event", (b) => b.kind === "text" && b.final)).body.text).toBe("decision: acceptForSession");
    await h!.console.waitType("session.event", (b) => b.kind === "turn_ended");
    h!.console.command("session.send", { session_id: "s_X2", msg_id: "m_c3", text: "use the tool" });
    const call = await h!.console.waitType("tool.call");
    expect(call.body).toMatchObject({ tool: "complete_task", args: { task_id: "t3", summary: "codex done" } });
    h!.console.command("tool.result", { session_id: "s_X2", call_id: call.body.call_id, ok: true, content: [{ type: "text", text: "recorded" }] });
    await h!.console.waitType("session.event", (b) => b.kind === "text" && b.final && b.text === "tool: recorded");
  });

  it("reports resume_failed when the thread cannot be resumed", async () => {
    await startCodex();
    h!.console.command("session.open", codexOpen("s_X3", { codex_thread_id: "th_missing" }));
    const opened = await h!.console.waitType("session.opened");
    expect(opened.body).toMatchObject({ resume_failed: true });
  });

  it("rotates the loopback URL on resume and rejects the old one", async () => {
    const calls = await startCodex();
    h!.console.command("session.open", codexOpen("s_X4", { codex_thread_id: "th_9" }));
    await h!.console.waitType("session.opened");
    const url = calls().find((c) => c.method === "thread/resume")!.params.config.mcp_servers.platform.url as string;
    expect((await fetch(url, { method: "POST", headers: { "content-type": "application/json", accept: "application/json, text/event-stream" }, body: JSON.stringify({ jsonrpc: "2.0", id: 1, method: "tools/list" }) })).status).toBe(200);
    expect((await fetch(url.replace(/\/[^/]+$/, "/wrong"), { method: "POST" })).status).toBe(404);
    h!.console.command("session.close", { session_id: "s_X4", open_gen: 1 });
    await h!.console.waitType("session.closed");
    expect((await fetch(url, { method: "POST" })).status).toBe(404);
  });
});

describe("codex dedup after a daemon restart", () => {
  it("answers session.delivered without a new turn for a message already in the thread", async () => {
    const calls = await startCodex();
    h!.console.command("session.open", codexOpen("s_X5", { codex_thread_id: "th_5" }));
    await h!.console.waitType("session.opened");
    const rel = h!.console.command("session.release", { session_id: "s_X5", open_gen: 1 });
    await h!.console.waitFor((f) => f.type === "ack" && f.body.seq >= rel.seq!, 5000, "release ack");
    await h!.restart();
    await h!.console.waitFor(() => h!.console.ofType("sessions.report").length === 2, 10_000, "report after restart");
    h!.console.command("session.send", { session_id: "s_X5", msg_id: "m_seen", text: "old" });
    await h!.console.waitType("session.delivered", (b) => b.msg_id === "m_seen");
    expect(calls().filter((c) => c.method === "turn/start")).toEqual([]);
    h!.console.command("session.send", { session_id: "s_X5", msg_id: "m_new", text: "new" });
    await h!.console.waitType("session.event", (b) => b.kind === "text" && b.final && b.text === "echo: new");
  });
});

describe("codex error mapping", () => {
  it("reports 401s once as auth_failed and drops other transient retries", async () => {
    const { errorNotification } = await import("../src/sessions/codex/mapper.js");
    expect(errorNotification({ message: "Reconnecting... 2/5", codexErrorInfo: { responseStreamDisconnected: { httpStatusCode: 401 } }, additionalDetails: "401 Unauthorized" }, true)).toMatchObject({ kind: "error", code: "auth_failed" });
    expect(errorNotification({ message: "Reconnecting... 1/5", codexErrorInfo: { responseStreamDisconnected: { httpStatusCode: 502 } } }, true)).toBeNull();
    expect(errorNotification({ message: "limit", codexErrorInfo: "usageLimitExceeded" }, false)).toMatchObject({ kind: "rate_limited" });
    expect(errorNotification({ message: "boom", codexErrorInfo: "other" }, false)).toMatchObject({ kind: "error", code: "protocol" });
  });
});
