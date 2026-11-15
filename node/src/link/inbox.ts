import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { appendJsonl, readJsonFile, readJsonl, writeFileAtomic, writeJsonlAtomic } from "../util/jsonl.js";
import type { Frame } from "../protocol.js";

export interface Reply {
  type: string;
  body: object;
}

interface DoneEntry {
  key: string;
  at: number;
  replies: Reply[];
}

interface Received {
  seq: number;
  key: string;
  frame: Frame;
}

const RETENTION_MS = 48 * 3600_000;

/**
 * Console command bookkeeping (protocol.md §2.2):
 * - done.jsonl: idempotency key -> the replies sent, kept 48 h, so a repeated command
 *   re-sends its original result instead of running again;
 * - received.jsonl: commands accepted but not finished yet (re-run after a crash);
 * - acked.json: highest console seq durably accepted (hello.acked_cmd_seq).
 */
export class Inbox {
  private readonly done = new Map<string, DoneEntry>();
  private readonly received = new Map<string, Received>();
  private ackedCmdSeq = 0;
  private readonly dir: string;

  constructor(stateDir: string, private readonly now: () => number = Date.now) {
    this.dir = stateDir;
    mkdirSync(stateDir, { recursive: true, mode: 0o700 });
    this.load();
  }

  get acked(): number {
    return this.ackedCmdSeq;
  }

  isDuplicateSeq(seq: number): boolean {
    return seq <= this.ackedCmdSeq;
  }

  repliesFor(key: string): Reply[] | undefined {
    return this.done.get(key)?.replies;
  }

  isInFlight(key: string): boolean {
    return this.received.has(key);
  }

  /** Records acceptance of a command; after this the seq may be acknowledged. */
  accept(frame: Frame, key: string): void {
    const entry: Received = { seq: frame.seq!, key, frame };
    this.received.set(key, entry);
    appendJsonl(join(this.dir, "received.jsonl"), entry);
    this.noteSeq(frame.seq!);
  }

  /** Records that a console seq was handled (also for repeats answered from done.jsonl). */
  noteSeq(seq: number): void {
    if (seq <= this.ackedCmdSeq) return;
    this.ackedCmdSeq = seq;
    writeFileAtomic(join(this.dir, "acked.json"), JSON.stringify({ seq: this.ackedCmdSeq }));
  }

  finish(key: string, replies: Reply[]): void {
    const entry: DoneEntry = { key, at: this.now(), replies };
    this.done.set(key, entry);
    appendJsonl(join(this.dir, "done.jsonl"), entry);
    if (this.received.delete(key)) this.rewriteReceived();
  }

  /** Commands accepted by an earlier process that never finished. */
  unfinished(): Frame[] {
    return [...this.received.values()].sort((a, b) => a.seq - b.seq).map((r) => r.frame);
  }

  forgetUnfinished(key: string): void {
    if (this.received.delete(key)) this.rewriteReceived();
  }

  private rewriteReceived(): void {
    writeJsonlAtomic(join(this.dir, "received.jsonl"), [...this.received.values()]);
  }

  private load(): void {
    this.ackedCmdSeq = readJsonFile<{ seq: number }>(join(this.dir, "acked.json"), { seq: 0 }).seq;
    const cutoff = this.now() - RETENTION_MS;
    for (const e of readJsonl<DoneEntry>(join(this.dir, "done.jsonl"))) if (e.at >= cutoff) this.done.set(e.key, e);
    writeJsonlAtomic(join(this.dir, "done.jsonl"), [...this.done.values()]);
    for (const r of readJsonl<Received>(join(this.dir, "received.jsonl"))) if (!this.done.has(r.key)) this.received.set(r.key, r);
  }
}
