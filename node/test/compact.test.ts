// Context management (shared-members.md §5, protocol.md §4.10): context use on turn_ended,
// session.compact / session.compacted, the autocompact settings of session.open.context and the
// task anchor re-injected after a compaction the runtime did by itself.
import { readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it, afterEach } from "vitest";
import { Daemon } from "../src/daemon.js";
import { claudeCompactEnv } from "../src/sessions/claude/compact.js";
import { buildClaudeOptions } from "../src/sessions/claude/options.js";
import { autoCompactConfig } from "../src/sessions/codex/driver.js";
import { claudeOpen, startHarness, type Harness } from "./helpers/daemon.js";

describe("claudeCompactEnv", () => {
  it("turns the role's limit × in-task pct into the CLI's window and a percentage of window − 20K", () => {
    const env = (limit_tokens?: number, in_task_pct?: number, window_tokens?: number) => claudeCompactEnv({ limit_tokens, in_task_pct, window_tokens });
    expect(env(600_000, 80)).toEqual({ CLAUDE_CODE_AUTO_COMPACT_WINDOW: "600000", CLAUDE_AUTOCOMPACT_PCT_OVERRIDE: "82.76" });
    // 600000 × 82.76% of 580000 = 480008: compaction at ~480K whatever the model's window
    expect(Math.floor(580_000 * 0.8276)).toBeGreaterThanOrEqual(480_000);
    expect(env(30_000, 80)).toEqual({ CLAUDE_CODE_AUTO_COMPACT_WINDOW: "100000", CLAUDE_AUTOCOMPACT_PCT_OVERRIDE: "30" });
    expect(env(2_000_000, 50)).toEqual({ CLAUDE_CODE_AUTO_COMPACT_WINDOW: "1000000", CLAUDE_AUTOCOMPACT_PCT_OVERRIDE: "100" });
    expect(env(600_000)).toEqual({ CLAUDE_CODE_AUTO_COMPACT_WINDOW: "600000" });
    expect(env(undefined, undefined)).toEqual({});
    expect(claudeCompactEnv(undefined)).toEqual({});
  });

  it("without a limit takes the pct of the window the last session reported, leaving the window to the CLI", () => {
    // 200K window seen: 70% = 140000 tokens = 77.78% of 180000 (window − 20K); no window env (it would stick)
    expect(claudeCompactEnv({ in_task_pct: 70, window_tokens: 200_000 })).toEqual({ CLAUDE_AUTOCOMPACT_PCT_OVERRIDE: "77.78" });
    // nothing known: the pct applies to the CLI's effective window (window − 20K)
    expect(claudeCompactEnv({ in_task_pct: 70 })).toEqual({ CLAUDE_AUTOCOMPACT_PCT_OVERRIDE: "70" });
    // a limit wins over the seen window (the CLI clamps its window to the model's by itself)
    expect(claudeCompactEnv({ limit_tokens: 600_000, in_task_pct: 80, window_tokens: 200_000 })).toEqual({ CLAUDE_CODE_AUTO_COMPACT_WINDOW: "600000", CLAUDE_AUTOCOMPACT_PCT_OVERRIDE: "82.76" });
  });

  it("goes into the session's env, on top of the daemon's", () => {
    const opts = buildClaudeOptions({
      claude: { model: "m", tools: [], allowed_tools: [], permission_mode: "default", setting_sources: [] } as never, root: "/r", cwd: "/r/c",
      env: { PATH: "/bin" }, sessionId: "x", resume: false, server: {} as never, canUseTool: (async () => ({ behavior: "deny", message: "" })) as never,
      hooks: {}, secretPaths: [], context: { limit_tokens: 600_000, in_task_pct: 80 },
    });
    expect(opts.env).toMatchObject({ PATH: "/bin", CLAUDE_CODE_AUTO_COMPACT_WINDOW: "600000", CLAUDE_AUTOCOMPACT_PCT_OVERRIDE: "82.76" });
  });

  it("gives Codex an absolute token limit, capped at the model's window", () => {
    expect(autoCompactConfig({ limit_tokens: 600_000, in_task_pct: 80 })).toEqual({ model_auto_compact_token_limit: 480_000 });
    // a 600K limit on a 272K model would never trigger: 80% of the window instead
    expect(autoCompactConfig({ limit_tokens: 600_000, in_task_pct: 80, window_tokens: 272_000 })).toEqual({ model_auto_compact_token_limit: 217_600 });
    expect(autoCompactConfig({ limit_tokens: 100_000, in_task_pct: 80, window_tokens: 272_000 })).toEqual({ model_auto_compact_token_limit: 80_000 });
    // no limit: the pct of the window the last session reported
    expect(autoCompactConfig({ in_task_pct: 80, window_tokens: 272_000 })).toEqual({ model_auto_compact_token_limit: 217_600 });
    expect(autoCompactConfig({ in_task_pct: 80 })).toEqual({});
    expect(autoCompactConfig(undefined)).toEqual({});
  });
});

let h: Harness | undefined;
afterEach(async () => {
  if (!h) return;
  const invalid = [...h.console.invalid];
  await h.close();
  h = undefined;
  expect(invalid).toEqual([]);
});

const FOCUS = "keep the task id and branch";
const ANCHOR = "[任务锚点 t5] 给登录接口加限流，分支 thread/12/dev-2";

async function open(id: string) {
  h!.console.command("session.open", claudeOpen(id, 1, { context: { limit_tokens: 600_000, in_task_pct: 80, focus: FOCUS } }));
  await h!.console.waitType("session.opened", (b) => b.session_id === id);
  h!.console.command("session.release", { session_id: id, open_gen: 1 });
}

async function say(id: string, msg: string, text: string, extra: Record<string, unknown> = {}) {
  h!.console.command("session.send", { session_id: id, msg_id: msg, text, priority: "next", kind: "task", ...extra });
  return h!.console.waitType("session.event", (b) => b.kind === "turn_ended" && h!.console.frames.some((f) => f.type === "session.delivered" && f.body.msg_id === msg));
}

describe("claude sessions", () => {
  it("says hello with context_usage and session_compact", async () => {
    h = await startHarness();
    expect(h.console.ofType("hello")[0]!.body.features).toEqual(expect.arrayContaining(["context_usage", "session_compact"]));
  });

  it("reports context use with turn_ended and sets the autocompact window from session.open", async () => {
    h = await startHarness();
    await open("s_C1");
    const ended = await say("s_C1", "m_1", "hello");
    expect(ended.body.context).toEqual({ used_tokens: 6000, max_tokens: 600_000 });
    expect(h.log.options[0]!.env).toMatchObject({ CLAUDE_CODE_AUTO_COMPACT_WINDOW: "600000", CLAUDE_AUTOCOMPACT_PCT_OVERRIDE: "82.76" });
  });

  it("compacts on session.compact with /compact <instructions> and answers session.compacted", async () => {
    h = await startHarness();
    await open("s_C2");
    await say("s_C2", "m_1", "hello", { anchor: ANCHOR });
    h.console.command("session.compact", { session_id: "s_C2", open_gen: 1, compact_id: "cp_1", instructions: "keep t5" });
    const done = await h.console.waitType("session.compacted");
    expect(done.body).toEqual({ session_id: "s_C2", open_gen: 1, compact_id: "cp_1", ok: true, context: { used_tokens: 500, max_tokens: 600_000 } });
    expect(h.log.prompts).toContain("/compact keep t5");
    // the anchor of the finished task is dropped; the console's own compaction adds nothing
    expect(h.log.hooks).toEqual([{ event: "PreCompact", output: {} }, { event: "SessionStart", output: {} }]);
  });

  it("re-injects the task anchor after a compaction the runtime did by itself", async () => {
    h = await startHarness();
    await open("s_C3");
    await say("s_C3", "m_1", "start", { anchor: ANCHOR });
    await say("s_C3", "m_2", "autocompact");
    expect(h.log.hooks).toEqual([
      { event: "PreCompact", output: { systemMessage: `${FOCUS}\n\n${ANCHOR}` } },
      { event: "SessionStart", output: { hookSpecificOutput: { hookEventName: "SessionStart", additionalContext: ANCHOR } } },
    ]);
  });

  it("takes the anchor of a fresh session's first message", async () => {
    h = await startHarness();
    h.console.command("session.open", claudeOpen("s_C4", 1, { first_message: { msg_id: "m_f", text: "brief", anchor: ANCHOR } }));
    await h.console.waitType("session.opened");
    h.console.command("session.release", { session_id: "s_C4", open_gen: 1 });
    await h.console.waitType("session.event", (b) => b.kind === "turn_ended");
    await say("s_C4", "m_2", "autocompact");
    expect(h.log.hooks!.at(-1)).toEqual({ event: "SessionStart", output: { hookSpecificOutput: { hookEventName: "SessionStart", additionalContext: ANCHOR } } });
  });

  it("starts a (re)opened session with session.open's anchor", async () => {
    h = await startHarness();
    h.console.command("session.open", claudeOpen("s_C9", 2, { anchor: ANCHOR }));
    await h.console.waitType("session.opened");
    h.console.command("session.release", { session_id: "s_C9", open_gen: 2 });
    await say("s_C9", "m_1", "autocompact");
    expect(h.log.hooks!.at(-1)).toEqual({ event: "SessionStart", output: { hookSpecificOutput: { hookEventName: "SessionStart", additionalContext: ANCHOR } } });
  });

  it("clears the anchor when complete_task's tool.result says so", async () => {
    h = await startHarness();
    await open("s_C5");
    await say("s_C5", "m_1", "start", { anchor: ANCHOR });
    h.console.command("session.send", { session_id: "s_C5", msg_id: "m_2", text: 'use complete_task {"task_id":"t5","summary":"done"}' });
    const call = await h.console.waitType("tool.call");
    h.console.command("tool.result", { session_id: "s_C5", call_id: call.body.call_id, ok: true, content: [{ type: "text", text: "任务 t5 已提交" }], anchor: "" });
    await h.console.waitType("session.event", (b) => b.kind === "turn_ended" && h!.console.ofType("session.event").filter((f) => f.body.kind === "turn_ended").length === 2);
    await say("s_C5", "m_3", "autocompact");
    expect(h.log.hooks!.at(-1)).toEqual({ event: "SessionStart", output: {} });
  });

  it("answers ok=false (exited) when the runtime dies during the compaction", async () => {
    h = await startHarness({ crashDelaysMs: [10_000] });
    await open("s_C6");
    await say("s_C6", "m_1", "hello");
    h.console.command("session.compact", { session_id: "s_C6", open_gen: 1, compact_id: "cp_crash", instructions: "crash" });
    const done = await h.console.waitType("session.compacted");
    expect(done.body).toMatchObject({ compact_id: "cp_crash", ok: false });
    expect(String(done.body.error)).toMatch(/^exited: /);
  });

  it("gives up (busy) when the session stays in a turn, or a message arrives first", async () => {
    h = await startHarness({ compactWaitMs: 400 });
    await open("s_C7");
    h.console.command("session.send", { session_id: "s_C7", msg_id: "m_1", text: 'use ask_lead {"question":"which db?"}' });
    await h.console.waitType("tool.call");
    h.console.command("session.compact", { session_id: "s_C7", open_gen: 1, compact_id: "cp_slow" });
    const slow = await h.console.waitType("session.compacted", (b) => b.compact_id === "cp_slow");
    expect(slow.body).toMatchObject({ ok: false, error: "busy: the session stayed in a turn" });

    await h.restart();
    await h.console.waitType("sessions.report", () => h!.console.ofType("sessions.report").length === 2);
    h.console.command("session.compact", { session_id: "s_C7", open_gen: 1, compact_id: "cp_mail" });
    h.console.command("session.send", { session_id: "s_C7", msg_id: "m_2", text: 'use ask_lead {"question":"again?"}' });
    const mail = await h.console.waitType("session.compacted", (b) => b.compact_id === "cp_mail");
    expect(mail.body).toMatchObject({ ok: false, error: "busy: a new message arrived before the compaction" });
    expect(h.log.prompts.some((p) => p.startsWith("/compact"))).toBe(false);
  });

  it("answers ok=false (restarted) for a compaction the daemon was running when it died", async () => {
    h = await startHarness();
    await open("s_C8");
    await say("s_C8", "m_1", "hello");
    await h.daemon.stop();
    const file = join(h.opts.stateDir, "sessions", "s_C8.json");
    writeFileSync(file, JSON.stringify({ ...JSON.parse(readFileSync(file, "utf8")), compacting: ["cp_lost"] }));
    h.daemon = new Daemon(h.opts);
    await h.daemon.start();
    const done = await h.console.waitType("session.compacted");
    expect(done.body).toMatchObject({ session_id: "s_C8", open_gen: 1, compact_id: "cp_lost", ok: false });
    expect(String(done.body.error)).toMatch(/^restarted: /);
    expect(JSON.parse(readFileSync(file, "utf8")).compacting).toBeUndefined();
  });

  it("answers ok=false for a session that is not on the node", async () => {
    h = await startHarness();
    h.console.command("session.compact", { session_id: "s_none", open_gen: 1, compact_id: "cp_x" });
    const done = await h.console.waitType("session.compacted");
    expect(done.body).toMatchObject({ compact_id: "cp_x", ok: false });
  });
});
