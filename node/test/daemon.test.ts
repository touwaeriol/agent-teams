import { mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import type { Query } from "@anthropic-ai/claude-agent-sdk";
import { claudeOpen, startHarness, type Harness } from "./helpers/daemon.js";
import { fakeQueryFactory, type FakeQueryLog } from "./helpers/fake-query.js";

let h: Harness;
afterEach(async () => {
  const invalid = [...h.console.invalid];
  await h.close();
  expect(invalid).toEqual([]);
});

async function openAndRelease(id: string, extra: Record<string, unknown> = {}) {
  h.console.command("session.open", claudeOpen(id, 1, extra));
  const opened = await h.console.waitType("session.opened", (b) => b.session_id === id);
  h.console.command("session.release", { session_id: id, open_gen: 1 });
  return opened;
}

describe("daemon with a fake console", () => {
  it("says hello with protocol 2 and reports sessions after welcome", async () => {
    h = await startHarness();
    const hello = h.console.ofType("hello")[0]!;
    expect(hello.body.protocols).toEqual([2]);
    expect(hello.body).toMatchObject({ last_seq: 0, acked_cmd_seq: 0, isolation: "none", capacity: { max_active_sessions: 4 } });
    expect(h.console.ofType("sessions.report")[0]!.body).toEqual({ sessions: [] });
  });

  it("opens a session, holds the first message until release, then delivers it", async () => {
    h = await startHarness();
    h.console.command("session.open", claudeOpen("s_A", 1, { first_message: { msg_id: "m_first", text: "hello there" } }));
    const opened = await h.console.waitType("session.opened");
    expect(opened.body.init.tools).toEqual(["Read", "Bash", "mcp__platform__complete_task", "mcp__platform__ask_lead"]);
    expect(opened.body.init.commands).toEqual([{ name: "agent-teams-skills:design", description: "Make HTML prototypes" }]);
    expect(opened.body.sdk_session_id).toMatch(/^[0-9a-f-]{36}$/);
    await new Promise((r) => setTimeout(r, 200));
    expect(h.log.prompts).toEqual([]);
    h.console.command("session.release", { session_id: "s_A", open_gen: 1 });
    await h.console.waitType("session.delivered", (b) => b.msg_id === "m_first");
    const text = await h.console.waitType("session.event", (b) => b.kind === "text" && b.final === true);
    expect(text.body.text).toBe("echo: hello there");
    const started = h.console.ofType("session.event").find((f) => f.body.kind === "turn_started")!;
    expect(started.body.trigger_msg_ids).toEqual(["m_first"]);
    await h.console.waitType("session.event", (b) => b.kind === "turn_ended" && b.result_subtype === "success");
    const opts = h.log.options[0]!;
    expect(opts).toMatchObject({ settingSources: [], permissionMode: "default", strictMcpConfig: true, includeHookEvents: true });
    expect(opts.env!.MCP_TOOL_TIMEOUT).toBe("86400000");
    expect(Object.keys(opts.env!).some((k) => k.startsWith("AGENT_TEAMS_"))).toBe(false);
  });

  it("runs a bare /skill as the session's own command, not the CLI built-in of that name", async () => {
    h = await startHarness();
    await openAndRelease("s_SL");
    h.console.command("session.send", { session_id: "s_SL", msg_id: "m_sl", text: "/design 做个登录页", priority: "next", kind: "user" });
    await h.console.waitType("session.event", (b) => b.kind === "text" && b.final === true);
    expect(h.log.prompts).toContain("/agent-teams-skills:design 做个登录页");
  });

  it("forwards canUseTool as approval.request and applies approval.resolve", async () => {
    h = await startHarness();
    await openAndRelease("s_B");
    h.console.command("session.send", { session_id: "s_B", msg_id: "m_1", text: "run pnpm add -D vitest", priority: "next", kind: "user" });
    const req = await h.console.waitType("approval.request");
    expect(req.body).toMatchObject({ session_id: "s_B", source: "claude_can_use_tool", tool_name: "Bash", input: { command: "pnpm add -D vitest" }, available: ["allow", "allow_always", "deny"] });
    expect(req.body.approval_id).toMatch(/^ap_[0-9a-f]{24}$/);
    h.console.command("approval.resolve", { approval_id: req.body.approval_id, decision: "allow_always", decided_by: "user:1" });
    await h.console.waitType("approval.ack", (b) => b.approval_id === req.body.approval_id && !b.stale);
    const text = await h.console.waitType("session.event", (b) => b.kind === "text" && b.final === true);
    expect(text.body.text).toContain('"destination":"session"');
    h.console.command("approval.resolve", { approval_id: "ap_000000000000000000000000", decision: "deny", decided_by: "user:1" });
    await h.console.waitType("approval.ack", (b) => b.stale === true);
  });

  it("allows in place without an approval.request in bypassPermissions sessions", async () => {
    h = await startHarness();
    const open = claudeOpen("s_BP");
    Object.assign(open.claude as Record<string, unknown>, { permission_mode: "bypassPermissions", allow_dangerously_skip_permissions: true });
    h.console.command("session.open", open);
    await h.console.waitType("session.opened");
    h.console.command("session.release", { session_id: "s_BP", open_gen: 1 });
    h.console.command("session.send", { session_id: "s_BP", msg_id: "m_bp", text: "run rm -rf build", priority: "next", kind: "user" });
    const text = await h.console.waitType("session.event", (b) => b.kind === "text" && b.final === true);
    expect(text.body.text).toBe("allowed []");
    expect(h.log.options.at(-1)).toMatchObject({ permissionMode: "bypassPermissions", allowDangerouslySkipPermissions: true });
    expect(h.console.ofType("approval.request")).toEqual([]);
  });

  it("downloads attached images and hands them to Claude as image blocks", async () => {
    h = await startHarness();
    h.console.images.set("img_1", Buffer.from("\x89PNG\r\n\x1a\nfake", "binary"));
    await openAndRelease("s_IMG");
    h.console.command("session.send", { session_id: "s_IMG", msg_id: "m_i1", text: "看这张图", kind: "user", images: [{ id: "img_1", media_type: "image/png", name: "a.png", size: 12 }] });
    await h.console.waitType("session.delivered", (b) => b.msg_id === "m_i1");
    expect(h.log.prompts).toContain("[image image/png 12B] 看这张图");
    h.console.command("session.send", { session_id: "s_IMG", msg_id: "m_i2", text: "还有这张", kind: "user", images: [{ id: "img_missing", media_type: "image/png" }] });
    await h.console.waitType("session.delivered", (b) => b.msg_id === "m_i2");
    expect(h.log.prompts.find((p) => p.startsWith("还有这张"))).toContain("附带的图片下载失败");
  });

  it("downloads session.send attachments into .attachments/ and lists them, advertising attachments_v2", async () => {
    h = await startHarness();
    expect(h.console.ofType("hello")[0]!.body.features).toContain("attachments_v2");
    // The fake console has no /url endpoint: the daemon falls back to GET /nodes/attachments/{id}.
    h.console.images.set("img_v2a", Buffer.from("\x89PNG\r\n\x1a\nfake", "binary"));
    h.console.images.set("img_v2b", Buffer.from("PK zip"));
    await openAndRelease("s_ATT");
    h.console.command("session.send", {
      session_id: "s_ATT", msg_id: "m_a1", text: "看附件", kind: "user",
      attachments: [
        { id: "img_v2a", name: "a.png", content_type: "image/png", size: 12 },
        { id: "img_v2b", name: "数据.zip", content_type: "application/zip", size: 6 },
        { id: "img_v2c", name: "gone.pdf", content_type: "application/pdf", size: 10 },
      ],
    });
    await h.console.waitType("session.delivered", (b) => b.msg_id === "m_a1");
    const prompt = h.log.prompts.find((p) => p.includes("看附件"))!;
    expect(prompt.startsWith("[image image/png 12B] 看附件")).toBe(true);
    const cwd = join(h.opts.root, "projects/p1/threads/12/s_ATT");
    expect(prompt).toContain(`img_v2a a.png（image/png，12 B） → ${join(cwd, ".attachments", "img_v2a-a.png")}（已作为图片附上）`);
    expect(prompt).toContain(`${join(cwd, ".attachments", "img_v2b-数据.zip")}（只给路径`);
    expect(prompt).toMatch(/img_v2c gone\.pdf.*下载失败/);
    expect(readFileSync(join(cwd, ".attachments", "img_v2b-数据.zip"), "utf8")).toBe("PK zip");
    expect(JSON.parse(readFileSync(join(h.opts.stateDir, "attachment-dirs.json"), "utf8"))).toEqual([join(cwd, ".attachments")]);
  });

  it("forwards platform tool calls as tool.call and returns tool.result", async () => {
    h = await startHarness();
    await openAndRelease("s_C");
    h.console.command("session.send", { session_id: "s_C", msg_id: "m_2", text: 'use complete_task {"task_id":"t7","summary":"done it"}' });
    const call = await h.console.waitType("tool.call");
    expect(call.body).toMatchObject({ session_id: "s_C", open_gen: 1, tool: "complete_task", args: { task_id: "t7", summary: "done it" }, tool_use_id: "toolu_complete_task" });
    expect(call.body.call_id).toMatch(/^tc_/);
    h.console.command("tool.result", { session_id: "s_C", call_id: call.body.call_id, ok: true, content: [{ type: "text", text: "task t7 done" }] });
    const text = await h.console.waitType("session.event", (b) => b.kind === "text" && b.final === true);
    expect(text.body.text).toBe("tool ok: task t7 done");
    const started = await h.console.waitType("session.event", (b) => b.kind === "tool_started");
    expect(started.body).toMatchObject({ tool: "mcp__platform__complete_task", category: "platform" });
  });

  it("denies everything but platform tools for the coordinator", async () => {
    h = await startHarness();
    const coord = claudeOpen("s_K", 1, { employee: { name: "coordinator", tier: "coordinator", project_id: "p1", thread_no: null }, cwd: "projects/p1/coordinator" });
    (coord.claude as Record<string, unknown>).tools = [];
    (coord.claude as Record<string, unknown>).deny_non_platform_tools = true;
    h.console.command("session.open", coord);
    await h.console.waitType("session.opened");
    h.console.command("session.release", { session_id: "s_K", open_gen: 1 });
    h.console.command("session.send", { session_id: "s_K", msg_id: "m_k", text: "run ls" });
    const text = await h.console.waitType("session.event", (b) => b.kind === "text" && b.final === true);
    expect(text.body.text).toBe("denied by hook: This session may only use platform tools.");
    expect(h.console.ofType("approval.request")).toEqual([]);
  });

  it("replays unacknowledged frames after a reconnect and dedups repeated commands", async () => {
    h = await startHarness();
    await openAndRelease("s_D");
    h.console.autoAck = false;
    const send = h.console.command("session.send", { session_id: "s_D", msg_id: "m_3", text: "first" });
    const delivered = await h.console.waitType("session.delivered", (b) => b.msg_id === "m_3");
    await h.console.waitType("session.event", (b) => b.kind === "turn_ended");
    const promptsBefore = h.log.prompts.length;
    h.console.welcomeAckSeq = delivered.seq! - 1;
    h.console.dropConnection();
    await h.console.waitFor((f) => f.type === "hello" && h.console.connections === 2, 10_000, "second hello");
    const hello2 = h.console.ofType("hello")[1]!;
    expect(hello2.body.last_seq).toBeGreaterThanOrEqual(delivered.seq!);
    expect(hello2.body.acked_cmd_seq).toBe(send.seq);
    await h.console.waitFor((f) => f.type === "session.delivered" && f.seq === delivered.seq && f !== delivered, 10_000, "replayed delivered");
    h.console.autoAck = true;
    h.console.command("session.send", { session_id: "s_D", msg_id: "m_3", text: "first" });
    await h.console.waitFor((f) => f.type === "session.delivered" && f.body.msg_id === "m_3" && f.seq! > delivered.seq! + 3, 10_000, "re-sent reply");
    expect(h.log.prompts.length).toBe(promptsBefore);
  });

  it("parks an idle session and resumes it on the next message", async () => {
    h = await startHarness();
    await openAndRelease("s_E");
    h.console.command("session.send", { session_id: "s_E", msg_id: "m_4", text: "one" });
    await h.console.waitType("session.event", (b) => b.kind === "turn_ended");
    h.console.command("node.config", { park_after_s: 1 });
    await new Promise((r) => setTimeout(r, 1100));
    await (h.daemon.sessions as unknown as { parkIdle(): Promise<void> }).parkIdle();
    await h.console.waitType("session.state", (b) => b.session_id === "s_E" && b.state === "parked");
    expect(h.log.closed).toBe(1);
    h.console.command("session.send", { session_id: "s_E", msg_id: "m_5", text: "two" });
    await h.console.waitType("session.delivered", (b) => b.msg_id === "m_5");
    expect(h.log.options.length).toBe(2);
    expect(h.log.options[1]!.resume).toBe(h.log.options[0]!.sessionId);
  });

  it("queues sessions beyond max_active_sessions by parking the idle one", async () => {
    h = await startHarness();
    h.console.command("node.config", { max_active_sessions: 1 });
    await openAndRelease("s_F");
    h.console.command("session.open", claudeOpen("s_G"));
    await h.console.waitType("session.opened", (b) => b.session_id === "s_G");
    await h.console.waitType("session.state", (b) => b.session_id === "s_F" && b.state === "parked");
    expect(h.daemon.sessions.stats.active).toBe(1);
    const report = h.daemon.sessions.report().sessions.map((s) => [s.session_id, s.state]);
    expect(report).toEqual(expect.arrayContaining([["s_F", "parked"], ["s_G", "active"]]));
  });

  it("starts every session when no limit is set", async () => {
    h = await startHarness({ maxActive: undefined });
    expect(h.console.ofType("hello")[0]!.body.capacity.max_active_sessions).toBeUndefined();
    const ids = ["s_U1", "s_U2", "s_U3", "s_U4", "s_U5", "s_U6"];
    for (const id of ids) await openAndRelease(id);
    expect(h.daemon.sessions.stats).toMatchObject({ active: ids.length, parked: 0, queued: 0 });
    expect(h.console.ofType("session.state").some((f) => f.body.state === "parked" || f.body.state === "queued")).toBe(false);
  });

  it("never parks a session that is still starting to make room for another", async () => {
    // Before: s_K, still waiting for its initialize answer, counted as idle, so opening s_L parked
    // it and closed its query: "Query closed before initialize was sent" → spawn_failed.
    const log: FakeQueryLog = { options: [], prompts: [], closed: 0, interrupts: 0, modes: [] };
    const base = fakeQueryFactory(log);
    let releaseInit!: () => void;
    const initGate = new Promise<void>((r) => (releaseInit = r));
    const queryFn = (p: Parameters<typeof base>[0]): Query => {
      const q = base(p);
      if (log.options.length > 1) return q;
      const init = q.initializationResult.bind(q);
      q.initializationResult = async () => (await initGate, init());
      return q;
    };
    h = await startHarness({ maxActive: 1, queryFn });
    h.console.command("session.open", claudeOpen("s_K"));
    await h.console.waitType("session.state", (b) => b.session_id === "s_K" && b.state === "starting");
    await h.console.waitFor(() => log.options.length === 1, 5000, "s_K query");
    h.console.command("session.open", claudeOpen("s_L"));
    await h.console.waitType("session.state", (b) => b.session_id === "s_L" && b.state === "queued");
    releaseInit();
    await h.console.waitType("session.opened", (b) => b.session_id === "s_K");
    expect(log.closed).toBe(0);
    expect(h.console.ofType("session.event").filter((f) => f.body.code === "spawn_failed")).toEqual([]);
  });

  it("closes a session, cancels its pending tool calls and reports session.closed", async () => {
    h = await startHarness();
    await openAndRelease("s_H");
    h.console.command("session.send", { session_id: "s_H", msg_id: "m_6", text: 'use ask_lead {"question":"which db?"}' });
    const call = await h.console.waitType("tool.call");
    h.console.command("session.close", { session_id: "s_H", open_gen: 1, reason: "thread_done" });
    await h.console.waitType("tool.cancel", (b) => b.call_id === call.body.call_id && b.reason === "session_closed");
    await h.console.waitType("session.closed", (b) => b.session_id === "s_H");
    h.console.command("session.send", { session_id: "s_H", msg_id: "m_7", text: "x" });
    await h.console.waitType("error", (b) => b.code === "unknown_session");
  });

  it("interrupts, changes mode and streams deltas while watched", async () => {
    h = await startHarness();
    await openAndRelease("s_M");
    h.console.command("session.interrupt", { session_id: "s_M", reason: "user_stop" });
    expect((await h.console.waitType("session.interrupted")).body).toEqual({ session_id: "s_M", open_gen: 1, queued_msg_ids: [] });
    expect(h.log.interrupts).toBe(1);
    h.console.command("session.set_mode", { session_id: "s_M", permission_mode: "bypassPermissions" });
    await h.console.waitType("error", (b) => b.code === "unsupported");
    h.console.command("session.set_mode", { session_id: "s_M", permission_mode: "acceptEdits" });
    h.console.command("session.watch", { session_id: "s_M", on: true });
    h.console.command("session.send", { session_id: "s_M", msg_id: "m_w", text: "watch me" });
    expect((await h.console.waitType("stream.delta")).body).toMatchObject({ session_id: "s_M", text: "echo: watch me" });
    expect(h.log.modes).toEqual(["acceptEdits"]);
  });

  it("switches the model of a running session (Query.setModel) and reports it with turn_ended", async () => {
    h = await startHarness();
    await openAndRelease("s_MD");
    h.console.command("session.send", { session_id: "s_MD", msg_id: "m_md1", text: "hi" });
    expect((await h.console.waitType("session.event", (b) => b.kind === "turn_ended" && b.session_id === "s_MD")).body.model).toBe("claude-opus-5-5");
    h.console.command("session.set_mode", { session_id: "s_MD", model: "claude-haiku-x" });
    await h.console.waitFor(() => (h.log.models ?? []).length === 1, 5000, "setModel");
    expect(h.log.models).toEqual(["claude-haiku-x"]);
    h.console.command("session.send", { session_id: "s_MD", msg_id: "m_md2", text: "again" });
    await h.console.waitType("session.event", (b) => b.kind === "turn_ended" && b.session_id === "s_MD" && b.model === "claude-haiku-x");
    expect(h.log.modes).toEqual([]);
  });

  it("reports the session's models and switches mode, model and effort at once with session.mode_set (session_controls)", async () => {
    h = await startHarness();
    expect(h.console.ofType("hello")[0]!.body.features).toContain("session_controls");
    const opened = await openAndRelease("s_SC", { claude: { ...claudeOpen("s_SC").claude, effort: "high" } });
    expect(opened.body.init.effort).toBe("high");
    expect(opened.body.init.models).toEqual([
      { id: "claude-opus-5-5", name: "Opus 5.5", description: "For complex work", efforts: ["low", "medium", "high", "xhigh", "max"], supports_auto: true, default: true },
      { id: "claude-haiku-x", name: "Haiku X", efforts: ["low", "medium", "high"], supports_auto: true },
      { id: "claude-haiku-4-5-20251001", name: "Haiku 4.5", efforts: [], supports_auto: false },
    ]);
    h.console.command("session.set_mode", { session_id: "s_SC", permission_mode: "plan", model: "claude-haiku-x", effort: "low", request_id: "r1" });
    const set = await h.console.waitType("session.mode_set", (b) => b.request_id === "r1");
    expect(set.body).toEqual({ session_id: "s_SC", open_gen: 1, request_id: "r1", ok: true, permission_mode: "plan", model: "claude-haiku-x", effort: "low", applies: "now" });
    expect(h.log.modes).toEqual(["plan"]);
    expect(h.log.flags).toEqual([{ effortLevel: "low" }]);
    h.console.command("session.set_mode", { session_id: "s_SC", permission_mode: "bypassPermissions", request_id: "r2" });
    const refused = await h.console.waitType("session.mode_set", (b) => b.request_id === "r2");
    expect(refused.body).toMatchObject({ ok: false, permission_mode: "plan", model: "claude-haiku-x", effort: "low" });
    expect(refused.body.error).toMatch(/allow_dangerously_skip_permissions/);
    // A model without the effort in force (t45): the old level is dropped, not reported.
    h.console.command("session.set_mode", { session_id: "s_SC", model: "claude-haiku-4-5-20251001", request_id: "r4" });
    const dropped = await h.console.waitType("session.mode_set", (b) => b.request_id === "r4");
    expect(dropped.body).toEqual({ session_id: "s_SC", open_gen: 1, request_id: "r4", ok: true, permission_mode: "plan", model: "claude-haiku-4-5-20251001", applies: "now" });
    h.console.command("session.set_mode", { session_id: "s_SC", model: "claude-haiku-x", request_id: "r5" });
    const back = await h.console.waitType("session.mode_set", (b) => b.request_id === "r5");
    expect(back.body).not.toHaveProperty("effort");
  });

  it("opens a prefer_auto session in auto when its model supports it, else stays in default", async () => {
    h = await startHarness();
    const auto = await openAndRelease("s_PA", { claude: { ...claudeOpen("s_PA").claude, prefer_auto: true } });
    expect(auto.body.init.permission_mode).toBe("auto");
    expect(h.log.options[0]!.permissionMode).toBe("default");
    const plain = await openAndRelease("s_PD", { claude: { ...claudeOpen("s_PD").claude, model: "claude-haiku-4-5-20251001", prefer_auto: true } });
    expect(plain.body.init.permission_mode).toBe("default");
    expect(h.log.modes).toEqual(["auto"]);
    h.console.command("session.set_mode", { session_id: "s_PD", permission_mode: "auto", request_id: "r3" });
    expect((await h.console.waitType("session.mode_set", (b) => b.request_id === "r3")).body).toMatchObject({ ok: false, permission_mode: "default", error: expect.stringMatching(/auto mode unavailable/) });
  });

  it("restarts a crashed session by resuming it", async () => {
    h = await startHarness({ crashDelaysMs: [10, 10, 10] });
    await openAndRelease("s_N");
    h.console.command("session.send", { session_id: "s_N", msg_id: "m_n1", text: "hi" });
    await h.console.waitType("session.event", (b) => b.kind === "turn_ended");
    h.console.command("session.send", { session_id: "s_N", msg_id: "m_n2", text: "crash" });
    await h.console.waitType("session.state", (b) => b.state === "starting" && b.detail === "restarting after crash");
    await h.console.waitFor(() => h.log.options.length === 2, 5000, "restart");
    expect(h.log.options[1]!.resume).toBe(h.log.options[0]!.sessionId);
    await h.console.waitType("session.state", (b) => b.state === "active" && h.log.options.length === 2);
  });

  it("refuses a cwd outside the root", async () => {
    h = await startHarness();
    h.console.command("session.open", claudeOpen("s_I", 1, { cwd: "../escape" }));
    const err = await h.console.waitType("session.event", (b) => b.kind === "error");
    expect(err.body).toMatchObject({ code: "spawn_failed", fatal: true });
    expect(err.body.message).toMatch(/outside/);
    await h.console.waitType("session.state", (b) => b.state === "crashed");
    expect(h.console.ofType("session.opened")).toEqual([]);
  });

  it("turns orphaned approval requests into cancels after a daemon restart", async () => {
    h = await startHarness();
    await openAndRelease("s_J");
    h.console.autoAck = false;
    h.console.command("session.send", { session_id: "s_J", msg_id: "m_8", text: "run make deploy" });
    const req = await h.console.waitType("approval.request");
    await h.restart();
    const cancel = await h.console.waitType("approval.cancel", (b) => b.approval_id === req.body.approval_id);
    expect(cancel.body.reason).toBe("process_restarted");
    const report = h.console.ofType("sessions.report").at(-1)!;
    expect(report.body.sessions[0]).toMatchObject({ session_id: "s_J", state: "parked", pending_approvals: [] });
    const replayed = h.console.frames.filter((f) => f.type === "approval.request" && f.body.approval_id === req.body.approval_id);
    expect(replayed.length).toBe(1);
  });

  it("routes Claude's AskUserQuestion to a blocking ask_user and hands the answers back as the tool input", async () => {
    h = await startHarness();
    await openAndRelease("s_Q");
    const ask = { questions: [{ question: "Switch sessions too?", header: "Sessions", multiSelect: false, options: [{ label: "Switch", description: "Drop JWT" }, { label: "Keep", description: "Keep JWT" }] }] };
    h.console.command("session.send", { session_id: "s_Q", msg_id: "m_q", text: `builtin AskUserQuestion ${JSON.stringify(ask)}` });
    const call = await h.console.waitType("tool.call", (b) => b.tool === "ask_user");
    expect(call.body.args).toEqual({ via: "AskUserQuestion", questions: [{ question: "Switch sessions too?", header: "Sessions", options: [{ label: "Switch", description: "Drop JWT" }, { label: "Keep", description: "Keep JWT" }] }] });
    expect(String(call.body.tool_use_id)).toMatch(/^toolu_AskUserQuestion_/);
    h.console.command("tool.result", { session_id: "s_Q", call_id: call.body.call_id, ok: true, content: [{ type: "text", text: "Switch" }],
      structured: { question_id: "q4", answer: "Switch", answers: [{ question: "Switch sessions too?", header: "Sessions", selected: ["Switch"], other: "after the release" }] } });
    const text = await h.console.waitType("session.event", (b) => b.kind === "text" && b.final === true);
    expect(text.body.text).toBe('AskUserQuestion ran with {"Switch sessions too?":"Switch, after the release"}');
  });

  it("refuses AskUserQuestion with the reason when the platform call fails", async () => {
    h = await startHarness();
    await openAndRelease("s_Q2");
    h.console.command("session.send", { session_id: "s_Q2", msg_id: "m_q2", text: 'builtin AskUserQuestion {"questions":[{"question":"A?","header":"A","options":[{"label":"x"},{"label":"y"}]}]}' });
    const call = await h.console.waitType("tool.call", (b) => b.tool === "ask_user");
    h.console.command("tool.result", { session_id: "s_Q2", call_id: call.body.call_id, ok: false, content: [{ type: "text", text: "thread archived" }] });
    const text = await h.console.waitType("session.event", (b) => b.kind === "text" && b.final === true);
    expect(text.body.text).toBe("denied: The user did not answer on the platform: thread archived");
  });

  it("reports TodoWrite as a todos event and ExitPlanMode as a plan event", async () => {
    h = await startHarness();
    await openAndRelease("s_T");
    const todos = { todos: [{ content: "Write tests", status: "completed", activeForm: "Writing tests" }, { content: "Fix bug", status: "in_progress", activeForm: "Fixing bug" }, { content: "Ship", status: "pending", activeForm: "Shipping" }] };
    h.console.command("session.send", { session_id: "s_T", msg_id: "m_t", text: `builtin TodoWrite ${JSON.stringify(todos)}` });
    const ev = await h.console.waitType("session.event", (b) => b.kind === "todos");
    expect(ev.body.todos).toEqual([{ content: "Write tests", status: "completed", active_form: "Writing tests" }, { content: "Fix bug", status: "in_progress", active_form: "Fixing bug" }, { content: "Ship", status: "pending", active_form: "Shipping" }]);
    const plans = join(h.opts.claudeConfigDir, "plans");
    mkdirSync(plans, { recursive: true });
    writeFileSync(join(plans, "p.md"), "# Plan\n\n1. Do it");
    h.console.command("session.send", { session_id: "s_T", msg_id: "m_p", text: `builtin ExitPlanMode ${JSON.stringify({ planFile: join(plans, "p.md") })}` });
    const plan = await h.console.waitType("session.event", (b) => b.kind === "plan");
    expect(plan.body.plan).toBe("# Plan\n\n1. Do it");
  });

  it("answers resume_failed when a parked session's transcript is gone, without a closed state first", async () => {
    h = await startHarness();
    await openAndRelease("s_R");
    h.console.command("session.send", { session_id: "s_R", msg_id: "m_r1", text: "remember the word heron" });
    await h.console.waitType("session.event", (b) => b.kind === "turn_ended");
    await (h.daemon.sessions as unknown as { park(s: unknown): Promise<void>; sessions: Map<string, unknown> }).park((h.daemon.sessions as unknown as { sessions: Map<string, unknown> }).sessions.get("s_R"));
    await h.console.waitType("session.state", (b) => b.session_id === "s_R" && b.state === "parked");
    rmSync(join(h.opts.claudeConfigDir, "projects"), { recursive: true, force: true });
    h.console.command("session.send", { session_id: "s_R", msg_id: "m_r2", text: "what was the word?" });
    const opened = await h.console.waitType("session.opened", (b) => b.session_id === "s_R" && b.resume_failed === true);
    expect(opened.body.error).toMatch(/not found/);
    expect(h.console.ofType("session.state").some((f) => f.body.session_id === "s_R" && f.body.state === "closed")).toBe(false);
    expect(h.daemon.sessions.report().sessions.map((x) => x.session_id)).not.toContain("s_R");
  });

  it("answers resume_failed when a crashed session cannot be resumed", async () => {
    h = await startHarness({ crashDelaysMs: [10, 10, 10] });
    await openAndRelease("s_X");
    h.console.command("session.send", { session_id: "s_X", msg_id: "m_x1", text: "hi" });
    await h.console.waitType("session.event", (b) => b.kind === "turn_ended");
    rmSync(join(h.opts.claudeConfigDir, "projects"), { recursive: true, force: true });
    h.console.command("session.send", { session_id: "s_X", msg_id: "m_x2", text: "crash" });
    const opened = await h.console.waitType("session.opened", (b) => b.session_id === "s_X" && b.resume_failed === true);
    expect(opened.body.open_gen).toBe(1);
    expect(h.log.options.length).toBe(1);
  });
});
