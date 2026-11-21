import { createHash, createPublicKey, verify } from "node:crypto";
import { existsSync, mkdirSync, readdirSync, readFileSync, renameSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import type { NodeUpgrade, NodeUpgradeResult } from "./protocol.js";
import { run } from "./git/exec.js";
import { RELEASE_ID_RE, ReleaseLayout } from "./release.js";
import type { Logger } from "./util/log.js";

export class UpgradeError extends Error {}

/** What the updater needs from the session manager. */
export interface DrainTarget {
  drain(): void;
  undrain(): void;
  /** Turns running right now. */
  busyTurns(): number;
}

export interface UpdaterOptions {
  data: string;
  /** This process's release id (<version>+<build>). */
  self: string;
  consoleHttpBase: () => string;
  releaseKeyPath: string;
  sessions: DrainTarget;
  send: (body: NodeUpgradeResult) => void;
  exit: (code: number) => void;
  log: Logger;
  fetchFn?: typeof fetch;
  /** Defaults: poll the drain every 2 s, confirm a new release after 60 s connected. */
  pollMs?: number;
  confirmAfterMs?: number;
  now?: () => number;
  npm?: (cwd: string) => Promise<{ code: number; stderr: string }>;
}

const DEFAULT_DEADLINE_S = 1800;

/**
 * node.upgrade (protocol.md §9, architecture.md §14.3): stage the release next to the running
 * one, drain, switch `current`, exit 75 for the launcher. The next process confirms itself once it
 * has been connected for a minute; the launcher rolls back a release that never confirms.
 */
export class Updater {
  private job: { version: string; now: boolean; cancelled: boolean } | null = null;
  private confirmTimer: NodeJS.Timeout | null = null;
  private readonly layout: ReleaseLayout;

  constructor(private readonly o: UpdaterOptions) {
    this.layout = new ReleaseLayout(o.data);
  }

  get busy(): boolean {
    return this.job !== null;
  }

  handle(msg: NodeUpgrade): void {
    const mode = msg.mode ?? "wait_idle";
    if (mode === "cancel") return this.cancel(msg.version);
    if (this.job) {
      if (this.job.version !== msg.version) return this.o.send({ ok: false, phase: "failed", version: msg.version, error: `正在更新到 ${this.job.version}` });
      if (mode === "now") this.job.now = true;
      return;
    }
    if (msg.version === this.o.self) return this.o.send({ ok: true, phase: "done", version: msg.version });
    const job = { version: msg.version, now: mode === "now", cancelled: false };
    this.job = job;
    void this.run(msg, job).catch((err: Error) => {
      this.o.sessions.undrain();
      this.o.log.warn({ err: err.message, version: msg.version }, "update failed");
      this.o.send({ ok: false, phase: "failed", version: msg.version, error: err.message.slice(0, 1000) });
      if (this.job === job) this.job = null;
    });
  }

  private cancel(version: string): void {
    if (!this.job) return this.o.send({ ok: true, phase: "cancelled", version });
    this.job.cancelled = true;
  }

  private async run(msg: NodeUpgrade, job: { version: string; now: boolean; cancelled: boolean }): Promise<void> {
    this.o.send({ ok: true, phase: "staging", version: msg.version });
    const id = await this.stage(msg);
    if (job.cancelled) return this.cancelled(job);
    this.o.sessions.drain();
    const deadline = this.now() + (msg.deadline_s ?? DEFAULT_DEADLINE_S) * 1000;
    let timedOut = false;
    this.o.send({ ok: true, phase: "draining", version: msg.version, active_turns: this.o.sessions.busyTurns() });
    while (!job.now && this.o.sessions.busyTurns() > 0) {
      if (job.cancelled) return this.cancelled(job);
      if (!timedOut && this.now() > deadline) {
        timedOut = true;
        this.o.send({ ok: true, phase: "drain_timeout", version: msg.version, active_turns: this.o.sessions.busyTurns() });
      }
      await new Promise((r) => setTimeout(r, this.o.pollMs ?? 2000));
    }
    if (job.cancelled) return this.cancelled(job);
    this.layout.switchTo(id);
    this.layout.writeState({ state: "switched", from: this.o.self, to: id, boots: 0, switched_at: this.now() });
    this.o.log.info({ from: this.o.self, to: id }, "switched release; restarting");
    this.o.send({ ok: true, phase: "restarting", version: msg.version });
    setTimeout(() => this.o.exit(75), 300);
  }

  private cancelled(job: { version: string }): void {
    this.o.sessions.undrain();
    this.job = null;
    this.o.send({ ok: true, phase: "cancelled", version: job.version });
  }

  private now(): number {
    return (this.o.now ?? Date.now)();
  }

  /** Download, verify, unpack and install dependencies into releases/<id>; the running release is never touched. */
  async stage(msg: NodeUpgrade): Promise<string> {
    const id = msg.version;
    if (!RELEASE_ID_RE.test(id)) throw new UpgradeError("invalid version");
    if (msg.min_node && Number(process.versions.node.split(".")[0]) < msg.min_node) {
      throw new UpgradeError(`需要 Node.js ${msg.min_node} 或更高版本（当前 ${process.versions.node}）`);
    }
    const dir = this.layout.dir(id);
    if (readInstalled(dir) === msg.sha256) return id;
    if (this.layout.target("current") === id) throw new UpgradeError(`${id} 正在运行但内容不同`);
    const tgz = await download(`${this.o.consoleHttpBase()}/node/dist/agent-teams-node-${encodeURIComponent(id)}.tgz`, this.o.fetchFn ?? fetch);
    verifyRelease(tgz, msg, existsSync(this.o.releaseKeyPath) ? readFileSync(this.o.releaseKeyPath, "utf8") : null);
    const staging = `${dir}.staging`;
    rmSync(staging, { recursive: true, force: true });
    mkdirSync(staging, { recursive: true });
    const file = join(staging, "package.tgz");
    writeFileSync(file, tgz);
    await must(run("tar", ["-xzf", file, "-C", staging, "--strip-components=1"]), "unpack failed");
    rmSync(file);
    await this.installDeps(staging);
    writeFileSync(join(staging, ".installed"), `${msg.sha256}\n`);
    rmSync(dir, { recursive: true, force: true });
    renameSync(staging, dir);
    return id;
  }

  /** Same lock file as the running release → hard-link its node_modules (seconds instead of a ~400 MB download). */
  private async installDeps(staging: string): Promise<void> {
    const cur = this.layout.resolve("current");
    const lock = (d: string) => readOr(join(d, "npm-shrinkwrap.json"));
    if (cur && existsSync(join(cur, "node_modules")) && lock(cur) !== "" && lock(cur) === lock(staging)) {
      const r = await run("cp", ["-al", join(cur, "node_modules"), join(staging, "node_modules")]);
      if (r.code === 0) return;
      rmSync(join(staging, "node_modules"), { recursive: true, force: true });
    }
    const npm = this.o.npm ?? ((cwd: string) => run("npm", ["ci", "--omit=dev", "--ignore-scripts", "--no-audit", "--no-fund"], { cwd, timeoutMs: 15 * 60_000 }));
    await must(npm(staging), "npm ci failed");
  }

  /**
   * After a welcome: a release that was switched to and is this process confirms itself once the
   * link has stayed up for confirmAfterMs; a rolled-back update is reported by the old release.
   */
  onConnected(): void {
    const s = this.layout.readState();
    if (!s) return;
    if (s.state === "rolled_back" && !s.reported) {
      this.layout.writeState({ ...s, reported: true });
      this.o.send({ ok: false, phase: "rolled_back", version: s.to, error: s.error ?? "rolled back" });
      return;
    }
    if (s.state !== "switched" || s.to !== this.o.self || this.confirmTimer) return;
    this.confirmTimer = setTimeout(() => this.confirm(), this.o.confirmAfterMs ?? 60_000);
    this.confirmTimer.unref?.();
  }

  onDisconnected(): void {
    if (this.confirmTimer) clearTimeout(this.confirmTimer);
    this.confirmTimer = null;
  }

  private confirm(): void {
    this.confirmTimer = null;
    const s = this.layout.readState();
    if (s?.state !== "switched" || s.to !== this.o.self) return;
    this.layout.writeState({ ...s, state: "confirmed", reported: true });
    this.o.log.info({ release: this.o.self }, "update confirmed");
    this.o.send({ ok: true, phase: "done", version: this.o.self });
    this.prune();
  }

  /** Keeps current and previous; other releases go. */
  prune(): void {
    const keep = new Set([this.layout.target("current"), this.layout.target("previous")]);
    let names: string[] = [];
    try {
      names = readdirSync(this.layout.releases);
    } catch {
      return;
    }
    for (const n of names) if (!keep.has(n)) rmSync(join(this.layout.releases, n), { recursive: true, force: true });
  }
}

function readInstalled(dir: string): string {
  return readOr(join(dir, ".installed")).trim();
}

function readOr(path: string): string {
  try {
    return readFileSync(path, "utf8");
  } catch {
    return "";
  }
}

async function download(url: string, fetchFn: typeof fetch): Promise<Buffer> {
  const res = await fetchFn(url);
  if (!res.ok) throw new UpgradeError(`download failed: HTTP ${res.status}`);
  return Buffer.from(await res.arrayBuffer());
}

/** sha256 always; the Ed25519 signature too when the node has a release key. */
export function verifyRelease(tgz: Buffer, msg: Pick<NodeUpgrade, "sha256" | "signature">, publicKey: string | null): void {
  const sum = createHash("sha256").update(tgz).digest("hex");
  if (sum !== msg.sha256.toLowerCase()) throw new UpgradeError("sha256 mismatch");
  if (publicKey === null) return;
  if (!msg.signature) throw new UpgradeError("release is not signed but this node requires a signature");
  const key = createPublicKey(publicKey.includes("BEGIN") ? publicKey : { key: Buffer.from(publicKey.trim(), "base64"), format: "der", type: "spki" });
  if (!verify(null, tgz, key, Buffer.from(msg.signature, "base64"))) throw new UpgradeError("bad signature");
}

async function must(p: Promise<{ code: number; stderr: string }>, what: string): Promise<void> {
  const r = await p;
  if (r.code !== 0) throw new UpgradeError(`${what}: ${r.stderr.slice(-500)}`);
}
