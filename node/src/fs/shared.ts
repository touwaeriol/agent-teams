import { createHash } from "node:crypto";
import { spawnSync } from "node:child_process";
import { accessSync, chmodSync, constants, existsSync, mkdirSync, mkdtempSync, renameSync, rmSync, statSync, writeFileSync } from "node:fs";
import { join, resolve } from "node:path";
import { run as defaultRun, type RunOptions, type RunResult } from "../git/exec.js";
import type { FsMount, FsStatus, FsUnmount, SessionOpen } from "../protocol.js";

type SessionOpenFs = NonNullable<SessionOpen["fs"]>;
import type { Logger } from "../util/log.js";

/**
 * The user's JuiceFS shared folder (one volume per user, docs/v2/shared-folder.md).
 * - external: something else mounted it at AGENT_TEAMS_FS_DIR (the Docker volume plugin); the
 *   daemon only uses it and never sees credentials (feature fs_external).
 * - daemon: the console sends fs.mount on the live link; the daemon downloads the pinned juicefs
 *   client, mounts the volume with `juicefs mount -d` (a separate process that outlives daemon
 *   restarts and self-updates) and keeps it mounted (feature fs_mount).
 * Credentials live in memory only (META_PASSWORD reaches the juicefs process through its env).
 */
export interface SharedFsOptions {
  /** <data>: the client goes to <data>/tools/juicefs-<version>, mounts to <data>/mnt/<volume>, cache to <data>/jfs-cache. */
  dataDir: string;
  /** AGENT_TEAMS_FS_DIR: an externally mounted volume root. */
  externalDir?: string;
  log: Logger;
  send: (type: "fs.status", body: FsStatus) => void;
  /** Tests: command runner, fetch, platform key, fuse probe, health period. */
  run?: (cmd: string, args: string[], opts?: RunOptions) => Promise<RunResult>;
  fetchFn?: typeof fetch;
  platform?: string;
  fuse?: () => boolean;
  checkMs?: number;
  /** Tests: how long to wait for a fresh mount to appear (default 30 s). */
  mountWaitMs?: number;
}

type Mounted = { spec: FsMount; dir: string };

export class SharedFs {
  private current: Mounted | null = null;
  private status: FsStatus;
  private queue: Promise<unknown> = Promise.resolve();
  private timer: NodeJS.Timeout | null = null;
  private readonly run: NonNullable<SharedFsOptions["run"]>;
  private readonly canMount: boolean;

  constructor(private readonly o: SharedFsOptions) {
    this.run = o.run ?? defaultRun;
    this.canMount = !o.externalDir && (o.fuse ?? hasFuse)();
    this.status = o.externalDir ? this.externalStatus() : { state: "unmounted", mode: "daemon" };
  }

  /** hello.features. */
  features(): string[] {
    if (this.o.externalDir) return ["fs_external"];
    return this.canMount ? ["fs_mount"] : [];
  }

  /** Root of the mounted volume, or null. */
  root(): string | null {
    if (this.o.externalDir) return isMounted(this.o.externalDir) ? resolve(this.o.externalDir) : null;
    return this.current && this.status.state === "mounted" ? this.current.dir : null;
  }

  /** Sends the current status (after welcome). */
  report(): void {
    if (this.o.externalDir) this.status = this.externalStatus();
    this.o.send("fs.status", this.status);
  }

  start(): void {
    if (!this.canMount && !this.o.externalDir) return;
    this.timer ??= setInterval(() => void this.check(), this.o.checkMs ?? 60_000);
    this.timer.unref?.();
  }

  stop(): void {
    if (this.timer) clearInterval(this.timer);
    this.timer = null;
  }

  /** fs.mount: mounts (or remounts after a generation change); a healthy same-generation mount is kept. */
  mount(m: FsMount): Promise<void> {
    return this.serial(() => this.doMount(m));
  }

  /** fs.unmount: unmounts and forgets the credentials. */
  unmount(u: FsUnmount): Promise<void> {
    return this.serial(async () => {
      if (!this.current || this.current.spec.volume !== u.volume) return;
      await this.umount(this.current.dir);
      this.current = null;
      this.set({ state: "unmounted", mode: "daemon" });
    });
  }

  /**
   * session.open fs: the session's project and user-shared directories under the mounted
   * volume, created when missing; null when the volume is not mounted here.
   */
  sessionDirs(fs: SessionOpenFs | undefined): { project: string; shared: string } | null {
    const root = fs ? this.root() : null;
    if (!fs || !root) return null;
    const project = inside(root, fs.project);
    const shared = inside(root, fs.shared);
    if (!project || !shared) return null;
    try {
      mkdirSync(project, { recursive: true });
      mkdirSync(shared, { recursive: true });
    } catch (err) {
      this.o.log.warn({ err: (err as Error).message }, "shared folder: cannot create the session directories");
      return null;
    }
    return { project, shared };
  }

  private serial<T>(fn: () => Promise<T>): Promise<T> {
    const next = this.queue.then(fn, fn);
    this.queue = next.catch(() => undefined);
    return next;
  }

  private async doMount(m: FsMount): Promise<void> {
    if (this.o.externalDir) return this.report();
    if (!this.canMount) return this.set({ state: "error", mode: "daemon", volume: m.volume, generation: m.generation, error: "no FUSE on this node (/dev/fuse and fusermount3 are needed)" });
    const dir = join(this.o.dataDir, "mnt", m.volume);
    const same = this.current?.spec.volume === m.volume && this.current.spec.generation === m.generation;
    if (same && isMounted(dir)) {
      this.current = { spec: m, dir };
      return this.set(mountedStatus(m));
    }
    if (this.current && this.current.dir !== dir) await this.umount(this.current.dir);
    this.current = { spec: m, dir };
    await this.mountNow(m, dir);
  }

  private async mountNow(m: FsMount, dir: string): Promise<void> {
    this.set({ state: "mounting", mode: "daemon", volume: m.volume, generation: m.generation });
    try {
      const bin = await this.client(m.client);
      if (isMounted(dir)) await this.umount(dir);
      mkdirSync(dir, { recursive: true, mode: 0o700 });
      const cache = join(this.o.dataDir, "jfs-cache");
      mkdirSync(cache, { recursive: true, mode: 0o700 });
      const args = ["mount", "-d", "--no-usage-report", "--cache-dir", cache, "--cache-size", String(m.cache_size_mb ?? 10240),
        "--log", join(this.o.dataDir, "jfs-mount.log"), m.meta_url, dir];
      const r = await this.run(bin, args, { env: { PATH: process.env.PATH ?? "/usr/bin:/bin", HOME: process.env.HOME ?? "/", META_PASSWORD: m.meta_password }, timeoutMs: 120_000 });
      if (r.code !== 0) throw new Error(lastLine(r.stderr || r.stdout) || `juicefs mount exited ${r.code}`);
      if (!(await waitFor(() => isMounted(dir), this.o.mountWaitMs ?? 30_000))) throw new Error("the mount did not appear");
      this.set(mountedStatus(m));
    } catch (err) {
      this.o.log.warn({ err: (err as Error).message, volume: m.volume }, "shared folder: mount failed");
      this.set({ state: "error", mode: "daemon", volume: m.volume, generation: m.generation, error: (err as Error).message.slice(0, 500) });
    }
  }

  private async umount(dir: string): Promise<void> {
    if (!isMounted(dir)) return;
    const r = await this.run("fusermount3", ["-u", "-z", dir], { timeoutMs: 30_000 });
    if (r.code !== 0) await this.run("umount", ["-l", dir], { timeoutMs: 30_000 });
  }

  /** Health check: a daemon mount that went away is mounted again; an external one is re-reported when it changes. */
  private async check(): Promise<void> {
    if (this.o.externalDir) {
      const next = this.externalStatus();
      if (next.state !== this.status.state) this.set(next);
      return;
    }
    const c = this.current;
    if (!c || this.status.state === "mounting" || isMounted(c.dir)) return;
    this.o.log.warn({ volume: c.spec.volume }, "shared folder: mount lost, mounting again");
    await this.serial(() => (this.current === c ? this.mountNow(c.spec, c.dir) : Promise.resolve()));
  }

  private externalStatus(): FsStatus {
    const ok = isMounted(this.o.externalDir!);
    return ok ? { state: "mounted", mode: "external" } : { state: "error", mode: "external", error: `${this.o.externalDir} is not a JuiceFS mount` };
  }

  private set(s: FsStatus): void {
    this.status = s;
    this.o.send("fs.status", s);
  }

  /** The juicefs binary of this version: downloaded once, sha256-checked. */
  private async client(c: FsMount["client"]): Promise<string> {
    const dest = join(this.o.dataDir, "tools", `juicefs-${c.version}`, "juicefs");
    if (existsSync(dest)) return dest;
    const key = this.o.platform ?? platformKey();
    const asset = c.assets.find((a) => a.platform === key);
    if (!asset) throw new Error(`no juicefs client for ${key}`);
    const r = await (this.o.fetchFn ?? fetch)(asset.url, { redirect: "follow", signal: AbortSignal.timeout(10 * 60_000) });
    if (!r.ok) throw new Error(`GET ${asset.url}: ${r.status}`);
    const data = Buffer.from(await r.arrayBuffer());
    if (createHash("sha256").update(data).digest("hex") !== asset.sha256) throw new Error(`juicefs ${c.version} ${key}: sha256 mismatch`);
    return unpack(dest, data);
  }
}

function mountedStatus(m: FsMount): FsStatus {
  return { state: "mounted", mode: "daemon", volume: m.volume, generation: m.generation };
}

/** A JuiceFS mount root has the virtual .config file. */
export function isMounted(dir: string): boolean {
  try {
    return statSync(join(dir, ".config")).isFile();
  } catch {
    return false;
  }
}

/** /dev/fuse usable and fusermount3 on PATH (Linux), or macFUSE installed (macOS). */
function hasFuse(): boolean {
  if (process.platform === "darwin") return existsSync("/Library/Filesystems/macfuse.fs");
  if (process.platform !== "linux") return false;
  try {
    accessSync("/dev/fuse", constants.R_OK | constants.W_OK);
  } catch {
    return false;
  }
  return spawnSync("fusermount3", ["-V"], { encoding: "utf8" }).status === 0;
}

/** linux-amd64, linux-arm64, darwin-arm64… (the console's asset names). */
export function platformKey(platform: string = process.platform, arch: string = process.arch): string {
  const a = arch === "x64" ? "amd64" : arch;
  return `${platform}-${a}`;
}

/** rel joined under root; null when it escapes. */
function inside(root: string, rel: string): string | null {
  const p = resolve(root, rel);
  return p === root || p.startsWith(root + "/") ? p : null;
}

function unpack(dest: string, data: Buffer): string {
  const parent = join(dest, "..", "..");
  mkdirSync(parent, { recursive: true, mode: 0o755 });
  const stage = mkdtempSync(join(parent, ".jfs-"));
  try {
    const archive = join(stage, "juicefs.tar.gz");
    writeFileSync(archive, data);
    const r = spawnSync("tar", ["-xzf", archive, "-C", stage, "--no-same-owner", "juicefs"], { encoding: "utf8" });
    if (r.status !== 0) throw new Error(`cannot unpack juicefs: ${r.stderr || r.error?.message}`);
    chmodSync(join(stage, "juicefs"), 0o755);
    mkdirSync(join(dest, ".."), { recursive: true, mode: 0o755 });
    renameSync(join(stage, "juicefs"), dest);
    return dest;
  } finally {
    rmSync(stage, { recursive: true, force: true });
  }
}

function lastLine(s: string): string {
  return s.trim().split("\n").pop()?.slice(0, 500) ?? "";
}

async function waitFor(ok: () => boolean, ms: number): Promise<boolean> {
  const end = Date.now() + ms;
  while (Date.now() < end) {
    if (ok()) return true;
    await new Promise((r) => setTimeout(r, 200));
  }
  return ok();
}
