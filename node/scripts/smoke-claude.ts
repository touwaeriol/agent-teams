// Real smoke test: one Claude employee session through the daemon's session layer, using
// this machine's Claude Code login. Runs the daemon against the in-repo fake console.
//   npm run smoke:claude
import { mkdtempSync, rmSync, existsSync } from "node:fs";
import { homedir, tmpdir } from "node:os";
import { join, dirname } from "node:path";
import { Daemon } from "../src/daemon.js";
import { createLogger } from "../src/util/log.js";
import { findTranscript } from "../src/sessions/claude/transcript.js";
import { FakeConsole } from "../test/helpers/fake-console.js";

const claudeConfigDir = process.env.CLAUDE_CONFIG_DIR ?? join(homedir(), ".claude");
const dir = mkdtempSync(join(tmpdir(), "at-smoke-claude-"));
const fc = new FakeConsole();

function check(cond: unknown, what: string): void {
  if (!cond) throw new Error(`smoke failed: ${what}`);
  console.log(`ok - ${what}`);
}

async function finalText(after: number): Promise<string> {
  const f = await fc.waitFor((x) => fc.frames.indexOf(x) > after && x.type === "session.event" && x.body.kind === "text" && x.body.final === true, 180_000, "final text");
  return String(f.body.text);
}

async function main(): Promise<void> {
  const url = await fc.start();
  const daemon = new Daemon({
    url, token: fc.token, root: join(dir, "root"), stateDir: join(dir, "state"), configDir: join(dir, "config"), platformDir: join(dir, "platform"),
    runtimeDir: join(dir, "run"), installDir: join(dir, "install"), maxActive: 2, passEnv: [], gitBaseUrl: "https://github.com",
    claudeConfigDir, codexHome: join(dir, "codex"), nodeCmd: [process.execPath, "agent-teams-node"], log: createLogger("warn"), isolation: "none",
  });
  await daemon.start();
  try {
    await fc.waitType("sessions.report", () => true, 120_000);
    const hello = fc.ofType("hello")[0]!;
    console.log("hello runtimes:", JSON.stringify(hello.body.runtimes));
    check(hello.body.runtimes.claude.auth !== "none", "hello reports a Claude login");

    fc.command("session.open", {
      session_id: "s_SMOKE", open_gen: 1, employee: { name: "dev-1", role: "dev", tier: "member", project_id: "smoke", thread_no: 1 },
      runtime: "claude", cwd: "projects/smoke/threads/1/dev-1", resume: null,
      claude: { model: "claude-opus-5-5", tools: [], allowed_tools: ["mcp__platform__*"], permission_mode: "dontAsk", setting_sources: [], system_append: "You are a terse test employee." },
      platform_tools: [{ name: "complete_task", description: "Finish your current task.", blocking: false, input_schema: { type: "object", required: ["task_id", "summary"], additionalProperties: false, properties: { task_id: { type: "string", pattern: "^t[0-9]+$" }, summary: { type: "string", minLength: 1 } } } }],
      first_message: { msg_id: "m_smoke_1", text: "reply with the word pong" },
    });
    const opened = await fc.waitType("session.opened", () => true, 120_000);
    console.log("session.opened init:", JSON.stringify(opened.body.init));
    check(JSON.stringify(opened.body.init.tools) === JSON.stringify(["mcp__platform__complete_task"]), "init tools are exactly the platform tool (no claude.ai connectors)");
    const mark = fc.frames.length - 1;
    fc.command("session.release", { session_id: "s_SMOKE", open_gen: 1 });
    const reply = await finalText(mark);
    console.log("reply:", reply);
    check(/pong/i.test(reply), "Claude replied with pong");

    const mark2 = fc.frames.length - 1;
    fc.command("session.send", { session_id: "s_SMOKE", msg_id: "m_smoke_2", text: "Call the complete_task tool with task_id t1 and summary smoke, then say done." });
    const call = await fc.waitType("tool.call", () => true, 180_000);
    check(call.body.tool === "complete_task" && call.body.args.task_id === "t1", "platform tool call reached the console as tool.call");
    fc.command("tool.result", { session_id: "s_SMOKE", call_id: call.body.call_id, ok: true, content: [{ type: "text", text: "Task t1 recorded." }] });
    console.log("reply:", await finalText(mark2));

    fc.command("node.config", { park_after_s: 1 });
    await new Promise((r) => setTimeout(r, 1500));
    await (daemon.sessions as unknown as { parkIdle(): Promise<void> }).parkIdle();
    await fc.waitType("session.state", (b) => b.state === "parked", 30_000);
    console.log("ok - parked");
    const mark3 = fc.frames.length - 1;
    fc.command("session.send", { session_id: "s_SMOKE", msg_id: "m_smoke_3", text: "Which single word did you reply with to my first message? Answer with that word only." });
    const resumed = await finalText(mark3);
    console.log("reply after resume:", resumed);
    check(/pong/i.test(resumed), "resumed session remembers the conversation");
    check(fc.invalid.length === 0, `every daemon frame validates against /protocol (${fc.invalid.join("; ")})`);
  } finally {
    await daemon.stop();
    await fc.stop();
    cleanupTranscript(openedSdkSessionId());
    rmSync(dir, { recursive: true, force: true });
  }
}

function openedSdkSessionId(): string | undefined {
  return fc.ofType("session.opened")[0]?.body.sdk_session_id as string | undefined;
}

/** Removes the smoke session's transcript directory from ~/.claude/projects. */
function cleanupTranscript(sid: string | undefined): void {
  const path = sid ? findTranscript(claudeConfigDir, sid) : null;
  if (!path) return;
  const projectDir = dirname(path);
  if (projectDir.includes("at-smoke-claude-") && existsSync(projectDir)) rmSync(projectDir, { recursive: true, force: true });
  else rmSync(path, { force: true });
}

main().then(
  () => process.exit(0),
  (err: Error) => {
    console.error(err.message);
    process.exit(1);
  },
);
