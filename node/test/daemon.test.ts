import { afterEach, describe, expect, it } from "vitest";
import { claudeOpen, startHarness, type Harness } from "./helpers/daemon.js";

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
});
