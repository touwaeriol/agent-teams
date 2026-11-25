import { mkdirSync, readdirSync, rmSync } from "node:fs";
import { join } from "node:path";
import type { ApprovalBroker } from "../approvals.js";
import type { Reply } from "../link/inbox.js";
import type { GitResult, SessionClose, SessionCompact, SessionCompacted, SessionContext, SessionContextRefresh, SessionInterrupt, SessionOpen, SessionReportItem, SessionSend, SessionSetMode, SessionWatch } from "../protocol.js";
import type { ToolBroker } from "../toolhost/broker.js";
import { deferred, sleep, withTimeout, type Deferred } from "../util/async.js";
import { readJsonFile, writeFileAtomic } from "../util/jsonl.js";
import type { Logger } from "../util/log.js";
import { isInside, resolveInRoot } from "../util/paths.js";
import { loadImages, removeImages, type ImageFetcher, type LoadedImage } from "./images.js";
import { ATTACH_DIR, DEFAULT_ATTACHMENT_OPTIONS, downloadAttachment, planDelivery, sweepAttachments, type AttachmentOptions, type AttachmentOutcome, type AttachmentRuntime, type ConsoleHttp, type ResourceLink } from "./attachments.js";
import { Session, type SendFn, type SessionHost, type SessionRecord } from "./session.js";
import { ResumeFailed, type CompactResult, type Driver, type DriverContext, type ModeApplied, type ModeChange, type ModelOption } from "./types.js";
import { codexMode } from "./codex/mode.js";

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
  /** session.open.import: puts a session moved from another node in place (move/mover.ts); throws when it cannot. */
  importMove?: (spec: SessionOpen, absCwd: string) => Promise<void>;
  /** Builds env/policy and the runtime driver for a session. */
  createDriver: (spec: SessionOpen, absCwd: string, sink: Session) => { driver: Driver; ctx: DriverContext };
  /** Downloads an attached image from the console (session.send images). */
  fetchImage?: ImageFetcher;
  /** The console over HTTP (session.send attachments: /nodes/attachments/{id}/url and the legacy download). */
  http?: ConsoleHttp;
  /** Download budget, free-space reserve and retention of .attachments/ copies. */
  attachments?: Partial<AttachmentOptions>;
  /** How often old .attachments/ copies are removed (default 1 h). */
  attachmentSweepMs?: number;
  /** Called when a session stops for good (unregister cred secrets, loopback routes). */
  onSessionGone?: (sessionId: string) => void;
  /** A session's turn ended (session.digest, move/live.ts). */
  onTurnEnd?: (s: { id: string; gen: number; spec: SessionOpen; absCwd: string }) => void;
  /** The console takes session.context (welcome.features has context_detail). */
  contextDetail?: () => boolean;
  /** Called whenever a session's driver stops (closed, parked, crashed): per-driver secrets such as the SSH key are dropped. */
  onDriverGone?: (sessionId: string) => void;
  log: Logger;
  parkAfterMs?: number;
  maxActive?: number;
  crashDelaysMs?: number[];
  prepareTimeoutMs?: number;
  /** How long session.compact waits for a running turn to end before it gives up (busy). */
  compactWaitMs?: number;
  /** How long the compaction itself may take. */
  compactRunMs?: number;
}

/** session.compact waits at most this long for the session to go idle (protocol.md §4.10). */
const COMPACT_WAIT_MS = 120_000;
/** …and the runtime this long to compact: both together stay under the console's 10 minutes. */
const COMPACT_RUN_MS = 420_000;

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
  private sweepTimer: NodeJS.Timeout | null = null;
  private readonly dir: string;
  readonly attachmentOptions: AttachmentOptions;
  /** .attachments folders this daemon wrote into (swept for old copies), persisted in <state>/attachment-dirs.json. */
  private readonly attachmentDirs: Set<string>;

  constructor(private readonly d: ManagerDeps) {
    this.send = d.send;
    this.approvals = d.approvals;
    this.tools = d.tools;
    this.parkAfterMs = d.parkAfterMs ?? 900_000;
    this.maxActive = d.maxActive ?? Infinity;
    this.dir = join(d.stateDir, "sessions");
    mkdirSync(this.dir, { recursive: true, mode: 0o700 });
    this.attachmentOptions = { ...DEFAULT_ATTACHMENT_OPTIONS, ...d.attachments };
    this.attachmentDirs = new Set(readJsonFile<string[]>(this.attachmentDirsPath(), []).filter((x) => typeof x === "string"));
    this.loadRecords();
  }

  startTimers(intervalMs = 30_000): void {
    this.timer ??= setInterval(() => void this.parkIdle(), intervalMs);
    if (!this.sweepTimer && this.attachmentOptions.retentionMs > 0) {
      void this.sweepAttachments();
      this.sweepTimer = setInterval(() => void this.sweepAttachments(), this.d.attachmentSweepMs ?? 3600_000);
      this.sweepTimer.unref?.();
    }
  }

  /** Removes .attachments/ copies older than the retention (by mtime); forgets folders that are gone. */
  async sweepAttachments(now = Date.now()): Promise<number> {
    const { removed, gone } = await sweepAttachments([...this.attachmentDirs], this.attachmentOptions.retentionMs, now);
    if (gone.length) {
      for (const g of gone) this.attachmentDirs.delete(g);
      this.saveAttachmentDirs();
    }
    if (removed) this.d.log.info({ removed }, "old attachment copies removed");
    return removed;
  }

  /** Remembers a working directory's .attachments folder for the sweep. */
  noteAttachmentDir(absCwd: string): void {
    const dir = join(absCwd, ATTACH_DIR);
    if (this.attachmentDirs.has(dir)) return;
    this.attachmentDirs.add(dir);
    this.saveAttachmentDirs();
  }

  private attachmentDirsPath(): string {
    return join(this.d.stateDir, "attachment-dirs.json");
  }

  private saveAttachmentDirs(): void {
    writeFileAtomic(this.attachmentDirsPath(), JSON.stringify([...this.attachmentDirs]));
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
    const moved = await this.importMoved(spec);
    if (moved) return this.resumeFailed(new Session(spec, this), moved);
    return this.startNew(spec);
  }

  /**
   * A session moved from another node (protocol.md §4.11): its transcript and checkout snapshots
   * go in place before the driver resumes it. A failure is a resume failure: the console hands off
   * with a recap instead.
   */
  private async importMoved(spec: SessionOpen): Promise<ResumeFailed | null> {
    if (!spec.import) return null;
    if (!this.d.importMove) return new ResumeFailed("this daemon cannot import moved sessions");
    try {
      await this.d.importMove(spec, resolveInRoot(this.d.root, spec.cwd));
      return null;
    } catch (err) {
      return new ResumeFailed(`import of moved session failed: ${(err as Error).message}`);
    }
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
    session.taskAnchor = spec.anchor ?? ""; // the task the employee holds; a first_message anchor replaces it
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
    const queue = [...(first ? [{ session_id: s.id, msg_id: first.msg_id, text: first.text, kind: "system" as const, ...(first.images ? { images: first.images } : {}), ...(first.attachments ? { attachments: first.attachments } : {}), ...(first.anchor !== undefined ? { anchor: first.anchor } : {}) }] : []), ...s.held.splice(0)];
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
    s.sends++;
    if (body.anchor !== undefined) this.setAnchor(s, body.anchor);
    if (await this.alreadyDelivered(s, body.msg_id)) return [delivered(s, body.msg_id)];
    try {
      await this.ensureRunning(s);
    } catch (err) {
      if (err instanceof ResumeFailed) return this.resumeFailed(s, err);
      throw err;
    }
    if (body.interrupt_first) await s.driver!.interrupt();
    const { text, images, links } = await this.withAttachments(s, body);
    const r = await s.driver!.push({ msg_id: body.msg_id, text, priority: body.priority ?? "next", ...(images.length ? { images } : {}), ...(links.length ? { links } : {}) });
    return [delivered(s, body.msg_id, r.turn_id)];
  }

  /**
   * The message's legacy images and its attachments (attachments_v2). Attachments are downloaded into
   * <cwd>/.attachments/ one by one; one that cannot be fetched (error, timeout, no disk space) becomes
   * a line in the [附件] block and the message is delivered anyway.
   */
  private async withAttachments(s: Session, body: SessionSend): Promise<{ text: string; images: LoadedImage[]; links: ResourceLink[] }> {
    const legacy = await this.withImages(s, body);
    if (!body.attachments?.length) return { ...legacy, links: [] };
    const outcomes: AttachmentOutcome[] = [];
    if (!this.d.http || !s.absCwd) {
      for (const ref of body.attachments) outcomes.push({ ref, error: "这个节点无法下载附件" });
    } else {
      this.noteAttachmentDir(s.absCwd);
      for (const ref of body.attachments) {
        try {
          outcomes.push({ ref, saved: await downloadAttachment(s.absCwd, ref, this.d.http, this.attachmentOptions) });
        } catch (err) {
          this.d.log.warn({ session: s.id, attachment: ref.id, err: (err as Error).message }, "attachment download failed");
          outcomes.push({ ref, error: (err as Error).message });
        }
      }
    }
    const budget = this.pdfBudget(s);
    const before = budget.used;
    const plan = await planDelivery(s.spec.runtime as AttachmentRuntime, outcomes, budget).catch((err: Error) => {
      this.d.log.warn({ session: s.id, err: err.message }, "attachment planning failed");
      return { note: `\n\n[附件无法读取：${err.message}]`, images: [], links: [] };
    });
    if (budget.used !== before) this.persist(s);
    return { text: legacy.text + plan.note, images: [...legacy.images, ...plan.images], links: plan.links };
  }

  /** The PDF budget of the session's current SDK conversation (a new conversation starts from zero). */
  private pdfBudget(s: Session): { used: number } {
    const sdk = s.spec.resume?.sdk_session_id ?? "";
    if (!s.pdfBudget || s.pdfBudget.sdk_session_id !== sdk) s.pdfBudget = { sdk_session_id: sdk, used: 0 };
    return s.pdfBudget;
  }

  /** A compaction summarized the old turns away: their documents no longer count. */
  onCompacted(s: Session): void {
    if (!s.pdfBudget?.used) return;
    s.pdfBudget.used = 0;
    this.persist(s);
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
    const scratch = !s.driver;
    const driver = s.driver ?? this.d.createDriver(s.spec, s.absCwd, s).driver;
    const found = await driver.deliveredMsgIds?.([msgId]).catch(() => new Set<string>());
    // A driver built only to read the transcript never starts: drop what it set up (SSH agent).
    if (scratch && !s.driver) this.d.onDriverGone?.(s.id);
    return found?.has(msgId) ?? false;
  }

  private setAnchor(s: Session, anchor: string): void {
    s.taskAnchor = anchor;
    this.persist(s);
  }

  /** tool.result anchor: the call ended the session's task (complete_task). */
  setTaskAnchor(sessionId: string, anchor: string): void {
    const s = this.sessions.get(sessionId);
    if (s && s.taskAnchor !== anchor) this.setAnchor(s, anchor);
  }

  /**
   * session.compact (protocol.md §4.10): answered later with session.compacted, so the session's
   * command lane is not held while the runtime summarizes. The anchor belongs to the task that
   * just ended and is dropped.
   */
  compact(body: SessionCompact): Reply[] {
    const s = this.sessions.get(body.session_id);
    if (!s || s.gen !== body.open_gen || s.state === "closed") return [compacted(body, { ok: false, error: "no such session on this node" })];
    s.taskAnchor = "";
    s.compacts.add(body.compact_id);
    this.persist(s);
    void this.runCompact(s, body).then((r) => {
      s.compacts.delete(body.compact_id);
      if (this.sessions.get(s.id) === s) this.persist(s);
      this.send("session.compacted", compacted(body, r).body);
    });
    return [];
  }

  /**
   * Runs the compaction once the session is idle. It gives up (ok=false) when the session stays
   * in a turn for COMPACT_WAIT_MS or gets a message first (the brief waiting for it then goes out
   * on top of the full context), and reports a runtime that exits during the compaction.
   */
  private async runCompact(s: Session, body: SessionCompact): Promise<CompactResult> {
    const sends = s.sends;
    try {
      await this.ensureRunning(s);
      const busy = await this.untilIdle(s, sends);
      if (busy) return { ok: false, error: busy };
      const driver = s.driver;
      if (!driver?.compact) return { ok: false, error: "the runtime cannot compact" };
      const r = await withTimeout(driver.compact(body.instructions ?? ""), this.d.compactRunMs ?? COMPACT_RUN_MS, () => new Error("timed out: the runtime did not finish compacting"));
      return !r.ok && s.driver !== driver ? { ...r, error: `exited: ${r.error ?? "the runtime ended during the compaction"}` } : r;
    } catch (err) {
      return { ok: false, error: (err as Error).message.slice(0, 1000) };
    }
  }

  /** Waits for the session's running turn to end; why it gave up ("busy: …"), or null when idle. */
  private async untilIdle(s: Session, sends: number): Promise<string | null> {
    const until = Date.now() + (this.d.compactWaitMs ?? COMPACT_WAIT_MS);
    for (;;) {
      if (s.sends !== sends) return "busy: a new message arrived before the compaction";
      if (!s.driver?.busy) return null;
      if (Date.now() > until) return "busy: the session stayed in a turn";
      await sleep(100);
    }
  }

  async interrupt(body: SessionInterrupt): Promise<Reply[]> {
    const s = this.require(body.session_id);
    const queued = s.driver ? await s.driver.interrupt() : [];
    for (const m of s.held) queued.push(m.msg_id);
    return [{ type: "session.interrupted", body: { session_id: s.id, open_gen: s.gen, queued_msg_ids: queued } }];
  }

  /**
   * session.set_mode: the running driver applies it (Claude at once, Codex/Antigravity from the next
   * turn) and the session record keeps it for resumes. With a request_id (session_controls) the
   * outcome goes back as session.mode_set, failures included; without one a failure is a command error.
   */
  async setMode(body: SessionSetMode): Promise<Reply[]> {
    const s = this.require(body.session_id);
    const change: ModeChange = { ...(body.permission_mode ? { mode: body.permission_mode } : {}), model: body.model ?? null, effort: body.effort ?? null };
    try {
      const applied = await this.applyMode(s, change);
      return body.request_id ? [modeSet(s, body.request_id, true, applied)] : [];
    } catch (err) {
      if (!body.request_id) throw err;
      return [modeSet(s, body.request_id, false, { ...currentMode(s.spec), applies: "now" }, (err as Error).message)];
    }
  }

  private async applyMode(s: Session, change: ModeChange): Promise<ModeApplied> {
    const spec = s.spec;
    if (change.mode === "bypassPermissions" && spec.claude && !spec.claude.allow_dangerously_skip_permissions) throw new CommandError("unsupported", "bypassPermissions requires allow_dangerously_skip_permissions");
    if (change.mode === "bypassPermissions" && spec.antigravity?.read_only) throw new CommandError("unsupported", "a read-only session cannot skip permission prompts");
    if (change.mode && !spec.claude && (change.mode === "plan" || change.mode === "acceptEdits")) throw new CommandError("unsupported", `${spec.runtime} has no ${change.mode} mode`);
    if (change.mode === "auto" && spec.antigravity) throw new CommandError("unsupported", "antigravity has no auto mode");
    change.dropEffort = dropsEffort(s.init?.models, change, currentMode(spec).effort);
    const fallback = change.dropEffort ? s.init?.models?.find((o) => o.id === change.model)?.default_effort : undefined;
    if (fallback) change.defaultEffort = fallback;
    const applied = s.driver ? await s.driver.setMode(change) : undefined;
    rememberMode(spec, change);
    this.persist(s);
    return applied ?? { ...currentMode(spec), applies: "next_open" as ModeApplied["applies"] };
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

  /**
   * ssh.servers_update for this session and open_gen: the spec keeps the new list (persisted), so a
   * parked or restarted session reopens with it. False when there is no such session or gen.
   */
  setSshServers(sessionId: string, openGen: number, servers: SessionOpen["ssh_servers"]): boolean {
    const s = this.sessions.get(sessionId);
    if (!s || s.gen !== openGen || s.state === "closed") return false;
    s.spec.ssh_servers = servers;
    this.persist(s);
    return true;
  }

  /**
   * session.context_refresh (protocol.md §4.10 上下文细分): the precise breakdown now, read from the
   * live runtime only (a parked session is not started for it: error instead).
   */
  async refreshContext(body: SessionContextRefresh): Promise<Reply[]> {
    const answer = (extra: Pick<SessionContext, "context" | "error">): Reply[] => [
      { type: "session.context", body: { session_id: body.session_id, open_gen: body.open_gen, reason: "refresh", refresh_id: body.refresh_id, ...extra } satisfies SessionContext },
    ];
    const s = this.sessions.get(body.session_id);
    if (!s || s.gen !== body.open_gen || s.state === "closed") return answer({ error: "no such session on this node" });
    if (!s.driver) return answer({ error: "the session has no live process" });
    if (!s.driver.contextDetail) return answer({ error: "the runtime has no context breakdown" });
    const context = await s.driver.contextDetail("full");
    return answer(context ? { context } : { error: "the runtime did not report its context" });
  }

  contextDetailOn(): boolean {
    return this.d.contextDetail?.() ?? false;
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

  /** Ends a drain (a cancelled or failed update): sessions open again. */
  undrain(): void {
    this.draining = false;
  }

  /** Turns running now; a turn waiting on the user (approval or question card) does not count (it survives a restart). */
  busyTurns(): number {
    return [...this.sessions.values()].filter((s) => s.driver?.busy && this.approvals.pendingFor(s.id).length === 0).length;
  }

  report(): { sessions: SessionReportItem[] } {
    return { sessions: [...this.sessions.values()].map((s) => reportItem(s, this.approvals, this.tools)) };
  }

  async shutdown(): Promise<void> {
    if (this.timer) clearInterval(this.timer);
    if (this.sweepTimer) clearInterval(this.sweepTimer);
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
      this.d.onDriverGone?.(s.id);
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
    this.d.onDriverGone?.(s.id);
    this.freeSlot(s);
  }

  onTurn(s: Session, running: boolean): void {
    if (!s.driver) return;
    if (!running) s.crashes = 0;
    s.setState(running ? "turn_running" : "active");
    if (!running) this.d.onTurnEnd?.(s);
  }

  onExit(s: Session, err: Error): void {
    const driver = s.driver;
    if (!driver) return;
    this.d.log.warn({ session: s.id, err: err.message }, "session process exited");
    s.driver = null;
    void driver.close().catch(() => undefined); // settles a session.compact in progress (ok=false)
    this.d.onDriverGone?.(s.id);
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
      s.taskAnchor = rec.anchor ?? "";
      s.pdfBudget = rec.pdf_budget ?? null;
      s.absCwd = resolveInRoot(this.d.root, rec.spec.cwd);
      this.sessions.set(s.id, s);
      this.failCompactions(s, rec.compacting ?? []);
    }
  }

  /** A session.compact that was running when the daemon stopped: the console hears it failed. */
  private failCompactions(s: Session, ids: string[]): void {
    if (ids.length === 0) return;
    for (const id of ids) this.send("session.compacted", { session_id: s.id, open_gen: s.gen, compact_id: id, ok: false, error: "restarted: the daemon restarted during the compaction" });
    this.persist(s);
  }
}

function opened(s: Session): Reply {
  const r = s.spec.resume ?? {};
  return { type: "session.opened", body: { session_id: s.id, open_gen: s.gen, sdk_session_id: r.sdk_session_id ?? null, codex_thread_id: r.codex_thread_id ?? null, init: s.init! } };
}

function compacted(body: SessionCompact, r: CompactResult): { type: "session.compacted"; body: SessionCompacted } {
  return { type: "session.compacted", body: { session_id: body.session_id, open_gen: body.open_gen, compact_id: body.compact_id, ...r } };
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

/**
 * Whether a model switch leaves the effort in force behind: the new model (per the session's
 * init models) has no such level and the change picks none. Unknown models keep it.
 */
export function dropsEffort(models: ModelOption[] | undefined, change: ModeChange, effort: string | undefined): boolean {
  if (!change.model || change.effort || !effort) return false;
  const m = models?.find((o) => o.id === change.model);
  return !!m && !(m.efforts ?? []).includes(effort);
}

/** Keeps a set_mode change in the session's open spec (resumes after parking or a daemon restart use it). */
export function rememberMode(spec: SessionOpen, change: ModeChange): void {
  const { mode, model, effort } = change;
  if (spec.claude) {
    if (mode) {
      spec.claude.permission_mode = mode as NonNullable<SessionOpen["claude"]>["permission_mode"];
      spec.claude.prefer_auto = false;
    }
    if (model) spec.claude.model = model;
    if (change.dropEffort) delete spec.claude.effort;
    if (effort) spec.claude.effort = effort;
  }
  if (spec.codex) {
    if (mode) Object.assign(spec.codex, codexMode(mode, spec.codex));
    if (model) spec.codex.model = model;
    if (change.dropEffort) delete spec.codex.effort;
    if (effort) spec.codex.effort = effort;
  }
  if (spec.antigravity) {
    if (mode === "bypassPermissions" && !spec.antigravity.read_only) spec.antigravity.approval = "auto";
    if (mode === "default") spec.antigravity.approval = "ask";
    if (model) spec.antigravity.model = model;
  }
}

/** What the open spec says the session runs in. */
export function currentMode(spec: SessionOpen): Omit<ModeApplied, "applies"> {
  const c = spec.claude, x = spec.codex, a = spec.antigravity;
  const out: Omit<ModeApplied, "applies"> = {};
  if (c) Object.assign(out, { permission_mode: c.permission_mode, model: c.model, ...(c.effort ? { effort: c.effort } : {}) });
  if (x) Object.assign(out, { permission_mode: codexModeName(x), ...(x.model ? { model: x.model } : {}), ...(x.effort ? { effort: x.effort } : {}) });
  if (a) Object.assign(out, { permission_mode: a.approval === "auto" ? "bypassPermissions" : "default", ...(a.model ? { model: a.model } : {}) });
  return out;
}

/** The set_mode value a Codex sandbox/approval pair stands for. */
function codexModeName(x: NonNullable<SessionOpen["codex"]>): string {
  if (x.sandbox === "danger-full-access") return "bypassPermissions";
  if (x.approvals_reviewer === "auto_review") return "auto";
  return "default";
}

function modeSet(s: Session, requestId: string, ok: boolean, a: Partial<ModeApplied>, error?: string): Reply {
  const body: Record<string, unknown> = { session_id: s.id, open_gen: s.gen, request_id: requestId, ok };
  for (const k of ["permission_mode", "model", "effort", "applies"] as const) if (a[k]) body[k] = a[k];
  if (error) body.error = error.slice(0, 1000);
  return { type: "session.mode_set", body };
}
