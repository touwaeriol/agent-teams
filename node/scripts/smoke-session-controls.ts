// Real smoke test of session controls (#53): a session employee's Claude session opened with
// prefer_auto runs in auto when its model supports it and in default when not; session.set_mode
// switches mode, model and effort at once and answers with session.mode_set. Uses this machine's
// Claude Code login; sends no prompt (initialize and the control requests only).
//   npm run smoke:session-controls
import { mkdtempSync, rmSync } from "node:fs";
import { homedir, tmpdir } from "node:os";
import { join } from "node:path";
import { Daemon } from "../src/daemon.js";
import { createLogger } from "../src/util/log.js";
import { FakeConsole } from "../test/helpers/fake-console.js";

const claudeConfigDir = process.env.CLAUDE_CONFIG_DIR ?? join(homedir(), ".claude");
const dir = mkdtempSync(join(tmpdir(), "at-smoke-controls-"));
const fc = new FakeConsole();

function check(cond: unknown, what: string): void {
  if (!cond) throw new Error(`smoke failed: ${what}`);
  console.log(`ok - ${what}`);
}

function open(id: string, model: string): void {
  fc.command("session.open", {
    session_id: id, open_gen: 1, employee: { name: "lead-1", role: "dev", tier: "lead", project_id: "smoke", thread_no: 1 },
    runtime: "claude", cwd: `projects/smoke/threads/1/${id}`, resume: null,
    claude: { model, tools: ["Read"], allowed_tools: ["mcp__platform__*"], permission_mode: "default", prefer_auto: true, allow_dangerously_skip_permissions: true, setting_sources: [] },
    platform_tools: [],
  });
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
    check(fc.ofType("hello")[0]!.body.features.includes("session_controls"), "hello lists session_controls");

    open("s_AUTO", "claude-opus-5-5");
    const auto = await fc.waitType("session.opened", (b) => b.session_id === "s_AUTO", 120_000);
    const models = auto.body.init.models as Array<{ id: string; efforts: string[]; supports_auto: boolean }>;
    console.log("models:", models.map((m) => `${m.id}${m.supports_auto ? "+auto" : ""}[${m.efforts.join("/")}]`).join(" "));
    check(auto.body.init.permission_mode === "auto", "opus opens in auto (prefer_auto)");
    check(models.some((m) => m.id === "claude-opus-5-5" && m.supports_auto), "models list opus with supports_auto");

    fc.command("session.set_mode", { session_id: "s_AUTO", permission_mode: "plan", effort: "low", model: "claude-sonnet-5-5", request_id: "r1" });
    const set = await fc.waitType("session.mode_set", (b) => b.request_id === "r1", 60_000);
    console.log("mode_set:", JSON.stringify(set.body));
    check(set.body.ok === true && set.body.permission_mode === "plan" && set.body.model === "claude-sonnet-5-5" && set.body.effort === "low" && set.body.applies === "now", "plan + sonnet + low at once");

    const noAuto = models.find((m) => !m.supports_auto);
    if (noAuto) {
      open("s_DEF", noAuto.id);
      const def = await fc.waitType("session.opened", (b) => b.session_id === "s_DEF", 120_000);
      check(def.body.init.permission_mode === "default", `${noAuto.id} (no auto) stays in default`);
      fc.command("session.set_mode", { session_id: "s_DEF", permission_mode: "auto", request_id: "r2" });
      const refused = await fc.waitType("session.mode_set", (b) => b.request_id === "r2", 60_000);
      console.log("refused:", JSON.stringify(refused.body));
      check(refused.body.ok === false && refused.body.permission_mode === "default", "set_mode auto refused for a model without auto");
    } else console.log("skip - every listed model supports auto");
    console.log("SMOKE OK");
  } finally {
    await daemon.stop();
    await fc.stop();
    rmSync(dir, { recursive: true, force: true });
  }
}

main().then(() => process.exit(0), (err) => {
  console.error(err);
  process.exit(1);
});
