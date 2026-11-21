// The update launcher (architecture.md §14.3): the one process that never changes with a release.
// systemd (ExecStart) and the node image (entrypoint) start it instead of node.mjs. It
//   1. seeds the image's bundled release into the data dir (containers: AGENT_TEAMS_SEED),
//   2. counts starts of a switched, unconfirmed release and rolls back to previous after
//      MAX_UNCONFIRMED_BOOTS starts or CONFIRM_DEADLINE_MS,
//   3. runs <data>/current/node.mjs and restarts it: at once on exit 75 (update), with backoff otherwise.
// Only node: builtins: it runs without node_modules.
import { spawn, type ChildProcess } from "node:child_process";
import { cpSync, existsSync, readFileSync, renameSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { buildOf, CONFIRM_DEADLINE_MS, defaultDataDir, MAX_UNCONFIRMED_BOOTS, ReleaseLayout, releaseId } from "./release.js";

export const EXIT_UPGRADE = 75;
const CONFIG_ERROR = 2;

export interface LauncherOptions {
  data: string;
  args: string[];
  seed?: string;
  env?: NodeJS.ProcessEnv;
  now?: () => number;
  log?: (msg: string) => void;
  spawnFn?: (cmd: string, args: string[], env: NodeJS.ProcessEnv) => ChildProcess;
  sleep?: (ms: number) => Promise<void>;
  checkEveryMs?: number;
  /** Where SIGTERM / SIGINT come from (tests; default process). */
  signals?: NodeJS.EventEmitter;
}

const log0 = (msg: string): void => void process.stderr.write(`agent-teams-launcher: ${msg}\n`);

/** The release id of an unpacked release directory (package.json version + build of node.mjs). */
export function releaseIdOf(dir: string): string {
  const pkg = JSON.parse(readFileSync(join(dir, "package.json"), "utf8")) as { version: string };
  return releaseId(pkg.version, buildOf(join(dir, "node.mjs")));
}

/**
 * Copies the image's bundled release into releases/ when the data dir has none, or when the image
 * carries a release this data dir has not seeded yet (a pulled image wins over an older update).
 */
export function seedRelease(layout: ReleaseLayout, seed: string, log = log0): void {
  if (!existsSync(join(seed, "node.mjs"))) return;
  const id = releaseIdOf(seed);
  const marker = join(layout.data, "seeded");
  const seeded = existsSync(marker) ? readFileSync(marker, "utf8").trim() : "";
  if (seeded === id && layout.target("current")) return;
  const dir = layout.dir(id);
  if (!readInstalled(dir)) {
    const staging = `${dir}.staging`;
    rmSync(staging, { recursive: true, force: true });
    cpSync(seed, staging, { recursive: true, verbatimSymlinks: true });
    writeFileSync(join(staging, ".installed"), `seed:${id}\n`);
    rmSync(dir, { recursive: true, force: true });
    renameSync(staging, dir);
  }
  if (layout.target("current") !== id) layout.switchTo(id);
  writeFileSync(marker, `${id}\n`);
  log(`seeded release ${id} from the image`);
}

function readInstalled(dir: string): string {
  try {
    return readFileSync(join(dir, ".installed"), "utf8").trim();
  } catch {
    return "";
  }
}

/** Rolls a switched release back to previous; returns the error recorded. */
export function rollback(layout: ReleaseLayout, reason: string, now: number, log = log0): void {
  const s = layout.readState();
  if (!s || s.state !== "switched") return;
  const swapped = layout.swapBack();
  const error = swapped ? reason : `${reason}; no previous release to go back to`;
  layout.writeState({ ...s, state: "rolled_back", error, reported: false, switched_at: s.switched_at || now });
  log(`rolled back ${s.to} → ${swapped ? s.from : "(kept)"}: ${reason}`);
}

/** Before each start: count it and roll back an update that keeps failing. */
export function beforeStart(layout: ReleaseLayout, now: number, log = log0): void {
  const s = layout.readState();
  if (!s || s.state !== "switched") return;
  const boots = s.boots + 1;
  if (boots > MAX_UNCONFIRMED_BOOTS) return rollback(layout, `新版本 ${s.to} 连续启动 ${MAX_UNCONFIRMED_BOOTS} 次都没有确认正常`, now, log);
  if (now - s.switched_at > CONFIRM_DEADLINE_MS) return rollback(layout, `新版本 ${s.to} 在 5 分钟内没有确认正常`, now, log);
  layout.writeState({ ...s, boots });
}

/** Runs the daemon until the launcher is told to stop; resolves with the exit code to use. */
export async function launch(o: LauncherOptions): Promise<number> {
  const log = o.log ?? log0;
  const now = o.now ?? Date.now;
  const sleep = o.sleep ?? ((ms: number) => new Promise<void>((r) => setTimeout(r, ms)));
  const layout = new ReleaseLayout(o.data);
  const spawnFn = o.spawnFn ?? ((cmd, args, env) => spawn(cmd, args, { stdio: "inherit", env }));
  let child: ChildProcess | null = null;
  let stopping = false;
  let stopCode = 0;
  const stop = (sig: NodeJS.Signals) => {
    stopping = true;
    child?.kill(sig);
  };
  const signals = o.signals ?? process;
  const onTerm = () => stop("SIGTERM");
  const onInt = () => stop("SIGINT");
  signals.on("SIGTERM", onTerm);
  signals.on("SIGINT", onInt);
  try {
    return await loop();
  } finally {
    signals.off("SIGTERM", onTerm);
    signals.off("SIGINT", onInt);
  }

  async function loop(): Promise<number> {
  let backoff = 1000;
  while (!stopping) {
    if (o.seed) seedRelease(layout, o.seed, log);
    beforeStart(layout, now(), log);
    const entry = join(o.data, "current", "node.mjs");
    if (!existsSync(entry)) {
      log(`${entry} 不存在：先运行一次安装脚本`);
      return 1;
    }
    const started = now();
    child = spawnFn(process.execPath, [entry, ...o.args], { ...(o.env ?? process.env), AGENT_TEAMS_LAUNCHER: "1", AGENT_TEAMS_DATA: o.data });
    const watch = setInterval(() => {
      const s = layout.readState();
      if (s?.state === "switched" && now() - s.switched_at > CONFIRM_DEADLINE_MS) {
        rollback(layout, `新版本 ${s.to} 在 5 分钟内没有确认正常`, now(), log);
        child?.kill("SIGTERM");
      }
    }, o.checkEveryMs ?? 10_000);
    const code = await new Promise<number>((resolve) => {
      child!.on("error", (err) => (log(`start failed: ${err.message}`), resolve(1)));
      child!.on("exit", (c, sig) => resolve(c ?? (sig ? 128 : 1)));
    });
    clearInterval(watch);
    child = null;
    if (stopping) {
      stopCode = code === 143 || code === 128 ? 0 : code;
      break;
    }
    if (code === EXIT_UPGRADE) {
      backoff = 1000;
      continue;
    }
    if (now() - started > 60_000) backoff = 1000;
    const wait = code === CONFIG_ERROR ? 30_000 : backoff;
    log(`daemon exited with ${code}; restarting in ${Math.round(wait / 1000)}s`);
    await sleep(wait);
    backoff = Math.min(backoff * 2, 60_000);
  }
  return stopCode;
  }
}

/* c8 ignore start */
if (process.argv[1] && /launch\.m?js$/.test(process.argv[1])) {
  const args = process.argv.slice(2);
  launch({ data: defaultDataDir(), args: args.length ? args : ["run"], ...(process.env.AGENT_TEAMS_SEED ? { seed: process.env.AGENT_TEAMS_SEED } : {}) }).then(
    (code) => process.exit(code),
    (err: Error) => {
      log0(`fatal: ${err.message}`);
      process.exit(1);
    },
  );
}
/* c8 ignore stop */
