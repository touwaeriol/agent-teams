// Context breakdown (protocol.md §4.10 上下文细分): session.context after every Claude turn
// (getContextUsage summary) and on session.context_refresh (full), only to a console with context_detail.
import { describe, expect, it, afterEach } from "vitest";
import { contextDetailOf } from "../src/sessions/claude/context.js";
import { claudeOpen, startHarness, type Harness } from "./helpers/daemon.js";

let h: Harness | undefined;
afterEach(async () => {
  if (!h) return;
  const invalid = [...h.console.invalid];
  await h.close();
  h = undefined;
  expect(invalid).toEqual([]);
});

async function open(id: string) {
  h!.console.command("session.open", claudeOpen(id, 1));
  await h!.console.waitType("session.opened", (b) => b.session_id === id);
  h!.console.command("session.release", { session_id: id, open_gen: 1 });
}

async function say(id: string, msg: string, text: string) {
  h!.console.command("session.send", { session_id: id, msg_id: msg, text, priority: "next", kind: "task" });
  return h!.console.waitType("session.event", (b) => b.kind === "turn_ended" && h!.console.frames.some((f) => f.type === "session.delivered" && f.body.msg_id === msg));
}

describe("contextDetailOf", () => {
  it("keeps the rows, MCP tools, servers, memory files and skills; drops the grid", () => {
    const d = contextDetailOf({
      totalTokens: 1234.6, maxTokens: 200000, rawMaxTokens: 1000000, percentage: 1, gridRows: [[]], model: "m", isAutoCompactEnabled: false,
      categories: [
        { id: "skills", name: "Skills", tokens: 10, color: "x", kind: "used" },
        { name: "Old CLI row", tokens: -5, color: "x", isDeferred: true, kind: "weird" as never },
      ],
      mcpTools: [{ name: "t", serverName: "s", tokens: 3 }], memoryFiles: [], agents: [], apiUsage: null,
    }, "full");
    expect(d).toEqual({
      detail: "full", total_tokens: 1235, max_tokens: 200000, raw_max_tokens: 1000000, model: "m", auto_compact: false,
      categories: [{ id: "skills", name: "Skills", tokens: 10, kind: "used" }, { name: "Old CLI row", tokens: 0, kind: "deferred" }],
      mcp_tools: [{ name: "t", server: "s", tokens: 3 }],
    });
  });
});

describe("session.context", () => {
  it("says hello with context_detail", async () => {
    h = await startHarness();
    expect(h.console.ofType("hello")[0]!.body.features).toContain("context_detail");
  });

  it("follows every turn_ended with the summary breakdown", async () => {
    h = await startHarness();
    await open("s_X1");
    const ended = await say("s_X1", "m_1", "hello");
    const ctx = await h.console.waitType("session.context");
    expect(ended.body.context).toEqual({ used_tokens: 6000, max_tokens: 200000 });
    expect(ctx.body).toMatchObject({ session_id: "s_X1", open_gen: 1, reason: "turn" });
    expect(ctx.body.refresh_id).toBeUndefined();
    expect(ctx.body.context).toEqual({
      detail: "summary", model: "claude-fake", total_tokens: 6000, max_tokens: 200000, raw_max_tokens: 200000, auto_compact: true, auto_compact_threshold: 167000,
      categories: [
        { id: "system_prompt", name: "System prompt", tokens: 1000, kind: "used" },
        { id: "mcp_tools", name: "MCP tools", tokens: 2000, kind: "used" },
        { id: "messages", name: "Messages", tokens: 3000, kind: "used" },
        { id: "autocompact_buffer", name: "Autocompact buffer", tokens: 33000, kind: "buffer" },
        { id: "free_space", name: "Free space", tokens: 161000, kind: "free" },
        { id: "mcp_tools_deferred", name: "MCP tools (deferred)", tokens: 500, kind: "deferred" },
      ],
      mcp_tools: [
        { name: "mcp__platform__complete_task", server: "platform", tokens: 1200, loaded: true },
        { name: "mcp__platform__set_status", server: "platform", tokens: 800, loaded: true },
        { name: "mcp__github__search_code", server: "github", tokens: 500, loaded: false },
      ],
      mcp_servers: [{ server: "platform", tokens: 300 }],
      memory_files: [{ path: "/w/CLAUDE.md", type: "Project", tokens: 400 }],
      skills: [{ name: "design", source: "plugin", tokens: 90 }],
    });
    // one getContextUsage per turn serves both turn_ended and session.context
    expect(h.log.contextReads).toEqual(["summary"]);
  });

  it("answers session.context_refresh with the full breakdown and the same refresh_id", async () => {
    h = await startHarness();
    await open("s_X2");
    await say("s_X2", "m_1", "hello");
    h.console.unreliable("session.context_refresh", { session_id: "s_X2", open_gen: 1, refresh_id: "cr_1" });
    const r = await h.console.waitType("session.context", (b) => b.reason === "refresh");
    expect(r.body).toMatchObject({ session_id: "s_X2", open_gen: 1, reason: "refresh", refresh_id: "cr_1" });
    expect(r.body.context.detail).toBe("full");
    expect(r.body.context.mcp_tools[0]).toEqual({ name: "mcp__platform__complete_task", server: "platform", tokens: 1300, loaded: true });
    expect(h.log.contextReads).toEqual(["summary", "full"]);
  });

  it("answers a refresh it cannot read with an error, without starting the session", async () => {
    h = await startHarness();
    h.console.unreliable("session.context_refresh", { session_id: "s_none", open_gen: 1, refresh_id: "cr_2" });
    const r = await h.console.waitType("session.context", (b) => b.refresh_id === "cr_2");
    expect(r.body).toEqual({ session_id: "s_none", open_gen: 1, reason: "refresh", refresh_id: "cr_2", error: "no such session on this node" });
    expect(h.log.options).toHaveLength(0);
  });

  it("sends nothing to a console without context_detail", async () => {
    h = await startHarness();
    h.console.welcomeDrop = ["context_detail"];
    await h.restart();
    await h.console.waitType("sessions.report", () => h!.console.ofType("sessions.report").length >= 2);
    await open("s_X3");
    const ended = await say("s_X3", "m_1", "hello");
    expect(ended.body.context).toEqual({ used_tokens: 6000, max_tokens: 200000 });
    await new Promise((r) => setTimeout(r, 200));
    expect(h.console.ofType("session.context")).toEqual([]);
  });
});
