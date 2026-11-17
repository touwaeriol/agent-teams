import { mkdirSync, readdirSync, rmSync } from "node:fs";
import { join } from "node:path";
import type { ApprovalBroker } from "../approvals.js";
import type { Reply } from "../link/inbox.js";
import type { GitResult, SessionClose, SessionInterrupt, SessionOpen, SessionReportItem, SessionSend, SessionSetMode, SessionWatch } from "../protocol.js";
import type { ToolBroker } from "../toolhost/broker.js";
import { deferred, sleep, type Deferred } from "../util/async.js";
import { readJsonFile, writeFileAtomic } from "../util/jsonl.js";
import type { Logger } from "../util/log.js";
import { isInside, resolveInRoot } from "../util/paths.js";
import { loadImages, removeImages, type ImageFetcher, type LoadedImage } from "./images.js";
import { Session, type SendFn, type SessionHost, type SessionRecord } from "./session.js";
import { ResumeFailed, type Driver, type DriverContext } from "./types.js";

export class CommandError extends Error {
  constructor(readonly code: "unknown_session" | "busy" | "bad_schema" | "unsupported", message: string) {
    super(message);
  }
}

export interface ManagerDeps {
  root: string;
  stateDir: string;
  send: SendFn;
  approvals: ApprovalBroker;
  tools: ToolBroker;
  waitForJob: (jobId: string, timeoutMs: number) => Promise<GitResult>;
  /** Builds env/policy and the runtime driver for a session. */
  createDriver: (spec: SessionOpen, absCwd: string, sink: Session) => { driver: Driver; ctx: DriverContext };
  /** Downloads an attached image from the console (session.send images). */
  fetchImage?: ImageFetcher;
  /** Called when a session stops for good (unregister cred secrets, loopback routes). */
  onSessionGone?: (sessionId: string) => void;
  log: Logger;
  parkAfterMs?: number;
  maxActive?: number;
  crashDelaysMs?: number[];
  prepareTimeoutMs?: number;
}

const DEFAULT_CRASH_DELAYS = [5_000, 15_000, 45_000];

/**
 * Logical sessions on this node (architecture.md §6.2, protocol.md §4): open/resume,
 * hold messages until release, park idle processes, cap concurrent processes at
 * max_active_sessions, restart crashed ones, report for reconciliation.
 */
export class SessionManager implements SessionHost {
  readonly send: SendFn;
  readonly approvals: ApprovalBroker;
  readonly tools: ToolBroker;
  private readonly sessions = new Map<string, Session>();
  private readonly slotWaiters: Array<{ session: Session; ready: Deferred<void> }> = [];
  private parkAfterMs: number;
  private maxActive: number;
  private draining = false;
  private timer: NodeJS.Timeout | null = null;
  private readonly dir: string;

  constructor(private readonly d: ManagerDeps) {
    this.send = d.send;
    this.approvals = d.approvals;
    this.tools = d.tools;
    this.parkAfterMs = d.parkAfterMs ?? 900_000;
    this.maxActive = d.maxActive ?? Infinity;
    this.dir = join(d.stateDir, "sessions");
    mkdirSync(this.dir, { recursive: true, mode: 0o700 });
    this.loadRecords();
  }

  startTimers(intervalMs = 30_000): void {
    this.timer ??= setInterval(() => void this.parkIdle(), intervalMs);
  }

  configure(c: { park_after_s?: number; max_active_sessions?: number }): void {
    if (c.park_after_s) this.parkAfterMs = c.park_after_s * 1000;
    if (c.max_active_sessions) this.maxActive = c.max_active_sessions;
    this.wakeWaiters();
  }

  get stats(): { active: number; parked: number; queued: number } {
    const all = [...this.sessions.values()];
    return { active: all.filter((s) => s.driver).length, parked: all.filter((s) => s.state === "parked").length, queued: this.slotWaiters.length };
  }

  /** True when an active session works inside the path (git.purge guard). */
  busyUnder(abs: string): boolean {
    return [...this.sessions.values()].some((s) => s.driver && s.absCwd && isInside(abs, s.absCwd));
  }

  // ---- commands ----

  async open(spec: SessionOpen): Promise<Reply[]> {
    const existing = this.sessions.get(spec.session_id);
    if (existing && existing.gen === spec.open_gen && existing.driver && existing.init) return [opened(existing)];
    if (existing && existing.gen > spec.open_gen) return [];
    if (this.draining) throw new CommandError("busy", "node is draining");
    if (existing) await this.stop(existing, "process_restarted");
    const failure = await this.preflight(spec);
    if (failure) return this.openFailed(new Session(spec, this), failure);
    return this.startNew(spec);
  }

  private async preflight(spec: SessionOpen): Promise<string | null> {
    try {
      resolveInRoot(this.d.root, spec.cwd);
    } catch (err) {
      return (err as Error).message;
    }
    for (const job of prepareJobs(spec)) {
      const res = await this.d.waitForJob(job, this.d.prepareTimeoutMs ?? 600_000).catch((err: Error) => ({ ok: false, error: err.message }) as GitResult);
      if (!res.ok) return `prepare ${job} failed: ${res.error ?? res.error_code ?? "unknown"}`;
    }
    return null;
  }

  private async startNew(spec: SessionOpen): Promise<Reply[]> {
    const session = new Session(structuredClone(spec), this);
    session.absCwd = resolveInRoot(this.d.root, spec.cwd);
    mkdirSync(session.absCwd, { recursive: true });
    this.sessions.set(spec.session_id, session);
    session.setState("starting");
    try {
      await this.run(session);
      return [opened(session)];
    } catch (err) {
      return this.openError(session, err as Error);
    }
  }

  private openError(session: Session, err: Error): Reply[] {
    if (err instanceof ResumeFailed) return this.resumeFailed(session, err);
    this.sessions.delete(session.id);
    rmSync(this.recordPath(session.id), { force: true });
    return this.openFailed(session, err.message);
  }

  /**
   * The runtime cannot resume the session (transcript missing: another node, a wiped ~/.claude,
   * or a corrupt file). The session is dropped here and the console hands off to a fresh one
   * seeded with a recap, including the messages that were on their way (protocol.md §4.2).
   */
  private resumeFailed(session: Session, err: Error): Reply[] {
    this.d.log.warn({ session: session.id, err: err.message }, "resume failed");
    this.sessions.delete(session.id);
    rmSync(this.recordPath(session.id), { force: true });
    session.cancelPending("process_restarted", "process_restarted");
    session.state = "closed"; // no session.state: the console would close the row before it sees resume_failed
    return [{ type: "session.opened", body: { session_id: session.id, open_gen: session.gen, resume_failed: true, error: err.message.slice(0, 1000) } }];
  }

  /** session.opened can only carry success or resume_failed, so other failures are a fatal error event + crashed. */
  private openFailed(session: Session, error: string): Reply[] {
    session.event({ kind: "error", code: "spawn_failed", message: error.slice(0, 500), fatal: true });
    session.setState("crashed", error.slice(0, 200));
    return [];
  }

  release(body: { session_id: string; open_gen: number }): Reply[] {
    const s = this.sessions.get(body.session_id);
    if (!s || s.gen !== body.open_gen || s.released) return [];
    s.released = true;
    this.persist(s);
    const first = s.spec.first_message;
    const queue = [...(first ? [{ session_id: s.id, msg_id: first.msg_id, text: first.text, kind: "system" as const, ...(first.images ? { images: first.images } : {}) }] : []), ...s.held.splice(0)];
    void this.deliverQueued(s, queue);
    return [];
  }

  private async deliverQueued(s: Session, queue: SessionSend[]): Promise<void> {
    for (const msg of queue) {
      const replies = await this.send_(msg).catch((err: Error) => {
        this.d.log.warn({ session: s.id, err: err.message }, "held message delivery failed");
        return [] as Reply[];
      });
      for (const r of replies) this.send(r.type, r.body);
    }
  }

  async sendMessage(body: SessionSend): Promise<Reply[]> {
    return this.send_(body);
  }

  private async send_(body: SessionSend): Promise<Reply[]> {
    const s = this.require(body.session_id);
    if (!s.released) {
      s.held.push(body);
      return [];
    }
    if (await this.alreadyDelivered(s, body.msg_id)) return [delivered(s, body.msg_id)];
    try {
      await this.ensureRunning(s);
    } catch (err) {
      if (err instanceof ResumeFailed) return this.resumeFailed(s, err);
      throw err;
    }
    if (body.interrupt_first) await s.driver!.interrupt();
    const { text, images } = await this.withImages(s, body);
    const r = await s.driver!.push({ msg_id: body.msg_id, text, priority: body.priority ?? "next", ...(images.length ? { images } : {}) });
    return [delivered(s, body.msg_id, r.turn_id)];
  }

  /** Downloads the message's images; one that cannot be fetched becomes a note in the text. */
  private async withImages(s: Session, body: SessionSend): Promise<{ text: string; images: LoadedImage[] }> {
    if (!body.images?.length) return { text: body.text, images: [] };
    if (!this.d.fetchImage) return { text: `${body.text}\n\n[附带的 ${body.images.length} 张图片无法在这个节点上读取]`, images: [] };
    try {
      return { text: body.text, images: await loadImages(body.images, this.d.fetchImage, this.imageDir(s.id)) };
    } catch (err) {
      this.d.log.warn({ session: s.id, err: (err as Error).message }, "image download failed");
      return { text: `${body.text}\n\n[附带的图片下载失败：${(err as Error).message}]`, images: [] };
    }
  }

  private imageDir(sessionId: string): string {
    return join(this.d.stateDir, "attachments", sessionId);
  }

  private async alreadyDelivered(s: Session, msgId: string): Promise<boolean> {
    if (!s.checkTranscript) return false;
    s.checkTranscript = false;
    const driver = s.driver ?? this.d.createDriver(s.spec, s.absCwd, s).driver;
    const found = await driver.deliveredMsgIds?.([msgId]).catch(() => new Set<string>());
    return found?.has(msgId) ?? false;
  }

  async interrupt(body: SessionInterrupt): Promise<Reply[]> {
    const s = this.require(body.session_id);
    const queued = s.driver ? await s.driver.interrupt() : [];
    for (const m of s.held) queued.push(m.msg_id);
    return [{ type: "session.interrupted", body: { session_id: s.id, open_gen: s.gen, queued_msg_ids: queued } }];
  }

  async setMode(body: SessionSetMode): Promise<Reply[]> {
    const s = this.require(body.session_id);
    const claude = s.spec.claude;
    if (body.permission_mode === "bypassPermissions" && claude && !claude.allow_dangerously_skip_permissions) throw new CommandError("unsupported", "bypassPermissions requires allow_dangerously_skip_permissions");
    if (s.driver) await s.driver.setMode(body.permission_mode, body.model);
    if (claude && body.permission_mode) claude.permission_mode = body.permission_mode;
    if (claude && body.model) claude.model = body.model;
    if (s.spec.codex && body.model) s.spec.codex.model = body.model;
    this.persist(s);
    return [];
  }

  async close(body: SessionClose): Promise<Reply[]> {
    const s = this.sessions.get(body.session_id);
    const reply: Reply = { type: "session.closed", body: { session_id: body.session_id, open_gen: body.open_gen ?? s?.gen ?? 0, ...(body.reason ? { reason: body.reason } : {}) } };
    if (!s) return [reply];
    await this.stop(s, "session_closed");
    s.setState("closed", body.reason);
    this.sessions.delete(s.id);
    if (body.forget) rmSync(this.recordPath(s.id), { force: true });
    else this.persist(s);
    removeImages(this.imageDir(s.id));
    this.d.onSessionGone?.(s.id);
    return [reply];
  }

  watch(body: SessionWatch): Reply[] {
    const s = this.sessions.get(body.session_id);
    if (s) s.watching = body.on;
    return [];
  }

  drain(): void {
    this.draining = true;
    void this.parkIdle(true);
  }

  report(): { sessions: SessionReportItem[] } {
    return { sessions: [...this.sessions.values()].map((s) => reportItem(s, this.approvals, this.tools)) };
  }

  async shutdown(): Promise<void> {
    if (this.timer) clearInterval(this.timer);
    this.timer = null;
    for (const s of this.sessions.values()) if (s.driver) await this.park(s);
  }

  // ---- lifecycle ----

  private async run(s: Session): Promise<void> {
    await this.acquireSlot(s);
    const { driver } = this.d.createDriver(s.spec, s.absCwd, s);
    s.driver = driver;
    try {
      const res = await driver.start();
      s.init = res.init;
      s.spec.resume = res.codex_thread_id ? { codex_thread_id: res.codex_thread_id } : { sdk_session_id: res.sdk_session_id ?? undefined };
      s.setState("active");
      this.persist(s);
    } catch (err) {
      s.driver = null;
      this.freeSlot(s);
      throw err;
    }
  }

  private async ensureRunning(s: Session): Promise<void> {
    if (s.driver) return;
    if (s.state === "closed") throw new CommandError("unknown_session", `session ${s.id} is closed`);
    s.setState("starting");
    await this.run(s);
  }

  private async acquireSlot(s: Session): Promise<void> {
    if (s.slot) return;
    if (this.activeCount() < this.maxActive) {
      s.slot = true;
      return;
    }
    if (await this.parkOldestIdle(s)) return this.acquireSlot(s);
    const ready = deferred<void>();
    this.slotWaiters.push({ session: s, ready });
    s.setState("queued");
    await ready.promise;
  }

  private activeCount(): number {
    return [...this.sessions.values()].filter((x) => x.slot).length;
  }

  private freeSlot(s: Session): void {
    if (!s.slot) return;
    s.slot = false;
    this.wakeWaiters();
  }

  private async parkOldestIdle(except: Session): Promise<boolean> {
    const idle = [...this.sessions.values()].filter((x) => x !== except && x.driver && x.idle).sort((a, b) => a.lastActivity - b.lastActivity);
    if (!idle[0]) return false;
    await this.park(idle[0]);
    return true;
  }

  private wakeWaiters(): void {
    while (this.slotWaiters.length && this.activeCount() < this.maxActive) {
      const w = this.slotWaiters.shift()!;
      w.session.slot = true;
      w.session.setState("starting");
      w.ready.resolve();
    }
  }

  private async parkIdle(all = false): Promise<void> {
    const now = Date.now();
    for (const s of [...this.sessions.values()]) {
      if (s.driver && s.idle && (all || now - s.lastActivity >= this.parkAfterMs)) await this.park(s);
    }
  }

  private async park(s: Session): Promise<void> {
    await this.stop(s, "process_restarted");
    s.setState("parked");
    this.persist(s);
  }

  private async stop(s: Session, reason: "process_restarted" | "session_closed"): Promise<void> {
    const driver = s.driver;
    s.driver = null;
    s.cancelPending(reason, reason === "session_closed" ? "session_closed" : "process_restarted");
    if (driver) await driver.close().catch(() => undefined);
    this.freeSlot(s);
  }

  onTurn(s: Session, running: boolean): void {
    if (!s.driver) return;
    if (!running) s.crashes = 0;
    s.setState(running ? "turn_running" : "active");
  }

  onExit(s: Session, err: Error): void {
    if (!s.driver) return;
    this.d.log.warn({ session: s.id, err: err.message }, "session process exited");
    s.driver = null;
    s.cancelPending("process_restarted", "process_restarted");
    this.freeSlot(s);
    void this.restartAfterCrash(s, err);
  }

  private async restartAfterCrash(s: Session, err: Error): Promise<void> {
    const delays = this.d.crashDelaysMs ?? DEFAULT_CRASH_DELAYS;
    while (s.crashes < delays.length && this.sessions.get(s.id) === s) {
      await sleep(delays[s.crashes++]!);
      if (this.sessions.get(s.id) !== s || s.driver) return;
      s.setState("starting", "restarting after crash");
      const failure = await this.run(s).then(() => null, (e: Error) => e);
      if (!failure) return;
      if (failure instanceof ResumeFailed) {
        for (const r of this.resumeFailed(s, failure)) this.send(r.type, r.body);
        return;
      }
    }
    if (this.sessions.get(s.id) !== s) return;
    s.setState("crashed", err.message.slice(0, 200));
    s.event({ kind: "error", code: "crashed", message: err.message.slice(0, 500), fatal: true });
    this.persist(s);
  }

  // ---- persistence ----

  private require(id: string): Session {
    const s = this.sessions.get(id);
    if (!s) throw new CommandError("unknown_session", `no session ${id} on this node`);
    return s;
  }

  private recordPath(id: string): string {
    return join(this.dir, `${id.replace(/[^A-Za-z0-9_-]/g, "_")}.json`);
  }

  private persist(s: Session): void {
    writeFileAtomic(this.recordPath(s.id), JSON.stringify(s.record()));
  }

  private loadRecords(): void {
    for (const f of readdirSync(this.dir).filter((n) => n.endsWith(".json"))) {
      const rec = readJsonFile<SessionRecord | null>(join(this.dir, f), null);
      if (!rec || rec.state === "closed") continue;
      const s = new Session(rec.spec, this);
      s.state = "parked";
      s.released = rec.released;
      s.checkTranscript = true;
      s.absCwd = resolveInRoot(this.d.root, rec.spec.cwd);
      this.sessions.set(s.id, s);
    }
  }
}

function opened(s: Session): Reply {
  const r = s.spec.resume ?? {};
  return { type: "session.opened", body: { session_id: s.id, open_gen: s.gen, sdk_session_id: r.sdk_session_id ?? null, codex_thread_id: r.codex_thread_id ?? null, init: s.init! } };
}

function delivered(s: Session, msgId: string, turnId?: string): Reply {
  return { type: "session.delivered", body: { session_id: s.id, open_gen: s.gen, msg_id: msgId, ...(turnId ? { turn_id: turnId } : {}) } };
}

function reportItem(s: Session, approvals: ApprovalBroker, tools: ToolBroker): SessionReportItem {
  const state = s.state === "turn_running" || s.state === "parked" || s.state === "closed" ? s.state : s.driver ? "active" : "parked";
  return {
    session_id: s.id, open_gen: s.gen, state, kind: s.spec.runtime,
    sdk_session_id: s.spec.resume?.sdk_session_id ?? null, codex_thread_id: s.spec.resume?.codex_thread_id ?? null,
    pending_approvals: approvals.pendingFor(s.id), pending_tool_calls: tools.pendingFor(s.id),
  };
}

/** The git.prepare jobs a session waits for: one per repository (prepare_jobs), else the single prepare. */
export function prepareJobs(spec: Pick<SessionOpen, "prepare" | "prepare_jobs">): string[] {
  if (spec.prepare_jobs?.length) return [...spec.prepare_jobs];
  return spec.prepare ? [spec.prepare] : [];
}
