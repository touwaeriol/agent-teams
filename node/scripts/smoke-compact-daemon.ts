// Real smoke test of context management through the daemon (protocol.md §4.10), using this
// machine's Claude Code login and the in-repo fake console: turn_ended carries context, the
// role's limit × in-task pct makes the session compact by itself, the task anchor of the brief
// comes back after that compaction, and session.compact answers session.compacted.
//   SMOKE_MODEL=<model> npm run smoke:compact-daemon      (a small model keeps it cheap)
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { homedir, tmpdir } from "node:os";
import { join } from "node:path";
import { Daemon } from "../src/daemon.js";
import { createLogger } from "../src/util/log.js";
import { FakeConsole } from "../test/helpers/fake-console.js";

const MODEL = process.env.SMOKE_MODEL ?? "claude-opus-5-5";
const claudeConfigDir = process.env.CLAUDE_CONFIG_DIR ?? join(homedir(), ".claude");
const dir = mkdtempSync(join(tmpdir(), "at-smoke-compactd-"));
const fc = new FakeConsole();
const SID = "s_SMOKECD";
const CWD = "projects/smoke/threads/1/dev-1";
const ANCHOR = "[任务锚点] The secret anchor word is PELICAN-7.";

function check(cond: unknown, what: string): void {
  console.log(`${cond ? "ok" : "NOT OK"} - ${what}`);
}

/** Waits for the next turn_ended after the frame index mark. */
async function turnEnded(mark: number) {
  return fc.waitFor((x) => fc.frames.indexOf(x) > mark && x.type === "session.event" && x.body.kind === "turn_ended", 240_000, "turn_ended");
}

async function say(n: number, text: string) {
  const mark = fc.frames.length - 1;
  fc.command("session.send", { session_id: SID, msg_id: `m_cd_${n}`, text, priority: "next", kind: "task" });
  const ended = await turnEnded(mark);
  console.log(`[turn ${n}] context ${JSON.stringify(ended.body.context)}`);
  return { mark, ended };
}

async function main(): Promise<void> {
  const url = await fc.start();
  const root = join(dir, "root");
  const daemon = new Daemon({
    url, token: fc.token, root, stateDir: join(dir, "state"), configDir: join(dir, "config"), platformDir: join(dir, "platform"),
    runtimeDir: join(dir, "run"), installDir: join(dir, "install"), maxActive: 2, passEnv: [], gitBaseUrl: "https://github.com",
    claudeConfigDir, codexHome: join(dir, "codex"), nodeCmd: [process.execPath, "agent-teams-node"], log: createLogger("warn"), isolation: "none",
  });
  await daemon.start();
  try {
    await fc.waitType("sessions.report", () => true, 120_000);
    const filler = "lorem ipsum dolor sit amet consectetur adipiscing elit sed do eiusmod tempor\n";
    mkdirSync(join(root, CWD), { recursive: true });
    for (const f of ["a.txt", "b.txt"]) writeFileSync(join(root, CWD, f), filler.repeat(260));
    fc.command("session.open", {
      session_id: SID, open_gen: 1, employee: { name: "dev-1", role: "dev", tier: "member", project_id: "smoke", thread_no: 1 },
      runtime: "claude", cwd: CWD, resume: null, context: { limit_tokens: 30000, in_task_pct: 80, focus: "Keep the anchor word." },
      claude: { model: MODEL, tools: ["Read"], allowed_tools: ["Read"], permission_mode: "dontAsk", setting_sources: [], system_append: "You are a terse test employee. Answer in one short line." },
      platform_tools: [],
      first_message: { msg_id: "m_cd_0", text: "Reply with the word ok.", anchor: ANCHOR },
    });
    await fc.waitType("session.opened", () => true, 120_000);
    const mark0 = fc.frames.length - 1;
    fc.command("session.release", { session_id: SID, open_gen: 1 });
    const first = await turnEnded(mark0);
    const ctx0 = first.body.context as { used_tokens: number; max_tokens: number } | undefined;
    check(ctx0 && ctx0.used_tokens > 0 && ctx0.max_tokens === 100000, `turn_ended carries context ${JSON.stringify(ctx0)} (window 100000 for a 30K limit)`);

    const compBefore = fc.frames.length - 1;
    await say(1, "Use the Read tool to read a.txt completely, then reply with the word read.");
    await say(2, "Use the Read tool to read b.txt completely, then reply with the word read.");
    await say(3, "Reply with the word ok.");
    const compacting = fc.frames.filter((f, i) => i > compBefore && f.type === "session.event" && f.body.kind === "compacting");
    check(compacting.some((f) => f.body.on === false), `the runtime compacted by itself (${compacting.length} compacting events)`);
    const { mark } = await say(4, "What is the secret anchor word? Reply with only that word.");
    const answer = fc.frames.find((f, i) => i > mark && f.type === "session.event" && f.body.kind === "text" && f.body.final === true);
    console.log("anchor answer:", answer?.body.text);
    check(String(answer?.body.text ?? "").includes("PELICAN-7"), "the task anchor came back after the compaction");

    fc.command("session.compact", { session_id: SID, open_gen: 1, compact_id: "cp_smoke", instructions: "Keep only the word ok." });
    const done = await fc.waitType("session.compacted", () => true, 240_000);
    console.log("session.compacted:", JSON.stringify(done.body));
    check(done.body.ok === true && done.body.compact_id === "cp_smoke", "session.compact answered session.compacted ok");
    const cost = fc.ofType("session.event").filter((f) => f.body.kind === "turn_ended").reduce((s, f) => s + Number(f.body.cost_usd ?? 0), 0);
    console.log(`total cost_usd (sum of turns): ${cost.toFixed(4)}`);
    check(fc.invalid.length === 0, `every daemon frame validates against /protocol (${fc.invalid.join("; ")})`);
  } finally {
    await daemon.stop();
    await fc.stop();
    rmSync(dir, { recursive: true, force: true });
  }
}

main().then(
  () => process.exit(0),
  (err: Error) => {
    console.error(err.message);
    process.exit(1);
  },
);
