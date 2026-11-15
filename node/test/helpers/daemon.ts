import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { Daemon, type DaemonOptions } from "../../src/daemon.js";
import { silentLogger } from "../../src/util/log.js";
import { fakeQueryFactory, type FakeQueryLog } from "./fake-query.js";
import { FakeConsole } from "./fake-console.js";

export interface Harness {
  console: FakeConsole;
  daemon: Daemon;
  dir: string;
  log: FakeQueryLog;
  opts: DaemonOptions;
  restart(): Promise<void>;
  close(): Promise<void>;
}

/** A daemon (fake Claude query, no runtime probes) connected to a fake console, all in a temp dir. */
export async function startHarness(overrides: Partial<DaemonOptions> = {}): Promise<Harness> {
  const dir = mkdtempSync(join(tmpdir(), "at-node-"));
  const fc = new FakeConsole();
  const url = await fc.start();
  const log: FakeQueryLog = { options: [], prompts: [], closed: 0, interrupts: 0, modes: [] };
  const opts: DaemonOptions = {
    url, token: fc.token, root: join(dir, "root"), stateDir: join(dir, "state"), configDir: join(dir, "config"),
    platformDir: join(dir, "platform"), runtimeDir: join(dir, "run"), installDir: join(dir, "install"),
    maxActive: 4, passEnv: [], gitBaseUrl: `file://${join(dir, "remotes")}`, claudeConfigDir: join(dir, "claude"),
    codexHome: join(dir, "codex"), nodeCmd: [process.execPath, "agent-teams-node"], log: silentLogger,
    probeRuntimes: false, isolation: "none", queryFn: fakeQueryFactory(log),
    link: { pingMs: 1000, deadMs: 5000, backoff: { minMs: 50, maxMs: 200 } }, ...overrides,
  };
  const h: Harness = {
    console: fc, dir, log, opts, daemon: new Daemon(opts),
    async restart() {
      await h.daemon.stop();
      h.daemon = new Daemon(opts);
      await h.daemon.start();
    },
    async close() {
      await h.daemon.stop();
      await fc.stop();
      rmSync(dir, { recursive: true, force: true });
    },
  };
  await h.daemon.start();
  await fc.waitType("sessions.report");
  return h;
}

export function claudeOpen(sessionId: string, gen = 1, extra: Record<string, unknown> = {}) {
  return {
    session_id: sessionId, open_gen: gen,
    employee: { name: "dev-2", role: "dev", tier: "member", project_id: "p1", thread_no: 12 },
    runtime: "claude", cwd: `projects/p1/threads/12/${sessionId}`, resume: null,
    claude: { model: "claude-opus-5-5", tools: ["Read", "Bash"], allowed_tools: ["mcp__platform__*"], permission_mode: "default", setting_sources: [] },
    platform_tools: [
      { name: "complete_task", description: "Finish the task", blocking: false, input_schema: { type: "object", required: ["task_id", "summary"], additionalProperties: false, properties: { task_id: { type: "string", pattern: "^t[0-9]+$" }, summary: { type: "string", minLength: 1 } } } },
      { name: "ask_lead", description: "Ask the lead", blocking: true, input_schema: { type: "object", required: ["question"], additionalProperties: false, properties: { question: { type: "string" } } } },
    ],
    ...extra,
  };
}
