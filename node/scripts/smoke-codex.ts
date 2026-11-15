// Real smoke test for the pinned `codex app-server`: initialize, account/read, thread/start
// with the loopback platform MCP server, mcpServerStatus/list, then one turn (fails without
// an OpenAI login; the script reports what happened).   npm run smoke:codex
import { mkdtempSync, mkdirSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { AppServer } from "../src/sessions/codex/appserver.js";
import { LoopbackToolHost } from "../src/toolhost/http-server.js";
import { createLogger } from "../src/util/log.js";

const dir = mkdtempSync(join(tmpdir(), "at-smoke-codex-"));
const log = createLogger("warn");

async function main(): Promise<void> {
  const codexHome = join(dir, "codex-home");
  const cwd = join(dir, "work");
  mkdirSync(codexHome, { recursive: true });
  mkdirSync(cwd, { recursive: true });
  const toolhost = new LoopbackToolHost(log);
  await toolhost.start();
  const calls: string[] = [];
  const url = toolhost.rotate("s_codex_smoke", [{ name: "complete_task", description: "Finish the task", blocking: false, input_schema: { type: "object", required: ["task_id"], properties: { task_id: { type: "string" } } } }], async (tool, args) => {
    calls.push(`${tool} ${JSON.stringify(args)}`);
    return { content: [{ type: "text", text: "ok" }] };
  });
  const server = new AppServer({ env: { PATH: process.env.PATH ?? "", HOME: dir, CODEX_HOME: codexHome }, log, clientVersion: "2.0.0" });
  const notes: string[] = [];
  server.on("notification", (m, p) => notes.push(`${m} ${JSON.stringify(p).slice(0, 300)}`));
  server.onRequest(async (method) => {
    notes.push(`server request ${method}`);
    return { decision: "decline" };
  });
  try {
    await server.start();
    console.log("initialize:", JSON.stringify(server.initialize));
    console.log("account/read:", JSON.stringify(await server.request("account/read", {}).catch((e: Error) => e.message)));
    const thread = await server.request<{ thread: { id: string } }>("thread/start", {
      model: "gpt-5.5-codex", cwd, approvalPolicy: "on-request", sandbox: "workspace-write",
      config: { mcp_servers: { platform: { url, default_tools_approval_mode: "approve", tool_timeout_sec: 86400 } } },
    });
    console.log("thread/start ok, thread id:", thread.thread.id);
    const status = await server.request<{ data: Array<{ name: string; tools: Record<string, unknown>; toolsError: string | null }> }>("mcpServerStatus/list", { threadId: thread.thread.id, serverName: "platform", detail: "toolsAndAuthOnly" });
    console.log("mcpServerStatus/list:", JSON.stringify(status.data.map((s) => ({ name: s.name, tools: Object.keys(s.tools ?? {}), toolsError: s.toolsError }))));
    const turn = await server.request("turn/start", { threadId: thread.thread.id, input: [{ type: "text", text: "reply with the word pong", text_elements: [] }] }).catch((e: Error) => `error: ${e.message}`);
    console.log("turn/start:", JSON.stringify(turn).slice(0, 300));
    await new Promise((r) => setTimeout(r, 8000));
    console.log("notifications:\n  " + notes.slice(0, 25).join("\n  "));
    console.log("platform tool calls:", calls);
  } finally {
    server.stop();
    await toolhost.stop();
    rmSync(dir, { recursive: true, force: true });
  }
}

main().then(
  () => process.exit(0),
  (err: Error) => {
    console.error("codex smoke failed:", err.message);
    process.exit(1);
  },
);
