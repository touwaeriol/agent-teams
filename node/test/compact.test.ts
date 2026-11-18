// Context management (shared-members.md §5, protocol.md §4.10): context use on turn_ended,
// session.compact / session.compacted, the autocompact settings of session.open.context and the
// task anchor re-injected after a compaction the runtime did by itself.
import { describe, expect, it, afterEach } from "vitest";
import { claudeCompactEnv } from "../src/sessions/claude/compact.js";
import { buildClaudeOptions } from "../src/sessions/claude/options.js";
import { autoCompactConfig } from "../src/sessions/codex/driver.js";
import { claudeOpen, startHarness, type Harness } from "./helpers/daemon.js";

describe("claudeCompactEnv", () => {
  it("turns the role's limit × in-task pct into the CLI's window and a percentage of window − 20K", () => {
    expect(claudeCompactEnv(600_000, 80)).toEqual({ CLAUDE_CODE_AUTO_COMPACT_WINDOW: "600000", CLAUDE_AUTOCOMPACT_PCT_OVERRIDE: "82.76" });
    // 600000 × 82.76% of 580000 = 480008: compaction at ~480K whatever the model's window
    expect(Math.floor(580_000 * 0.8276)).toBeGreaterThanOrEqual(480_000);
    expect(claudeCompactEnv(30_000, 80)).toEqual({ CLAUDE_CODE_AUTO_COMPACT_WINDOW: "100000", CLAUDE_AUTOCOMPACT_PCT_OVERRIDE: "30" });
    expect(claudeCompactEnv(2_000_000, 50)).toEqual({ CLAUDE_CODE_AUTO_COMPACT_WINDOW: "1000000", CLAUDE_AUTOCOMPACT_PCT_OVERRIDE: "100" });
    expect(claudeCompactEnv(600_000, null)).toEqual({ CLAUDE_CODE_AUTO_COMPACT_WINDOW: "600000" });
    expect(claudeCompactEnv(null, 70)).toEqual({ CLAUDE_AUTOCOMPACT_PCT_OVERRIDE: "70" });
    expect(claudeCompactEnv(undefined, undefined)).toEqual({});
  });

  it("goes into the session's env, on top of the daemon's", () => {
    const opts = buildClaudeOptions({
      claude: { model: "m", tools: [], allowed_tools: [], permission_mode: "default", setting_sources: [] } as never, root: "/r", cwd: "/r/c",
      env: { PATH: "/bin" }, sessionId: "x", resume: false, server: {} as never, canUseTool: (async () => ({ behavior: "deny", message: "" })) as never,
      hooks: {}, secretPaths: [], context: { limit_tokens: 600_000, in_task_pct: 80 },
    });
    expect(opts.env).toMatchObject({ PATH: "/bin", CLAUDE_CODE_AUTO_COMPACT_WINDOW: "600000", CLAUDE_AUTOCOMPACT_PCT_OVERRIDE: "82.76" });
  });

  it("gives Codex an absolute token limit", () => {
    expect(autoCompactConfig({ limit_tokens: 600_000, in_task_pct: 80 })).toEqual({ model_auto_compact_token_limit: 480_000 });
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

  it("answers ok=false for a session that is not on the node", async () => {
    h = await startHarness();
    h.console.command("session.compact", { session_id: "s_none", open_gen: 1, compact_id: "cp_x" });
    const done = await h.console.waitType("session.compacted");
    expect(done.body).toMatchObject({ compact_id: "cp_x", ok: false });
  });
});
