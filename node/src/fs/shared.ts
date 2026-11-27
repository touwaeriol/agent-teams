import { createHash } from "node:crypto";
import { spawnSync } from "node:child_process";
import { accessSync, chmodSync, constants, existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, renameSync, rmSync, statSync, writeFileSync } from "node:fs";
import { join, resolve } from "node:path";
import { run as defaultRun, type RunOptions, type RunResult } from "../git/exec.js";
import type { FsMount, FsStatus, FsUnmount, SessionOpen } from "../protocol.js";
import type { Logger } from "../util/log.js";

type SessionOpenFs = NonNullable<SessionOpen["fs"]>;

/**
 * The user's shared folder: their prefix of the platform's object-store bucket, reached with a key
 * limited to it (docs/v2/shared-folder.md).
 * - external: something else mounted it at AGENT_TEAMS_FS_DIR (a host mount bound into the
 *   container); the daemon only uses it and never sees credentials (feature fs_external).
 * - daemon: the console sends fs.mount on the live link; the daemon downloads the pinned geesefs
 *   binary and mounts bucket:prefix with it (geesefs forks into the background) and keeps it mounted
 *   (feature fs_s3_mount). geesefs keeps writes in memory until it uploads them, so the daemon
 *   unmounts and waits for geesefs to exit before it stops (shutdown).
 * The user's key lives in memory only (it reaches geesefs through AWS_* in its env).
 */
export interface SharedFsOptions {
  /** <data>: geesefs goes to <data>/tools/geesefs-<version>, mounts to <data>/mnt/<name>. */
  dataDir: string;
  /** AGENT_TEAMS_FS_MOUNT_DIR: mount here instead of <data>/mnt/<name> (Docker: /home/agent/.fs). */
  mountDir?: string;
  /** Where the node image keeps geesefs-<version> (default /opt/geesefs): used when its sha256 matches, instead of a download. */
  preinstalledDir?: string;
  /** AGENT_TEAMS_FS_DIR: an externally mounted folder. */
  externalDir?: string;
  /** Where the mounted {name, generation} (never secrets) is kept across daemon restarts (default dataDir). */
  stateDir?: string;
  /** External mode: the mount that was healthy went away; the process should exit so the container restart brings it back. */
  onExternalLost?: () => void;
  log: Logger;
  send: (type: "fs.status", body: FsStatus) => void;
  /** Tests: command runner, fetch, platform key, fuse probe, mount probe, geesefs processes, health period. */
  run?: (cmd: string, args: string[], opts?: RunOptions) => Promise<RunResult>;
  fetchFn?: typeof fetch;
  platform?: string;
  fuse?: () => boolean;
  mounted?: (dir: string) => boolean;
  procs?: (dir: string) => number[];
  checkMs?: number;
  /** Tests: how long to wait for a fresh mount to appear (default 30 s) and for geesefs to exit (default 120 s). */
  mountWaitMs?: number;
  exitWaitMs?: number;
}

type Mounted = { spec: FsMount; dir: string };
/** What survives a daemon restart: the name and generation of the daemon's own mount. */
type Saved = { name: string; generation: number };

export class SharedFs {
  private current: Mounted | null = null;
  private status: FsStatus;
  private queue: Promise<unknown> = Promise.resolve();
  private timer: NodeJS.Timeout | null = null;
  private readonly run: NonNullable<SharedFsOptions["run"]>;
  private readonly canMount: boolean;
  /** External mode: the mount was seen healthy at least once. */
  private externalSeen = false;

  private readonly isMounted: (dir: string) => boolean;

  constructor(private readonly o: SharedFsOptions) {
    this.run = o.run ?? defaultRun;
    this.isMounted = o.mounted ?? isMountPoint;
    this.canMount = !o.externalDir && (o.fuse ?? hasFuse)();
    this.status = o.externalDir ? this.externalStatus() : { state: "unmounted", mode: "daemon" };
  }

  /** hello.features. */
  features(): string[] {
    if (this.o.externalDir) return ["fs_external"];
    return this.canMount ? ["fs_s3_mount"] : [];
  }

  /** Root of the mounted folder, or null. */
  root(): string | null {
    if (this.o.externalDir) return this.isMounted(this.o.externalDir) ? resolve(this.o.externalDir) : null;
    const c = this.current;
    return c && this.status.state === "mounted" && this.isMounted(c.dir) ? c.dir : null;
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

  /**
   * Daemon exit: unmounts the daemon's mount and waits until geesefs has uploaded what it holds
   * and exited (a container stop would otherwise kill it with writes still in memory). The next
   * daemon mounts again on fs.mount.
   */
  shutdown(): Promise<void> {
    this.stop();
    return this.serial(async () => {
      if (this.o.externalDir) return;
      const dir = this.current?.dir ?? (this.saved() ? this.mountDir(this.saved()!.name) : null);
      if (dir) await this.umount(dir);
    });
  }

  /** fs.mount: mounts (or remounts after a generation change); a healthy same-generation mount is kept. */
  mount(m: FsMount): Promise<void> {
    return this.serial(() => this.doMount(m));
  }

  /** fs.unmount: unmounts and forgets the credentials (also a mount left by a previous daemon process). */
  unmount(u: FsUnmount): Promise<void> {
    return this.serial(async () => {
      if (this.o.externalDir || (this.current && this.current.spec.name !== u.name)) return;
      await this.umount(this.current?.dir ?? this.mountDir(u.name));
      this.current = null;
      this.save(null);
      this.set({ state: "unmounted", mode: "daemon" });
    });
  }

  /**
   * session.open fs: the session's project and user-shared directories under the mounted
   * folder, created when missing; null when it is not mounted here.
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
    if (!this.canMount) return this.set({ state: "error", mode: "daemon", name: m.name, generation: m.generation, error: "no FUSE on this node (/dev/fuse and fusermount3 are needed)" });
    const dir = this.mountDir(m.name);
    const known: Saved | null = this.current?.spec ?? this.saved();
    const same = known?.name === m.name;
    if (same && m.generation < known.generation) {
      this.o.log.info({ name: m.name, generation: m.generation, mounted: known.generation }, "shared folder: ignoring an older fs.mount");
      return;
    }
    if (same && m.generation === known.generation && this.isMounted(dir)) {
      // Kept (or adopted after a daemon restart): the spec stays in memory for health remounts.
      this.current = { spec: m, dir };
      this.save(m);
      return this.set(mountedStatus(m));
    }
    if (known && !same) await this.umount(this.mountDir(known.name));
    this.current = { spec: m, dir };
    await this.mountNow(m, dir);
  }

  private mountDir(name: string): string {
    return this.o.mountDir ?? join(this.o.dataDir, "mnt", name);
  }

  private get stateFile(): string {
    return join(this.o.stateDir ?? this.o.dataDir, "fs-mount.json");
  }

  /** The daemon mount recorded by this or a previous process, or null. */
  private saved(): Saved | null {
    try {
      const v = JSON.parse(readFileSync(this.stateFile, "utf8")) as Partial<Saved>;
      return typeof v.name === "string" && typeof v.generation === "number" ? { name: v.name, generation: v.generation } : null;
    } catch {
      return null;
    }
  }

  private save(m: Saved | null): void {
    try {
      if (!m) return rmSync(this.stateFile, { force: true });
      writeFileSync(this.stateFile, JSON.stringify({ name: m.name, generation: m.generation }), { mode: 0o600 });
    } catch (err) {
      this.o.log.warn({ err: (err as Error).message }, "shared folder: cannot record the mount");
    }
  }

  private async mountNow(m: FsMount, dir: string): Promise<void> {
    this.set({ state: "mounting", mode: "daemon", name: m.name, generation: m.generation });
    try {
      const bin = await this.client(m.client);
      if (this.isMounted(dir)) await this.umount(dir);
      mkdirSync(dir, { recursive: true, mode: 0o700 });
      const r = await this.run(bin, geesefsArgs(m, dir, join(this.o.dataDir, "geesefs.log")), {
        env: { PATH: process.env.PATH ?? "/usr/bin:/bin", HOME: process.env.HOME ?? "/", AWS_ACCESS_KEY_ID: m.access_key, AWS_SECRET_ACCESS_KEY: m.secret_key },
        timeoutMs: 120_000,
      });
      if (r.code !== 0) throw new Error(lastLine(r.stderr || r.stdout) || `geesefs exited ${r.code}`);
      if (!(await waitFor(() => this.isMounted(dir), this.o.mountWaitMs ?? 30_000))) throw new Error("the mount did not appear");
      this.save(m);
      this.set(mountedStatus(m));
    } catch (err) {
      this.o.log.warn({ err: (err as Error).message, name: m.name }, "shared folder: mount failed");
      this.set({ state: "error", mode: "daemon", name: m.name, generation: m.generation, error: (err as Error).message.slice(0, 500) });
    }
  }

  /**
   * Unmounts dir and waits for its geesefs to exit, which it does once it has uploaded what it
   * held. A busy mount (a file still open) is detached lazily: geesefs then exits when the last
   * user lets go.
   */
  private async umount(dir: string): Promise<void> {
    if (this.isMounted(dir)) {
      const r = await this.run("fusermount3", ["-u", dir], { timeoutMs: 30_000 });
      if (r.code !== 0) {
        const lazy = await this.run("fusermount3", ["-u", "-z", dir], { timeoutMs: 30_000 });
        if (lazy.code !== 0) await this.run("umount", ["-l", dir], { timeoutMs: 30_000 });
      }
    }
    const procs = this.o.procs ?? geesefsProcs;
    if (!(await waitFor(() => procs(dir).length === 0, this.o.exitWaitMs ?? 120_000))) {
      this.o.log.warn({ dir }, "shared folder: geesefs still running after the unmount");
    }
  }

  /** Health check: a daemon mount that went away is mounted again; an external one is re-reported when it changes. */
  private async check(): Promise<void> {
    if (this.o.externalDir) return this.checkExternal();
    const c = this.current;
    if (!c || this.status.state === "mounting" || this.isMounted(c.dir)) return;
    this.o.log.warn({ name: c.spec.name }, "shared folder: mount lost, mounting again");
    await this.serial(() => (this.current === c ? this.mountNow(c.spec, c.dir) : Promise.resolve()));
  }

  /** An external mount that was healthy and went away cannot come back inside this container: exit (restart policy). */
  private checkExternal(): void {
    const next = this.externalStatus();
    if (next.state !== this.status.state) this.set(next);
    if (next.state === "mounted" || !this.externalSeen || !this.o.onExternalLost) return;
    this.o.log.error({ dir: this.o.externalDir }, "shared folder: the external mount went away; exiting so the container restart mounts it again");
    this.stop();
    this.o.onExternalLost();
  }

  private externalStatus(): FsStatus {
    const ok = this.isMounted(this.o.externalDir!);
    if (ok) this.externalSeen = true;
    return ok ? { state: "mounted", mode: "external" } : { state: "error", mode: "external", error: `${this.o.externalDir} is not a mount point` };
  }

  private set(s: FsStatus): void {
    this.status = s;
    this.o.send("fs.status", s);
  }

  /** The geesefs binary of this version: downloaded once, sha256-checked. */
  private async client(c: FsMount["client"]): Promise<string> {
    if (c.tool !== "geesefs") throw new Error(`mount client ${c.tool} is not supported (geesefs only)`);
    const dest = join(this.o.dataDir, "tools", `geesefs-${c.version}`, "geesefs");
    if (existsSync(dest)) return dest;
    const key = this.o.platform ?? platformKey();
    const asset = c.assets.find((a) => a.platform === key);
    if (!asset) throw new Error(`no geesefs for ${key}`);
    const pre = join(this.o.preinstalledDir ?? "/opt/geesefs", `geesefs-${c.version}`);
    if (existsSync(pre) && sha256File(pre) === asset.sha256) return pre;
    const r = await (this.o.fetchFn ?? fetch)(asset.url, { redirect: "follow", signal: AbortSignal.timeout(10 * 60_000) });
    if (!r.ok) throw new Error(`GET ${asset.url}: ${r.status}`);
    const data = Buffer.from(await r.arrayBuffer());
    if (createHash("sha256").update(data).digest("hex") !== asset.sha256) throw new Error(`geesefs ${c.version} ${key}: sha256 mismatch`);
    return install(dest, data);
  }
}

/**
 * geesefs's command line: the owner's bucket (RustFS) or its prefix of the shared bucket
 * (bucket:prefix, B2) at dir. Files
 * changed elsewhere show within 5 s (stat cache); close waits for the upload (fsync-on-close), so a
 * closed file is in the store; at most 1000 MB of memory.
 */
export function geesefsArgs(m: FsMount, dir: string, log: string): string[] {
  return ["--endpoint", m.endpoint, "--region", m.region ?? "us-east-1", "--stat-cache-ttl", "5s", "--memory-limit", "1000",
    "--fsync-on-close", "--log-file", log, m.prefix ? `${m.bucket}:${m.prefix}` : m.bucket, dir];
}

function mountedStatus(m: FsMount): FsStatus {
  return { state: "mounted", mode: "daemon", name: m.name, generation: m.generation };
}

/** dir is a mount point: on another device than its parent. */
export function isMountPoint(dir: string): boolean {
  try {
    return statSync(dir).dev !== statSync(join(dir, "..")).dev;
  } catch {
    return false;
  }
}

/** The geesefs processes serving dir (Linux /proc; none elsewhere). */
function geesefsProcs(dir: string): number[] {
  let pids: string[];
  try {
    pids = readdirSync("/proc").filter((p) => /^\d+$/.test(p));
  } catch {
    return [];
  }
  const out: number[] = [];
  for (const pid of pids) {
    try {
      const argv = readFileSync(`/proc/${pid}/cmdline`, "utf8").split("\0");
      if (/(^|\/)geesefs[^/]*$/.test(argv[0] ?? "") && argv.includes(dir)) out.push(Number(pid));
    } catch {
      // gone meanwhile
    }
  }
  return out;
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

function install(dest: string, data: Buffer): string {
  const parent = join(dest, "..", "..");
  mkdirSync(parent, { recursive: true, mode: 0o755 });
  const stage = mkdtempSync(join(parent, ".geesefs-"));
  try {
    writeFileSync(join(stage, "geesefs"), data, { mode: 0o755 });
    chmodSync(join(stage, "geesefs"), 0o755);
    mkdirSync(join(dest, ".."), { recursive: true, mode: 0o755 });
    renameSync(join(stage, "geesefs"), dest);
    return dest;
  } finally {
    rmSync(stage, { recursive: true, force: true });
  }
}

function sha256File(p: string): string {
  try {
    return createHash("sha256").update(readFileSync(p)).digest("hex");
  } catch {
    return "";
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
