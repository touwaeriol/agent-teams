// Keeps the console's view of every runtime CLI's install and login state current (protocol.md §9.2):
// a check after welcome, one every minute, one shortly after a watched credential file changes and
// one per node.runtimes_refresh. Only a changed result (ignoring checked_at) is reported, except for
// the hello report and refresh answers, which are always sent.
import { statSync, watch, type FSWatcher } from "node:fs";
import { basename, dirname } from "node:path";
import type { NodeRuntimes, RuntimeStatus } from "./protocol.js";
import type { RuntimeDetector, RuntimeState } from "./sessions/registry.js";
import { errorText } from "./usage.js";
import { withTimeout } from "./util/async.js";
import type { Logger } from "./util/log.js";

export type RuntimeStatuses = Record<string, RuntimeStatus>;
type Reason = NodeRuntimes["reason"];
/** Which detectors run forced: all, or these ids. */
type Force = "all" | ReadonlySet<string>;

const RUNTIME_ID = /^[a-z][a-z0-9_-]{0,31}$/;

export interface RuntimeMonitorOptions {
  /** The detectors to run (read on every check, so a registered runtime is reported from the next check on). */
  detectors: () => RuntimeDetector[];
  /** Sends node.runtimes (unreliable: dropped while the link is down; the hello report after welcome catches up). */
  send: (body: NodeRuntimes) => void;
  log: Logger;
  /** Periodic check (default 60 s; 0 disables the timer). */
  intervalMs?: number;
  /** Delay between a file event and the check, so a burst of writes is one check (default 2 s). */
  debounceMs?: number;
  /** Budget handed to a detector that has to spawn something (default 12 s); a detector gets 2 s more before it counts as failed. */
  probeTimeoutMs?: number;
  /** Called after a check whose result differs from the previous check's. */
  onChange?: (prev: RuntimeStatuses, next: RuntimeStatuses) => void;
  now?: () => Date;
}

export class RuntimeMonitor {
  /** Key of the last result sent to the console (null: nothing sent on this link yet). */
  private sentKey: string | null = null;
  private latest: RuntimeStatuses = {};
  private latestKey = "";
  private running: Promise<RuntimeStatuses> | null = null;
  private queued: { p: Promise<RuntimeStatuses>; force: Force } | null = null;
  private timer: NodeJS.Timeout | null = null;
  private debounce: NodeJS.Timeout | null = null;
  private readonly watches = new Map<string, FileWatch>();
  private stopped = false;

  constructor(private readonly o: RuntimeMonitorOptions) {}

  /** The result of the last check. */
  get statuses(): RuntimeStatuses { return this.latest; }

  /** Starts the periodic check and the file watches. */
  start(): void {
    this.stopped = false;
    const every = this.o.intervalMs ?? 60_000;
    if (every > 0 && !this.timer) {
      this.timer = setInterval(() => void this.periodic(), every);
      this.timer.unref?.();
    }
    this.arm();
  }

  stop(): void {
    this.stopped = true;
    if (this.timer) clearInterval(this.timer);
    if (this.debounce) clearTimeout(this.debounce);
    this.timer = this.debounce = null;
    for (const w of this.watches.values()) w.close();
    this.watches.clear();
  }

  /** After welcome: a fresh (unforced) check, always reported, since the console starts this link knowing nothing. */
  async hello(): Promise<void> {
    const r = await this.check(new Set());
    this.emit("hello", r);
  }

  /** node.runtimes_refresh: re-checks the given runtimes forced (all when omitted); the answer is always built, changed or not. */
  async refresh(ids: string[] | undefined, inReplyTo: string): Promise<NodeRuntimes> {
    const r = await this.check(ids?.length ? new Set(ids) : "all");
    this.sentKey = key(r);
    return { runtimes: r, reason: "refresh", in_reply_to: inReplyTo };
  }

  /** Runs one check now (unforced) and reports it if it changed; what the timer and file events call. */
  async poll(reason: "periodic" | "fs_event"): Promise<void> {
    const r = await this.check(new Set());
    if (key(r) !== this.sentKey) this.emit(reason, r);
  }

  private async periodic(): Promise<void> {
    this.arm();
    await this.poll("periodic");
  }

  private emit(reason: Reason, r: RuntimeStatuses): void {
    if (this.stopped) return;
    this.sentKey = key(r);
    this.o.send({ runtimes: r, reason });
  }

  /** Watches every detector's files; a watch that cannot be set up now is retried on the next periodic check. */
  private arm(): void {
    if (this.stopped) return;
    const want = new Set<string>();
    for (const d of this.o.detectors()) {
      let paths: string[] = [];
      try {
        paths = d.watchPaths();
      } catch {
        paths = [];
      }
      for (const p of paths) want.add(p);
    }
    for (const [p, w] of this.watches) if (!want.has(p)) (w.close(), this.watches.delete(p));
    for (const p of want) {
      let w = this.watches.get(p);
      if (!w) this.watches.set(p, (w = new FileWatch(p, () => this.fileChanged())));
      w.arm();
    }
  }

  private fileChanged(): void {
    if (this.stopped) return;
    if (this.debounce) clearTimeout(this.debounce);
    this.debounce = setTimeout(() => {
      this.debounce = null;
      void this.poll("fs_event");
    }, this.o.debounceMs ?? 2_000);
    this.debounce.unref?.();
  }

  /**
   * One check of every detector. Concurrent requests share work: while a check runs, everything asked
   * meanwhile waits for one more check that starts after it (so a file change or a refresh is never
   * answered by a check that began before it).
   */
  private check(force: Force): Promise<RuntimeStatuses> {
    if (!this.running) return this.begin(force);
    if (this.queued) {
      this.queued.force = union(this.queued.force, force);
      return this.queued.p;
    }
    const q = { force, p: Promise.resolve() as unknown as Promise<RuntimeStatuses> };
    q.p = this.running.then(() => {
      this.queued = null;
      return this.begin(q.force);
    });
    this.queued = q;
    return q.p;
  }

  private begin(force: Force): Promise<RuntimeStatuses> {
    const p = this.detectAll(force).finally(() => {
      if (this.running === p) this.running = null;
    });
    this.running = p;
    return p;
  }

  private async detectAll(force: Force): Promise<RuntimeStatuses> {
    const checkedAt = (this.o.now?.() ?? new Date()).toISOString();
    const budget = this.o.probeTimeoutMs ?? 12_000;
    const out: RuntimeStatuses = {};
    const detectors = this.o.detectors().filter((d) => {
      if (RUNTIME_ID.test(d.id)) return true;
      this.o.log.warn({ runtime: d.id }, "runtime id not valid for node.runtimes; skipped");
      return false;
    });
    await Promise.all(detectors.map(async (d) => {
      let s: RuntimeState;
      try {
        s = await withTimeout(Promise.resolve().then(() => d.detect({ force: force === "all" || force.has(d.id), timeoutMs: budget })), budget + 2_000, () => new Error("timeout"));
        if (s.error) s = { ...s, error: errorText(s.error) };
      } catch (err) {
        s = { installed: this.latest[d.id]?.installed ?? false, auth: "unknown", error: `detect_failed: ${errorText(err)}` };
      }
      out[d.id] = { ...s, checked_at: checkedAt };
    }));
    const sorted: RuntimeStatuses = {};
    for (const id of Object.keys(out).sort()) sorted[id] = out[id]!;
    const prev = this.latest;
    const k = key(sorted);
    this.latest = sorted;
    if (k !== this.latestKey) {
      this.latestKey = k;
      try {
        this.o.onChange?.(prev, sorted);
      } catch (err) {
        this.o.log.warn({ err: (err as Error).message }, "runtime change handler failed");
      }
    }
    return sorted;
  }
}

function union(a: Force, b: Force): Force {
  if (a === "all" || b === "all") return "all";
  return new Set([...a, ...b]);
}

/** Comparison key of a result: every field but checked_at, in a fixed order. */
export function key(r: RuntimeStatuses): string {
  return JSON.stringify(Object.keys(r).sort().map((id) => {
    const { checked_at: _, ...rest } = r[id]!;
    return [id, Object.keys(rest).sort().map((k) => [k, (rest as Record<string, unknown>)[k]])];
  }));
}

/**
 * Watches one file through the closest directory on its path that exists (the file's own directory once
 * it does), so a credentials file or a whole config directory created later is noticed. Events for other
 * names in the watched directory are ignored. Any failure just leaves the file to the periodic check.
 */
export class FileWatch {
  private w: FSWatcher | null = null;
  private dir = "";
  private ino = -1;

  constructor(private readonly target: string, private readonly onChange: () => void) {}

  arm(): void {
    const at = nearestDir(this.target);
    if (this.w && at && at.dir === this.dir && at.ino === this.ino) return;
    this.close();
    if (!at) return;
    try {
      const w = watch(at.dir, { persistent: false }, (_event, name) => {
        if (name !== null && String(name) !== at.next) return;
        this.onChange();
        if (this.w === w) this.arm();
      });
      w.on("error", () => {
        if (this.w === w) this.close();
      });
      this.w = w;
      this.dir = at.dir;
      this.ino = at.ino;
    } catch {
      this.close();
    }
  }

  close(): void {
    try {
      this.w?.close();
    } catch {
      // already gone
    }
    this.w = null;
    this.dir = "";
    this.ino = -1;
  }

  /** The directory being watched ("" when none). */
  get watching(): string { return this.dir; }
}

function nearestDir(target: string): { dir: string; ino: number; next: string } | null {
  let child = target;
  let dir = dirname(target);
  for (;;) {
    try {
      const s = statSync(dir);
      if (s.isDirectory()) return { dir, ino: s.ino, next: basename(child) };
    } catch {
      // not there yet: go up
    }
    const up = dirname(dir);
    if (up === dir) return null;
    child = dir;
    dir = up;
  }
}
